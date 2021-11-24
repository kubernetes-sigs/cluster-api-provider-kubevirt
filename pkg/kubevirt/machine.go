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

	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/context"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1alpha4"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// Machine implement a service for managing the KubeVirt VM hosting a kubernetes node.
type Machine struct {
	client         client.Client
	machineContext *context.MachineContext
	vmInstance     *kubevirtv1.VirtualMachineInstance
}

// NewMachine returns a new Machine service for the given context.
func NewMachine(ctx *context.MachineContext, client client.Client) (*Machine, error) {
	machine := &Machine{client, ctx, nil}

	namespacedName := types.NamespacedName{Namespace: ctx.KubevirtMachine.Namespace, Name: ctx.KubevirtMachine.Name}
	vmi := &kubevirtv1.VirtualMachineInstance{}

	err := client.Get(ctx.Context, namespacedName, vmi)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return machine, nil
		} else {
			return nil, err
		}
	}

	machine.vmInstance = vmi

	return machine, nil
}

// Exists checks if the VM has been provisioned already.
func (m *Machine) Exists() bool {
	return m.vmInstance != nil
}

// Create creates a new VM for this machine.
func (m *Machine) Create() error {
	m.machineContext.Logger.Info(fmt.Sprintf("Creating VM with role '%s'...", nodeRole(m.machineContext)))

	virtualMachine := newVirtualMachineFromKubevirtMachine(m.machineContext)

	mutateFn := func() (err error) {
		// Ensure the KubevirtMachine is marked as an owner of the VirtualMachine.
		virtualMachine.SetOwnerReferences(util.EnsureOwnerRef(
			virtualMachine.OwnerReferences,
			metav1.OwnerReference{
				APIVersion: m.machineContext.KubevirtMachine.APIVersion,
				Kind:       m.machineContext.KubevirtMachine.Kind,
				Name:       m.machineContext.KubevirtMachine.Name,
				UID:        m.machineContext.KubevirtMachine.UID,
			}))

		// TODO: to remove those labels
		if virtualMachine.Labels == nil {
			virtualMachine.Labels = map[string]string{}
		}
		virtualMachine.Labels[clusterv1.ClusterLabelName] = "capk"

		return nil
	}
	if _, err := controllerutil.CreateOrUpdate(gocontext.Background(), m.client, virtualMachine, mutateFn); err != nil {
		return err
	}

	namespacedName := types.NamespacedName{Namespace: m.machineContext.KubevirtMachine.Namespace, Name: m.machineContext.KubevirtMachine.Name}
	vmi := &kubevirtv1.VirtualMachineInstance{}
	if err := m.client.Get(m.machineContext.Context, namespacedName, vmi); err != nil {
		if apierrors.IsNotFound(err) {
			return errors.New("failed to create VM instance")
		}
	}

	return nil
}

// Address returns the IP address of the VM.
func (m *Machine) Address() string {
	if m.vmInstance != nil && len(m.vmInstance.Status.Interfaces) > 0 {
		return m.vmInstance.Status.Interfaces[0].IP
	}

	return ""
}

// IsBooted checks if the VM is booted.
func (m *Machine) IsBooted(executor CommandExecutor) bool {
	if m.Address() == "" {
		return false
	}

	output, err := executor.ExecuteCommand("hostname")
	if err != nil || output != m.machineContext.KubevirtMachine.Name {
		return false
	}

	return true
}

// IsBootstrapped checks if the VM is bootstrapped with Kubernetes.
func (m *Machine) IsBootstrapped(executor CommandExecutor) bool {
	if !m.IsBooted(executor) {
		return false
	}

	output, err := executor.ExecuteCommand("cat /run/cluster-api/bootstrap-success.complete")
	if err != nil || output != "success" {
		return false
	}

	return true
}

// GenerateProviderID generates the KubeVirt provider ID to be used for the NodeRef
func (m *Machine) GenerateProviderID() (string, error) {
	if m.vmInstance == nil {
		return "", errors.New("Underlying Kubevirt VM is NOT running")
	}

	providerID := fmt.Sprintf("kubevirt://%s", m.machineContext.KubevirtMachine.Name)

	return providerID, nil
}
