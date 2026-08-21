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

package kubevirt

import (
	gocontext "context"
	"fmt"
	"time"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubedrain "k8s.io/kubectl/pkg/drain"
	kubevirtv1 "kubevirt.io/api/core/v1"
	cdiv1 "kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/controllers/noderefutil"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha1"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/context"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/ssh"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/workloadcluster"
)

const (
	vmiDeleteGraceTimeoutDurationSeconds = 600 // 10 minutes
)

// Machine implement a service for managing the KubeVirt VM hosting a kubernetes node.
type Machine struct {
	client         client.Client
	namespace      string
	machineContext *context.MachineContext
	vmiInstance    *kubevirtv1.VirtualMachineInstance
	vmInstance     *kubevirtv1.VirtualMachine
	dataVolumes    []*cdiv1.DataVolume

	sshKeys            *ssh.ClusterNodeSshKeys
	getCommandExecutor func(string, *ssh.ClusterNodeSshKeys) ssh.VMCommandExecutor
}

// NewMachine returns a new Machine service for the given context.
func NewMachine(ctx *context.MachineContext, client client.Client, namespace string, sshKeys *ssh.ClusterNodeSshKeys) (*Machine, error) {
	machine := &Machine{
		client:             client,
		namespace:          namespace,
		machineContext:     ctx,
		vmiInstance:        nil,
		vmInstance:         nil,
		sshKeys:            sshKeys,
		dataVolumes:        nil,
		getCommandExecutor: ssh.NewVMCommandExecutor,
	}

	namespacedName := types.NamespacedName{Namespace: namespace, Name: ctx.KubevirtMachine.Name}
	vm := &kubevirtv1.VirtualMachine{}
	vmi := &kubevirtv1.VirtualMachineInstance{}

	// Get the active running VMI if it exists
	err := client.Get(ctx.Context, namespacedName, vmi)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, err
		}
	} else {
		machine.vmiInstance = vmi
	}

	// Get the top level VM object if it exists
	err = client.Get(ctx.Context, namespacedName, vm)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, err
		}
	} else {
		machine.vmInstance = vm
	}

	if machine.vmInstance != nil {
		for _, dvTemp := range machine.vmInstance.Spec.DataVolumeTemplates {
			dv := &cdiv1.DataVolume{}
			err = client.Get(ctx.Context, types.NamespacedName{Name: dvTemp.Name, Namespace: namespace}, dv)
			if err != nil {
				if !apierrors.IsNotFound(err) {
					return nil, err
				}
			} else {
				machine.dataVolumes = append(machine.dataVolumes, dv)
			}
		}
	}

	return machine, nil
}

// IsTerminal Reports back if the VM is either being requested to terminate or is terminated
// in a way that it will never recover from.
func (m *Machine) IsTerminal() (bool, string, error) {
	if m.vmInstance == nil || m.vmiInstance == nil {
		// vm/vmi hasn't been created yet
		return false, "", nil
	}

	// VMI is being asked to terminate gracefully due to node drain
	if !m.vmiInstance.IsFinal() &&
		!m.vmiInstance.IsMigratable() &&
		m.vmiInstance.Status.EvacuationNodeName != "" {
		// VM's infra node is being drained and VM is not live migratable.
		// We need to report a FailureReason so the MachineHealthCheck and
		// MachineSet controllers will gracefully take the VM down.
		return true, "The Machine's VM pod is marked for eviction due to infra node drain.", nil
	}

	// The infrav1.KubevirtVMTerminalLabel is a way users or automation to mark
	// a VM as being in a terminal state that requires remediation. This is used
	// by the functional test suite to test remediation and can also be triggered
	// by users as a way to manually trigger remediation.
	terminalReason, ok := m.vmInstance.Labels[infrav1.KubevirtMachineVMTerminalLabel]
	if ok {
		return true, fmt.Sprintf("VM's %s label has the vm marked as being terminal with reason [%s]", infrav1.KubevirtMachineVMTerminalLabel, terminalReason), nil
	}

	// Also check the VMI for this label
	terminalReason, ok = m.vmiInstance.Labels[infrav1.KubevirtMachineVMTerminalLabel]
	if ok {
		return true, fmt.Sprintf("VMI's %s label has the vm marked as being terminal with reason [%s]", infrav1.KubevirtMachineVMTerminalLabel, terminalReason), nil
	}

	runStrategy, err := m.vmInstance.RunStrategy()
	if err != nil {
		return false, "", err
	}

	switch runStrategy {
	case kubevirtv1.RunStrategyAlways:
		// VM should recover if it is down.
		return false, "", nil
	case kubevirtv1.RunStrategyManual:
		// If VM is manually controlled, we stay out of the loop
		return false, "", nil
	case kubevirtv1.RunStrategyHalted, kubevirtv1.RunStrategyOnce:
		if m.vmiInstance.IsFinal() {
			return true, "VMI has reached a permanent finalized state", nil
		}
		return false, "", nil
	case kubevirtv1.RunStrategyRerunOnFailure:
		// only recovers when vmi is failed
		if m.vmiInstance.Status.Phase == kubevirtv1.Succeeded {
			return true, "VMI has reached a permanent finalized state", nil
		}
		return false, "", nil
	}

	return false, "", nil
}

