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

package context

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha1"
)

func TestEnsureNonEmptyConditionMessages(t *testing.T) {
	machine := &infrav1.KubevirtMachine{
		Status: infrav1.KubevirtMachineStatus{
			Conditions: []metav1.Condition{
				{Type: "Ready", Reason: "Ready", Message: "already set"},
				{Type: "VMProvisioned", Reason: "VMProvisioned"},
				{Type: "BootstrapExecSucceeded"},
			},
		},
	}

	ensureNonEmptyConditionMessages(machine)

	conditions := machine.Status.Conditions
	if conditions[0].Message != "already set" {
		t.Fatalf("existing message was changed: %q", conditions[0].Message)
	}
	if conditions[1].Message != conditions[1].Reason {
		t.Fatalf("missing message was not derived from reason: %q", conditions[1].Message)
	}
	if conditions[2].Reason == "" || conditions[2].Message == "" {
		t.Fatalf("missing reason and message were not defaulted: %#v", conditions[2])
	}
}
