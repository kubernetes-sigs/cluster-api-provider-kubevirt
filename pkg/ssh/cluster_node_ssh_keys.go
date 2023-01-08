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
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	clusterutil "sigs.k8s.io/cluster-api/util"
	runtimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	clustercontext "sigs.k8s.io/cluster-api-provider-kubevirt/pkg/context"
)

const (
	sshKeysSecretSuffix = "-ssh-keys"
)

// ClusterNodeSshKeys is a struct containing nodes ssh keys.
type ClusterNodeSshKeys struct {
	ClusterContext *clustercontext.ClusterContext
	Client         runtimeclient.Client
	PublicKey      []byte // in the format "ssh-rsa ...", base64 encoded
	PrivateKey     []byte // in PEM format
}

// NewClusterNodeSshKeys creates a new struct for cluster nodes ssh keys
func NewClusterNodeSshKeys(clusterContext *clustercontext.ClusterContext, client runtimeclient.Client) *ClusterNodeSshKeys {
	return &ClusterNodeSshKeys{
		ClusterContext: clusterContext,
		Client:         client,
	}
}

// GenerateNewKeys generates a new pair of ssh keys
func (c *ClusterNodeSshKeys) GenerateNewKeys() error {
	if pub, key, err := generateKeys(); err != nil {
		return errors.Wrap(err, "failed to generate new ssh keys")
	} else {
		c.PublicKey = pub
		c.PrivateKey = key
		return nil
	}
}

// PersistKeysToSecret persists public and private keys to secret
func (c *ClusterNodeSshKeys) PersistKeysToSecret() (*corev1.Secret, error) {
	if c.PublicKey == nil || c.PrivateKey == nil {
		return nil, errors.New("cannot persist nil values to secret")
	}

	newSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      c.ClusterContext.KubevirtCluster.Name + sshKeysSecretSuffix,
			Namespace: c.ClusterContext.KubevirtCluster.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(c.ClusterContext.Context, c.Client, newSecret, func() error {
		if newSecret.Labels == nil {
			newSecret.Labels = map[string]string{}
		}

		newSecret.Labels[clusterv1.ClusterLabelName] = c.ClusterContext.Cluster.Name
		newSecret.Type = clusterv1.ClusterSecretType
		if newSecret.Data == nil {
			newSecret.Data = map[string][]byte{}
		}

		_, exists := newSecret.Data["key"]
		if !exists {
			newSecret.Data["pub"] = c.PublicKey
			newSecret.Data["key"] = c.PrivateKey
		}

		newSecret.SetOwnerReferences(clusterutil.EnsureOwnerRef(
			newSecret.OwnerReferences,
			metav1.OwnerReference{
				APIVersion: c.ClusterContext.KubevirtCluster.APIVersion,
				Kind:       c.ClusterContext.KubevirtCluster.Kind,
				Name:       c.ClusterContext.KubevirtCluster.Name,
				UID:        c.ClusterContext.KubevirtCluster.UID,
			}))
		return nil
	})
	if err != nil {
		return nil, errors.Wrapf(err, "failed to reconcile ssh keys secret for cluster")
	}

	return newSecret, nil
}

// GetKeysDataSecret retrieves a pointer to the ssh keys secret.
func (c *ClusterNodeSshKeys) GetKeysDataSecret() (*corev1.Secret, error) {
	keysSecretName := c.sshKeysSecretName()
	if keysSecretName == "" {
		return nil, errors.New("ssh keys secret data has not been set in KubevirtCluster.Spec.SshKeys")
	}

	s := &corev1.Secret{}
	objectKey := runtimeclient.ObjectKey{Namespace: c.ClusterContext.KubevirtCluster.Namespace, Name: keysSecretName}
	if err := c.Client.Get(c.ClusterContext.Context, objectKey, s); err != nil {
		return nil, errors.Wrapf(err, "failed to fetch secret %s/%s", c.ClusterContext.KubevirtCluster.Namespace, keysSecretName)
	}
	return s, nil
}

// IsPersistedToSecret checks if a secret with ssh keys already exists, and contains key values
func (c *ClusterNodeSshKeys) IsPersistedToSecret() bool {
	if sshKeysSecret, err := c.GetKeysDataSecret(); err == nil {
		return sshKeysSecret.Data != nil && sshKeysSecret.Data["pub"] != nil && sshKeysSecret.Data["key"] != nil
	}

	return false
}

// FetchPersistedKeysFromSecret fetches public and private keys from secret
// note, the public key is base64 encoded "ssh-rsa ..." string
// note, the private key is in PEM format
func (c *ClusterNodeSshKeys) FetchPersistedKeysFromSecret() error {
	if !c.IsPersistedToSecret() {
		return errors.New("keys have not been persisted to secret yet")
	}

	sshKeysSecret, _ := c.GetKeysDataSecret()
	if pub, ok := sshKeysSecret.Data["pub"]; !ok {
		return errors.New("error retrieving secret data: pub value is missing")
	} else {
		c.PublicKey = pub
	}
	if key, ok := sshKeysSecret.Data["key"]; !ok {
		return errors.New("error retrieving secret data: key value is missing")
	} else {
		c.PrivateKey = key
	}

	return nil
}

// sshKeysSecretName returns the name of ssh keys secret
func (c *ClusterNodeSshKeys) sshKeysSecretName() string {
	sshKeysSecretName := c.ClusterContext.KubevirtCluster.Spec.SshKeys.DataSecretName
	if sshKeysSecretName == nil {
		return ""
	}

	return *c.ClusterContext.KubevirtCluster.Spec.SshKeys.DataSecretName
}
