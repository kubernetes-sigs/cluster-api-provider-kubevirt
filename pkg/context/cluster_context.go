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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
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

// WithStepCounterIf is a custom merge strategy that adds a step counter to the message.
type WithStepCounterIf struct {
	defaultStrategy conditions.MergeStrategy
	addStepCounter  bool
}

// Merge merges the conditions and adds a step counter to the message if addStepCounter is true.
func (s *WithStepCounterIf) Merge(op conditions.MergeOperation, conditions []conditions.ConditionWithOwnerInfo, conditionTypes []string) (metav1.ConditionStatus, string, string, error) {
	status, reason, message, err := s.defaultStrategy.Merge(op, conditions, conditionTypes)
	if err != nil {
		return status, reason, message, err
	}

	if s.addStepCounter {
		// 조건 중 True인 개수를 계산
		trueCount := 0
		for _, c := range conditions {
			if c.Status == metav1.ConditionTrue {
				trueCount++
			}
		}
		totalCount := len(conditionTypes)
		message = fmt.Sprintf("%s (%d/%d conditions met)", message, trueCount, totalCount)
	}

	return status, reason, message, nil
}

// PatchKubevirtCluster patches the KubevirtCluster object and status.
func (c *ClusterContext) PatchKubevirtCluster(patchHelper *patch.Helper) error {
	// Always update the readyCondition by summarizing the state of other conditions.
	// A step counter is added to represent progress during the provisioning process (instead we are hiding it during the deletion process).
	if err := conditions.SetSummaryCondition(
		c.KubevirtCluster,
		c.KubevirtCluster,
		clusterv1.ReadyCondition,
		conditions.ForConditionTypes{infrav1.LoadBalancerAvailableCondition},
		conditions.CustomMergeStrategy{
			MergeStrategy: &WithStepCounterIf{
				defaultStrategy: conditions.DefaultMergeStrategy(),
				addStepCounter:  c.KubevirtCluster.DeletionTimestamp.IsZero(),
			},
		},
	); err != nil {
		return fmt.Errorf("failed to set summary condition: %w", err)
	}

	// Patch the object, ignoring conflicts on the conditions owned by this controller.
	return patchHelper.Patch(
		c.Context,
		c.KubevirtCluster,
		patch.WithOwnedConditions{Conditions: []string{
			clusterv1.ReadyCondition,
			infrav1.LoadBalancerAvailableCondition,
		}},
	)
}
