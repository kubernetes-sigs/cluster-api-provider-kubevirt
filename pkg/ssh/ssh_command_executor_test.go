/*
Copyright 2026 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package ssh

import (
	"bytes"
	"crypto/elliptic"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// startTestSSHServer serves a single SSH connection on host, accepting only
// authorizedKey and answering every exec request with output. It returns the
// listening port and a channel that is closed once the client has closed the
// connection.
func startTestSSHServer(t *testing.T, host string, authorizedKey []byte, output string) (string, <-chan struct{}) {
	t.Helper()

	hostKey, err := generatePrivateKey(elliptic.P256())
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		t.Fatal(err)
	}
	allowed, _, _, _, err := ssh.ParseAuthorizedKey(authorizedKey)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if bytes.Equal(key.Marshal(), allowed.Marshal()) {
				return nil, nil
			}
			return nil, ssh.ErrNoAuth
		},
	}
	config.AddHostKey(hostSigner)

	listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Skipf("cannot listen on %s: %v", host, err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	closed := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		serverConn, channels, requests, err := ssh.NewServerConn(conn, config)
		if err != nil {
			_ = conn.Close()
			return
		}
		go ssh.DiscardRequests(requests)
		go func() {
			for newChannel := range channels {
				channel, channelRequests, err := newChannel.Accept()
				if err != nil {
					continue
				}
				go func() {
					for req := range channelRequests {
						if req.Type != "exec" {
							_ = req.Reply(false, nil)
							continue
						}
						_ = req.Reply(true, nil)
						_, _ = channel.Write([]byte(output + "\n"))
						_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
						_ = channel.Close()
					}
				}()
			}
		}()
		// Wait returns once the client closes the connection.
		_ = serverConn.Wait()
		close(closed)
	}()

	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return port, closed
}

func TestVMCommandExecutor(t *testing.T) {
	for _, tt := range []struct {
		name string
		host string
	}{
		{name: "IPv4 address", host: "127.0.0.1"},
		{name: "IPv6 address", host: "::1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pub, key, err := generateKeys()
			if err != nil {
				t.Fatal(err)
			}
			port, closed := startTestSSHServer(t, tt.host, pub, "success")

			executor := vmCommandExecutor{IPAddress: tt.host, PublicKey: pub, PrivateKey: key, port: port}
			output, err := executor.ExecuteCommand("cat /run/cluster-api/bootstrap-success.complete")
			if err != nil {
				t.Fatalf("ExecuteCommand() error = %v", err)
			}
			if output != "success" {
				t.Errorf("ExecuteCommand() output = %q, want %q", output, "success")
			}

			// The executor runs on every bootstrap check, so it must not
			// leave the SSH connection open.
			select {
			case <-closed:
			case <-time.After(5 * time.Second):
				t.Error("the SSH connection is still open after ExecuteCommand returned")
			}
		})
	}
}

func TestVMCommandExecutorAddress(t *testing.T) {
	keys := &ClusterNodeSshKeys{}
	for _, tt := range []struct {
		ip   string
		want string
	}{
		{ip: "10.0.0.5", want: "10.0.0.5:22"},
		{ip: "fd00::5", want: "[fd00::5]:22"},
	} {
		executor, ok := NewVMCommandExecutor(tt.ip, keys).(vmCommandExecutor)
		if !ok {
			t.Fatal("NewVMCommandExecutor() did not return a vmCommandExecutor")
		}
		if got := executor.address(); got != tt.want {
			t.Errorf("address() for %s = %q, want %q", tt.ip, got, tt.want)
		}
	}
}
