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

package context

import (
	"context"
	"fmt"
	"regexp"

	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/patch"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha4"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1alpha4"
)

// MachineContext is a Go context used with a KubeVirt machine.
type MachineContext struct {
	context.Context
	Cluster             *clusterv1.Cluster
	Machine             *clusterv1.Machine
	KubevirtCluster     *infrav1.KubevirtCluster
	KubevirtMachine     *infrav1.KubevirtMachine
	BootstrapDataSecret *corev1.Secret
	Logger              logr.Logger
}

// String returns KubeVirt machine GroupVersionKind
func (c *MachineContext) String() string {
	return fmt.Sprintf("%s %s/%s", c.KubevirtMachine.GroupVersionKind(), c.KubevirtMachine.Namespace, c.KubevirtMachine.Name)
}

// PatchKubevirtMachine patches the KubevirtMachine object and status.
func (c *MachineContext) PatchKubevirtMachine(patchHelper *patch.Helper) error {
	// Always update the readyCondition by summarizing the state of other conditions.
	// A step counter is added to represent progress during the provisioning process (instead we are hiding the step counter during the deletion process).
	conditions.SetSummary(c.KubevirtMachine,
		conditions.WithConditions(
			infrav1.VMProvisionedCondition,
			infrav1.BootstrapExecSucceededCondition,
		),
		conditions.WithStepCounterIf(c.KubevirtMachine.ObjectMeta.DeletionTimestamp.IsZero()),
	)

	// Patch the object, ignoring conflicts on the conditions owned by this controller.
	return patchHelper.Patch(
		c.Context,
		c.KubevirtMachine,
		patch.WithOwnedConditions{Conditions: []clusterv1.ConditionType{
			clusterv1.ReadyCondition,
			infrav1.VMProvisionedCondition,
			infrav1.BootstrapExecSucceededCondition,
		}},
	)
}

func (c *MachineContext) HasInjectedCapkSSHKeys() bool {
	// TODO get secret if it doesn't already exist
	if c.BootstrapDataSecret == nil {
		return false
	}
	value, ok := c.BootstrapDataSecret.Data["value"]
	if !ok {
		return false
	}
	// TODO actually look for ssh key in user data
	return regexp.MustCompile(`gecos: CAPK User`).MatchString(string(value))
}

func (c *MachineContext) HasCloudConfigUserData() bool {
	// TODO get secret if it doesn't already exist
	if c.BootstrapDataSecret == nil {
		return false
	}
	value, ok := c.BootstrapDataSecret.Data["value"]
	if !ok {
		return false
	}
	return regexp.MustCompile(`^#cloud-config`).MatchString(string(value))
}