// Exists checks if the VM has been provisioned already.
func (m *Machine) Exists() bool {
	return m.vmInstance != nil
}

// Create creates a new VM for this machine.
func (m *Machine) Create(ctx gocontext.Context) error {
	m.machineContext.Logger.Info(fmt.Sprintf("Creating VM with role '%s'...", nodeRole(m.machineContext)))

	virtualMachine := newVirtualMachineFromKubevirtMachine(m.machineContext, m.namespace)

	mutateFn := func() (err error) {
		if virtualMachine.Labels == nil {
			virtualMachine.Labels = map[string]string{}
		}
		if virtualMachine.Annotations == nil {
			virtualMachine.Annotations = map[string]string{}
		}
		if virtualMachine.Spec.Template.ObjectMeta.Labels == nil {
			virtualMachine.Spec.Template.ObjectMeta.Labels = map[string]string{}
		}
		virtualMachine.Labels[clusterv1.ClusterNameLabel] = m.machineContext.Cluster.Name

		virtualMachine.Labels[infrav1.KubevirtMachineNameLabel] = m.machineContext.KubevirtMachine.Name
		virtualMachine.Labels[infrav1.KubevirtMachineNamespaceLabel] = m.machineContext.KubevirtMachine.Namespace

		virtualMachine.Spec.Template.ObjectMeta.Labels[infrav1.KubevirtMachineNameLabel] = m.machineContext.KubevirtMachine.Name
		virtualMachine.Spec.Template.ObjectMeta.Labels[infrav1.KubevirtMachineNamespaceLabel] = m.machineContext.KubevirtMachine.Namespace
		return nil
	}
	if _, err := controllerutil.CreateOrUpdate(ctx, m.client, virtualMachine, mutateFn); err != nil {
		return err
	}

	return nil
}

// Returns if VMI has ready condition or not.
func (m *Machine) hasReadyCondition() bool {

	if m.vmiInstance == nil {
		return false
	}

	for _, cond := range m.vmiInstance.Status.Conditions {
		if cond.Type == kubevirtv1.VirtualMachineInstanceReady &&
			cond.Status == corev1.ConditionTrue {
			return true
		}
	}

	return false
}

// Address returns the IP address of the VM.
func (m *Machine) Address() string {
	if m.vmiInstance != nil && len(m.vmiInstance.Status.Interfaces) > 0 {
		return m.vmiInstance.Status.Interfaces[0].IP
	}

	return ""
}

// Addresses returns all IP addresses of the VM (for dual-stack support).
// It collects IPs from all network interfaces, supporting both IPv4 and IPv6.
func (m *Machine) Addresses() []string {
	if m.vmiInstance == nil {
		return nil
	}

	var addresses []string
	for _, iface := range m.vmiInstance.Status.Interfaces {
		addresses = append(addresses, iface.IPs...)
	}
	return addresses
}

