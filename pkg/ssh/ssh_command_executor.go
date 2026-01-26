/*
Copyright 2021 The Kubernetes Authors.

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
	"fmt"
	"net"
	"strings"

	"golang.org/x/crypto/ssh"
)

type VMCommandExecutor interface {
	ExecuteCommand(string) (string, error)
}

type vmCommandExecutor struct {
	IPAddress  string
	PublicKey  []byte
	PrivateKey []byte
}

func NewVMCommandExecutor(address string, keys *ClusterNodeSshKeys) VMCommandExecutor {
	return vmCommandExecutor{
		IPAddress:  address,
		PublicKey:  keys.PublicKey,
		PrivateKey: keys.PrivateKey,
	}
}

// ExecuteCommand runs command inside a VM, via SSH, and returns the command output.
func (e vmCommandExecutor) ExecuteCommand(command string) (string, error) {
	// create signer
	signer, err := signerFromPem(e.PrivateKey, []byte(""))
	if err != nil {
		return "", fmt.Errorf("can't get signer from PEM; %w", err)
	}

	sshConfig := &ssh.ClientConfig{
		User: "capk",
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			return nil
		},
	}

	hostAddress := strings.Join([]string{e.IPAddress, "22"}, ":")

	connection, err := ssh.Dial("tcp", hostAddress, sshConfig)
	if err != nil {
		return "", fmt.Errorf("ssh: failed to dial IP %s, error: %s", hostAddress, err.Error())
	}

	session, err := connection.NewSession()
	if err != nil {
		return "", fmt.Errorf("ssh: failed to create session, error: %s", err.Error())
	}
	defer func(session *ssh.Session) {
		err := session.Close()
		if err != nil {
			fmt.Printf("ssh: failed to close session, error: %s", err.Error())
		}
	}(session)

	var b bytes.Buffer
	session.Stdout = &b
	if err := session.Run(command); err != nil {
		return "", fmt.Errorf("ssh: failed to run command `%s`, error: %s", command, err.Error())
	}

	output := strings.Trim(b.String(), "\n")

	return output, nil
}
