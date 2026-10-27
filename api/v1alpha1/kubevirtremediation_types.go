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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// KubevirtRemediationType names a remediation strategy.
type KubevirtRemediationType string

const (
	// RebootRemediationType restarts the VirtualMachineInstance backing the
	// unhealthy Machine, preserving the VirtualMachine (and therefore the node
	// name and its disks) instead of replacing the whole Machine.
	RebootRemediationType KubevirtRemediationType = "Reboot"
)

// Remediation phases, stored in KubevirtRemediationStatus.Phase.
const (
	// PhaseRunning means a restart is about to be, or has just been, issued.
	PhaseRunning = "Running"
	// PhaseWaiting means a restart was issued and the controller is waiting
	// for the node to become healthy or for the retry timeout to expire.
	PhaseWaiting = "Waiting"
	// PhaseFailed means the retry budget is exhausted (or the VM cannot be
	// restarted at all) and the Machine has been handed back to Cluster API
	// for deletion by setting the OwnerRemediated condition to False.
	PhaseFailed = "Failed"
)

// KubevirtRemediationSpec defines the desired state of KubevirtRemediation.
type KubevirtRemediationSpec struct {
	// strategy field defines the remediation strategy.
	// +optional
	Strategy *RemediationStrategy `json:"strategy,omitempty"`
}

// RemediationStrategy describes how to remediate the Machine's VM.
type RemediationStrategy struct {
	// type of remediation. Only Reboot is supported.
	// +optional
	// +kubebuilder:validation:Enum=Reboot
	// +kubebuilder:default=Reboot
	Type KubevirtRemediationType `json:"type,omitempty"`

	// retryLimit sets the maximum number of restarts to attempt before
	// handing the Machine back to Cluster API for replacement.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	RetryLimit int32 `json:"retryLimit,omitempty"`

	// timeoutSeconds is how long to wait after a restart for the node to
	// become healthy before retrying or giving up. The Machine is considered
	// healthy again when the MachineHealthCheck deletes this remediation
	// object, so the timeout only has to outlast a normal boot.
	// +optional
	// +kubebuilder:validation:Minimum=60
	// +kubebuilder:default=600
	TimeoutSeconds int32 `json:"timeoutSeconds,omitempty"`
}

// KubevirtRemediationStatus defines the observed state of KubevirtRemediation.
type KubevirtRemediationStatus struct {
	// phase represents the current phase of remediation.
	// +optional
	// +kubebuilder:validation:Enum=Running;Waiting;Failed
	Phase string `json:"phase,omitempty"`

	// retryCount is the number of restarts issued so far.
	// +optional
	RetryCount int32 `json:"retryCount,omitempty"`

	// lastRemediated identifies when the last restart was issued.
	// +optional
	LastRemediated *metav1.Time `json:"lastRemediated,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=kubevirtremediations,scope=Namespaced,categories=cluster-api,shortName=kvr
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase",description="Current phase of the remediation"
// +kubebuilder:printcolumn:name="Retry count",type=string,JSONPath=".status.retryCount",description="Restarts issued so far"
// +kubebuilder:printcolumn:name="Retry limit",type=string,JSONPath=".spec.strategy.retryLimit",description="Maximum restarts before giving up"
// +kubebuilder:printcolumn:name="Last Remediated",type=string,JSONPath=".status.lastRemediated",description="Timestamp of the last restart"

// KubevirtRemediation is the Schema for the kubevirtremediations API.
// It is created by the Cluster API MachineHealthCheck controller (from a
// KubevirtRemediationTemplate referenced in spec.remediation.templateRef)
// when a Machine fails its health check, and deleted by the same controller
// once the Machine passes again.
type KubevirtRemediation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KubevirtRemediationSpec   `json:"spec,omitempty"`
	Status KubevirtRemediationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// KubevirtRemediationList contains a list of KubevirtRemediation.
type KubevirtRemediationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KubevirtRemediation `json:"items"`
}