// IsReady checks if the VM is ready
func (m *Machine) IsReady() bool {
	return m.hasReadyCondition()
}

// IsLiveMigratable reports back the live-migratability state of the VM: Status, Reason and Message
func (m *Machine) IsLiveMigratable() (bool, string, string, error) {
	if m.vmiInstance == nil {
		return false, "", "", fmt.Errorf("VMI is nil")
	}

	for _, cond := range m.vmiInstance.Status.Conditions {
		if cond.Type == kubevirtv1.VirtualMachineInstanceIsMigratable {
			if cond.Status == corev1.ConditionTrue {
				return true, "", "", nil
			} else {
				return false, cond.Reason, cond.Message, nil
			}
		}
	}

	return false, "", "", fmt.Errorf("%s VMI does not have a %s condition",
		m.vmiInstance.Status.Phase, kubevirtv1.VirtualMachineInstanceIsMigratable)
}

const (
	defaultCondReason  = "VMNotReady"
	defaultCondMessage = "VM is not ready"
)

func (m *Machine) GetVMNotReadyReason() (reason string, message string) {
	reason = defaultCondReason

	if m.vmInstance == nil {
		message = defaultCondMessage
		return
	}

	message = fmt.Sprintf("%s: %s", defaultCondMessage, m.vmInstance.Status.PrintableStatus)

	cond := m.getVMCondition(kubevirtv1.VirtualMachineConditionType(corev1.PodScheduled))
	if cond != nil {
		switch cond.Status {
		case corev1.ConditionTrue:
			return
		case corev1.ConditionFalse:
			if cond.Reason == "Unschedulable" {
				return "Unschedulable", cond.Message
			}
		}
	}

	for _, dv := range m.dataVolumes {
		dvReason, dvMessage, foundDVReason := m.getDVNotProvisionedReason(dv)
		if foundDVReason {
			return dvReason, dvMessage
		}
	}

	return
}

func (m *Machine) getDVNotProvisionedReason(dv *cdiv1.DataVolume) (string, string, bool) {
	msg := fmt.Sprintf("DataVolume %s is not ready; Phase: %s", dv.Name, dv.Status.Phase)
	switch dv.Status.Phase {
	case cdiv1.Succeeded: // DV's OK, return default reason & message
		return "", "", false
	case cdiv1.Pending:
		return "DVPending", msg, true
	case cdiv1.Failed:
		return "DVFailed", msg, true
	default:
		reason := "DVNotReady"
		for _, dvCond := range dv.Status.Conditions {
			if dvCond.Type == cdiv1.DataVolumeRunning {
				if dvCond.Status == corev1.ConditionFalse {
					if dvCond.Reason == "ImagePullFailed" {
						reason = "DVImagePullFailed"
					}

					msg = fmt.Sprintf("DataVolume %s import is not running: %s", dv.Name, dvCond.Message)
				}
				break
			}
		}
		return reason, msg, true
	}
}

func (m *Machine) getVMCondition(t kubevirtv1.VirtualMachineConditionType) *kubevirtv1.VirtualMachineCondition {
	if m.vmInstance == nil {
		return nil
	}

	for _, cond := range m.vmInstance.Status.Conditions {
		if cond.Type == t {
			return cond.DeepCopy()
		}
	}

	return nil
}

// SupportsCheckingIsBootstrapped checks if we have a method of checking
// that this bootstrapper has completed.
func (m *Machine) SupportsCheckingIsBootstrapped() bool {
	// Right now, we can only check if bootstrapping has
	// completed if we are using a bootstrapper that allows
	// for us to inject ssh keys into the guest.

	if m.sshKeys != nil {
		return m.machineContext.HasInjectedCapkSSHKeys(m.sshKeys.PublicKey)
	}
	return false
}

