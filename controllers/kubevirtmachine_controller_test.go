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
	gocontext "context"
	"time"

	"github.com/golang/mock/gomock"
	. "github.com/onsi/ginkgo"
	"github.com/onsi/ginkgo/extensions/table"
	. "github.com/onsi/gomega"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubevirtv1 "kubevirt.io/api/core/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha1"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/context"
	infraclustermock "sigs.k8s.io/cluster-api-provider-kubevirt/pkg/infracluster/mock"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/testing"
	workloadclustermock "sigs.k8s.io/cluster-api-provider-kubevirt/pkg/workloadcluster/mock"
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

	vm  *kubevirtv1.VirtualMachine
	vmi *kubevirtv1.VirtualMachineInstance

	sshKeySecretName    string
	bootstrapSecretName string

	sshKeySecret            *corev1.Secret
	bootstrapSecret         *corev1.Secret
	bootstrapUserDataSecret *corev1.Secret

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

	It("should panic when kubevirt cluster is not specified.", func() {
		if err := recover(); err != nil {
			Expect(kubevirtMachineReconciler.KubevirtClusterToKubevirtMachines(cluster)).To(Panic())
		}
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

var _ = Describe("reconcile a kubevirt machine", func() {
	mockCtrl = gomock.NewController(GinkgoT())
	workloadClusterMock := workloadclustermock.NewMockWorkloadCluster(mockCtrl)
	infraClusterMock := infraclustermock.NewMockInfraCluster(mockCtrl)
	testLogger := ctrl.Log.WithName("test")
	var machineContext *context.MachineContext

	BeforeEach(func() {

		bootstrapSecretName = "bootstrap-secret"
		sshKeySecretName = "ssh-keys"
		clusterName = "kvcluster"
		machineName = "test-machine"
		kubevirtMachineName = "test-kubevirt-machine"
		kubevirtMachine = testing.NewKubevirtMachine(kubevirtMachineName, machineName)
		kubevirtCluster = testing.NewKubevirtCluster(clusterName, machineName)

		cluster = testing.NewCluster(clusterName, kubevirtCluster)
		machine = testing.NewMachine(clusterName, machineName, kubevirtMachine)

		machine.Spec = clusterv1.MachineSpec{
			Bootstrap: clusterv1.Bootstrap{
				DataSecretName: &bootstrapSecretName,
			},
		}

		kubevirtCluster.Spec.SshKeys = infrav1.SSHKeys{DataSecretName: &sshKeySecretName}

		sshKeySecret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: sshKeySecretName,
			},
			Data: map[string][]byte{
				"pub": []byte("sha-rsa 1234"),
				"key": []byte("sha-rsa 5678"),
			},
		}

		bootstrapSecret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: bootstrapSecretName,
			},
			Data: map[string][]byte{
				"value": []byte("shell-script"),
			},
		}

		bootstrapUserDataSecret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: bootstrapSecretName + "-userdata",
			},
			Data: map[string][]byte{
				"userdata": []byte("shell-script"),
			},
		}

		vm = &kubevirtv1.VirtualMachine{
			TypeMeta: metav1.TypeMeta{
				Kind:       "VirtualMachine",
				APIVersion: "kubevirt.io",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: kubevirtMachineName,
			},
		}

		vmi = &kubevirtv1.VirtualMachineInstance{
			TypeMeta: metav1.TypeMeta{
				Kind:       "VirtualMachineInstance",
				APIVersion: "kubevirt.io",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: kubevirtMachineName,
			},
		}

	})

	setupClient := func(objects []client.Object) {
		machineContext = &context.MachineContext{
			Context:         gocontext.Background(),
			Cluster:         cluster,
			KubevirtCluster: kubevirtCluster,
			Machine:         machine,
			KubevirtMachine: kubevirtMachine,
			Logger:          testLogger,
		}

		fakeClient = fake.NewClientBuilder().WithScheme(setupScheme()).WithObjects(objects...).Build()
		kubevirtMachineReconciler = KubevirtMachineReconciler{
			Client:          fakeClient,
			WorkloadCluster: workloadClusterMock,
			InfraCluster:    infraClusterMock,
		}

	}
	AfterEach(func() {})

	It("should create KubeVirt VM", func() {
		objects := []client.Object{
			cluster,
			kubevirtCluster,
			machine,
			kubevirtMachine,
			sshKeySecret,
			bootstrapSecret,
			bootstrapUserDataSecret,
		}

		setupClient(objects)

		clusterContext := &context.ClusterContext{Context: machineContext.Context, Cluster: machineContext.Cluster, KubevirtCluster: machineContext.KubevirtCluster, Logger: machineContext.Logger}
		infraClusterMock.EXPECT().GenerateInfraClusterClient(clusterContext).Return(fakeClient, cluster.Namespace, nil)

		out, err := kubevirtMachineReconciler.reconcileNormal(machineContext)

		Expect(err).ShouldNot(HaveOccurred())

		// should expect to re-enqueue while waiting for VMI to come online
		Expect(out).To(Equal(ctrl.Result{RequeueAfter: 20 * time.Second}))

		// should expect VM to be created with expected name
		vm := &kubevirtv1.VirtualMachine{}
		vmKey := client.ObjectKey{Namespace: kubevirtMachine.Namespace, Name: kubevirtMachine.Name}
		err = fakeClient.Get(gocontext.Background(), vmKey, vm)
		Expect(err).NotTo(HaveOccurred())

		// Should expect kubevirt machine is still not ready
		Expect(machineContext.KubevirtMachine.Status.Ready).To(BeFalse())
		Expect(machineContext.KubevirtMachine.Spec.ProviderID).To(BeNil())
	})

	It("should create KubeVirt VM with externally managed cluster and no ssh key", func() {

		kubevirtCluster.Annotations = map[string]string{
			"cluster.x-k8s.io/managed-by": "external",
		}

		objects := []client.Object{
			cluster,
			kubevirtCluster,
			machine,
			kubevirtMachine,
			bootstrapSecret,
			bootstrapUserDataSecret,
		}

		setupClient(objects)

		clusterContext := &context.ClusterContext{Context: machineContext.Context, Cluster: machineContext.Cluster, KubevirtCluster: machineContext.KubevirtCluster, Logger: machineContext.Logger}
		infraClusterMock.EXPECT().GenerateInfraClusterClient(clusterContext).Return(fakeClient, cluster.Namespace, nil)

		out, err := kubevirtMachineReconciler.reconcileNormal(machineContext)

		Expect(err).ShouldNot(HaveOccurred())

		// should expect to re-enqueue while waiting for VMI to come online
		Expect(out).To(Equal(ctrl.Result{RequeueAfter: 20 * time.Second}))

		// should expect VM to be created with expected name
		vm := &kubevirtv1.VirtualMachine{}
		vmKey := client.ObjectKey{Namespace: kubevirtMachine.Namespace, Name: kubevirtMachine.Name}
		err = fakeClient.Get(gocontext.Background(), vmKey, vm)
		Expect(err).NotTo(HaveOccurred())

		// Should expect kubevirt machine is still not ready
		Expect(machineContext.KubevirtMachine.Status.Ready).To(BeFalse())
		Expect(machineContext.KubevirtMachine.Spec.ProviderID).To(BeNil())
	})

	It("should create KubeVirt VM in custom namespace", func() {

		customNamespace := "custom"
		kubevirtMachine.Spec.VirtualMachineTemplate.ObjectMeta.Namespace = customNamespace

		objects := []client.Object{
			cluster,
			kubevirtCluster,
			machine,
			kubevirtMachine,
			sshKeySecret,
			bootstrapSecret,
			bootstrapUserDataSecret,
		}

		setupClient(objects)

		clusterContext := &context.ClusterContext{Context: machineContext.Context, Cluster: machineContext.Cluster, KubevirtCluster: machineContext.KubevirtCluster, Logger: machineContext.Logger}
		infraClusterMock.EXPECT().GenerateInfraClusterClient(clusterContext).Return(fakeClient, cluster.Namespace, nil)

		out, err := kubevirtMachineReconciler.reconcileNormal(machineContext)

		Expect(err).ShouldNot(HaveOccurred())

		// should expect to re-enqueue while waiting for VMI to come online
		Expect(out).To(Equal(ctrl.Result{RequeueAfter: 20 * time.Second}))

		// should expect VM to be created with expected name
		vm := &kubevirtv1.VirtualMachine{}
		vmKey := client.ObjectKey{Namespace: customNamespace, Name: kubevirtMachine.Name}
		err = fakeClient.Get(gocontext.Background(), vmKey, vm)
		Expect(err).NotTo(HaveOccurred())

		// Should expect kubevirt machine is still not ready
		Expect(machineContext.KubevirtMachine.Status.Ready).To(BeFalse())
		Expect(machineContext.KubevirtMachine.Spec.ProviderID).To(BeNil())
	})

	It("should detect when VMI is ready and mark KubevirtMachine ready", func() {
		vmi.Status.Conditions = []kubevirtv1.VirtualMachineInstanceCondition{
			{
				Type:   kubevirtv1.VirtualMachineInstanceReady,
				Status: corev1.ConditionTrue,
			},
		}
		vmi.Status.Interfaces = []kubevirtv1.VirtualMachineInstanceNetworkInterface{

			{
				IP: "1.1.1.1",
			},
		}

		objects := []client.Object{
			cluster,
			kubevirtCluster,
			machine,
			kubevirtMachine,
			sshKeySecret,
			bootstrapSecret,
			bootstrapUserDataSecret,
			vm,
			vmi,
		}

		setupClient(objects)

		clusterContext := &context.ClusterContext{Context: machineContext.Context, Cluster: machineContext.Cluster, KubevirtCluster: machineContext.KubevirtCluster, Logger: machineContext.Logger}
		infraClusterMock.EXPECT().GenerateInfraClusterClient(clusterContext).Return(fakeClient, cluster.Namespace, nil)

		Expect(machineContext.KubevirtMachine.Status.Ready).To(BeFalse())
		out, err := kubevirtMachineReconciler.reconcileNormal(machineContext)

		Expect(err).ShouldNot(HaveOccurred())

		// should expect to re-enqueue while waiting for VMI to come online
		Expect(out).To(Equal(ctrl.Result{}))

		// should expect VM to be created with expected name
		vm := &kubevirtv1.VirtualMachine{}
		vmKey := client.ObjectKey{Namespace: kubevirtMachine.Namespace, Name: kubevirtMachine.Name}
		err = fakeClient.Get(gocontext.Background(), vmKey, vm)
		Expect(err).NotTo(HaveOccurred())

		Expect(machineContext.KubevirtMachine.Status.Ready).To(BeTrue())
		Expect(*machineContext.KubevirtMachine.Spec.ProviderID).To(Equal("kubevirt://" + kubevirtMachineName))
	})

	It("should detect when a previous Ready KubeVirtMachine is no longer ready due to vmi ready condition being false", func() {
		vmi.Status.Conditions = []kubevirtv1.VirtualMachineInstanceCondition{
			{
				Type:   kubevirtv1.VirtualMachineInstanceReady,
				Status: corev1.ConditionFalse,
			},
		}

		kubevirtMachine.Status.Ready = true
		objects := []client.Object{
			cluster,
			kubevirtCluster,
			machine,
			kubevirtMachine,
			sshKeySecret,
			bootstrapSecret,
			bootstrapUserDataSecret,
			vm,
			vmi,
		}

		setupClient(objects)

		clusterContext := &context.ClusterContext{Context: machineContext.Context, Cluster: machineContext.Cluster, KubevirtCluster: machineContext.KubevirtCluster, Logger: machineContext.Logger}
		infraClusterMock.EXPECT().GenerateInfraClusterClient(clusterContext).Return(fakeClient, cluster.Namespace, nil)

		Expect(machineContext.KubevirtMachine.Status.Ready).To(BeTrue())
		out, err := kubevirtMachineReconciler.reconcileNormal(machineContext)
		Expect(machineContext.KubevirtMachine.Status.Ready).To(BeFalse())
		Expect(err).ToNot(HaveOccurred())
		Expect(out).To(Equal(ctrl.Result{RequeueAfter: 20 * time.Second}))
	})

	It("should detect when a previous Ready KubeVirtMachine is no longer ready due to missing vmi object", func() {
		kubevirtMachine.Status.Ready = true
		objects := []client.Object{
			cluster,
			kubevirtCluster,
			machine,
			kubevirtMachine,
			sshKeySecret,
			bootstrapSecret,
			bootstrapUserDataSecret,
		}

		setupClient(objects)

		clusterContext := &context.ClusterContext{Context: machineContext.Context, Cluster: machineContext.Cluster, KubevirtCluster: machineContext.KubevirtCluster, Logger: machineContext.Logger}
		infraClusterMock.EXPECT().GenerateInfraClusterClient(clusterContext).Return(fakeClient, cluster.Namespace, nil)

		Expect(machineContext.KubevirtMachine.Status.Ready).To(BeTrue())
		out, err := kubevirtMachineReconciler.reconcileNormal(machineContext)
		Expect(machineContext.KubevirtMachine.Status.Ready).To(BeFalse())
		Expect(err).ToNot(HaveOccurred())
		Expect(out).To(Equal(ctrl.Result{RequeueAfter: 20 * time.Second}))
	})
})

var _ = Describe("updateNodeProviderID", func() {
	mockCtrl = gomock.NewController(GinkgoT())
	workloadClusterMock := workloadclustermock.NewMockWorkloadCluster(mockCtrl)
	infraClusterMock := infraclustermock.NewMockInfraCluster(mockCtrl)
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
			InfraCluster:    infraClusterMock,
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
		Expect(err).ToNot(HaveOccurred())
		Expect(out).To(Equal(ctrl.Result{RequeueAfter: 10 * time.Second}))
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
