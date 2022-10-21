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

package controllers

import (
	gocontext "context"
	"fmt"
	"regexp"
	"time"

	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	kubevirtv1 "kubevirt.io/api/core/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	capierrors "sigs.k8s.io/cluster-api/errors"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/annotations"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/cluster-api/util/predicates"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"

	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha1"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/context"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/infracluster"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/kubevirt"
	kubevirthandler "sigs.k8s.io/cluster-api-provider-kubevirt/pkg/kubevirt"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/ssh"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/workloadcluster"
)

// KubevirtMachineReconciler reconciles a KubevirtMachine object.
type KubevirtMachineReconciler struct {
	client.Client
	InfraCluster    infracluster.InfraCluster
	WorkloadCluster workloadcluster.WorkloadCluster
	MachineFactory  kubevirt.MachineFactory
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=kubevirtmachines,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=kubevirtmachines/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters;machines,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets;,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachines;,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachineinstances;,verbs=get;list;watch

// Reconcile handles KubevirtMachine events.
func (r *KubevirtMachineReconciler) Reconcile(goctx gocontext.Context, req ctrl.Request) (_ ctrl.Result, rerr error) {
	log := ctrl.LoggerFrom(goctx)

	// Fetch the KubevirtMachine instance.
	kubevirtMachine := &infrav1.KubevirtMachine{}
	if err := r.Client.Get(goctx, req.NamespacedName, kubevirtMachine); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Fetch the Machine.
	machine, err := util.GetOwnerMachine(goctx, r.Client, kubevirtMachine.ObjectMeta)
	if err != nil {
		return ctrl.Result{}, err
	}
	if machine == nil {
		log.Info("Waiting for Machine Controller to set OwnerRef on KubevirtMachine")
		return ctrl.Result{}, nil
	}

	log = log.WithValues("machine", machine.Name)

	// Handle deleted machines
	if !kubevirtMachine.ObjectMeta.DeletionTimestamp.IsZero() {
		// Create the machine context for this request.
		// Deletion shouldn't require the presence of a
		// cluster or kubevirtcluster object as those objects
		// may have already been removed.
		machineContext := &context.MachineContext{
			Context:         goctx,
			Machine:         machine,
			KubevirtMachine: kubevirtMachine,
			Logger:          ctrl.LoggerFrom(goctx).WithName(req.Namespace).WithName(req.Name),
		}
		return r.reconcileDelete(machineContext)
	}

	// Fetch the Cluster.
	cluster, err := util.GetClusterFromMetadata(goctx, r.Client, machine.ObjectMeta)
	if err != nil {
		log.Info("KubevirtMachine owner Machine is missing cluster label or cluster does not exist")
		return ctrl.Result{}, err
	}
	if cluster == nil {
		log.Info(fmt.Sprintf("Please associate this machine with a cluster using the label %s: <name of cluster>", clusterv1.ClusterLabelName))
		return ctrl.Result{}, nil
	}

	log = log.WithValues("cluster", cluster.Name)

	// Fetch the KubevirtCluster.
	kubevirtCluster := &infrav1.KubevirtCluster{}
	kubevirtClusterName := client.ObjectKey{
		Namespace: kubevirtMachine.Namespace,
		Name:      cluster.Spec.InfrastructureRef.Name,
	}
	if err := r.Client.Get(goctx, kubevirtClusterName, kubevirtCluster); err != nil {
		log.Info("KubevirtCluster is not available yet")
		return ctrl.Result{}, nil
	}

	log = log.WithValues("kubevirt-cluster", kubevirtCluster.Name)

	// Create the machine context for this request.
	machineContext := &context.MachineContext{
		Context:         goctx,
		Cluster:         cluster,
		KubevirtCluster: kubevirtCluster,
		Machine:         machine,
		KubevirtMachine: kubevirtMachine,
		Logger:          ctrl.LoggerFrom(goctx).WithName(req.Namespace).WithName(req.Name),
	}

	// Initialize the patch helper
	patchHelper, err := patch.NewHelper(kubevirtMachine, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Always attempt to Patch the KubevirtMachine object and status after each reconciliation.
	defer func() {
		if err := machineContext.PatchKubevirtMachine(patchHelper); err != nil {
			machineContext.Logger.Error(err, "failed to patch KubevirtMachine")
			if rerr == nil {
				rerr = err
			}
		}
	}()

	// Add finalizer first if not exist to avoid the race condition between init and delete
	if !controllerutil.ContainsFinalizer(kubevirtMachine, infrav1.MachineFinalizer) {
		controllerutil.AddFinalizer(kubevirtMachine, infrav1.MachineFinalizer)
		return ctrl.Result{}, nil
	}

	// Check if the infrastructure is ready, otherwise return and wait for the cluster object to be updated
	if !cluster.Status.InfrastructureReady {
		log.Info("Waiting for KubevirtCluster Controller to create cluster infrastructure")
		conditions.MarkFalse(kubevirtMachine, infrav1.VMProvisionedCondition, infrav1.WaitingForClusterInfrastructureReason, clusterv1.ConditionSeverityInfo, "")
		return ctrl.Result{}, nil
	}

	// Handle non-deleted machines
	res, err := r.reconcileNormal(machineContext)

	if res.IsZero() && err == nil {
		// Update the providerID on the Node
		// The ProviderID on the Node and the providerID on  the KubevirtMachine are used to set the NodeRef
		// This code is needed here as long as there is no Kubevirt cloud provider setting the providerID in the node
		return r.updateNodeProviderID(machineContext)
	}

	return res, err
}

func (r *KubevirtMachineReconciler) reconcileNormal(ctx *context.MachineContext) (res ctrl.Result, retErr error) {

	// Make sure bootstrap data is available and populated.
	if ctx.Machine.Spec.Bootstrap.DataSecretName == nil {
		if !util.IsControlPlaneMachine(ctx.Machine) && !conditions.IsTrue(ctx.Cluster, clusterv1.ControlPlaneInitializedCondition) {
			ctx.Logger.Info("Waiting for the control plane to be initialized...")
			conditions.MarkFalse(ctx.KubevirtMachine, infrav1.VMProvisionedCondition, clusterv1.WaitingForControlPlaneAvailableReason, clusterv1.ConditionSeverityInfo, "")
			return ctrl.Result{}, nil
		}

		ctx.Logger.Info("Waiting for Machine.Spec.Bootstrap.DataSecretName...")
		conditions.MarkFalse(ctx.KubevirtMachine, infrav1.VMProvisionedCondition, infrav1.WaitingForBootstrapDataReason, clusterv1.ConditionSeverityInfo, "")
		return ctrl.Result{}, nil
	}

	// Fetch SSH keys to be used for cluster nodes, and update bootstrap script cloud-init with public key
	var clusterNodeSshKeys *ssh.ClusterNodeSshKeys

	if !annotations.IsExternallyManaged(ctx.KubevirtCluster) {
		clusterNodeSshKeys = ssh.NewClusterNodeSshKeys(ctx.ClusterContext(), r.Client)
		if persisted := clusterNodeSshKeys.IsPersistedToSecret(); !persisted {
			ctx.Logger.Info("Waiting for ssh keys data secret to be created by KubevirtCluster controller...")
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
		if err := clusterNodeSshKeys.FetchPersistedKeysFromSecret(); err != nil {
			return ctrl.Result{}, errors.Wrap(err, "failed to fetch ssh keys for cluster nodes")
		}
	}

	// Default the infra cluster secret ref when the
	// machine does not have one set.
	if ctx.KubevirtMachine.Spec.InfraClusterSecretRef == nil {
		ctx.KubevirtMachine.Spec.InfraClusterSecretRef = ctx.KubevirtCluster.Spec.InfraClusterSecretRef
	}

	infraClusterClient, infraClusterNamespace, err := r.InfraCluster.GenerateInfraClusterClient(ctx.KubevirtMachine.Spec.InfraClusterSecretRef, ctx.KubevirtMachine.Namespace, ctx.Context)
	if err != nil {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, errors.Wrap(err, "failed to generate infra cluster client")
	}

	// If there is not a namespace explicitly set on the vm template, then
	// use the infra namespace as a default. For internal clusters, the infraNamespace
	// will be the same as the KubeVirtCluster object, for external clusters the
	// infraNamespace will attempt to be detected from the infraClusterSecretRef's
	// kubeconfig
	vmNamespace := ctx.KubevirtMachine.Spec.VirtualMachineTemplate.ObjectMeta.Namespace
	if vmNamespace == "" {
		vmNamespace = infraClusterNamespace
	}

	if infraClusterClient == nil {
		ctx.Logger.Info("Waiting for infra cluster client...")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	if err := r.reconcileKubevirtBootstrapSecret(ctx, infraClusterClient, vmNamespace, clusterNodeSshKeys); err != nil {
		conditions.MarkFalse(ctx.KubevirtMachine, infrav1.VMProvisionedCondition, infrav1.WaitingForBootstrapDataReason, clusterv1.ConditionSeverityInfo, "")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, errors.Wrap(err, "failed to fetch kubevirt bootstrap secret")
	}

	// Create a helper for managing the KubeVirt VM hosting the machine.
	externalMachine, err := r.MachineFactory.NewMachine(ctx, infraClusterClient, vmNamespace, clusterNodeSshKeys)
	if err != nil {
		return ctrl.Result{}, errors.Wrapf(err, "failed to create helper for managing the externalMachine")
	}

	isTerminal, terminalReason, err := externalMachine.IsTerminal()
	if err != nil {
		return ctrl.Result{}, errors.Wrapf(err, "failed checking VM for terminal state")
	}
	if isTerminal {
		failureErr := capierrors.UpdateMachineError
		ctx.KubevirtMachine.Status.FailureReason = &failureErr
		ctx.KubevirtMachine.Status.FailureMessage = &terminalReason
	}

	// Provision the underlying VM if not existing
	if !isTerminal && !externalMachine.Exists() {
		ctx.KubevirtMachine.Status.Ready = false
		if err := externalMachine.Create(ctx.Context); err != nil {
			return ctrl.Result{}, errors.Wrap(err, "failed to create VM instance")
		}
		ctx.Logger.Info("VM Created, waiting on vm to be provisioned.")
		return ctrl.Result{RequeueAfter: 20 * time.Second}, nil
	}

	// Checks to see if a VM's active VMI is ready or not
	if externalMachine.IsReady() {
		// Mark VMProvisionedCondition to indicate that the VM has successfully started
		conditions.MarkTrue(ctx.KubevirtMachine, infrav1.VMProvisionedCondition)
	} else {
		// Waiting for VM to boot
		ctx.KubevirtMachine.Status.Ready = false
		ctx.Logger.Info("KubeVirt VM is not fully provisioned and running...")
		return ctrl.Result{RequeueAfter: 20 * time.Second}, nil
	}

	ipAddress := externalMachine.Address()
	if ipAddress == "" {
		ctx.Logger.Info(fmt.Sprintf("KubevirtMachine %s: Got empty ipAddress, requeue", ctx.KubevirtMachine.Name))
		ctx.KubevirtMachine.Status.Ready = false
		return ctrl.Result{RequeueAfter: 20 * time.Second}, nil
	}

	if externalMachine.SupportsCheckingIsBootstrapped() && !conditions.IsTrue(ctx.KubevirtMachine, infrav1.BootstrapExecSucceededCondition) {
		if !externalMachine.IsBootstrapped() {
			ctx.Logger.Info("Waiting for underlying VM to bootstrap...")
			conditions.MarkFalse(ctx.KubevirtMachine, infrav1.BootstrapExecSucceededCondition, infrav1.BootstrapFailedReason, clusterv1.ConditionSeverityWarning, "VM not bootstrapped yet")
			ctx.KubevirtMachine.Status.Ready = false
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
		// Update the condition BootstrapExecSucceededCondition
		conditions.MarkTrue(ctx.KubevirtMachine, infrav1.BootstrapExecSucceededCondition)
		ctx.Logger.Info("Underlying VM has boostrapped.")
	}

	ctx.KubevirtMachine.Status.Addresses = []clusterv1.MachineAddress{
		{
			Type:    clusterv1.MachineHostName,
			Address: ctx.KubevirtMachine.Name,
		},
		{
			Type:    clusterv1.MachineInternalIP,
			Address: ipAddress,
		},
		{
			Type:    clusterv1.MachineExternalIP,
			Address: ipAddress,
		},
		{
			Type:    clusterv1.MachineInternalDNS,
			Address: ctx.KubevirtMachine.Name,
		},
	}

	if ctx.KubevirtMachine.Spec.ProviderID == nil || *ctx.KubevirtMachine.Spec.ProviderID == "" {
		providerID, err := externalMachine.GenerateProviderID()
		if err != nil {
			ctx.Logger.Error(err, "Failed to patch node with provider id.")
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}

		// Set ProviderID so the Cluster API Machine Controller can pull it.
		ctx.KubevirtMachine.Spec.ProviderID = &providerID
	}

	// Ready should reflect if the VMI is ready or not
	if externalMachine.IsReady() {
		ctx.KubevirtMachine.Status.Ready = true
	} else {
		ctx.KubevirtMachine.Status.Ready = false
	}

	return ctrl.Result{}, nil
}

func (r *KubevirtMachineReconciler) updateNodeProviderID(ctx *context.MachineContext) (ctrl.Result, error) {
	// If the provider ID is already updated on the Node, return
	if ctx.KubevirtMachine.Status.NodeUpdated {
		return ctrl.Result{}, nil
	}

	workloadClusterClient, err := r.WorkloadCluster.GenerateWorkloadClusterClient(ctx)
	if err != nil {
		ctx.Logger.Error(err, "Workload cluster client is not available")
	}
	if workloadClusterClient == nil {
		ctx.Logger.Info("Waiting for workload cluster client...")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// using workload cluster client, get the corresponding cluster node
	workloadClusterNode := &corev1.Node{}
	workloadClusterNodeKey := client.ObjectKey{Namespace: ctx.KubevirtMachine.Namespace, Name: ctx.KubevirtMachine.Name}
	if err := workloadClusterClient.Get(ctx, workloadClusterNodeKey, workloadClusterNode); err != nil {
		if apierrors.IsNotFound(err) {
			ctx.Logger.Info(fmt.Sprintf("Waiting for workload cluster node to appear for machine %s/%s...", ctx.KubevirtMachine.Namespace, ctx.KubevirtMachine.Name))
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		} else {
			return ctrl.Result{RequeueAfter: 5 * time.Second}, errors.Wrapf(err, "failed to fetch workload cluster node")
		}
	}

	if workloadClusterNode.Spec.ProviderID == *ctx.KubevirtMachine.Spec.ProviderID {
		// Node is already updated, return
		return ctrl.Result{}, nil
	}

	// Patch node with provider id.
	// Usually a cloud provider will do this, but there is no cloud provider for KubeVirt.
	ctx.Logger.Info("Patching node with provider id...")

	// using workload cluster client, patch cluster node
	patchStr := fmt.Sprintf(`{"spec": {"providerID": "%s"}}`, *ctx.KubevirtMachine.Spec.ProviderID)
	mergePatch := client.RawPatch(types.MergePatchType, []byte(patchStr))
	if err := workloadClusterClient.Patch(gocontext.TODO(), workloadClusterNode, mergePatch); err != nil {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, errors.Wrapf(err, "failed to patch workload cluster node")
	}
	ctx.KubevirtMachine.Status.NodeUpdated = true

	return ctrl.Result{}, nil
}

func (r *KubevirtMachineReconciler) reconcileDelete(ctx *context.MachineContext) (ctrl.Result, error) {

	patchHelper, err := patch.NewHelper(ctx.KubevirtMachine, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}

	infraClusterClient, infraClusterNamespace, err := r.InfraCluster.GenerateInfraClusterClient(ctx.KubevirtMachine.Spec.InfraClusterSecretRef, ctx.KubevirtMachine.Namespace, ctx.Context)
	if err != nil {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, errors.Wrap(err, "failed to generate infra cluster client")
	}
	if infraClusterClient == nil {
		ctx.Logger.Info("Waiting for infra cluster client...")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// If there is not a namespace explicitly set on the vm template, then
	// use the infra namespace as a default. For internal clusters, the infraNamespace
	// will be the same as the KubeVirtCluster object, for external clusters the
	// infraNamespace will attempt to be detected from the infraClusterSecretRef's
	// kubeconfig
	vmNamespace := ctx.KubevirtMachine.Spec.VirtualMachineTemplate.ObjectMeta.Namespace
	if vmNamespace == "" {
		vmNamespace = infraClusterNamespace
	}

	ctx.Logger.Info("Deleting VM bootstrap secret...")
	if err := r.deleteKubevirtBootstrapSecret(ctx, infraClusterClient, vmNamespace); err != nil {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, errors.Wrap(err, "failed to delete bootstrap secret")
	}

	ctx.Logger.Info("Deleting VM...")
	externalMachine, err := kubevirthandler.NewMachine(ctx, infraClusterClient, vmNamespace, nil)
	if err != nil {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, errors.Wrap(err, "failed to create helper for externalMachine access")
	}

	if externalMachine.Exists() {
		if err := externalMachine.Delete(); err != nil {
			return ctrl.Result{RequeueAfter: 10 * time.Second}, errors.Wrap(err, "failed to delete VM")
		}
	}

	// Machine is deleted so remove the finalizer.
	controllerutil.RemoveFinalizer(ctx.KubevirtMachine, infrav1.MachineFinalizer)

	// Set the VMProvisionedCondition reporting delete is started, and attempt to issue a patch in
	// order to make this visible to the users.
	conditions.MarkFalse(ctx.KubevirtMachine, infrav1.VMProvisionedCondition, clusterv1.DeletingReason, clusterv1.ConditionSeverityInfo, "")
	if err := ctx.PatchKubevirtMachine(patchHelper); err != nil {
		if !apierrors.IsNotFound(utilerrors.Reduce(err)) {
			return ctrl.Result{}, errors.Wrap(err, "failed to patch KubevirtMachine")
		}
	}

	return ctrl.Result{}, nil
}

// SetupWithManager will add watches for this controller.
func (r *KubevirtMachineReconciler) SetupWithManager(goctx gocontext.Context, mgr ctrl.Manager, options controller.Options) error {
	clusterToKubevirtMachines, err := util.ClusterToObjectsMapper(mgr.GetClient(), &infrav1.KubevirtMachineList{}, mgr.GetScheme())
	if err != nil {
		return err
	}

	c, err := ctrl.NewControllerManagedBy(mgr).
		For(&infrav1.KubevirtMachine{}).
		WithOptions(options).
		WithEventFilter(predicates.ResourceNotPaused(ctrl.LoggerFrom(goctx))).
		Watches(
			&source.Kind{Type: &clusterv1.Machine{}},
			handler.EnqueueRequestsFromMapFunc(util.MachineToInfrastructureMapFunc(infrav1.GroupVersion.WithKind("KubevirtMachine"))),
		).
		Watches(
			&source.Kind{Type: &infrav1.KubevirtCluster{}},
			handler.EnqueueRequestsFromMapFunc(r.KubevirtClusterToKubevirtMachines),
		).
		Watches(
			&source.Kind{Type: &kubevirtv1.VirtualMachineInstance{}},
			handler.EnqueueRequestsFromMapFunc(r.VMIToKubevirtMachines),
		).
		Watches(
			&source.Kind{Type: &kubevirtv1.VirtualMachine{}},
			handler.EnqueueRequestsFromMapFunc(r.VMToKubevirtMachines),
		).
		Build(r)
	if err != nil {
		return err
	}
	return c.Watch(
		&source.Kind{Type: &clusterv1.Cluster{}},
		handler.EnqueueRequestsFromMapFunc(clusterToKubevirtMachines),
		predicates.ClusterUnpausedAndInfrastructureReady(ctrl.LoggerFrom(goctx)),
	)
}

func (r *KubevirtMachineReconciler) VMIToKubevirtMachines(o client.Object) []ctrl.Request {
	var result []ctrl.Request
	vmi, ok := o.(*kubevirtv1.VirtualMachineInstance)
	if !ok {
		panic(fmt.Sprintf("Expected a VirtualMachineInstance but got a %T", o))
	}

	machineNamespace, exists := vmi.Labels[infrav1.KubevirtMachineNamespaceLabel]
	if !exists {
		return result
	}
	machineName, exists := vmi.Labels[infrav1.KubevirtMachineNameLabel]
	if !exists {
		return result
	}

	name := client.ObjectKey{Namespace: machineNamespace, Name: machineName}
	result = append(result, ctrl.Request{NamespacedName: name})

	return result
}

func (r *KubevirtMachineReconciler) VMToKubevirtMachines(o client.Object) []ctrl.Request {
	var result []ctrl.Request
	vm, ok := o.(*kubevirtv1.VirtualMachine)
	if !ok {
		panic(fmt.Sprintf("Expected a VirtualMachine but got a %T", o))
	}

	machineNamespace, exists := vm.Labels[infrav1.KubevirtMachineNamespaceLabel]
	if !exists {
		return result
	}
	machineName, exists := vm.Labels[infrav1.KubevirtMachineNameLabel]
	if !exists {
		return result
	}

	name := client.ObjectKey{Namespace: machineNamespace, Name: machineName}
	result = append(result, ctrl.Request{NamespacedName: name})

	return result
}

// KubevirtClusterToKubevirtMachines is a handler.ToRequestsFunc to be used to enqueue
// requests for reconciliation of KubevirtMachines.
func (r *KubevirtMachineReconciler) KubevirtClusterToKubevirtMachines(o client.Object) []ctrl.Request {
	var result []ctrl.Request
	c, ok := o.(*infrav1.KubevirtCluster)
	if !ok {
		panic(fmt.Sprintf("Expected a KubevirtCluster but got a %T", o))
	}

	cluster, err := util.GetOwnerCluster(gocontext.TODO(), r.Client, c.ObjectMeta)
	switch {
	case apierrors.IsNotFound(err) || cluster == nil:
		return result
	case err != nil:
		return result
	}

	labels := map[string]string{clusterv1.ClusterLabelName: cluster.Name}
	machineList := &clusterv1.MachineList{}
	if err := r.Client.List(gocontext.TODO(), machineList, client.InNamespace(c.Namespace), client.MatchingLabels(labels)); err != nil {
		return nil
	}
	for _, m := range machineList.Items {
		if m.Spec.InfrastructureRef.Name == "" {
			continue
		}
		name := client.ObjectKey{Namespace: m.Namespace, Name: m.Name}
		result = append(result, ctrl.Request{NamespacedName: name})
	}

	return result
}

// reconcileKubevirtBootstrapSecret creates bootstrap cloud-init secret for KubeVirt virtual machines
func (r *KubevirtMachineReconciler) reconcileKubevirtBootstrapSecret(ctx *context.MachineContext, infraClusterClient client.Client, vmNamespace string, sshKeys *ssh.ClusterNodeSshKeys) error {
	if ctx.Machine.Spec.Bootstrap.DataSecretName == nil {
		return errors.New("error retrieving bootstrap data: linked Machine's bootstrap.dataSecretName is nil")
	}

	// Exit early if exists.
	bootstrapDataSecret := &corev1.Secret{}
	bootstrapDataSecretKey := client.ObjectKey{Namespace: vmNamespace, Name: *ctx.Machine.Spec.Bootstrap.DataSecretName + "-userdata"}
	if err := infraClusterClient.Get(ctx, bootstrapDataSecretKey, bootstrapDataSecret); err == nil {
		ctx.BootstrapDataSecret = bootstrapDataSecret
		return nil
	}

	s := &corev1.Secret{}
	key := client.ObjectKey{Namespace: ctx.Machine.GetNamespace(), Name: *ctx.Machine.Spec.Bootstrap.DataSecretName}
	if err := r.Client.Get(ctx, key, s); err != nil {
		return errors.Wrapf(err, "failed to retrieve bootstrap data secret for KubevirtMachine %s/%s", ctx.Machine.GetNamespace(), ctx.Machine.GetName())
	}

	value, ok := s.Data["value"]
	if !ok {
		return errors.New("error retrieving bootstrap data: secret value key is missing")
	}

	if sshKeys != nil {
		var err error
		var modified bool
		if value, modified, err = addCapkUserToCloudInitConfig(value, sshKeys.PublicKey); err != nil {
			return errors.Wrapf(err, "failed to add capk user to KubevirtMachine %s/%s userdata", ctx.Machine.GetNamespace(), ctx.Machine.GetName())
		} else if modified {
			ctx.Logger.Info("Add capk user with ssh config to bootstrap userdata")
		}
	}

	newBootstrapDataSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      s.Name + "-userdata",
			Namespace: vmNamespace,
			Labels:    s.Labels,
		},
	}
	ctx.BootstrapDataSecret = newBootstrapDataSecret

	_, err := controllerutil.CreateOrUpdate(ctx, infraClusterClient, newBootstrapDataSecret, func() error {
		newBootstrapDataSecret.Type = clusterv1.ClusterSecretType
		newBootstrapDataSecret.Data = map[string][]byte{
			"userdata": value,
		}

		return nil
	})

	if err != nil {
		return errors.Wrapf(err, "failed to create kubevirt bootstrap secret for cluster")
	}

	return nil
}

// deleteKubevirtBootstrapSecret deletes bootstrap cloud-init secret for KubeVirt virtual machines
func (r *KubevirtMachineReconciler) deleteKubevirtBootstrapSecret(ctx *context.MachineContext, infraClusterClient client.Client, vmNamespace string) error {

	if ctx.Machine.Spec.Bootstrap.DataSecretName == nil {
		// Machine never got to the point where a bootstrap secret was created
		return nil
	}

	bootstrapDataSecret := &corev1.Secret{}
	bootstrapDataSecretKey := client.ObjectKey{Namespace: vmNamespace, Name: *ctx.Machine.Spec.Bootstrap.DataSecretName + "-userdata"}
	if err := infraClusterClient.Get(ctx, bootstrapDataSecretKey, bootstrapDataSecret); err != nil {
		// the secret does not exist, exit without error
		return nil
	}

	if err := infraClusterClient.Delete(ctx, bootstrapDataSecret); err != nil {
		return errors.Wrapf(err, "failed to delete kubevirt bootstrap secret for cluster")
	}

	return nil
}

// addCapkUserToCloudInitConfig adds the 'capk' user with the provided ssh authorized key to the
// machine cloud-init bootstrap user-data.
// If the user-data is not the expected cloud-init config, then returns the latter content as-is.
// If a capk user is already defined, then overrides it.
// The returned boolean indicates whether the userdata was modified or not.
func addCapkUserToCloudInitConfig(userdata, sshAuthorizedKey []byte) ([]byte, bool, error) {

	// This uses yaml.Node and not an interface{} to preserve the comments, ordering, etc. of the
	// cloud-init user-data (the indentation might be modified and aligned).
	// Note that go yaml nodes are not a direct representation of the logic structure of the content;
	// e.g.
	//  - the 'users' key and the list (aka sequence) of actual users are sibling nodes
	//  - the 'name' key and the name value (like 'capk') are sibling nodes

	root := &yaml.Node{}
	if err := yaml.Unmarshal(userdata, root); err != nil {
		return nil, false, fmt.Errorf("failed to parse userdata yaml: %w", err)
	}

	if root.Kind != yaml.DocumentNode || len(root.Content) != 1 {
		return userdata, false, nil
	}
	data := root.Content[0]
	if data.Kind != yaml.MappingNode || len(data.Content) == 0 {
		return userdata, false, nil
	}

	// This resolves the first comment in the document; which can be associated with different nodes
	// based on how it is written.
	var headerComment string
	for _, headerComment = range []string{root.HeadComment, data.HeadComment, data.Content[0].HeadComment} {
		if headerComment != "" {
			break
		}
	}
	if !regexp.MustCompile(`(?m)^#cloud-config`).MatchString(headerComment) {
		return userdata, false, nil
	}

	var users *yaml.Node
	for i, section := range data.Content {
		if i%2 == 1 && section.Kind == yaml.SequenceNode && data.Content[i-1].Value == "users" {
			users = section
			break
		}
	}

	usersKey, usersWithCapk, err := usersYamlNodes(sshAuthorizedKey)
	if err != nil {
		return nil, false, err
	}

	// If the users section is not defined in the user-data, simply adds the one with the capk user.
	// Otherwise, loops through the users and, either, override the existing capk user or append it
	// to the sequence.
	if users == nil {
		data.Content = append(data.Content, usersKey, usersWithCapk)
	} else {

		for i, user := range users.Content {
			for j, field := range user.Content {
				if j%2 == 1 && user.Content[j-1].Value == "name" {
					if field.Value == "capk" {
						users.Content[i] = usersWithCapk.Content[0]
						ud, err := yaml.Marshal(root)
						return ud, true, err
					}
					break
				}
			}
		}

		users.Content = append(users.Content, usersWithCapk.Content...)
	}

	ud, err := yaml.Marshal(root)
	return ud, true, err
}

// usersYamlNodes generates the yaml.Nodes representing the 'users' key and the sequence of users
// with the capk user and the specified ssh authorized key.
func usersYamlNodes(sshAuthorizedKey []byte) (*yaml.Node, *yaml.Node, error) {
	usersYaml :=
		`users:
- name: capk
  gecos: CAPK User
  sudo: ALL=(ALL) NOPASSWD:ALL
  groups: users, admin
  ssh_authorized_keys:
  - ` + string(sshAuthorizedKey)

	var node yaml.Node
	if err := yaml.Unmarshal([]byte(usersYaml), &node); err != nil {
		return nil, nil, fmt.Errorf("failed to render capk user as valid yaml: %w", err)
	}

	data := node.Content[0].Content
	return data[0], data[1], nil
}