// IsBootstrapped checks if the VM is bootstrapped with Kubernetes.
func (m *Machine) IsBootstrapped() bool {
	// CheckStrategy value is already sanitized by apiserver
	switch m.machineContext.KubevirtMachine.Spec.BootstrapCheckSpec.CheckStrategy {
	case "none":
		// skip bootstrap check and always returns positively
		return true

	case "":
		fallthrough // ssh is default check strategy, fallthrough
	case "ssh":
		return m.IsBootstrappedWithSSH()

	default:
		// Since CRD CheckStrategy field is validated by an enum, this case should never be hit
		return false
	}
}

// IsBootstrappedWithSSH checks if the VM is bootstrapped with Kubernetes using SSH strategy.
func (m *Machine) IsBootstrappedWithSSH() bool {
	if !m.IsReady() || m.sshKeys == nil {
		return false
	}

	executor := m.getCommandExecutor(m.Address(), m.sshKeys)

	output, err := executor.ExecuteCommand("cat /run/cluster-api/bootstrap-success.complete")
	if err != nil || output != "success" {
		return false
	}
	return true
}

// GenerateProviderID generates the KubeVirt provider ID to be used for the NodeRef
func (m *Machine) GenerateProviderID() (string, error) {
	if m.vmiInstance == nil {
		return "", errors.New("Underlying Kubevirt VM is NOT running")
	}

	providerID := fmt.Sprintf("kubevirt://%s", m.machineContext.KubevirtMachine.Name)

	return providerID, nil
}

// Delete deletes VM for this machine.
func (m *Machine) Delete() error {
	namespacedName := types.NamespacedName{Namespace: m.namespace, Name: m.machineContext.KubevirtMachine.Name}
	vm := &kubevirtv1.VirtualMachine{}
	if err := m.client.Get(m.machineContext.Context, namespacedName, vm); err != nil {
		if apierrors.IsNotFound(err) {
			m.machineContext.Logger.Info("VM does not exist, nothing to do.")
			return nil
		}
		return errors.Wrapf(err, "failed to retrieve VM to delete")
	}

	if err := m.client.Delete(gocontext.Background(), vm); err != nil {
		return errors.Wrapf(err, "failed to delete VM")
	}

	return nil
}

func (m *Machine) DrainNodeIfNeeded(wrkldClstr workloadcluster.WorkloadCluster) (time.Duration, error) {
	if m.vmiInstance == nil || !m.shouldGracefulDeleteVMI() {
		if err := m.cleanupMigrationAnnotations(); err != nil {
			return 100 * time.Millisecond, err
		}

		// Only uncordon when the VMI is back and not being deleted. If the VMI
		// is nil (not yet recreated) or still has a DeletionTimestamp, the
		// eviction cycle is not complete and the node must stay cordoned.
		if m.vmiInstance != nil && m.vmiInstance.DeletionTimestamp == nil {
			if err := m.uncordonNodeIfNeeded(wrkldClstr); err != nil {
				m.machineContext.Logger.Error(err, "failed to uncordon guest node")
				return 100 * time.Millisecond, nil
			}
		}

		return 0, nil
	}

	// For migratable VMIs, attempt live migration before the disruptive
	// drain+delete path. If migration succeeds, the guest node is untouched.
	if m.vmiInstance.IsMigratable() {
		migrated, requeueAfter, err := m.tryMigrateVMI()
		if err != nil {
			return 0, err
		}
		if migrated {
			if err := m.cleanupMigrationAnnotations(); err != nil {
				return 100 * time.Millisecond, err
			}
			return 0, nil
		}
		if requeueAfter > 0 {
			return requeueAfter, nil
		}
		// Migration failed or timed out -- clean up migration annotations
		// and reset the grace period so the drain+delete fallback gets a
		// fresh timeout window.
		if err := m.cleanupMigrationAnnotations(); err != nil {
			return 100 * time.Millisecond, err
		}
	}

	exceeded, err := m.drainGracePeriodExceeded()
	if err != nil {
		return 0, err
	}

	if !exceeded {
		retryDuration, err := m.drainNode(wrkldClstr)
		if err != nil {
			return 0, err
		}

		if retryDuration > 0 {
			return retryDuration, nil
		}
	}

	// now, when the node is drained (or vmiDeleteGraceTimeoutDurationSeconds has passed), we can delete the VMI
	propagationPolicy := metav1.DeletePropagationForeground
	err = m.client.Delete(m.machineContext, m.vmiInstance, &client.DeleteOptions{PropagationPolicy: &propagationPolicy})
	if err != nil {
		m.machineContext.Logger.Error(err, "failed to delete VirtualMachineInstance")
		return 0, err
	}

	if err = m.removeGracePeriodAnnotation(); err != nil {
		return 100 * time.Millisecond, err
	}

	// requeue to force reading the VMI again
	return time.Second * 10, nil
}

