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
	"net/netip"
	"strings"
	"time"

	ipam "github.com/metal-stack/go-ipam"

	nmstate "github.com/nmstate/kubernetes-nmstate/api/shared"
	nmstatev1 "github.com/nmstate/kubernetes-nmstate/api/v1"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	kubevirtv1 "kubevirt.io/api/core/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha1"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/context"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/ssh"
)

// Machine implement a service for managing the KubeVirt VM hosting a kubernetes node.
type Machine struct {
	client         client.Client
	namespace      string
	machineContext *context.MachineContext
	vmiInstance    *kubevirtv1.VirtualMachineInstance
	vmInstance     *kubevirtv1.VirtualMachine

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

	return machine, nil
}

// Reports back if the VM is either being requested to terminate or is terminate
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

	virtualMachine, err := newVirtualMachineFromKubevirtMachine(m.machineContext, m.namespace)
	if err != nil {
		return err
	}

	mutateFn := func() (err error) {
		if virtualMachine.Labels == nil {
			virtualMachine.Labels = map[string]string{}
		}
		if virtualMachine.Spec.Template.ObjectMeta.Labels == nil {
			virtualMachine.Spec.Template.ObjectMeta.Labels = map[string]string{}
		}
		virtualMachine.Labels[clusterv1.ClusterLabelName] = m.machineContext.Cluster.Name

		virtualMachine.Labels[infrav1.KubevirtMachineNameLabel] = m.machineContext.KubevirtMachine.Name
		virtualMachine.Labels[infrav1.KubevirtMachineNamespaceLabel] = m.machineContext.KubevirtMachine.Namespace

		virtualMachine.Spec.Template.ObjectMeta.Labels[infrav1.KubevirtMachineNameLabel] = m.machineContext.KubevirtMachine.Name
		virtualMachine.Spec.Template.ObjectMeta.Labels[infrav1.KubevirtMachineNamespaceLabel] = m.machineContext.KubevirtMachine.Namespace
		return nil
	}
	if _, err := controllerutil.CreateOrUpdate(ctx, m.client, virtualMachine, mutateFn); err != nil {
		return err
	}

	return m.ensureNetworking()
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
func (m *Machine) ExternalAddress() string {
	return m.findAddressByIfaceName("pod")
}

// Address returns the IP address of the VM.
func (m *Machine) InternalAddress() string {
	return m.findAddressByIfaceName("multus")
}

// IsReady checks if the VM is ready
func (m *Machine) IsReady() bool {
	return m.hasReadyCondition()
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
	if !m.IsReady() || m.sshKeys == nil {
		return false
	}

	executor := m.getCommandExecutor(m.ExternalAddress(), m.sshKeys)

	output, err := executor.ExecuteCommand("cat /run/cluster-api/bootstrap-success.complete")
	if err != nil {
		m.machineContext.Logger.Error(err, "Failed checking bootstraping")
	}
	return err == nil && output == "success"
}

// Address returns the IP address of the VM.
func (m *Machine) findAddressByIfaceName(ifaceName string) string {
	if m.vmiInstance != nil && len(m.vmiInstance.Status.Interfaces) > 0 {
		for _, iface := range m.vmiInstance.Status.Interfaces {
			if iface.Name == ifaceName {
				return emptyIfLinkLocal(iface.IP)
			}
		}
		return emptyIfLinkLocal(m.vmiInstance.Status.Interfaces[0].IP)
	}
	return ""
}

func emptyIfLinkLocal(address string) string {
	addr, err := netip.ParseAddr(address)
	if err != nil {
		return ""
	}
	if addr.IsLinkLocalUnicast() {
		return ""
	}
	return address
}

