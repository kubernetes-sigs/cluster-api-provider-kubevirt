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

package controllers

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubevirtv1 "kubevirt.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha1"
	testutil "sigs.k8s.io/cluster-api-provider-kubevirt/pkg/testing"
)

var _ = Describe("KubevirtMachineTemplateReconciler - Reconcile", func() {
	var (
		mt         infrav1.KubevirtMachineTemplate
		fakeClient ctrlclient.Client
		reconciler *KubevirtMachineTemplateReconciler
		req        ctrl.Request
	)

	Context("when VM template has domain CPU set (happy path)", func() {
		BeforeEach(func() {
			mt = newMachineTemplate(nil, &kubevirtv1.CPU{Cores: 2}, nil)
			fakeClient = fake.NewClientBuilder().WithScheme(testutil.SetupScheme()).WithObjects(&mt).WithStatusSubresource(&mt).Build()
			reconciler = &KubevirtMachineTemplateReconciler{Client: fakeClient, Log: ctrl.Log.WithName("test")}
			req = ctrl.Request{NamespacedName: types.NamespacedName{Namespace: mt.Namespace, Name: mt.Name}}
		})

		It("should update status.capacity based on the VM template", func() {
			_, err := reconciler.Reconcile(context.TODO(), req)
			Expect(err).NotTo(HaveOccurred())

			// Read the object back and assert status.capacity was updated
			updated := &infrav1.KubevirtMachineTemplate{}
			Expect(fakeClient.Get(context.TODO(), types.NamespacedName{Namespace: mt.Namespace, Name: mt.Name}, updated)).To(Succeed())

			expected := corev1.ResourceList{corev1.ResourceCPU: *resource.NewQuantity(2, resource.DecimalSI)}
			Expect(updated.Status.Capacity).To(HaveLen(len(expected)))
			for k, ev := range expected {
				av, ok := updated.Status.Capacity[k]
				Expect(ok).To(BeTrue())
				Expect(ev.Equal(av)).To(BeTrue(), fmt.Sprintf("expected %v for %s, got %v", ev, k, av))
			}
		})
	})

	Context("when VM template pointer is nil", func() {
		BeforeEach(func() {
			mt = newMachineTemplate(nil, nil, nil)
			// make the inner virtualMachineTemplate template pointer nil to exercise early return
			mt.Spec.Template.Spec.VirtualMachineTemplate.Spec.Template = nil
			fakeClient = fake.NewClientBuilder().WithScheme(testutil.SetupScheme()).WithObjects(&mt).WithStatusSubresource(&mt).Build()
			reconciler = &KubevirtMachineTemplateReconciler{Client: fakeClient, Log: ctrl.Log.WithName("test-nil")}
			req = ctrl.Request{NamespacedName: types.NamespacedName{Namespace: mt.Namespace, Name: mt.Name}}
		})

		It("should not error and should leave status.capacity empty", func() {
			_, err := reconciler.Reconcile(context.TODO(), req)
			Expect(err).NotTo(HaveOccurred())

			updated := &infrav1.KubevirtMachineTemplate{}
			Expect(fakeClient.Get(context.TODO(), types.NamespacedName{Namespace: mt.Namespace, Name: mt.Name}, updated)).To(Succeed())
			Expect(updated.Status.Capacity).To(BeEmpty())
		})
	})

	Context("when Domain.Resources.Requests contains cpu and memory (no domain.cpu)", func() {
		BeforeEach(func() {
			resources := &kubevirtv1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("2"),
					corev1.ResourceMemory: resource.MustParse("4Gi"),
				},
			}
			mt = newMachineTemplate(resources, nil, nil)
			fakeClient = fake.NewClientBuilder().WithScheme(testutil.SetupScheme()).WithObjects(&mt).WithStatusSubresource(&mt).Build()
			reconciler = &KubevirtMachineTemplateReconciler{Client: fakeClient, Log: ctrl.Log.WithName("test-requests")}
			req = ctrl.Request{NamespacedName: types.NamespacedName{Namespace: mt.Namespace, Name: mt.Name}}
		})

		It("should derive capacity from requests", func() {
			_, err := reconciler.Reconcile(context.TODO(), req)
			Expect(err).NotTo(HaveOccurred())

			updated := &infrav1.KubevirtMachineTemplate{}
			Expect(fakeClient.Get(context.TODO(), types.NamespacedName{Namespace: mt.Namespace, Name: mt.Name}, updated)).To(Succeed())

			expected := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("4Gi")}
			Expect(updated.Status.Capacity).To(HaveLen(len(expected)))
			for k, ev := range expected {
				av, ok := updated.Status.Capacity[k]
				Expect(ok).To(BeTrue())
				Expect(ev.Equal(av)).To(BeTrue(), fmt.Sprintf("expected %v for %s, got %v", ev, k, av))
			}
		})
	})

	Context("when status already equals computed capacity (no-op)", func() {
		BeforeEach(func() {
			// Create mt with domain CPU=2 and pre-seed status.capacity with the same value
			mt = newMachineTemplate(nil, &kubevirtv1.CPU{Cores: 2}, nil)
			mt.Status.Capacity = corev1.ResourceList{corev1.ResourceCPU: *resource.NewQuantity(2, resource.DecimalSI)}
			fakeClient = fake.NewClientBuilder().WithScheme(testutil.SetupScheme()).WithObjects(&mt).WithStatusSubresource(&mt).Build()
			reconciler = &KubevirtMachineTemplateReconciler{Client: fakeClient, Log: ctrl.Log.WithName("test-noop")}
			req = ctrl.Request{NamespacedName: types.NamespacedName{Namespace: mt.Namespace, Name: mt.Name}}
		})

		It("should be idempotent and leave status unchanged", func() {
			// Run reconcile; it should succeed and not change the capacity (remains equal)
			_, err := reconciler.Reconcile(context.TODO(), req)
			Expect(err).NotTo(HaveOccurred())

			updated := &infrav1.KubevirtMachineTemplate{}
			Expect(fakeClient.Get(context.TODO(), types.NamespacedName{Namespace: mt.Namespace, Name: mt.Name}, updated)).To(Succeed())
			Expect(updated.Status.Capacity).To(HaveLen(1))
			av, ok := updated.Status.Capacity[corev1.ResourceCPU]
			Expect(ok).To(BeTrue())
			Expect(av.Equal(*resource.NewQuantity(2, resource.DecimalSI))).To(BeTrue())
		})
	})

	Context("when Limits are present and Requests/domain CPU absent (limits fallback)", func() {
		BeforeEach(func() {
			resources := &kubevirtv1.ResourceRequirements{
				Requests: corev1.ResourceList{},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("3"),
					corev1.ResourceMemory: resource.MustParse("6Gi"),
				},
			}
			mt = newMachineTemplate(resources, nil, nil)
			fakeClient = fake.NewClientBuilder().WithScheme(testutil.SetupScheme()).WithObjects(&mt).WithStatusSubresource(&mt).Build()
			reconciler = &KubevirtMachineTemplateReconciler{Client: fakeClient, Log: ctrl.Log.WithName("test-limits")}
			req = ctrl.Request{NamespacedName: types.NamespacedName{Namespace: mt.Namespace, Name: mt.Name}}
		})

		It("should derive capacity from limits as a final fallback", func() {
			_, err := reconciler.Reconcile(context.TODO(), req)
			Expect(err).NotTo(HaveOccurred())

			updated := &infrav1.KubevirtMachineTemplate{}
			Expect(fakeClient.Get(context.TODO(), types.NamespacedName{Namespace: mt.Namespace, Name: mt.Name}, updated)).To(Succeed())

			expected := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("3"), corev1.ResourceMemory: resource.MustParse("6Gi")}
			Expect(updated.Status.Capacity).To(HaveLen(len(expected)))
			for k, ev := range expected {
				av, ok := updated.Status.Capacity[k]
				Expect(ok).To(BeTrue())
				Expect(ev.Equal(av)).To(BeTrue(), fmt.Sprintf("expected %v for %s, got %v", ev, k, av))
			}
		})
	})
})