const removeGracePeriodAnnotationPatch = `[{"op": "remove", "path": "/metadata/annotations/` + infrav1.VmiDeletionGraceTimeEscape + `"}]`

func (m *Machine) removeGracePeriodAnnotation() error {
	patch := client.RawPatch(types.JSONPatchType, []byte(removeGracePeriodAnnotationPatch))

	if err := m.client.Patch(m.machineContext, m.machineContext.KubevirtMachine, patch); err != nil {
		return fmt.Errorf("failed to remove the %s annotation to the KubeVirtMachine %s; %w", infrav1.VmiDeletionGraceTime, m.machineContext.KubevirtMachine.Name, err)
	}

	return nil
}

// cleanupMigrationAnnotations removes both the migration-submitted marker and
// the grace period annotation (if they exist). Called when migration succeeds
// or when falling back to drain+delete.
func (m *Machine) cleanupMigrationAnnotations() error {
	if _, exists := m.machineContext.KubevirtMachine.Annotations[infrav1.VmiMigrationSubmitted]; exists {
		if err := m.removeMigrationSubmittedAnnotation(); err != nil {
			return err
		}
	}
	if _, exists := m.machineContext.KubevirtMachine.Annotations[infrav1.VmiDeletionGraceTime]; exists {
		if err := m.removeGracePeriodAnnotation(); err != nil {
			return err
		}
	}
	return nil
}

func (m *Machine) setNodeCordonedAnnotation() error {
	patch := fmt.Sprintf(`{"metadata":{"annotations":{"%s": "true"}}}`, infrav1.NodeCordonedByCapk)
	patchRequest := client.RawPatch(types.MergePatchType, []byte(patch))

	if err := m.client.Patch(m.machineContext, m.machineContext.KubevirtMachine, patchRequest); err != nil {
		return fmt.Errorf("failed to set the %s annotation on KubeVirtMachine %s; %w", infrav1.NodeCordonedByCapk, m.machineContext.KubevirtMachine.Name, err)
	}

	return nil
}

const removeNodeCordonedAnnotationPatch = `[{"op": "remove", "path": "/metadata/annotations/` + infrav1.NodeCordonedByCapkEscape + `"}]`

func (m *Machine) removeNodeCordonedAnnotation() error {
	patch := client.RawPatch(types.JSONPatchType, []byte(removeNodeCordonedAnnotationPatch))

	if err := m.client.Patch(m.machineContext, m.machineContext.KubevirtMachine, patch); err != nil {
		return fmt.Errorf("failed to remove the %s annotation from KubeVirtMachine %s; %w", infrav1.NodeCordonedByCapk, m.machineContext.KubevirtMachine.Name, err)
	}

	return nil
}

