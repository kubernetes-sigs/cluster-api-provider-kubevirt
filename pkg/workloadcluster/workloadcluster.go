package workloadcluster

import (
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	k8sclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/context"
)

//go:generate mockgen -source=./workloadcluster.go -destination=./mock/workloadcluster_generated.go -package=mock
type WorkloadCluster interface {
	GenerateWorkloadClusterClient(ctx *context.MachineContext) (client.Client, error)
	GenerateWorkloadClusterK8sClient(ctx *context.MachineContext) (k8sclient.Interface, error)
}

func New(client client.Client) WorkloadCluster {
	return &workloadCluster{
		Client: client,
	}
}

// KubevirtMachineReconciler is struct provides workloadCluster access info
type workloadCluster struct {
	client.Client
}

// GenerateWorkloadClusterClient creates a client for workload cluster.
func (w *workloadCluster) GenerateWorkloadClusterClient(ctx *context.MachineContext) (client.Client, error) {
	// get workload cluster kubeconfig
	kubeConfig, err := w.getKubeconfigForWorkloadCluster(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get kubeconfig for workload cluster")
	}

	// generate REST config
	restConfig, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeConfig))
	if err != nil {
		return nil, errors.Wrap(err, "failed to create REST config")
	}

	// create the client
	workloadClusterClient, err := client.New(restConfig, client.Options{Scheme: w.Client.Scheme()})
	if err != nil {
		return nil, errors.Wrap(err, "failed to create workload cluster client")
	}

	return workloadClusterClient, nil
}

// GenerateWorkloadClusterK8sClient creates a kubernetes client for workload cluster.
func (w *workloadCluster) GenerateWorkloadClusterK8sClient(ctx *context.MachineContext) (k8sclient.Interface, error) {
	// get workload cluster kubeconfig
	kubeConfig, err := w.getKubeconfigForWorkloadCluster(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get kubeconfig for workload cluster")
	}

	// generate REST config
	restConfig, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeConfig))
	if err != nil {
		return nil, errors.Wrap(err, "failed to create REST config")
	}

	// create the client
	workloadClusterClient, err := k8sclient.NewForConfig(restConfig)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create workload cluster client")
	}

	return workloadClusterClient, nil
}

// getKubeconfigForWorkloadCluster fetches kubeconfig for workload cluster from the corresponding secret.
func (w *workloadCluster) getKubeconfigForWorkloadCluster(ctx *context.MachineContext) (string, error) {
	// workload cluster kubeconfig can be found in a secret with suffix "-kubeconfig"
	kubeconfigSecret := &corev1.Secret{}
	kubeconfigSecretKey := client.ObjectKey{Namespace: ctx.KubevirtCluster.Namespace, Name: ctx.Cluster.Name + "-kubeconfig"}
	if err := w.Client.Get(ctx, kubeconfigSecretKey, kubeconfigSecret); err != nil {
		return "", errors.Wrapf(err, "failed to fetch kubeconfig for workload cluster")
	}

	// read kubeconfig
	value, ok := kubeconfigSecret.Data["value"]
	if !ok {
		return "", errors.New("error retrieving kubeconfig data: secret value key is missing")
	}

	return string(value), nil
}
