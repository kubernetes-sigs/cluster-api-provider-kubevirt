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
	"time"

	"github.com/golang/mock/gomock"
	. "github.com/onsi/ginkgo"
	"github.com/onsi/ginkgo/extensions/table"
	. "github.com/onsi/gomega"
	"github.com/pkg/errors"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/context"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/testing"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/workloadcluster/mock"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubevirtv1 "kubevirt.io/api/core/v1"
	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1beta1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
)

var (
	mockCtrl *gomock.Controller

	clusterName         string
	kubevirtClusterName string
	kubevirtCluster     *infrav1.KubevirtCluster
	cluster             *clusterv1.Cluster

	machineName             string
	kubevirtMachineName     string
	kubevirtMachine         *infrav1.KubevirtMachine
	kubevirtMachineNotExist *infrav1.KubevirtMachine
	machine                 *clusterv1.Machine

	anotherMachineName         string
	anotherKubevirtMachineName string
	anotherKubevirtMachine     *infrav1.KubevirtMachine
	anotherMachine             *clusterv1.Machine

	fakeClient                client.Client
	kubevirtMachineReconciler KubevirtMachineReconciler
	fakeWorkloadClusterClient client.Client
)

var _ = Describe("KubevirtClusterToKubevirtMachines", func() {

	BeforeEach(func() {
		clusterName = "test-cluster"
		kubevirtClusterName = "test-kubevirt-cluster"
		kubevirtCluster = testing.NewKubevirtCluster(clusterName, kubevirtClusterName)
		cluster = testing.NewCluster(clusterName, kubevirtCluster)

		machineName = "test-machine"
		kubevirtMachineName = "test-kubevirt-machine"
		kubevirtMachine = testing.NewKubevirtMachine(kubevirtMachineName, machineName)
		machine = testing.NewMachine(clusterName, machineName, kubevirtMachine)

		anotherMachineName = "another-test-machine"
		anotherKubevirtMachineName = "another-test-kubevirt-machine"
		anotherKubevirtMachine = testing.NewKubevirtMachine(anotherKubevirtMachineName, anotherMachineName)
		anotherMachine = testing.NewMachine(clusterName, anotherMachineName, anotherKubevirtMachine)

		objects := []client.Object{
			cluster,
			kubevirtCluster,
			machine,
			anotherMachine,
			// add one more machine without corresponding kubevirt machine, to test that no request is created for it
			testing.NewMachine(clusterName, "machine-without-corresponding-kubevirt-machine", nil),
		}
		fakeClient = fake.NewClientBuilder().WithScheme(setupScheme()).WithObjects(objects...).Build()
		kubevirtMachineReconciler = KubevirtMachineReconciler{
			Client: fakeClient,
		}
	})

	AfterEach(func() {})

	It("should generate requests for all Kubevirt machines in the cluster", func() {
		out := kubevirtMachineReconciler.KubevirtClusterToKubevirtMachines(kubevirtCluster)
		Expect(out).To(HaveLen(2))
		machineNames := make([]string, len(out))
		for i := range out {
			machineNames[i] = out[i].Name
		}
		Expect(machineNames).To(ConsistOf("test-machine", "another-test-machine"))
	})
})

var _ = Describe("utility functions", func() {
	table.DescribeTable("should detect userdata is cloud-config", func(userData []byte, expected bool) {
		Expect(isCloudConfigUserData(userData)).To(Equal(expected))
	},
		table.Entry("should detect cloud-config", []byte("#something\n\n#something else\n#cloud-config\nthe end"), true),
		table.Entry("should not detect cloud-config", []byte("#something\n\n#something else\n#not-cloud-config\nthe end"), false),
		table.Entry("should not detect cloud-config", []byte("#something\n\n#something else\n   #cloud-config\nthe end"), false),
	)
})

