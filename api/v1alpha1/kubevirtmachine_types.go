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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubevirtv1 "kubevirt.io/api/core/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/errors" //nolint:staticcheck // deprecated but no replacement yet
)

const (
	// MachineFinalizer allows ReconcileKubevirtMachine to clean up resources associated with machine before
	// removing it from the apiserver.
	MachineFinalizer = "kubevirtmachine.infrastructure.cluster.x-k8s.io"
)

// VirtualMachineTemplateSpec defines the desired state of the kubevirt VM.
type VirtualMachineTemplateSpec struct {
	// +kubebuilder:pruning:PreserveUnknownFields
	// +nullable
	ObjectMeta metav1.ObjectMeta `json:"metadata,omitempty"`
	// VirtualMachineSpec contains the VirtualMachine specification.
	Spec kubevirtv1.VirtualMachineSpec `json:"spec,omitempty" valid:"required"`
}

// KubevirtMachineSpec defines the desired state of KubevirtMachine.
type KubevirtMachineSpec struct {
	VirtualMachineTemplate VirtualMachineTemplateSpec `json:"virtualMachineTemplate,omitempty"`

	// ProviderID TBD what to use for Kubevirt
	// +optional
	ProviderID *string `json:"providerID,omitempty"`

	// BootstrapCheckSpec defines how the CAPK controller is checking CAPI Sentinel file inside the VM.
	// +optional
	BootstrapCheckSpec VirtualMachineBootstrapCheckSpec `json:"virtualMachineBootstrapCheck,omitempty"`

	// InfraClusterSecretRef is a reference to a secret with a kubeconfig for external cluster used for infra.
	// When nil, this defaults to the value present in the KubevirtCluster object's spec associated with this machine.
	// +optional
	InfraClusterSecretRef *corev1.ObjectReference `json:"infraClusterSecretRef,omitempty"`

	// CloudInit configures the cloud-init data source attached to the virtual machine.
	// When nil, a config drive data source carrying only the bootstrap user data is attached.
	// Cloud-init data is applied during the VM's first boot, so this field's presence and value
	// are immutable on KubevirtMachine after creation.
	//
	// The same holds for the content of the network data, wherever it comes from: it is copied to
	// the infra cluster once, when the controller first prepares the machine, and that copy is what
	// the guest boots with for the lifetime of the machine. Changing a referenced secret afterwards
	// has no effect on existing machines, not even after a restart or a live migration; it applies
	// to the machines created from then on. To change the network configuration of running
	// machines, replace them, for example by rolling out a new KubevirtMachineTemplate.
	// +optional
	CloudInit *CloudInitSpec `json:"cloudInit,omitempty"`
}

// CloudInitDataSource describes the type of cloud-init data source attached to the virtual machine.
type CloudInitDataSource string

const (
	// CloudInitDataSourceConfigDrive attaches an OpenStack config drive data source. Network
	// configuration is expected in the OpenStack network_data.json format.
	CloudInitDataSourceConfigDrive CloudInitDataSource = "configDrive"

	// CloudInitDataSourceNoCloud attaches a NoCloud data source. Network configuration is expected
	// in the cloud-init network configuration format, version 1 or 2.
	CloudInitDataSourceNoCloud CloudInitDataSource = "noCloud"
)

