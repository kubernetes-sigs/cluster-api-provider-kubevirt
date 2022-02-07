package kubevirt

//go:generate mockgen -source=./machine_factory.go -destination=./mock/machine_factory_generated.go -package=mock

import (
	gocontext "context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/context"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/ssh"

	"github.com/pkg/errors"
)

// MachineInterface abstracts the functions that the kubevirt.machine interface implements.

type MachineInterface interface {
	// Create creates a new VM for this machine.
	Create(ctx gocontext.Context) error
	// Delete deletes VM for this machine.
	Delete() error
	// Exists checks if the VM has been provisioned already.
	Exists() bool
	// IsReady checks if the VM is ready
	IsReady() bool
	// Address returns the IP address of the VM.
	Address() string
	// SupportsCheckingIsBootstrapped checks if we have a method of checking
	// that this bootstrapper has completed.
	SupportsCheckingIsBootstrapped() bool
	// IsBootstrapped checks if the VM is bootstrapped with Kubernetes.
	IsBootstrapped() bool
	// GenerateProviderID generates the KubeVirt provider ID to be used for the NodeRef
	GenerateProviderID() (string, error)
}

// MachineFactory allows creating new instances of kubevirt.machine

type MachineFactory interface {
	// NewMachine returns a new Machine service for the given context. 
	NewMachine(ctx *context.MachineContext, client client.Client, namespace string, sshKeys *ssh.ClusterNodeSshKeys) (MachineInterface, error)
}

// DefaultMachineFactory is the default implementation of MachineFactory
type DefaultMachineFactory struct {
}

// NewMachine creates a new kubevirt.machine
func (defaultMachineFactory DefaultMachineFactory) NewMachine(ctx *context.MachineContext, client client.Client, namespace string, sshKeys *ssh.ClusterNodeSshKeys) (MachineInterface, error) {
	externalMachine, err := NewMachine(ctx, client, namespace, sshKeys)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create helper for managing the externalMachine")
	}
	return externalMachine, nil
}
