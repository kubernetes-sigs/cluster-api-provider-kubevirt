/*
Copyright 2026 The Kubernetes Authors.

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

package controllers

import (
	gocontext "context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	kubevirtv1 "kubevirt.io/api/core/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/annotations"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/cluster-api/util/predicates"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha1"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/infracluster"
)

// resumeGracePeriod is how long the controller stays idle after a pause was
// lifted. The MachineHealthCheck controller was paused for just as long,
// so its verdict on the Machine is stale; this gives it time to look at the
// node again and delete the remediation request if the node has recovered.
const resumeGracePeriod = time.Minute

// restartImpossibleError reports a precondition that retrying cannot fix. Any
// other error from restartVM is transient and retried with backoff.
type restartImpossibleError struct {
	reason string
}

func (e *restartImpossibleError) Error() string {
	return e.reason
}

// KubevirtRemediationReconciler reconciles KubevirtRemediation objects created
// by the Cluster API MachineHealthCheck controller.
//
// Instead of letting CAPI delete and replace an unhealthy Machine, it restarts
// the VirtualMachineInstance backing it: with runStrategy Always the KubeVirt
// VM controller immediately recreates the VMI, which boots with the same name
// and disks. If the Machine is still unhealthy after the configured retries,
// the reconciler sets the OwnerRemediated condition to False, handing the
// Machine back to CAPI for the usual delete-and-replace.
//
// Success is not observed directly: when the node passes its health check
// again, the MachineHealthCheck controller deletes the KubevirtRemediation
// object and reconciliation simply stops.
//
// A restart only preserves the node if its state lives on persistent storage.
// VMs with containerDisk or ephemeral volumes boot from a pristine image after
// the VMI is recreated, so they are handed back to CAPI without restarting.
type KubevirtRemediationReconciler struct {
	client.Client
	InfraCluster infracluster.InfraCluster
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=kubevirtremediations,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=kubevirtremediations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines/status,verbs=update;patch
// +kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachines,verbs=get
// +kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachineinstances,verbs=delete

// Reconcile drives one KubevirtRemediation through its phases.
func (r *KubevirtRemediationReconciler) Reconcile(ctx gocontext.Context, req ctrl.Request) (_ ctrl.Result, rerr error) {
	logger := log.FromContext(ctx)

	remediation := &infrav1.KubevirtRemediation{}
	if err := r.Get(ctx, req.NamespacedName, remediation); err != nil {
		if apierrors.IsNotFound(err) {
			// Deleted by the MachineHealthCheck controller once the Machine is
			// healthy again, or garbage-collected with its owner Machine.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if remediation.Status.Phase == infrav1.PhaseFailed {
		// Terminal: the Machine was handed back to CAPI. The MachineSet will
		// delete the Machine and this object goes with it via its owner ref.
		return ctrl.Result{}, nil
	}

	machine, err := util.GetOwnerMachine(ctx, r.Client, remediation.ObjectMeta)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// The Machine is gone; this object is garbage-collected with it.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, errors.Wrap(err, "failed to get owner Machine")
	}
	if machine == nil {
		// The MachineHealthCheck controller always sets the owner reference at
		// creation time, so this only happens for hand-made objects.
		logger.Info("KubevirtRemediation has no owner Machine, ignoring")
		return ctrl.Result{}, nil
	}
	logger = logger.WithValues("machine", machine.Name)

	if !machine.DeletionTimestamp.IsZero() {
		// Something else is already replacing the Machine; restarting its VM
		// now would only race the deletion.
		return ctrl.Result{}, nil
	}

	cluster, err := util.GetClusterByName(ctx, r.Client, machine.Namespace, machine.Spec.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}

	if annotations.IsPaused(cluster, remediation) || annotations.HasPaused(machine) {
		// Neither restart nor escalate while paused: the MachineHealthCheck
		// controller is paused as well, so nothing re-evaluates the Machine and
		// a timed retry would hit a node that is under maintenance. The Cluster
		// and Machine watches bring this object back once the pause is lifted.
		logger.Info("Cluster, Machine or KubevirtRemediation is paused, skipping remediation")
		return ctrl.Result{}, nil
	}

	if wait := resumeGraceRemaining(cluster, machine); wait > 0 {
		logger.Info("Waiting out the grace period after a pause, so the Machine's health can be re-evaluated", "wait", wait)
		return ctrl.Result{RequeueAfter: wait}, nil
	}

	patchHelper, err := patch.NewHelper(remediation, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}
	defer func() {
		if err := patchHelper.Patch(ctx, remediation); err != nil {
			rerr = kerrors.NewAggregate([]error{rerr, err})
		}
	}()

	strategy := effectiveStrategy(remediation)
	if strategy.Type != infrav1.RebootRemediationType {
		// The enum on the CRD only admits Reboot; anything else means the API
		// grew a strategy this controller does not implement yet.
		return ctrl.Result{}, r.handOverToCAPI(ctx, logger, remediation, machine,
			fmt.Sprintf("unsupported remediation strategy %q", strategy.Type))
	}

	if remediation.Status.Phase == "" {
		remediation.Status.Phase = infrav1.PhaseRunning
	}

	timeout := time.Duration(strategy.TimeoutSeconds) * time.Second

	if remediation.Status.Phase == infrav1.PhaseWaiting {
		// Both fields are written together; without a timestamp there is no
		// window to wait for.
		var remaining time.Duration
		if remediation.Status.LastRemediated != nil {
			remaining = time.Until(remediation.Status.LastRemediated.Add(timeout))
		}
		if remaining > 0 {
			// Still inside the boot window. If the node comes back healthy the
			// MachineHealthCheck deletes this object before the requeue fires.
			return ctrl.Result{RequeueAfter: remaining}, nil
		}
	}

	if remediation.Status.RetryCount > 0 && conditions.IsTrue(machine, clusterv1.MachineHealthCheckSucceededCondition) {
		// The node recovered after the last restart: the MachineHealthCheck
		// already considers the Machine healthy and is about to delete this
		// object. Restarting or replacing it now would undo the recovery. If
		// the verdict flips back, the Machine watch brings us here again.
		//
		// This does not apply to the first restart: the MachineHealthCheck
		// creates the request before it patches the condition, and other
		// creators may request a restart for a fault it does not see.
		logger.Info("Machine passes its health check, waiting for the remediation request to be removed")
		return ctrl.Result{}, nil
	}

	if remediation.Status.Phase == infrav1.PhaseWaiting {
		// Timed out and still unhealthy: retry if budget remains, otherwise
		// give the Machine back.
		if remediation.Status.RetryCount >= strategy.RetryLimit {
			return ctrl.Result{}, r.handOverToCAPI(ctx, logger, remediation, machine,
				fmt.Sprintf("machine still unhealthy after %d restart(s)", remediation.Status.RetryCount))
		}
		remediation.Status.Phase = infrav1.PhaseRunning
	}

	// Phase Running: issue a restart.
	if err := r.restartVM(ctx, machine); err != nil {
		var impossible *restartImpossibleError
		if !errors.As(err, &impossible) {
			// Transient (API or infra cluster hiccup): retry with backoff
			// rather than replacing a Machine over it.
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, r.handOverToCAPI(ctx, logger, remediation, machine, impossible.reason)
	}

	now := metav1.Now()
	remediation.Status.LastRemediated = &now
	remediation.Status.RetryCount++
	remediation.Status.Phase = infrav1.PhaseWaiting
	logger.Info("Restarted VirtualMachineInstance", "retryCount", remediation.Status.RetryCount,
		"retryLimit", strategy.RetryLimit)

	return ctrl.Result{RequeueAfter: timeout}, nil
}

// restartVM deletes the VirtualMachineInstance backing the Machine. With
// runStrategy Always the KubeVirt VM controller recreates it immediately —
// same VM object, same disks, same node name — which is the closest thing to a
// power cycle the KubeVirt API offers to a plain Kubernetes client. (The
// virtctl restart subresource is not reachable through controller-runtime.)
//
// A restartImpossibleError means no restart can bring the node back and the
// caller hands the Machine over to CAPI. Any other error is transient.
func (r *KubevirtRemediationReconciler) restartVM(ctx gocontext.Context, machine *clusterv1.Machine) error {
	kubevirtMachine := &infrav1.KubevirtMachine{}
	kvmKey := types.NamespacedName{Namespace: machine.Namespace, Name: machine.Spec.InfrastructureRef.Name}
	if err := r.Get(ctx, kvmKey, kubevirtMachine); err != nil {
		if apierrors.IsNotFound(err) {
			return &restartImpossibleError{reason: fmt.Sprintf("KubevirtMachine %s not found", kvmKey)}
		}
		return errors.Wrap(err, "failed to get KubevirtMachine")
	}

	infraClient, infraNamespace, err := r.InfraCluster.GenerateInfraClusterClient(
		kubevirtMachine.Spec.InfraClusterSecretRef, kubevirtMachine.Namespace, ctx)
	if err != nil {
		return errors.Wrap(err, "failed to generate infra cluster client")
	}

	// Mirror the namespace defaulting the KubevirtMachine controller applies
	// when it creates the VM.
	vmNamespace := kubevirtMachine.Spec.VirtualMachineTemplate.ObjectMeta.Namespace
	if vmNamespace == "" {
		vmNamespace = infraNamespace
	}
	vmKey := types.NamespacedName{Namespace: vmNamespace, Name: kubevirtMachine.Name}

	vm := &kubevirtv1.VirtualMachine{}
	if err := infraClient.Get(ctx, vmKey, vm); err != nil {
		if apierrors.IsNotFound(err) {
			return &restartImpossibleError{reason: fmt.Sprintf("VirtualMachine %s not found", vmKey)}
		}
		return errors.Wrap(err, "failed to get VirtualMachine")
	}

	runStrategy, err := vm.RunStrategy()
	if err != nil {
		// Only happens when both running and runStrategy are set.
		return &restartImpossibleError{reason: fmt.Sprintf("invalid VM run strategy: %v", err)}
	}
	if runStrategy != kubevirtv1.RunStrategyAlways {
		// Only Always guarantees the VM controller brings the VMI back after
		// we delete it. Under Manual or Halted a deleted VMI stays gone, which
		// would silently turn a restart into an outage.
		return &restartImpossibleError{
			reason: fmt.Sprintf("VM run strategy is %q, restart requires %q", runStrategy, kubevirtv1.RunStrategyAlways),
		}
	}

	if name, found := findNonPersistentVolume(vm); found {
		// The recreated VMI would boot from a pristine image: kubelet
		// credentials and, on control plane nodes, local etcd data are gone,
		// and the bootstrap token needed to join again has usually expired.
		// Such a node cannot come back, so skip straight to replacement.
		return &restartImpossibleError{
			reason: fmt.Sprintf("volume %q does not persist across a restart, restart requires persistent storage", name),
		}
	}

	vmi := &kubevirtv1.VirtualMachineInstance{}
	vmi.Namespace = vmKey.Namespace
	vmi.Name = vmKey.Name
	if err := infraClient.Delete(ctx, vmi); err != nil && !apierrors.IsNotFound(err) {
		return errors.Wrap(err, "failed to delete VirtualMachineInstance")
	}

	return nil
}

// findNonPersistentVolume returns the first containerDisk or ephemeral volume
// of the VM. Those are the volume types a node boots from whose writes are
// discarded when the VirtualMachineInstance is recreated. emptyDisk is scratch
// space by definition and hostDisk depends on where the VMI is scheduled;
// neither is inspected, so they must not hold node state.
func findNonPersistentVolume(vm *kubevirtv1.VirtualMachine) (string, bool) {
	if vm.Spec.Template == nil {
		return "", false
	}
	for _, volume := range vm.Spec.Template.Spec.Volumes {
		if volume.ContainerDisk != nil || volume.Ephemeral != nil {
			return volume.Name, true
		}
	}

	return "", false
}

// resumeGraceRemaining returns how much longer the controller should stay idle
// because a pause was lifted only recently. It relies on the Paused conditions
// Cluster API maintains: the one of the Cluster follows Cluster.spec.paused
// promptly, the one of the Machine also covers the paused annotation on the
// Machine (but can lag behind a Cluster pause for Machines that are not owned
// by a MachineSet).
func resumeGraceRemaining(cluster *clusterv1.Cluster, machine *clusterv1.Machine) time.Duration {
	clusterPaused := conditions.Get(cluster, clusterv1.PausedCondition)
	if annotations.HasPaused(cluster) {
		// The paused annotation on the Cluster object keeps its condition
		// True, but only pauses the Cluster and topology controllers. Machines
		// and MachineHealthChecks keep reconciling, and so does this one. A
		// lift of spec.paused on such a Cluster is then only covered by the
		// condition of the Machine.
		clusterPaused = nil
	}

	return max(
		graceRemaining(clusterPaused),
		graceRemaining(conditions.Get(machine, clusterv1.PausedCondition)),
	)
}

func graceRemaining(paused *metav1.Condition) time.Duration {
	if paused == nil {
		return 0
	}
	if paused.Status == metav1.ConditionTrue {
		// The pause is lifted but the owning controller has not caught up
		// yet; the grace period starts once it does.
		return resumeGracePeriod
	}

	return max(0, resumeGracePeriod-time.Since(paused.LastTransitionTime.Time))
}

// handOverToCAPI marks the remediation as failed and sets the OwnerRemediated
// condition to False on the Machine. The MachineSet controller watches for
// exactly that combination (unhealthy + OwnerRemediated=False) and responds by
// deleting the Machine and creating a replacement — the same outcome the
// MachineHealthCheck would have produced without external remediation.
func (r *KubevirtRemediationReconciler) handOverToCAPI(ctx gocontext.Context, logger logr.Logger,
	remediation *infrav1.KubevirtRemediation, machine *clusterv1.Machine, reason string) error {
	if conditions.IsTrue(machine, clusterv1.MachineHealthCheckSucceededCondition) {
		// The owner only replaces a Machine that fails its health check, and
		// removes the OwnerRemediated condition from one that passes. A
		// brand-new request can get here before the MachineHealthCheck has
		// patched HealthCheckSucceeded=False onto the Machine; the Machine
		// watch brings it back once that happened.
		logger.Info("Machine passes its health check, not handing it back", "reason", reason)
		return nil
	}

	logger.Info("Handing Machine back to Cluster API for replacement", "reason", reason)

	machineHelper, err := patch.NewHelper(machine, r.Client)
	if err != nil {
		return err
	}

	conditions.Set(machine, metav1.Condition{
		Type:    clusterv1.MachineOwnerRemediatedCondition,
		Status:  metav1.ConditionFalse,
		Reason:  clusterv1.MachineOwnerRemediatedWaitingForRemediationReason,
		Message: fmt.Sprintf("KubeVirt remediation gave up: %s", reason),
	})
	if err := machineHelper.Patch(ctx, machine); err != nil {
		return errors.Wrap(err, "failed to patch Machine with OwnerRemediated condition")
	}

	remediation.Status.Phase = infrav1.PhaseFailed

	return nil
}

// effectiveStrategy returns the remediation strategy with API defaults applied,
// so the controller behaves the same for objects that bypassed the CRD
// defaulting (unit tests, direct Go clients).
func effectiveStrategy(remediation *infrav1.KubevirtRemediation) infrav1.RemediationStrategy {
	strategy := infrav1.RemediationStrategy{
		Type:           infrav1.RebootRemediationType,
		RetryLimit:     1,
		TimeoutSeconds: 600,
	}
	if in := remediation.Spec.Strategy; in != nil {
		if in.Type != "" {
			strategy.Type = in.Type
		}
		if in.RetryLimit > 0 {
			strategy.RetryLimit = in.RetryLimit
		}
		if in.TimeoutSeconds > 0 {
			strategy.TimeoutSeconds = in.TimeoutSeconds
		}
	}

	return strategy
}

// SetupWithManager sets up the controller with the Manager.
func (r *KubevirtRemediationReconciler) SetupWithManager(ctx gocontext.Context, mgr ctrl.Manager) error {
	clusterToRemediations, err := util.ClusterToTypedObjectsMapper(mgr.GetClient(), &infrav1.KubevirtRemediationList{}, mgr.GetScheme())
	if err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&infrav1.KubevirtRemediation{}).
		WithEventFilter(predicates.ResourceNotPaused(mgr.GetScheme(), ctrl.LoggerFrom(ctx))).
		Watches(
			&clusterv1.Machine{},
			handler.EnqueueRequestsFromMapFunc(machineToRemediation),
		).
		Watches(
			&clusterv1.Cluster{},
			handler.EnqueueRequestsFromMapFunc(clusterToRemediations),
			builder.WithPredicates(predicates.ClusterUnpaused(mgr.GetScheme(), ctrl.LoggerFrom(ctx))),
		).
		Complete(r)
}

// machineToRemediation maps a Machine to its remediation request. The
// MachineHealthCheck controller names the request after the Machine, so no
// lookup is needed; requests for Machines without one are dropped on Get.
func machineToRemediation(_ gocontext.Context, o client.Object) []reconcile.Request {
	return []reconcile.Request{{NamespacedName: client.ObjectKeyFromObject(o)}}
}
