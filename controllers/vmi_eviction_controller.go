package controllers

import (
	goContext "context"
	"fmt"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"time"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubedrain "k8s.io/kubectl/pkg/drain"
	kubevirtv1 "kubevirt.io/api/core/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/controllers/noderefutil"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha1"
	context "sigs.k8s.io/cluster-api-provider-kubevirt/pkg/context"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/workloadcluster"
)

const (
	vmiDeleteGraceTimeoutDurationSeconds = 600 // 10 minutes
)

type VmiEvictionReconciler struct {
	client.Client
	workloadCluster workloadcluster.WorkloadCluster
}

// NewVmiEvictionReconciler creates a new VmiEvictionReconciler
func NewVmiEvictionReconciler(cl client.Client) *VmiEvictionReconciler {
	return &VmiEvictionReconciler{Client: cl, workloadCluster: workloadcluster.New(cl)}
}

// SetupWithManager will add watches for this controller.
func (r *VmiEvictionReconciler) SetupWithManager(ctx goContext.Context, mgr ctrl.Manager) error {
	selector, err := getLabelPredicate()

	if err != nil {
		return fmt.Errorf("can't setup the VMI eviction controller; %w", err)
	}

	_, err = ctrl.NewControllerManagedBy(mgr).
		For(&kubevirtv1.VirtualMachineInstance{}).
		WithEventFilter(selector).
		Build(r)

	return err
}

// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters;machines,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets;,verbs=get;list;watch
// +kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachineinstances;,verbs=get;list;watch;patch;update;delete

// Reconcile handles VMI events.
func (r VmiEvictionReconciler) Reconcile(ctx goContext.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := ctrl.LoggerFrom(ctx)

	vmi := &kubevirtv1.VirtualMachineInstance{}
	err := r.Get(ctx, req.NamespacedName, vmi)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(4).Info(fmt.Sprintf("Can't find virtualMachineInstance %s; it was already deleted.", req.NamespacedName))
			return ctrl.Result{}, nil
		}
		logger.Error(err, fmt.Sprintf("failed to read VMI %s", req.Name))
		return ctrl.Result{}, err
	}

	if !shouldGracefulDeleteVMI(vmi, logger, req.NamespacedName) {
		return ctrl.Result{}, nil
	}

	exceeded, err := r.drainGracePeriodExceeded(ctx, vmi, logger)
	if err != nil {
		return ctrl.Result{}, err
	}

	if !exceeded {
		cluster, err := r.getCluster(ctx, vmi)
		if err != nil {
			logger.Error(err, "Can't get the cluster form the VirtualMachineInstance", "VirtualMachineInstance name", req.NamespacedName)
			return ctrl.Result{}, err
		}

		nodeDrained, retryDuration, err := r.drainNode(ctx, cluster, vmi.Status.EvacuationNodeName, logger)
		if err != nil {
			return ctrl.Result{RequeueAfter: retryDuration}, err
		}

		if !nodeDrained {
			return ctrl.Result{RequeueAfter: retryDuration}, nil
		}
	}

	// now, when the node is drained (or vmiDeleteGraceTimeoutDurationSeconds has passed), we can delete the VMI
	propagationPolicy := metav1.DeletePropagationForeground
	err = r.Delete(ctx, vmi, &client.DeleteOptions{PropagationPolicy: &propagationPolicy})
	if err != nil {
		logger.Error(err, "failed to delete VirtualMachineInstance", "VirtualMachineInstance name", req.NamespacedName)
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func shouldGracefulDeleteVMI(vmi *kubevirtv1.VirtualMachineInstance, logger logr.Logger, namespacedName types.NamespacedName) bool {
	if vmi.DeletionTimestamp != nil {
		logger.V(4).Info("The virtualMachineInstance is already in deletion process. Nothing to do here", "VirtualMachineInstance name", namespacedName)
		return false
	}

	if vmi.Spec.EvictionStrategy == nil || *vmi.Spec.EvictionStrategy != kubevirtv1.EvictionStrategyExternal {
		logger.V(4).Info("Graceful deletion is not supported for virtualMachineInstance. Nothing to do here", "VirtualMachineInstance name", namespacedName)
		return false
	}

	// KubeVirt will set the EvacuationNodeName field in case of guest node eviction. If the field is not set, there is
	// nothing to do.
	if len(vmi.Status.EvacuationNodeName) == 0 {
		logger.V(4).Info("The virtualMachineInstance is not marked for deletion. Nothing to do here", "VirtualMachineInstance name", namespacedName)
		return false
	}

	return true
}

func (r VmiEvictionReconciler) getCluster(ctx goContext.Context, vmi *kubevirtv1.VirtualMachineInstance) (*clusterv1.Cluster, error) {
	// get cluster from vmi
	clusterNS, ok := vmi.Labels[infrav1.KubevirtMachineNamespaceLabel]
	if !ok {
		return nil, fmt.Errorf("can't find the cluster namespace from the VM; missing %s label", infrav1.KubevirtMachineNamespaceLabel)
	}

	clusterName, ok := vmi.Labels[clusterv1.ClusterLabelName]
	if !ok {
		return nil, fmt.Errorf("can't find the cluster name from the VM; missing %s label", clusterv1.ClusterLabelName)
	}

	cluster := &clusterv1.Cluster{}
	err := r.Get(ctx, client.ObjectKey{Namespace: clusterNS, Name: clusterName}, cluster)
	if err != nil {
		return nil, fmt.Errorf("can't find the cluster %s/%s; %w", clusterNS, clusterName, err)
	}

	return cluster, nil
}

// This functions drains a node from a tenant cluster.
// The function returns 3 values:
// * drain done - boolean
// * retry time, or 0 if not needed
// * error - to be returned if we want to retry
func (r VmiEvictionReconciler) drainNode(goctx goContext.Context, cluster *clusterv1.Cluster, nodeName string, logger logr.Logger) (bool, time.Duration, error) {
	ctx := &context.MachineContext{Context: goctx, KubevirtCluster: &infrav1.KubevirtCluster{ObjectMeta: metav1.ObjectMeta{Namespace: cluster.Namespace, Name: cluster.Name}}}
	kubeClient, err := r.workloadCluster.GenerateWorkloadClusterK8sClient(ctx)
	if err != nil {
		logger.Error(err, "Error creating a remote client while deleting Machine, won't retry")
		return false, 0, nil
	}

	node, err := kubeClient.CoreV1().Nodes().Get(goctx, nodeName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			// If an admin deletes the node directly, we'll end up here.
			logger.Error(err, "Could not find node from noderef, it may have already been deleted")
			return true, 0, nil
		}
		return false, 0, fmt.Errorf("unable to get node %q: %w", nodeName, err)
	}

	drainer := &kubedrain.Helper{
		Client:              kubeClient,
		Ctx:                 ctx,
		Force:               true,
		IgnoreAllDaemonSets: true,
		DeleteEmptyDirData:  true,
		GracePeriodSeconds:  -1,
		// If a pod is not evicted in 20 seconds, retry the eviction next time the
		// machine gets reconciled again (to allow other machines to be reconciled).
		Timeout: 20 * time.Second,
		OnPodDeletedOrEvicted: func(pod *corev1.Pod, usingEviction bool) {
			verbStr := "Deleted"
			if usingEviction {
				verbStr = "Evicted"
			}
			logger.Info(fmt.Sprintf("%s pod from Node", verbStr),
				"pod", fmt.Sprintf("%s/%s", pod.Name, pod.Namespace))
		},
		Out: writer{logger.Info},
		ErrOut: writer{func(msg string, keysAndValues ...interface{}) {
			logger.Error(nil, msg, keysAndValues...)
		}},
	}

	if noderefutil.IsNodeUnreachable(node) {
		// When the node is unreachable and some pods are not evicted for as long as this timeout, we ignore them.
		drainer.SkipWaitForDeleteTimeoutSeconds = 60 * 5 // 5 minutes
	}

	if err = kubedrain.RunCordonOrUncordon(drainer, node, true); err != nil {
		// Machine will be re-reconciled after a cordon failure.
		logger.Error(err, "Cordon failed")
		return false, 0, errors.Errorf("unable to cordon node %s: %v", nodeName, err)
	}

	if err = kubedrain.RunNodeDrain(drainer, node.Name); err != nil {
		// Machine will be re-reconciled after a drain failure.
		logger.Error(err, "Drain failed, retry in 20s", "node name", nodeName)
		return false, 20 * time.Second, nil
	}

	logger.Info("Drain successful", "node name", nodeName)
	return true, 0, nil
}

