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
	if x509.IsEncryptedPEMBlock(pemBlock) {
		// decrypt PEM
		pemBlock.Bytes, err = x509.DecryptPEMBlock(pemBlock, password)
		if err != nil {
			return nil, fmt.Errorf("Decrypting PEM block failed %v", err)
		}

		// get RSA, EC or DSA key
		key, err := parsePemBlock(pemBlock)
		if err != nil {
			return nil, err
		}

		// generate signer instance from key
		signer, err := ssh.NewSignerFromKey(key)
		if err != nil {
			return nil, fmt.Errorf("Creating signer from encrypted key failed %v", err)
		}

		return signer, nil
	} else {
		// generate signer instance from plain key
		signer, err := ssh.ParsePrivateKey(pemBytes)
		if err != nil {
			return nil, fmt.Errorf("Parsing plain private key failed %v", err)
		}

		return signer, nil
	}
}

func parsePemBlock(block *pem.Block) (interface{}, error) {
	switch block.Type {
	case "RSA PRIVATE KEY":
		key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("Parsing PKCS private key failed %v", err)
		} else {
			return key, nil
		}
	case "EC PRIVATE KEY":
		key, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("Parsing EC private key failed %v", err)
		} else {
			return key, nil
		}
	case "DSA PRIVATE KEY":
		key, err := ssh.ParseDSAPrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("Parsing DSA private key failed %v", err)
		} else {
			return key, nil
		}
	default:
		return nil, fmt.Errorf("Parsing private key failed, unsupported key type %q", block.Type)
	}
}
