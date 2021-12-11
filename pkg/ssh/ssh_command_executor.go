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
		return "", fmt.Errorf("failed to dial IP %s: %s", hostAddress, err)
	}

	session, err := connection.NewSession()
	if err != nil {
		return "", fmt.Errorf("failed to create session: %s", err)
	}
	defer session.Close()

	// ctx.Logger.Info(fmt.Sprintf("ssh: running command inside VM `%s`...", command))
	var b bytes.Buffer
	session.Stdout = &b
	if err := session.Run(command); err != nil {
		return "", fmt.Errorf("failed to run the command: " + err.Error())
	}

	output := strings.Trim(b.String(), "\n")
	// ctx.Logger.Info(fmt.Sprintf("ssh: command `%s` output is `%s`", command, output))

	return output, nil
}