func (m *Machine) ensureNetworking() error {
	m.machineContext.Logger.Info("ensureNetworking")
	if m.machineContext.KubevirtCluster.Spec.InfraClusterNodeNetwork != nil {
		nncp := nmstatev1.NodeNetworkConfigurationPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name: m.namespace,
				Labels: map[string]string{
					"cluster.x-k8s.io/cluster-name": m.machineContext.Cluster.Name,
				},
			},
			Spec: m.machineContext.KubevirtCluster.Spec.InfraClusterNodeNetwork.Setup,
		}
		err := m.client.Get(m.machineContext, types.NamespacedName{Name: nncp.Name}, &nncp)
		if err != nil {
			if apierrors.IsNotFound(err) {
				if err := m.client.Create(m.machineContext, &nncp); err != nil {
					return err
				}
				if err := m.waitNNCPReadiness(nncp); err != nil {
					return err
				}
			} else {
				return err
			}
		}
	}
	dnsmasqServiceAccount := m.generateDNSMasqSA()
	err := m.client.Get(m.machineContext, types.NamespacedName{Namespace: dnsmasqServiceAccount.Namespace, Name: dnsmasqServiceAccount.Name}, &corev1.ServiceAccount{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			if err := m.client.Create(m.machineContext, dnsmasqServiceAccount); err != nil {
				return err
			}
		} else {
			return err
		}
	}
	dnsmasqDeployment, err := m.generateDNSMasqDeployment()
	if err != nil {
		return err
	}
	err = m.client.Get(m.machineContext, types.NamespacedName{Namespace: dnsmasqDeployment.Namespace, Name: dnsmasqDeployment.Name}, &appsv1.Deployment{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			m.machineContext.Logger.Info("creating dnsmasq")
			if err := m.client.Create(m.machineContext, dnsmasqDeployment); err != nil {
				return err
			}
		} else {
			return err
		}
	}
	m.machineContext.Logger.Info("updating dnsmasq")
	if err := m.client.Update(m.machineContext, dnsmasqDeployment); err != nil {
		return err
	}
	//TODO: wait for dnsmasq readiness
	return nil
}

func (m *Machine) waitNNCPReadiness(nncp nmstatev1.NodeNetworkConfigurationPolicy) error {
	nncpIsReady := func() (bool, error) {
		err := m.client.Get(m.machineContext, types.NamespacedName{Name: nncp.Name}, &nncp)
		if err != nil {
			// Stil not created retry
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		degraded := nncp.Status.Conditions.Find(nmstate.NodeNetworkConfigurationPolicyConditionDegraded)
		if degraded != nil && degraded.Status == corev1.ConditionTrue {
			return false, fmt.Errorf("infra node network configuration degraded: %s, %s", degraded.Reason, degraded.Message)
		}
		available := nncp.Status.Conditions.Find(nmstate.NodeNetworkConfigurationPolicyConditionAvailable)
		return available != nil && available.Status == corev1.ConditionTrue, nil
	}

	if err := wait.PollImmediate(5*time.Second, 8*time.Minute, nncpIsReady); err != nil {
		return err
	}
	return nil
}

func (m *Machine) generateDNSMasqSA() *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "dnsmasq",
			Namespace: m.namespace,
			Labels: map[string]string{
				"cluster.x-k8s.io/cluster-name": m.machineContext.Cluster.Name,
				"app":                           "dnsmasq",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: m.machineContext.KubevirtCluster.APIVersion,
					Kind:       m.machineContext.KubevirtCluster.Kind,
					Name:       m.machineContext.KubevirtCluster.Name,
					UID:        m.machineContext.KubevirtCluster.UID,
				},
			},
		},
	}
}

func (m *Machine) generateDNSMasqDeployment() (*appsv1.Deployment, error) {
	m.machineContext.Logger.Info("generateDNSMasqDeployment")
	script, err := m.generateDNSMasqScript()
	if err != nil {
		return nil, err
	}
	m.machineContext.Logger.Info(fmt.Sprintf("dnsmasq script: %s", script))
	replicas := int32(1)
	privileged := true
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "dnsmasq",
			Namespace: m.namespace,
			Labels: map[string]string{
				"cluster.x-k8s.io/cluster-name": m.machineContext.Cluster.Name,
				"app":                           "dnsmasq",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: m.machineContext.KubevirtCluster.APIVersion,
					Kind:       m.machineContext.KubevirtCluster.Kind,
					Name:       m.machineContext.KubevirtCluster.Name,
					UID:        m.machineContext.KubevirtCluster.UID,
				},
			},
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"cluster.x-k8s.io/cluster-name": m.machineContext.Cluster.Name,
					"app":                           "dnsmasq",
				},
			},
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "dnsmasq",
					Namespace: m.namespace,
					Labels: map[string]string{
						"cluster.x-k8s.io/cluster-name": m.machineContext.Cluster.Name,
						"app":                           "dnsmasq",
					},
					Annotations: map[string]string{
						"k8s.v1.cni.cncf.io/networks": fmt.Sprintf(`[{"interface":"net1","name":"bridge-network-pods","namespace":"%s"}]`, m.machineContext.Cluster.Namespace),
					},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: "dnsmasq",
					Containers: []corev1.Container{
						{
							Name:    "dnsmasq",
							Image:   "quay.io/coreos/dnsmasq",
							Command: []string{"sh"},
							Args:    []string{"-c", script},
							SecurityContext: &corev1.SecurityContext{
								Privileged: &privileged,
								Capabilities: &corev1.Capabilities{
									Add: []corev1.Capability{"ALL"},
								},
							},
						},
					},
				},
			},
		},
	}, nil
}

