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

	"github.com/go-logr/logr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/patch"

	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha1"
)

// ClusterContext is a Go context used with a KubeVirt cluster.
type ClusterContext struct {
	context.Context
	Cluster         *clusterv1.Cluster
	KubevirtCluster *infrav1.KubevirtCluster
	Logger          logr.Logger
}

// String returns KubeVirt cluster GroupVersionKind.
func (c *ClusterContext) String() string {
	return fmt.Sprintf("%s %s/%s", c.KubevirtCluster.GroupVersionKind(), c.KubevirtCluster.Namespace, c.KubevirtCluster.Name)
}

// PatchKubevirtCluster patches the KubevirtCluster object and status.
func (c *ClusterContext) PatchKubevirtCluster(patchHelper *patch.Helper) error {
	// Always update the readyCondition by summarizing the state of other conditions.
	// A step counter is added to represent progress during the provisioning process (instead we are hiding it during the deletion process).
	conditions.SetSummary(c.KubevirtCluster,
		conditions.WithConditions(
			infrav1.LoadBalancerAvailableCondition,
		),
		conditions.WithStepCounterIf(c.KubevirtCluster.ObjectMeta.DeletionTimestamp.IsZero()),
	)

	// Patch the object, ignoring conflicts on the conditions owned by this controller.
	return patchHelper.Patch(
		c.Context,
		c.KubevirtCluster,
		patch.WithOwnedConditions{Conditions: []clusterv1.ConditionType{
			clusterv1.ReadyCondition,
			infrav1.LoadBalancerAvailableCondition,
		}},
	)
}