// +kubebuilder:validation:XValidation:rule="!(has(self.networkData) && has(self.networkDataSecretRef))", message="networkData and networkDataSecretRef are mutually exclusive"
// +kubebuilder:validation:XValidation:rule="!has(self.networkDataSecretRef) || size(self.networkDataSecretRef.name) > 0", message="networkDataSecretRef.name must not be empty"
// +kubebuilder:validation:XValidation:rule="!has(self.networkData) || size(self.networkData) > 0", message="networkData must not be empty"
// +kubebuilder:validation:XValidation:rule="(!has(self.networkData) && !has(self.networkDataSecretRef)) || has(self.dataSource)", message="dataSource must be set explicitly when networkData or networkDataSecretRef is set, because it decides the format the network data is read in"
// CloudInitSpec defines the cloud-init data source attached to the virtual machine.
type CloudInitSpec struct {
	// DataSource selects the type of cloud-init data source attached to the virtual machine.
	// This choice determines the format expected for the network configuration document, because
	// cloud-init parses it differently per data source: "configDrive" expects the OpenStack
	// network_data.json format, while "noCloud" expects the cloud-init network configuration
	// format, version 1 or 2. It also determines where the guest reads user data: NoCloud uses
	// /user-data, while configDrive uses /openstack/latest/user_data.
	//
	// When omitted, a config drive is attached. The field is deliberately not defaulted in the
	// API, and it is required as soon as network data is set: a network data document in the
	// format of the other data source is not an error anywhere, the guest just boots without
	// network, so the choice has to be a stated one.
	// +optional
	// +kubebuilder:validation:Enum=configDrive;noCloud
	DataSource CloudInitDataSource `json:"dataSource,omitempty"`

	// NetworkDataSecretRef is a reference to a secret in the KubevirtMachine's namespace, holding a
	// network configuration document under a "networkdata" or "networkData" key, in the format
	// expected by DataSource.
	// When set, the content is copied to the infra cluster and attached to the cloud-init data
	// source of the virtual machine, allowing the guest to configure its network without relying
	// on DHCP. This is intended for setups where addresses are managed externally, for example on
	// secondary interfaces attached through Multus.
	// When nil, the guest is expected to obtain its network configuration by other means, such as DHCP.
	//
	// The two keys are the ones KubeVirt itself looks up in a network data secret
	// (readFirstFoundFileFromDir in kubevirt/kubevirt pkg/cloud-init/cloud-init.go), in the same
	// order. The handling of a bad secret is deliberately stricter than KubeVirt's: KubeVirt
	// silently ignores a missing key or an empty value and boots the guest without network data,
	// whereas a KubevirtMachine is not provisioned until the secret holds a well formed document,
	// and reports why in its VMProvisioned condition and in a Warning event.
	//
	// The controller labels the referenced secret with clusterctl.cluster.x-k8s.io/move, so that
	// "clusterctl move" takes it along with the machines that reference it.
	// +optional
	NetworkDataSecretRef *corev1.LocalObjectReference `json:"networkDataSecretRef,omitempty"`

	// NetworkData is the network configuration document, in the format expected by DataSource:
	// a JSON object for configDrive, a YAML mapping for noCloud. A document that is not well
	// formed is rejected on admission.
	// This field is mutually exclusive with NetworkDataSecretRef. It is meant for small documents;
	// it is stored in the KubevirtMachine object and again in the infra cluster, so anything
	// larger than its limit belongs in a secret referenced by NetworkDataSecretRef.
	// +optional
	// +kubebuilder:validation:MaxLength=16384
	NetworkData *string `json:"networkData,omitempty"`
}

// GetDataSource returns the configured cloud-init data source type, defaulting to config drive when
// the cloud-init configuration or the data source type is unset.
func (c *CloudInitSpec) GetDataSource() CloudInitDataSource {
	if c == nil || c.DataSource == "" {
		return CloudInitDataSourceConfigDrive
	}

	return c.DataSource
}

// GetNetworkDataSecretRef returns the configured network data secret reference, or nil when the
// cloud-init configuration is unset.
func (c *CloudInitSpec) GetNetworkDataSecretRef() *corev1.LocalObjectReference {
	if c == nil {
		return nil
	}

	return c.NetworkDataSecretRef
}

// GetNetworkData returns inline network data, or nil when none is configured.
func (c *CloudInitSpec) GetNetworkData() *string {
	if c == nil {
		return nil
	}

	return c.NetworkData
}

// VirtualMachineBootstrapCheckSpec defines how the controller will remotely check CAPI Sentinel file content.
type VirtualMachineBootstrapCheckSpec struct {
	// CheckStrategy describes how CAPK controller will validate a successful CAPI bootstrap.
	// Following specified method, CAPK will try to retrieve the state of the CAPI Sentinel file from the VM.
	// Possible values are: "none" or "ssh" (default is "ssh") and this value is validated by apiserver.
	// +optional
	// +kubebuilder:validation:Enum=none;ssh
	// +kubebuilder:default:=ssh
	CheckStrategy string `json:"checkStrategy,omitempty"`
}