// wait vmiDeleteGraceTimeoutDurationSeconds to the node to be drained. If this time had passed, don't wait anymore.
func (r VmiEvictionReconciler) drainGracePeriodExceeded(ctx goContext.Context, vmi *kubevirtv1.VirtualMachineInstance, logger logr.Logger) (bool, error) {
	if graceTime, found := vmi.Annotations[infrav1.VmiDeletionGraceTime]; found {
		deletionGraceTime, err := time.Parse(time.RFC3339, graceTime)
		if err != nil { // wrong format - rewrite
			if err = r.setVmiDeletionGraceTime(ctx, vmi, logger); err != nil {
				return false, err
			}
		} else {
			return time.Now().UTC().After(deletionGraceTime), nil
		}
	} else {
		if err := r.setVmiDeletionGraceTime(ctx, vmi, logger); err != nil {
			return false, err
		}
	}

	return false, nil
}

func (r VmiEvictionReconciler) setVmiDeletionGraceTime(ctx goContext.Context, vmi *kubevirtv1.VirtualMachineInstance, logger logr.Logger) error {
	logger.V(2).Info(fmt.Sprintf("setting the %s annotation", infrav1.VmiDeletionGraceTime))
	graceTime := time.Now().Add(vmiDeleteGraceTimeoutDurationSeconds * time.Second).UTC().Format(time.RFC3339)
	patch := fmt.Sprintf(`{"metadata":{"annotations":{"%s": "%s"}}}`, infrav1.VmiDeletionGraceTime, graceTime)
	patchRequest := client.RawPatch(types.MergePatchType, []byte(patch))

	if err := r.Patch(ctx, vmi, patchRequest); err != nil {
		return fmt.Errorf("failed to add the %s annotation to the VMI; %w", infrav1.VmiDeletionGraceTime, err)
	}

	return nil
}

func getLabelPredicate() (predicate.Predicate, error) {
	return predicate.LabelSelectorPredicate(
		metav1.LabelSelector{
			MatchExpressions: []metav1.LabelSelectorRequirement{{
				Key:      infrav1.KubevirtMachineNameLabel,
				Operator: metav1.LabelSelectorOpExists,
				Values:   nil,
			}},
		})
}

// writer implements io.Writer interface as a pass-through for klog.
type writer struct {
	logFunc func(msg string, keysAndValues ...interface{})
}

// Write passes string(p) into writer's logFunc and always returns len(p).
func (w writer) Write(p []byte) (n int, err error) {
	w.logFunc(string(p))
	return len(p), nil
}
