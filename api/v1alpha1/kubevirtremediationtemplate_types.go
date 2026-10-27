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

// KubevirtRemediationTemplateResource describes the data needed to create a
// KubevirtRemediation from a template.
type KubevirtRemediationTemplateResource struct {
	// spec is the specification of the desired behavior of the KubevirtRemediation.
	Spec KubevirtRemediationSpec `json:"spec"`
}

// KubevirtRemediationTemplateSpec defines the desired state of KubevirtRemediationTemplate.
type KubevirtRemediationTemplateSpec struct {
	Template KubevirtRemediationTemplateResource `json:"template"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=kubevirtremediationtemplates,scope=Namespaced,categories=cluster-api,shortName=kvrt
// +kubebuilder:storageversion

// KubevirtRemediationTemplate is the Schema for the kubevirtremediationtemplates API.
// Reference it from MachineHealthCheck spec.remediation.templateRef to have
// unhealthy Machines restarted instead of deleted.
type KubevirtRemediationTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec KubevirtRemediationTemplateSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// KubevirtRemediationTemplateList contains a list of KubevirtRemediationTemplate.
type KubevirtRemediationTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KubevirtRemediationTemplate `json:"items"`
}
