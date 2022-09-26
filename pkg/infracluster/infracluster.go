package infracluster

import (
	gocontext "context"
	"fmt"
	"net/netip"
	"strings"
	"time"

	nmstateshared "github.com/nmstate/kubernetes-nmstate/api/shared"
	nmstatev1 "github.com/nmstate/kubernetes-nmstate/api/v1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/context"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/nmstate"
	k8sclient "sigs.k8s.io/controller-runtime/pkg/client"
)

//go:generate mockgen -source=./infracluster.go -destination=./mock/infracluster_generated.go -package=mock
type InfraCluster interface {
	GenerateInfraClusterClient(infraClusterSecretRef *corev1.ObjectReference, ownerNamespace string, context gocontext.Context) (k8sclient.Client, string, error)
	EnsureNetworking(*context.ClusterContext) error
	TeardownNetworking(*context.ClusterContext) error
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

// EnsureInfraNodeNetworking creates the specified infra networking using a
// NodeNetworkConfigurationPolicy per node to specify different IPs
func (w *infraCluster) EnsureNetworking(ctx *context.ClusterContext) error {

	//TODO: Retrieve only the metadata
	nodeList := corev1.NodeList{}
	if err := w.List(ctx, &nodeList); err != nil {
		return err
	}
	for _, node := range nodeList.Items {
		if err := w.ensureNetworkingForNode(ctx, node.Name); err != nil {
			return err
		}
	}
	return nil
}

// TeardownNetworking cleanup the specified infra networking using a
// NodeNetworkConfigurationPolicy per node to specify different IPs
func (w *infraCluster) TeardownNetworking(ctx *context.ClusterContext) error {

	//TODO: Retrieve only the metadata
	nodeList := corev1.NodeList{}
	if err := w.List(ctx, &nodeList); err != nil {
		return err
	}
	for _, node := range nodeList.Items {
		if err := w.teardownNetworkingForNode(ctx, node.Name); err != nil {
			return err
		}
	}
	return nil
}

func (w *infraCluster) ensureNetworkingForNode(ctx *context.ClusterContext, nodeName string) error {
	nncp := nmstatev1.NodeNetworkConfigurationPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name: nmstate.GenerateNNCPName(ctx.Cluster.Name, nodeName),
			Labels: map[string]string{
				"cluster.x-k8s.io/cluster-name": ctx.Cluster.Name,
			},
		},
		Spec: ctx.KubevirtCluster.Spec.InfraClusterNodeNetwork.Setup,
	}
	nncp.Spec.NodeSelector = map[string]string{"kubernetes.io/hostname": nodeName}
	err := w.Get(ctx, types.NamespacedName{Name: nncp.Name}, &nncp)
	if err != nil {
		if apierrors.IsNotFound(err) {
			clusterNetworkPrefix, err := netip.ParsePrefix(ctx.KubevirtCluster.Spec.Network)
			if err != nil {
				return err
			}
			// TODO: give back the IP on errors with a defer or the like
			infraNodeAddress, err := ctx.Ipamer.AcquireIP(ctx, ctx.KubevirtCluster.Spec.Network)
			if err != nil {
				return err
			}
			infraNodeIP := infraNodeAddress.IP.String()
			infraNodeNetworkState, err := nmstate.AddInfraNodeIP(nncp.Spec.DesiredState, infraNodeIP, clusterNetworkPrefix.Bits())
			if err != nil {
				return err
			}
			nncp.Spec.DesiredState = *infraNodeNetworkState
			if err := w.Create(ctx, &nncp); err != nil {
				return err
			}
			//TODO: Wait after create all the NNCPs this will reduce
			//      wait time.
			if err := w.waitNNCPReadiness(ctx, nncp); err != nil {
				return err
			}
		} else {
			return err
		}
	}
	return nil
}

func (w *infraCluster) teardownNetworkingForNode(ctx *context.ClusterContext, nodeName string) error {
	nncp := nmstatev1.NodeNetworkConfigurationPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name: nmstate.GenerateNNCPName(ctx.Cluster.Name, nodeName),
		},
		Spec: ctx.KubevirtCluster.Spec.InfraClusterNodeNetwork.TearDown,
	}
	nncp.Spec.NodeSelector = map[string]string{"kubernetes.io/hostname": nodeName}
	// We cannot modify the NNCP since "capture" section cannot be modified
	if err := w.deleteNNCP(ctx, nncp); err != nil {
		return err
	}
	err := w.Create(ctx, &nncp)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if err := w.waitNNCPReadiness(ctx, nncp); err != nil {
		return err
	}
	if err := w.deleteNNCP(ctx, nncp); err != nil {
		return err
	}
	return nil
}

func (w *infraCluster) deleteNNCP(ctx *context.ClusterContext, nncp nmstatev1.NodeNetworkConfigurationPolicy) error {
	if err := w.Delete(ctx, &nncp); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func (w *infraCluster) waitNNCPReadiness(ctx *context.ClusterContext, nncp nmstatev1.NodeNetworkConfigurationPolicy) error {
	nncpIsReady := func() (bool, error) {
		err := w.Get(ctx, types.NamespacedName{Name: nncp.Name}, &nncp)
		if err != nil {
			// Stil not created retry
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		degraded := nncp.Status.Conditions.Find(nmstateshared.NodeNetworkConfigurationPolicyConditionDegraded)
		if degraded != nil && degraded.Status == corev1.ConditionTrue {
			return false, fmt.Errorf("infra node network configuration degraded: %s, %s", degraded.Reason, degraded.Message)
		}
		available := nncp.Status.Conditions.Find(nmstateshared.NodeNetworkConfigurationPolicyConditionAvailable)
		return available != nil && available.Status == corev1.ConditionTrue, nil
	}

	if err := wait.PollImmediate(5*time.Second, 8*time.Minute, nncpIsReady); err != nil {
		return err
	}
	return nil
}