// KubevirtMachineInitializationStatus tracks CAPI v1beta2 contract fields.
type KubevirtMachineInitializationStatus struct {
	// Provisioned indicates that the infrastructure has been provisioned.
	// Required by CAPI v1beta2 Machine controller to proceed with providerID propagation.
	// +optional
	Provisioned *bool `json:"provisioned,omitempty"`
}

// KubevirtMachineStatus defines the observed state of KubevirtMachine.
type KubevirtMachineStatus struct {
	// Ready denotes that the machine is ready
	// +kubebuilder:default=false
	Ready bool `json:"ready"`

	// Initialization tracks CAPI v1beta2 contract initialization fields.
	// +optional
	Initialization *KubevirtMachineInitializationStatus `json:"initialization,omitempty"`

	// LoadBalancerConfigured denotes that the machine has been
	// added to the load balancer
	// +optional
	LoadBalancerConfigured bool `json:"loadBalancerConfigured,omitempty"`

	// Addresses contains the associated addresses for the machine.
	// +optional
	Addresses []clusterv1.MachineAddress `json:"addresses,omitempty"`

	// Conditions defines current service state of the KubevirtMachine.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// NodeUpdated denotes that the ProviderID is updated on Node of this KubevirtMachine
	// +optional
	NodeUpdated bool `json:"nodeupdated"`

	// FailureReason will be set in the event that there is a terminal problem
	// reconciling the Machine and will contain a succinct value suitable
	// for machine interpretation.
	//
	// This field should not be set for transitive errors that a controller
	// faces that are expected to be fixed automatically over
	// time (like service outages), but instead indicate that something is
	// fundamentally wrong with the Machine's spec or the configuration of
	// the controller, and that manual intervention is required. Examples
	// of terminal errors would be invalid combinations of settings in the
	// spec, values that are unsupported by the controller, or the
	// responsible controller itself being critically misconfigured.
	//
	// Any transient errors that occur during the reconciliation of Machines
	// can be added as events to the Machine object and/or logged in the
	// controller's output.
	// +optional
	FailureReason *errors.MachineStatusError `json:"failureReason,omitempty"`

	// FailureMessage will be set in the event that there is a terminal problem
	// reconciling the Machine and will contain a more verbose string suitable
	// for logging and human consumption.
	//
	// This field should not be set for transitive errors that a controller
	// faces that are expected to be fixed automatically over
	// time (like service outages), but instead indicate that something is
	// fundamentally wrong with the Machine's spec or the configuration of
	// the controller, and that manual intervention is required. Examples
	// of terminal errors would be invalid combinations of settings in the
	// spec, values that are unsupported by the controller, or the
	// responsible controller itself being critically misconfigured.
	//
	// Any transient errors that occur during the reconciliation of Machines
	// can be added as events to the Machine object and/or logged in the
	// controller's output.
	// +optional
	FailureMessage *string `json:"failureMessage,omitempty"`
}

// +kubebuilder:resource:path=kubevirtmachines,scope=Namespaced,categories=cluster-api
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="Ready",type="boolean",JSONPath=".status.ready",description="Is machine ready"
// +kubebuilder:validation:XValidation:rule="(has(self.spec) && has(self.spec.cloudInit)) == (has(oldSelf.spec) && has(oldSelf.spec.cloudInit))",message="CloudInit presence is immutable"
// +kubebuilder:validation:XValidation:rule="!has(self.spec) || !has(self.spec.cloudInit) || !has(oldSelf.spec) || !has(oldSelf.spec.cloudInit) || self.spec.cloudInit == oldSelf.spec.cloudInit",message="CloudInit is immutable"

// KubevirtMachine is the Schema for the kubevirtmachines API.
type KubevirtMachine struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KubevirtMachineSpec   `json:"spec,omitempty"`
	Status KubevirtMachineStatus `json:"status,omitempty"`
}

func (c *KubevirtMachine) GetConditions() []metav1.Condition {
	return c.Status.Conditions
}

func (c *KubevirtMachine) SetConditions(conditions []metav1.Condition) {
	c.Status.Conditions = conditions
}

// +kubebuilder:object:root=true

// KubevirtMachineList contains a list of KubevirtMachine.
type KubevirtMachineList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KubevirtMachine `json:"items"`
}
