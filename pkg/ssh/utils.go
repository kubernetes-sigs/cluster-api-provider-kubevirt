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
	ecdsa "crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"

	"github.com/pkg/errors"
	"golang.org/x/crypto/ssh"
)

// generateKeys generates a pair of public and private keys
func generateKeys() (pub, key []byte, err error) {
	ec := elliptic.P384()

	privateKey, err := generatePrivateKey(ec)
	if err != nil {
		return nil, nil, err
	}
	privateKeyBytes, err := encodePrivateKeyToPEM(privateKey)
	if err != nil {
		return nil, nil, err
	}

	publicKeyBytes, err := generatePublicKey(&privateKey.PublicKey)
	if err != nil {
		return nil, nil, err
	}

	return publicKeyBytes, privateKeyBytes, nil
}

// generatePrivateKey creates an ECDSA Private Key of specified byte size
func generatePrivateKey(c elliptic.Curve) (*ecdsa.PrivateKey, error) {
	// create key
	privateKey, err := ecdsa.GenerateKey(c, rand.Reader)
	if err != nil {
		return nil, err
	}

	return privateKey, nil
}

// encodePrivateKeyToPEM encodes Private Key from ECDSA to PEM format
func encodePrivateKeyToPEM(privateKey *ecdsa.PrivateKey) ([]byte, error) {
	// get ASN.1 DER format
	privDER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		return nil, err
	}

	// pem.Block
	privBlock := pem.Block{
		Type:    "EC PRIVATE KEY",
		Headers: nil,
		Bytes:   privDER,
	}

	// private key in PEM format
	privatePEM := pem.EncodeToMemory(&privBlock)

	return privatePEM, nil
}

// generatePublicKey takes a ecdsa.PublicKey and returns bytes suitable for writing to .pub file
// returns in the format "ssh-ecdsa ..."
func generatePublicKey(privateKey *ecdsa.PublicKey) ([]byte, error) {
	publicECKey, err := ssh.NewPublicKey(privateKey)
	if err != nil {
		return nil, err
	}

	pubKeyBytes := ssh.MarshalAuthorizedKey(publicECKey)

	return pubKeyBytes, nil
}

func signerFromPem(pemBytes []byte, password []byte) (ssh.Signer, error) {
	// read pem block
	err := errors.New("Pem decode failed, no key found")
	pemBlock, _ := pem.Decode(pemBytes)
	if pemBlock == nil {
		return nil, err
	}

	// handle encrypted key
	var signer ssh.Signer

	if len(password) > 0 {
		signer, err = ssh.ParsePrivateKeyWithPassphrase(pemBytes, password)
		if err != nil {
			return nil, fmt.Errorf("parsing encrypted private key failed: %v", err)
		}
	} else {
		// generate signer instance from plain key
		signer, err = ssh.ParsePrivateKey(pemBytes)
		if err != nil {
			return nil, fmt.Errorf("parsing plain private key failed: %v", err)
		}
	}

	return signer, nil
}
