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

package loadbalancer_test

import (
	gocontext "context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha1"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/context"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/loadbalancer"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/testing"
)

var (
	clusterName         = "test-cluster"
	kubevirtClusterName = "test-kubevirt-cluster"
	kubevirtCluster     = testing.NewKubevirtCluster(clusterName, kubevirtClusterName)
	cluster             = testing.NewCluster(clusterName, kubevirtCluster)
	loadBalancerService = newLoadBalancerService(clusterContext, kubevirtCluster)

	clusterContext = &context.ClusterContext{
		Logger:          ctrl.LoggerFrom(gocontext.TODO()).WithName("test"),
		Context:         gocontext.TODO(),
		Cluster:         cluster,
		KubevirtCluster: kubevirtCluster,
	}
)

var _ = Describe("Load Balancer", func() {
	var (
		fakeClient client.Client
		lb         *loadbalancer.LoadBalancer
		err        error
	)
	Context("when underlying service has not been created yet", func() {
		BeforeEach(func() {
			objects := []client.Object{
				cluster,
				kubevirtCluster,
			}
			fakeClient = fake.NewClientBuilder().WithScheme(testing.SetupScheme()).WithObjects(objects...).Build()
		})

		It("should initialize load balancer without error", func() {
			lb, err = loadbalancer.NewLoadBalancer(clusterContext, fakeClient, "")
			Expect(err).NotTo(HaveOccurred())
		})

		It("should return false for isFound()", func() {
			Expect(lb.IsFound()).To(BeFalse())
		})

		It("should return error for IP()", func() {
			_, err := lb.IP(clusterContext)
			Expect(err).To(HaveOccurred())
		})

		It("should succeed to create a new load balancer", func() {
			Expect(lb.Create(clusterContext)).To(Succeed())
		})
	})

	Context("when underlying service has been created already", func() {
		BeforeEach(func() {
			objects := []client.Object{
				cluster,
				kubevirtCluster,
				loadBalancerService,
			}
			fakeClient = fake.NewClientBuilder().WithScheme(testing.SetupScheme()).WithObjects(objects...).Build()
		})

		It("should initialize load balancer without error", func() {
			lb, err = loadbalancer.NewLoadBalancer(clusterContext, fakeClient, "")
			Expect(err).NotTo(HaveOccurred())
		})

		It("should return true for isFound()", func() {
			Expect(lb.IsFound()).To(BeTrue())
		})

		It("should return non-empty IP", func() {
			lbip, err := lb.IP(clusterContext)
			Expect(err).ToNot(HaveOccurred())
			Expect(lbip).ToNot(BeEmpty())
		})

		It("should NOT create a new load balancer", func() {
			err = lb.Create(clusterContext)
			Expect(err).To(HaveOccurred())
		})
	})
})

func newLoadBalancerService(ctx *context.ClusterContext, kubevirtCluster *infrav1.KubevirtCluster) *corev1.Service {
	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{
			Name: clusterName + "-lb",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: kubevirtCluster.APIVersion,
					Kind:       kubevirtCluster.Kind,
					Name:       kubevirtCluster.Name,
					UID:        kubevirtCluster.UID,
				},
			},
		},
		Spec: corev1.ServiceSpec{ClusterIP: "1.1.1.1"},
	}
}