// Helper functions

func newMachineTemplate(resources *kubevirtv1.ResourceRequirements, cpu *kubevirtv1.CPU, memory *kubevirtv1.Memory) infrav1.KubevirtMachineTemplate {
	mt := &infrav1.KubevirtMachineTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-template",
			Namespace: "default",
		},
		Spec: infrav1.KubevirtMachineTemplateSpec{
			Template: infrav1.KubevirtMachineTemplateResource{
				Spec: infrav1.KubevirtMachineSpec{
					VirtualMachineTemplate: infrav1.VirtualMachineTemplateSpec{
						Spec: kubevirtv1.VirtualMachineSpec{
							Template: &kubevirtv1.VirtualMachineInstanceTemplateSpec{
								Spec: kubevirtv1.VirtualMachineInstanceSpec{
									Domain: kubevirtv1.DomainSpec{},
								},
							},
						},
					},
				},
			},
		},
	}

	if resources != nil {
		mt.Spec.Template.Spec.VirtualMachineTemplate.Spec.Template.Spec.Domain.Resources = *resources
	}
	if cpu != nil {
		mt.Spec.Template.Spec.VirtualMachineTemplate.Spec.Template.Spec.Domain.CPU = cpu
	}
	if memory != nil {
		mt.Spec.Template.Spec.VirtualMachineTemplate.Spec.Template.Spec.Domain.Memory = memory
	}

	return *mt
}