//TODO: Configure it as static so all the dynamic is done with whereabouts
func (m *Machine) generateDNSMasqScript() (string, error) {
	m.machineContext.Logger.Info("generateDNSMasqScript")
	firstAddress, lastAddress, ipamer, err := m.vmsRange()
	if err != nil {
		return "", fmt.Errorf("failed retrieving VMs range: %v", err)
	}
	args := fmt.Sprintf("dnsmasq -d --interface=net1 --dhcp-option=option:dns-server --dhcp-option=option:router --dhcp-range=%s,%s,infinite", firstAddress, lastAddress)

	macToIP := map[string]string{}
	dnsmasqDeployment := &appsv1.Deployment{}
	if err = m.client.Get(m.machineContext, types.NamespacedName{Namespace: m.namespace, Name: "dnsmasq"}, dnsmasqDeployment); err != nil {
		if !apierrors.IsNotFound(err) {
			return "", err
		}
	} else {
		currentArgs := strings.Split(dnsmasqDeployment.Spec.Template.Spec.Containers[0].Args[1], " ")
		for _, arg := range currentArgs {
			if strings.Contains(arg, "dhcp-host") {
				dhcpHost := strings.Split(strings.Split(arg, "=")[1], ",")
				mac := dhcpHost[0]
				ip := dhcpHost[1]
				macToIP[mac] = ip
				_, err := ipamer.AcquireSpecificIP(m.machineContext, m.machineContext.KubevirtCluster.Spec.Network, ip)
				if err != nil {
					return "", err
				}
			}
		}
	}

	vmList := &kubevirtv1.VirtualMachineList{}
	if err = m.client.List(m.machineContext, vmList, client.InNamespace(m.namespace)); err != nil {
		return "", err
	}
	for _, vm := range vmList.Items {
		for _, iface := range vm.Spec.Template.Spec.Domain.Devices.Interfaces {
			if iface.Name == "multus" {
				if iface.MacAddress == "" {
					return "", fmt.Errorf("missing MAC address on multus interface")
				}
				_, ok := macToIP[iface.MacAddress]
				if !ok {
					ip, err := ipamer.AcquireIP(m.machineContext, m.machineContext.KubevirtCluster.Spec.Network)
					if err != nil {
						return "", err
					}
					macToIP[iface.MacAddress] = ip.IP.String()
				}
				break
			}
		}
	}
	finalArgs := []string{args}
	for mac, ip := range macToIP {
		finalArgs = append(finalArgs, fmt.Sprintf("--dhcp-host=%s,%s", mac, ip))
	}
	return strings.Join(finalArgs, " "), nil
}

// FIXME: Inneficient and dirty
func (m *Machine) vmsRange() (*netip.Addr, *netip.Addr, ipam.Ipamer, error) {
	ipamer := ipam.New()
	if _, err := ipamer.NewPrefix(m.machineContext, m.machineContext.KubevirtCluster.Spec.Network); err != nil {
		return nil, nil, nil, err
	}

	prefix, err := netip.ParsePrefix(m.machineContext.KubevirtCluster.Spec.Network)
	if err != nil {
		return nil, nil, nil, err
	}
	var lastAddress, firstAddress netip.Addr
	cnt := 0
	for addr := prefix.Addr(); prefix.Contains(addr); addr = addr.Next() {
		if cnt <= 40 {
			firstAddress = addr
			if cnt > 0 {
				if _, err := ipamer.AcquireSpecificIP(m.machineContext, m.machineContext.KubevirtCluster.Spec.Network, firstAddress.String()); err != nil {
					return nil, nil, nil, err
				}
			}
		}
		cnt++
		lastAddress = addr
	}

	return &firstAddress, &lastAddress, ipamer, nil
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