func (m *Machine) uncordonNodeIfNeeded(wrkldClstr workloadcluster.WorkloadCluster) error {
	if _, exists := m.machineContext.KubevirtMachine.Annotations[infrav1.NodeCordonedByCapk]; !exists {
		return nil
	}

	if wrkldClstr == nil {
		m.machineContext.Logger.Info("Workload cluster client is nil, skipping uncordon")
		return nil
	}

	kubeClient, err := wrkldClstr.GenerateWorkloadClusterK8sClient(m.machineContext)
	if err != nil {
		return fmt.Errorf("failed to get client to remote cluster for uncordon; %w", err)
	}

	nodeName := m.machineContext.KubevirtMachine.Name
	node, err := kubeClient.CoreV1().Nodes().Get(m.machineContext, nodeName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			m.machineContext.Logger.Info("Node not found during uncordon, removing annotation", "node", nodeName)
			return m.removeNodeCordonedAnnotation()
		}
		return fmt.Errorf("unable to get node %q for uncordon: %w", nodeName, err)
	}

	if !node.Spec.Unschedulable {
		m.machineContext.Logger.Info("Node is already schedulable, removing stale cordon annotation", "node", nodeName)
		return m.removeNodeCordonedAnnotation()
	}

	drainer := &kubedrain.Helper{
		Client: kubeClient,
		Ctx:    m.machineContext,
	}

	m.machineContext.Logger.Info("Uncordoning guest node after VMI restart", "node", nodeName)
	if err = kubedrain.RunCordonOrUncordon(drainer, node, false); err != nil {
		return fmt.Errorf("unable to uncordon node %s: %w", nodeName, err)
	}

	return m.removeNodeCordonedAnnotation()
}

// tryMigrateVMI attempts to live-migrate a migratable VMI instead of the
// disruptive drain+delete path. It returns:
//   - migrated=true when the VMI has already moved to a different host
//   - requeueAfter>0 when a migration is in progress or was just created
//   - both zero when migration failed/timed out (caller should fall through to drain+delete)
//
// The grace period timer is deliberately deferred until the migration is
// actively in progress (MigrationState is set). This ensures that on dense
// nodes where KubeVirt serializes outbound migrations (1 at a time), VMs
// waiting in the migration queue don't burn through their timeout window
// before their migration even starts.
func (m *Machine) tryMigrateVMI() (migrated bool, requeueAfter time.Duration, err error) {
	// If the VMI is already on a different node than the evacuation source,
	// migration has succeeded -- no guest disruption needed.
	if m.vmiInstance.Status.NodeName != m.vmiInstance.Status.EvacuationNodeName {
		m.machineContext.Logger.Info("VMI successfully migrated, skipping drain+delete",
			"from", m.vmiInstance.Status.EvacuationNodeName,
			"to", m.vmiInstance.Status.NodeName)
		return true, 0, nil
	}

	if ms := m.vmiInstance.Status.MigrationState; ms != nil {
		if ms.Failed {
			m.machineContext.Logger.Info("VMI migration failed, falling back to drain+delete",
				"vmi", m.vmiInstance.Name)
			return false, 0, nil
		}
		if !ms.Completed {
			// Migration is actively in progress -- the grace period timer
			// starts here (not when the migration CR was first created).
			exceeded, err := m.drainGracePeriodExceeded()
			if err != nil {
				return false, 0, err
			}
			if exceeded {
				m.machineContext.Logger.Info("Migration timeout exceeded while migration in progress, falling back to drain+delete",
					"vmi", m.vmiInstance.Name)
				return false, 0, nil
			}
			m.machineContext.Logger.Info("VMI migration in progress, waiting",
				"vmi", m.vmiInstance.Name)
			return false, 10 * time.Second, nil
		}
	}

	// No active migration. Check if we've already submitted a migration CR
	// on a previous reconcile (waiting for KubeVirt to pick it up).
	if _, exists := m.machineContext.KubevirtMachine.Annotations[infrav1.VmiMigrationSubmitted]; exists {
		m.machineContext.Logger.Info("Migration already submitted, waiting for KubeVirt to start it",
			"vmi", m.vmiInstance.Name)
		return false, 10 * time.Second, nil
	}

	// First reconcile for this evacuation -- create a migration CR.
	priority := kubevirtv1.PrioritySystemCritical
	migration := &kubevirtv1.VirtualMachineInstanceMigration{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: m.vmiInstance.Name + "-migration-",
			Namespace:    m.vmiInstance.Namespace,
		},
		Spec: kubevirtv1.VirtualMachineInstanceMigrationSpec{
			VMIName:  m.vmiInstance.Name,
			Priority: &priority,
		},
	}

	if err := m.client.Create(m.machineContext, migration); err != nil {
		m.machineContext.Logger.Error(err, "Failed to create VirtualMachineInstanceMigration, falling back to drain+delete",
			"vmi", m.vmiInstance.Name)
		return false, 0, nil
	}

	if err := m.setMigrationSubmittedAnnotation(); err != nil {
		return false, 0, err
	}

	m.machineContext.Logger.Info("Created VirtualMachineInstanceMigration for migratable VMI",
		"migration", migration.Name, "vmi", m.vmiInstance.Name)

	return false, 10 * time.Second, nil
}

