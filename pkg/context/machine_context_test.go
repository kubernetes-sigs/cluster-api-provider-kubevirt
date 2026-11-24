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
	gocontext "context"
	"testing"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha1"
	capktesting "sigs.k8s.io/cluster-api-provider-kubevirt/pkg/testing"
)

func TestEnsureNonEmptyConditionMessages(t *testing.T) {
	machine := &infrav1.KubevirtMachine{
		Status: infrav1.KubevirtMachineStatus{
			Conditions: []metav1.Condition{
				{Type: "Ready", Reason: "Ready", Message: "already set"},
				{Type: "VMProvisioned", Reason: "VMProvisioned"},
				{Type: "BootstrapExecSucceeded"},
				{Type: "example.com/Healthy", Reason: "Healthy"},
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
	if conditions[3].Message != "" {
		t.Fatalf("unowned condition message was changed: %q", conditions[3].Message)
	}
}

func TestPatchKubevirtMachinePreservesUnownedConditions(t *testing.T) {
	ctx := gocontext.Background()
	machine := capktesting.NewKubevirtMachine("kubevirt-machine", "machine")
	machine.Namespace = "default"
	machine.Status.Conditions = []metav1.Condition{
		{
			Type:               "example.com/Healthy",
			Status:             metav1.ConditionTrue,
			Reason:             "Healthy",
			Message:            "",
			LastTransitionTime: metav1.Now(),
		},
		{
			Type:               infrav1.VMProvisionedCondition,
			Status:             metav1.ConditionFalse,
			Reason:             "Waiting",
			Message:            "",
			LastTransitionTime: metav1.Now(),
		},
	}

	fakeClient := ctrlfake.NewClientBuilder().
		WithScheme(capktesting.SetupScheme()).
		WithObjects(machine).
		WithStatusSubresource(machine).
		Build()
	if err := fakeClient.Status().Update(ctx, machine); err != nil {
		t.Fatalf("failed to update initial status: %v", err)
	}

	patchHelper, err := patch.NewHelper(machine, fakeClient)
	if err != nil {
		t.Fatalf("failed to create patch helper: %v", err)
	}

	machineContext := &MachineContext{
		Context:         ctx,
		KubevirtMachine: machine,
		Logger:          logr.Discard(),
	}
	if err := machineContext.PatchKubevirtMachine(patchHelper); err != nil {
		t.Fatalf("failed to patch KubevirtMachine: %v", err)
	}

	updated := &infrav1.KubevirtMachine{}
	if err := fakeClient.Get(ctx, client.ObjectKeyFromObject(machine), updated); err != nil {
		t.Fatalf("failed to get updated KubevirtMachine: %v", err)
	}

	var unowned, owned *metav1.Condition
	for i := range updated.Status.Conditions {
		condition := &updated.Status.Conditions[i]
		switch condition.Type {
		case "example.com/Healthy":
			unowned = condition
		case infrav1.VMProvisionedCondition:
			owned = condition
		}
	}
	if unowned == nil || owned == nil {
		t.Fatalf("expected conditions were not found: %#v", updated.Status.Conditions)
	}
	if unowned.Message != "" {
		t.Fatalf("unowned condition message was changed: %q", unowned.Message)
	}
	if owned.Message == "" {
		t.Fatalf("owned condition message was not defaulted: %q", owned.Message)
	}
}
