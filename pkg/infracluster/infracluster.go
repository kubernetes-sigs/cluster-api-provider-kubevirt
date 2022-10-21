package infracluster

import (
	gocontext "context"
	"strings"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	k8sclient "sigs.k8s.io/controller-runtime/pkg/client"
)

//go:generate mockgen -source=./infracluster.go -destination=./mock/infracluster_generated.go -package=mock
type InfraCluster interface {
	GenerateInfraClusterClient(infraClusterSecretRef *corev1.ObjectReference, ownerNamespace string, context gocontext.Context) (k8sclient.Client, string, error)
}

// ClientFactoryFunc defines the function to create a new client
type ClientFactoryFunc func(config *rest.Config, options k8sclient.Options) (k8sclient.Client, error)

// New creates new InfraCluster instance
func New(client k8sclient.Client) InfraCluster {
	return NewWithFactory(client, k8sclient.New)
}

// NewWithFactory creates new InfraCluster instance that uses the provided client factory function.
func NewWithFactory(client k8sclient.Client, factory ClientFactoryFunc) InfraCluster {
	return &infraCluster{
		Client:        client,
		ClientFactory: factory,
	}
}

type infraCluster struct {
	k8sclient.Client
	ClientFactory ClientFactoryFunc
}

// GenerateInfraClusterClient creates a client for infra cluster.
func (w *infraCluster) GenerateInfraClusterClient(infraClusterSecretRef *corev1.ObjectReference, ownerNamespace string, context gocontext.Context) (k8sclient.Client, string, error) {
	if infraClusterSecretRef == nil {
		return w.Client, ownerNamespace, nil
	}

	infraKubeconfigSecret := &corev1.Secret{}
	secretNamespace := infraClusterSecretRef.Namespace
	if secretNamespace == "" {
		secretNamespace = ownerNamespace
	}
	infraKubeconfigSecretKey := k8sclient.ObjectKey{Namespace: secretNamespace, Name: infraClusterSecretRef.Name}
	if err := w.Client.Get(context, infraKubeconfigSecretKey, infraKubeconfigSecret); err != nil {
		return nil, "", errors.Wrapf(err, "failed to fetch infra kubeconfig secret %s/%s", infraClusterSecretRef.Namespace, infraClusterSecretRef.Name)
	}

	kubeConfig, ok := infraKubeconfigSecret.Data["kubeconfig"]
	if !ok {
		return nil, "", errors.New("failed to retrieve infra kubeconfig from secret: 'kubeconfig' key is missing")
	}

	clientConfig, err := clientcmd.NewClientConfigFromBytes(kubeConfig)
	if err != nil {
		return nil, "", errors.Wrap(err, "failed to create K8s-API client config")
	}

	namespace, _, err := clientConfig.Namespace()
	if err != nil {
		return nil, "", errors.Wrap(err, "failed to resolve namespace from client config")
	}
	if namespaceBytes, ok := infraKubeconfigSecret.Data["namespace"]; ok {
		namespace = string(namespaceBytes)
		namespace = strings.TrimSpace(namespace)
	}

	restConfig, err := clientConfig.ClientConfig()
	if err != nil {
		return nil, "", errors.Wrap(err, "failed to create REST config")
	}

	infraClusterClient, err := w.ClientFactory(restConfig, k8sclient.Options{Scheme: w.Client.Scheme()})
	if err != nil {
		return nil, "", errors.Wrap(err, "failed to create infra cluster client")
	}

	return infraClusterClient, namespace, nil
}