func (m *Machine) setMigrationSubmittedAnnotation() error {
	patch := fmt.Sprintf(`{"metadata":{"annotations":{"%s": "true"}}}`, infrav1.VmiMigrationSubmitted)
	patchRequest := client.RawPatch(types.MergePatchType, []byte(patch))
	if err := m.client.Patch(m.machineContext, m.machineContext.KubevirtMachine, patchRequest); err != nil {
		return fmt.Errorf("failed to set the %s annotation on KubevirtMachine %s: %w", infrav1.VmiMigrationSubmitted, m.machineContext.KubevirtMachine.Name, err)
	}
	return nil
}

const removeMigrationSubmittedPatch = `[{"op": "remove", "path": "/metadata/annotations/` + infrav1.VmiMigrationSubmittedEscape + `"}]`

func (m *Machine) removeMigrationSubmittedAnnotation() error {
	patch := client.RawPatch(types.JSONPatchType, []byte(removeMigrationSubmittedPatch))
	if err := m.client.Patch(m.machineContext, m.machineContext.KubevirtMachine, patch); err != nil {
		return fmt.Errorf("failed to remove the %s annotation from KubevirtMachine %s: %w", infrav1.VmiMigrationSubmitted, m.machineContext.KubevirtMachine.Name, err)
	}
	return nil
}

func (m *Machine) shouldGracefulDeleteVMI() bool {
	if m.vmiInstance.DeletionTimestamp != nil {
		m.machineContext.Logger.V(4).Info("DrainNode: the virtualMachineInstance is already in deletion process. Nothing to do here")
		return false
	}

	if m.vmiInstance.Spec.EvictionStrategy == nil || *m.vmiInstance.Spec.EvictionStrategy != kubevirtv1.EvictionStrategyExternal {
		m.machineContext.Logger.V(4).Info("DrainNode: graceful deletion is not supported for virtualMachineInstance. Nothing to do here")
		return false
	}

	// KubeVirt will set the EvacuationNodeName field in case of guest node eviction. If the field is not set, there is
	// nothing to do.
	if len(m.vmiInstance.Status.EvacuationNodeName) == 0 {
		m.machineContext.Logger.V(4).Info("DrainNode: the virtualMachineInstance is not marked for deletion. Nothing to do here")
		return false
	}

	return true
}

// wait vmiDeleteGraceTimeoutDurationSeconds to the node to be drained. If this time had passed, don't wait anymore.
func (m *Machine) drainGracePeriodExceeded() (bool, error) {
	if graceTime, found := m.machineContext.KubevirtMachine.Annotations[infrav1.VmiDeletionGraceTime]; found {
		deletionGraceTime, err := time.Parse(time.RFC3339, graceTime)
		if err != nil { // wrong format - rewrite
			if err = m.setVmiDeletionGraceTime(); err != nil {
				return false, err
			}
		} else {
			return time.Now().UTC().After(deletionGraceTime), nil
		}
	} else {
		if err := m.setVmiDeletionGraceTime(); err != nil {
			return false, err
		}
	}

	return false, nil
}

