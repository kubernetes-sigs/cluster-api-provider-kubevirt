package infracluster

import (
	"strings"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/context"
)

//go:generate mockgen -source=./infracluster.go -destination=./mock/infracluster_generated.go -package=mock
type InfraCluster interface {
	GenerateInfraClusterClient(ctx *context.ClusterContext) (client.Client, string, error)
}

// New creates new InfraCluster instance
func New(client client.Client) InfraCluster {
	return &infraCluster{
		Client: client,
	}
}

type infraCluster struct {
	client.Client
}

// GenerateInfraClusterClient creates a client for infra cluster.
func (w *infraCluster) GenerateInfraClusterClient(ctx *context.ClusterContext) (client.Client, string, error) {
	infraClusterSecretRef := ctx.KubevirtCluster.Spec.InfraClusterSecretRef

	if infraClusterSecretRef == nil {
		return w.Client, ctx.Cluster.Namespace, nil
	}

	infraKubeconfigSecret := &corev1.Secret{}
	infraKubeconfigSecretKey := client.ObjectKey{Namespace: infraClusterSecretRef.Namespace, Name: infraClusterSecretRef.Name}
	if err := w.Client.Get(ctx.Context, infraKubeconfigSecretKey, infraKubeconfigSecret); err != nil {
		return nil, "", errors.Wrapf(err, "failed to fetch infra kubeconfig secret %s/%s", infraClusterSecretRef.Namespace, infraClusterSecretRef.Name)
	}

	kubeConfig, ok := infraKubeconfigSecret.Data["kubeconfig"]
	if !ok {
		return nil, "", errors.New("Failed to retrieve infra kubeconfig from secret: 'kubeconfig' key is missing.")
	}

	namespace := "default"
	namespaceBytes, ok := infraKubeconfigSecret.Data["namespace"]
	if ok {
		namespace = string(namespaceBytes)
		namespace = strings.TrimSpace(namespace)
	}

	// generate REST config
	restConfig, err := clientcmd.RESTConfigFromKubeConfig(kubeConfig)
	if err != nil {
		return nil, "", errors.Wrap(err, "failed to create REST config")
	}

	// create the client
	infraClusterClient, err := client.New(restConfig, client.Options{Scheme: w.Client.Scheme()})
	if err != nil {
		return nil, "", errors.Wrap(err, "failed to create infra cluster client")
	}

	return infraClusterClient, namespace, nil
}