var _ = Describe("updateNodeProviderID", func() {
	mockCtrl = gomock.NewController(GinkgoT())
	workloadClusterMock := mock.NewMockWorkloadCluster(mockCtrl)
	expectedProviderId := "aa-66@test"
	testLogger := ctrl.Log.WithName("test")

	BeforeEach(func() {
		machineName = "test-machine"
		kubevirtMachineName = "test-kubevirt-machine"
		kubevirtMachine = testing.NewKubevirtMachine(kubevirtMachineName, machineName)
		kubevirtMachineNotExist = testing.NewKubevirtMachine("test-machine-2", machineName)

		objects := []client.Object{
			kubevirtMachine,
		}
		fakeClient = fake.NewClientBuilder().WithScheme(setupScheme()).WithObjects(objects...).Build()
		kubevirtMachineReconciler = KubevirtMachineReconciler{
			Client:          fakeClient,
			WorkloadCluster: workloadClusterMock,
		}

		workloadClusterObjects := []client.Object{
			&corev1.Node{
				TypeMeta: metav1.TypeMeta{
					Kind: "Node",
				},
				ObjectMeta: metav1.ObjectMeta{
					Namespace: kubevirtMachine.Namespace,
					Name:      kubevirtMachine.Name,
				},
			},
		}
		fakeWorkloadClusterClient = fake.NewClientBuilder().WithScheme(setupScheme()).WithObjects(workloadClusterObjects...).Build()

	})

	AfterEach(func() {})

	It("should set providerID to Node", func() {
		kubevirtMachine.Spec.ProviderID = &expectedProviderId
		machineContext := &context.MachineContext{KubevirtMachine: kubevirtMachine, Logger: testLogger}
		workloadClusterMock.EXPECT().GenerateWorkloadClusterClient(machineContext).Return(fakeWorkloadClusterClient, nil)
		out, err := kubevirtMachineReconciler.updateNodeProviderID(machineContext)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(out).To(Equal(ctrl.Result{}))
		workloadClusterNode := &corev1.Node{}
		workloadClusterNodeKey := client.ObjectKey{Namespace: kubevirtMachine.Namespace, Name: kubevirtMachine.Name}
		err = fakeWorkloadClusterClient.Get(machineContext, workloadClusterNodeKey, workloadClusterNode)
		Expect(err).NotTo(HaveOccurred())
		Expect(workloadClusterNode.Spec.ProviderID).To(Equal(expectedProviderId))
		Expect(kubevirtMachine.Status.NodeUpdated).To(Equal(true))
	})

	It("GenerateWorkloadClusterClient failure", func() {
		kubevirtMachine.Spec.ProviderID = &expectedProviderId
		machineContext := &context.MachineContext{KubevirtMachine: kubevirtMachine, Logger: testLogger}
		workloadClusterMock.EXPECT().GenerateWorkloadClusterClient(machineContext).Return(nil, errors.New("test error"))
		out, err := kubevirtMachineReconciler.updateNodeProviderID(machineContext)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(out).To(Equal(ctrl.Result{RequeueAfter: 10 * time.Second}))
		workloadClusterNode := &corev1.Node{}
		workloadClusterNodeKey := client.ObjectKey{Namespace: kubevirtMachine.Namespace, Name: kubevirtMachine.Name}
		err = fakeWorkloadClusterClient.Get(machineContext, workloadClusterNodeKey, workloadClusterNode)
		Expect(err).NotTo(HaveOccurred())
		Expect(workloadClusterNode.Spec.ProviderID).NotTo(Equal(expectedProviderId))
		Expect(kubevirtMachine.Status.NodeUpdated).To(Equal(false))
	})

	It("Node doesn't exist", func() {
		kubevirtMachine.Spec.ProviderID = &expectedProviderId
		machineContext := &context.MachineContext{KubevirtMachine: kubevirtMachineNotExist, Logger: testLogger}
		workloadClusterMock.EXPECT().GenerateWorkloadClusterClient(machineContext).Return(fakeWorkloadClusterClient, nil)
		out, err := kubevirtMachineReconciler.updateNodeProviderID(machineContext)
		Expect(err).Should(HaveOccurred())
		Expect(out).To(Equal(ctrl.Result{RequeueAfter: 5 * time.Second}))
		workloadClusterNode := &corev1.Node{}
		workloadClusterNodeKey := client.ObjectKey{Namespace: kubevirtMachine.Namespace, Name: kubevirtMachine.Name}
		err = fakeWorkloadClusterClient.Get(machineContext, workloadClusterNodeKey, workloadClusterNode)
		Expect(err).NotTo(HaveOccurred())
		Expect(workloadClusterNode.Spec.ProviderID).NotTo(Equal(expectedProviderId))
		Expect(kubevirtMachine.Status.NodeUpdated).To(Equal(false))
	})
})

func setupScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	if err := clusterv1.AddToScheme(s); err != nil {
		panic(err)
	}
	if err := infrav1.AddToScheme(s); err != nil {
		panic(err)
	}
	if err := kubevirtv1.AddToScheme(s); err != nil {
		panic(err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		panic(err)
	}
	return s
}