func (m *Machine) setVmiDeletionGraceTime() error {
	m.machineContext.Logger.Info(fmt.Sprintf("setting the %s annotation", infrav1.VmiDeletionGraceTime))
	graceTime := time.Now().Add(vmiDeleteGraceTimeoutDurationSeconds * time.Second).UTC().Format(time.RFC3339)
	patch := fmt.Sprintf(`{"metadata":{"annotations":{"%s": "%s"}}}`, infrav1.VmiDeletionGraceTime, graceTime)
	patchRequest := client.RawPatch(types.MergePatchType, []byte(patch))

	if err := m.client.Patch(m.machineContext, m.machineContext.KubevirtMachine, patchRequest); err != nil {
		return fmt.Errorf("failed to add the %s annotation to the KubeVirtMachine %s; %w", infrav1.VmiDeletionGraceTime, m.machineContext.KubevirtMachine.Name, err)
	}

	return nil
}

// This functions drains a node from a tenant cluster.
// The function returns 3 values:
// * drain done - boolean
// * retry time, or 0 if not needed
// * error - to be returned if we want to retry
func (m *Machine) drainNode(wrkldClstr workloadcluster.WorkloadCluster) (time.Duration, error) {
	kubeClient, err := wrkldClstr.GenerateWorkloadClusterK8sClient(m.machineContext)
	if err != nil {
		m.machineContext.Logger.Error(err, "Error creating a remote client while deleting Machine, won't retry")
		return 0, fmt.Errorf("failed to get client to remote cluster; %w", err)
	}

	nodeName := m.machineContext.KubevirtMachine.Name
	node, err := kubeClient.CoreV1().Nodes().Get(m.machineContext, nodeName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			// If an admin deletes the node directly, we'll end up here.
			m.machineContext.Logger.Error(err, "Could not find node from noderef, it may have already been deleted")
			return 0, nil
		}
		return 0, fmt.Errorf("unable to get node %q: %w", nodeName, err)
	}

	drainer := &kubedrain.Helper{
		Client:              kubeClient,
		Ctx:                 m.machineContext,
		Force:               true,
		IgnoreAllDaemonSets: true,
		DeleteEmptyDirData:  true,
		GracePeriodSeconds:  -1,
		// If a pod is not evicted in 20 seconds, retry the eviction next time the
		// machine gets reconciled again (to allow other machines to be reconciled).
		Timeout: 20 * time.Second,
		OnPodDeletionOrEvictionFinished: func(pod *corev1.Pod, usingEviction bool, err error) {
			verbStr := "Deleted"
			if usingEviction {
				verbStr = "Evicted"
			}
			m.machineContext.Logger.Info(fmt.Sprintf("%s pod from Node", verbStr),
				"pod", fmt.Sprintf("%s/%s", pod.Name, pod.Namespace))
		},
		Out: writer{m.machineContext.Logger.Info},
		ErrOut: writer{func(msg string, keysAndValues ...interface{}) {
			m.machineContext.Logger.Error(nil, msg, keysAndValues...)
		}},
	}

	if noderefutil.IsNodeUnreachable(node) {
		// When the node is unreachable and some pods are not evicted for as long as this timeout, we ignore them.
		drainer.SkipWaitForDeleteTimeoutSeconds = 60 * 5 // 5 minutes
	}

	if err = kubedrain.RunCordonOrUncordon(drainer, node, true); err != nil {
		// Machine will be re-reconciled after a cordon failure.
		m.machineContext.Logger.Error(err, "Cordon failed")
		return 0, errors.Errorf("unable to cordon node %s: %v", nodeName, err)
	}

	if err = m.setNodeCordonedAnnotation(); err != nil {
		m.machineContext.Logger.Error(err, "Failed to set node-cordoned annotation after cordon")
		return 100 * time.Millisecond, nil
	}

	if err = kubedrain.RunNodeDrain(drainer, node.Name); err != nil {
		// Machine will be re-reconciled after a drain failure.
		m.machineContext.Logger.Error(err, "Drain failed, retry in a second", "node name", nodeName)
		return time.Second, nil
	}

	m.machineContext.Logger.Info("Drain successful", "node name", nodeName)
	return 0, nil
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
