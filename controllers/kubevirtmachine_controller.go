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
	"encoding/base64"
	"fmt"
	"time"

	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/ssh"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/workloadcluster"

	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha4"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/context"
	kubevirthandler "sigs.k8s.io/cluster-api-provider-kubevirt/pkg/kubevirt"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1alpha4"
	"sigs.k8s.io/cluster-api/util"
	clusterutil "sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/cluster-api/util/predicates"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// KubevirtMachineReconciler reconciles a KubevirtMachine object.
type KubevirtMachineReconciler struct {
	client.Client
	WorkloadCluster workloadcluster.WorkloadCluster
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

	// Handle deleted machines
	if !kubevirtMachine.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(machineContext)
	}

	// Handle non-deleted machines
	if !machineContext.KubevirtMachine.Status.Ready {
		return r.reconcileNormal(machineContext)
	} else {
		// Update the providerID on the Node
		// The ProviderID on the Node and the providerID on  the KubevirtMachine are used to set the NodeRef
		// This code is needed here as long as there is no Kubevirt cloud provider setting the providerID in the node
		return r.updateNodeProviderID(machineContext)
	}
}

func (r *KubevirtMachineReconciler) reconcileNormal(ctx *context.MachineContext) (res ctrl.Result, retErr error) {
	// If the machine is already provisioned, return
	if ctx.KubevirtMachine.Status.Ready {
		return ctrl.Result{}, nil
	}

	// Make sure bootstrap data is available and populated.
	if ctx.Machine.Spec.Bootstrap.DataSecretName == nil {
		if !util.IsControlPlaneMachine(ctx.Machine) && !conditions.IsTrue(ctx.Cluster, clusterv1.ControlPlaneInitializedCondition) {
			ctx.Logger.Info("Waiting for the control plane to be initialized")
			conditions.MarkFalse(ctx.KubevirtMachine, infrav1.VMProvisionedCondition, clusterv1.WaitingForControlPlaneAvailableReason, clusterv1.ConditionSeverityInfo, "")
			return ctrl.Result{}, nil
		}

		ctx.Logger.Info("Waiting for the Bootstrap provider controller to set bootstrap data")
		conditions.MarkFalse(ctx.KubevirtMachine, infrav1.VMProvisionedCondition, infrav1.WaitingForBootstrapDataReason, clusterv1.ConditionSeverityInfo, "")
		return ctrl.Result{}, nil
	}

	clusterContext := &context.ClusterContext{
		Context:         ctx.Context,
		Cluster:         ctx.Cluster,
		KubevirtCluster: ctx.KubevirtCluster,
		Logger:          ctx.Logger,
	}

	// Fetch SSH keys to be used for cluster nodes, and update bootstrap script cloud-init with public key
	clusterNodeSshKeys := ssh.NewClusterNodeSshKeys(clusterContext, r.Client)
	if persisted := clusterNodeSshKeys.IsPersistedToSecret(); !persisted {
		ctx.Logger.Info("Waiting for ssh keys data secret to be created by KubevirtCluster controller...")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
	if err := clusterNodeSshKeys.FetchPersistedKeysFromSecret(); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "failed to fetch ssh keys for cluster nodes")
	}

	if err := r.reconcileKubevirtBootstrapSecret(ctx, clusterNodeSshKeys); err != nil {
		ctx.Logger.Info("Waiting for the Bootstrap provider controller to set bootstrap data")
		conditions.MarkFalse(ctx.KubevirtMachine, infrav1.VMProvisionedCondition, infrav1.WaitingForBootstrapDataReason, clusterv1.ConditionSeverityInfo, "")
		return ctrl.Result{}, nil
	}

	// Create a helper for managing the KubeVirt VM hosting the machine.
	externalMachine, err := kubevirthandler.NewMachine(ctx, r.Client)
	if err != nil {
		return ctrl.Result{}, errors.Wrapf(err, "failed to create helper for managing the externalMachine")
	}

	// Provision the underlying VM if not existing
	if !externalMachine.Exists() {
		ctx.Logger.Info("Creating underlying VM instance...")
		if err := externalMachine.Create(); err != nil {
			return ctrl.Result{}, errors.Wrap(err, "failed to create VM instance")
		}
	}

	vmCommandExecutor := ssh.VMCommandExecutor{
		IPAddress:  externalMachine.Address(),
		PublicKey:  clusterNodeSshKeys.PublicKey,
		PrivateKey: clusterNodeSshKeys.PrivateKey,
	}

	// Wait for VM to boot
	if !externalMachine.IsBooted(vmCommandExecutor) {
		ctx.Logger.Info("Waiting for underlying VM instance to boot...")
		return ctrl.Result{RequeueAfter: 20 * time.Second}, nil
	}

	// Update the VMProvisionedCondition condition
	// NOTE: it is required to create the patch helper at this point, otherwise it won't surface if we issue a patch down in the code
	// (because if we create patch helper after this point the VMProvisionedCondition=True exists both on before and after).
	patchHelper, err := patch.NewHelper(ctx.KubevirtMachine, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}
	conditions.MarkTrue(ctx.KubevirtMachine, infrav1.VMProvisionedCondition)

	// At, this stage, we are ready for bootstrap. However, if the BootstrapExecSucceededCondition is missing we add it and we
	// issue an patch so the user can see the change of state before the bootstrap actually starts.
	// NOTE: usually controller should not rely on status they are setting, but on the observed state; however
	// in this case we are doing this because we explicitly want to give a feedback to users.
	if !conditions.Has(ctx.KubevirtMachine, infrav1.BootstrapExecSucceededCondition) {
		conditions.MarkFalse(ctx.KubevirtMachine, infrav1.BootstrapExecSucceededCondition, infrav1.BootstrappingReason, clusterv1.ConditionSeverityInfo, "")
		if err := ctx.PatchKubevirtMachine(patchHelper); err != nil {
			return ctrl.Result{}, errors.Wrap(err, "failed to patch KubevirtMachine")
		}
	}

	// Wait for VM to bootstrap with Kubernetes
	if !ctx.KubevirtMachine.Spec.Bootstrapped {
		if !externalMachine.IsBootstrapped(vmCommandExecutor) {
			ctx.Logger.Info("Waiting for underlying VM to bootstrap...")
			conditions.MarkFalse(ctx.KubevirtMachine, infrav1.BootstrapExecSucceededCondition, infrav1.BootstrapFailedReason, clusterv1.ConditionSeverityWarning, "VM not bootstrapped yet")
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}

		ctx.KubevirtMachine.Spec.Bootstrapped = true
	}

	// Update the condition BootstrapExecSucceededCondition
	conditions.MarkTrue(ctx.KubevirtMachine, infrav1.BootstrapExecSucceededCondition)

	ipAddress := externalMachine.Address()
	ctx.Logger.Info(fmt.Sprintf("KubevirtMachine %s: Got ipAddress <%s>", ctx.KubevirtMachine.Name, ipAddress))
	if ipAddress == "" {
		ctx.Logger.Info(fmt.Sprintf("KubevirtMachine %s: Got empty ipAddress, requeue", ctx.KubevirtMachine.Name))
		return ctrl.Result{RequeueAfter: 20 * time.Second}, nil
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

	providerID, err := externalMachine.GenerateProviderID()
	if err != nil {
		ctx.Logger.Error(err, "Failed to patch node with provider id...")
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// Set ProviderID so the Cluster API Machine Controller can pull it.
	ctx.KubevirtMachine.Spec.ProviderID = &providerID

	// KubevirtMachine is ready! Set the status and the condition.
	ctx.KubevirtMachine.Status.Ready = true
	conditions.MarkTrue(ctx.KubevirtMachine, infrav1.VMProvisionedCondition)

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
		return ctrl.Result{RequeueAfter: 5 * time.Second}, errors.Wrapf(err, "failed to fetch workload cluster node")
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
	// Set the VMProvisionedCondition reporting delete is started, and issue a patch in order to make
	// this visible to the users.
	patchHelper, err := patch.NewHelper(ctx.KubevirtMachine, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}
	conditions.MarkFalse(ctx.KubevirtMachine, infrav1.VMProvisionedCondition, clusterv1.DeletingReason, clusterv1.ConditionSeverityInfo, "")
	if err := ctx.PatchKubevirtMachine(patchHelper); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "failed to patch KubevirtMachine")
	}

	// Machine is deleted so remove the finalizer.
	controllerutil.RemoveFinalizer(ctx.KubevirtMachine, infrav1.MachineFinalizer)

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
func (r *KubevirtMachineReconciler) reconcileKubevirtBootstrapSecret(ctx *context.MachineContext, sshKeys *ssh.ClusterNodeSshKeys) error {
	if ctx.Machine.Spec.Bootstrap.DataSecretName == nil {
		return errors.New("error retrieving bootstrap data: linked Machine's bootstrap.dataSecretName is nil")
	}

	// Exit early if exists.
	bootstrapDataSecret := &corev1.Secret{}
	bootstrapDataSecretKey := client.ObjectKey{Namespace: ctx.Machine.GetNamespace(), Name: *ctx.Machine.Spec.Bootstrap.DataSecretName + "-userdata"}
	if err := r.Client.Get(ctx, bootstrapDataSecretKey, bootstrapDataSecret); err == nil {
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

	ctx.Logger.Info("Adding users config to bootstrap data...")
	updatedValue := []byte(string(value) + usersCloudConfig(sshKeys.PublicKey))

	newBootstrapDataSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      s.Name + "-userdata",
			Namespace: ctx.Machine.GetNamespace(),
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, newBootstrapDataSecret, func() error {
		newBootstrapDataSecret.Type = clusterv1.ClusterSecretType
		newBootstrapDataSecret.Data = map[string][]byte{
			"userdata": updatedValue,
		}

		// set owner reference for secret
		mutateFn := func() (err error) {
			newBootstrapDataSecret.SetOwnerReferences(clusterutil.EnsureOwnerRef(
				newBootstrapDataSecret.OwnerReferences,
				metav1.OwnerReference{
					APIVersion: ctx.KubevirtMachine.APIVersion,
					Kind:       ctx.KubevirtMachine.Kind,
					Name:       ctx.KubevirtMachine.Name,
					UID:        ctx.KubevirtMachine.UID,
				}))
			return nil
		}
		if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, newBootstrapDataSecret, mutateFn); err != nil {
			return errors.Wrapf(err, "failed to set owner reference for secret")
		}

		return nil
	})

	if err != nil {
		return errors.Wrapf(err, "failed to create kubevirt bootstrap secret for cluster")
	}

	return nil
}

// usersCloudConfig generates 'users' cloud config for capk user with a given ssh public key
func usersCloudConfig(sshPublicKey []byte) string {
	sshPublicKeyString := base64.StdEncoding.EncodeToString(sshPublicKey)
	sshPublicKeyDecoded, _ := base64.StdEncoding.DecodeString(sshPublicKeyString)

	return `users:
  - name: capk
    gecos: CAPK User
    sudo: ALL=(ALL) NOPASSWD:ALL
    groups: users, admin
    ssh_authorized_keys:
      - ` + string(sshPublicKeyDecoded)
}
