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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/pkg/errors"

	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/kubevirt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubevirtv1 "kubevirt.io/api/core/v1"

	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/conditions"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	machinemocks "sigs.k8s.io/cluster-api-provider-kubevirt/pkg/kubevirt/mock"

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
			Client:         fakeClient,
			MachineFactory: kubevirt.DefaultMachineFactory{},
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
	DescribeTable("should detect userdata is cloud-config", func(userData []byte, expected bool) {
		Expect(isCloudConfigUserData(userData)).To(Equal(expected))
	},
		Entry("should detect cloud-config", []byte("#something\n\n#something else\n#cloud-config\nthe end"), true),
		Entry("should not detect cloud-config", []byte("#something\n\n#something else\n#not-cloud-config\nthe end"), false),
		Entry("should not detect cloud-config", []byte("#something\n\n#something else\n   #cloud-config\nthe end"), false),
	)
})

var _ = Describe("reconcile a kubevirt machine", func() {
	var (
		mockCtrl            *gomock.Controller
		workloadClusterMock *workloadclustermock.MockWorkloadCluster
		infraClusterMock    *infraclustermock.MockInfraCluster

		machineFactoryMock *machinemocks.MockMachineFactory
		machineMock        *machinemocks.MockMachineInterface
		machineContext     *context.MachineContext
		testLogger         = ctrl.Log.WithName("test")
	)

	BeforeEach(func() {

		mockCtrl = gomock.NewController(GinkgoT())
		workloadClusterMock = workloadclustermock.NewMockWorkloadCluster(mockCtrl)
		infraClusterMock = infraclustermock.NewMockInfraCluster(mockCtrl)

		bootstrapSecretName = "bootstrap-secret"
		sshKeySecretName = "ssh-keys"
		clusterName = "kvcluster"
		machineName = "test-machine"
		kubevirtMachineName = "test-kubevirt-machine"
		kubevirtMachine = testing.NewKubevirtMachine(kubevirtMachineName, machineName)
		kubevirtCluster = testing.NewKubevirtCluster(clusterName, machineName)

		workloadClusterMock = workloadclustermock.NewMockWorkloadCluster(mockCtrl)
		infraClusterMock = infraclustermock.NewMockInfraCluster(mockCtrl)
		machineFactoryMock = machinemocks.NewMockMachineFactory(mockCtrl)
		machineMock = machinemocks.NewMockMachineInterface(mockCtrl)

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

	setupClient := func(machineFactory kubevirt.MachineFactory, objects []client.Object) {
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
			MachineFactory:  machineFactory,
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

		setupClient(kubevirt.DefaultMachineFactory{}, objects)

		infraClusterMock.EXPECT().GenerateInfraClusterClient(kubevirtMachine.Spec.InfraClusterSecretRef, kubevirtMachine.Namespace, machineContext.Context).Return(fakeClient, kubevirtMachine.Namespace, nil)

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

	It("should ensure deletion of KubevirtMachine garbage collects everything successfully", func() {
		objects := []client.Object{
			cluster,
			kubevirtCluster,
			machine,
			kubevirtMachine,
			sshKeySecret,
			bootstrapSecret,
			bootstrapUserDataSecret,
		}

		setupClient(machineFactoryMock, objects)

		machineMock.EXPECT().IsTerminal().Return(false, "", nil).Times(1)
		machineMock.EXPECT().Exists().Return(true).Times(1)
		machineMock.EXPECT().IsReady().Return(false).AnyTimes()
		machineMock.EXPECT().Address().Return("1.1.1.1").AnyTimes()
		machineMock.EXPECT().SupportsCheckingIsBootstrapped().Return(false).AnyTimes()
		machineMock.EXPECT().GenerateProviderID().Return("abc", nil).AnyTimes()
		machineFactoryMock.EXPECT().NewMachine(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(machineMock, nil).Times(1)

		infraClusterMock.EXPECT().GenerateInfraClusterClient(kubevirtMachine.Spec.InfraClusterSecretRef, kubevirtMachine.Namespace, machineContext.Context).Return(fakeClient, kubevirtMachine.Namespace, nil).Times(3)

		out, err := kubevirtMachineReconciler.reconcileNormal(machineContext)

		Expect(err).ShouldNot(HaveOccurred())

		// should expect to re-enqueue while waiting for VMI to come online
		Expect(out).To(Equal(ctrl.Result{RequeueAfter: 20 * time.Second}))

		out, err = kubevirtMachineReconciler.reconcileDelete(machineContext)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(out).To(Equal(ctrl.Result{Requeue: false, RequeueAfter: 0}))

		//Check bootstrapData secret is deleted
		machineBootstrapSecretReferenceName := machineContext.Machine.Spec.Bootstrap.DataSecretName
		machineBootstrapSecretReferenceKey := client.ObjectKey{Namespace: machineContext.Machine.GetNamespace(), Name: *machineBootstrapSecretReferenceName + "-userdata"}
		infraClusterClient, _, err := infraClusterMock.GenerateInfraClusterClient(kubevirtMachine.Spec.InfraClusterSecretRef, kubevirtMachine.Namespace, machineContext.Context)
		Expect(err).NotTo(HaveOccurred())
		bootstrapDataSecret := &corev1.Secret{}
		err = infraClusterClient.Get(gocontext.Background(), machineBootstrapSecretReferenceKey, bootstrapDataSecret)
		expectedErrorMessage := "secrets \"" + *machineBootstrapSecretReferenceName + "-userdata" + "\" not found"
		Expect(err.Error()).To(Equal(expectedErrorMessage))

		//Check finalizer is removed from machine
		Expect(len(machineContext.Machine.ObjectMeta.Finalizers)).To(Equal(0))
	})

	It("should ensure deletion of KubevirtMachine when bootstrap secret was never created", func() {

		machine.Spec.Bootstrap.DataSecretName = nil
		objects := []client.Object{
			cluster,
			kubevirtCluster,
			machine,
			kubevirtMachine,
			sshKeySecret,
		}

		setupClient(machineFactoryMock, objects)

		infraClusterMock.EXPECT().GenerateInfraClusterClient(machineContext.KubevirtMachine.Spec.InfraClusterSecretRef, machineContext.KubevirtMachine.Namespace, machineContext.Context).Return(fakeClient, cluster.Namespace, nil).Times(1)

		out, err := kubevirtMachineReconciler.reconcileDelete(machineContext)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(out).To(Equal(ctrl.Result{Requeue: false, RequeueAfter: 0}))

		//Check finalizer is removed from machine
		Expect(len(machineContext.Machine.ObjectMeta.Finalizers)).To(Equal(0))
	})

	It("should update userdata correctly at KubevirtMachine reconcile", func() {
		//Get Machine
		//Get userdata secret name from machine
		//Get userdata secret and assert equality to original secret
		objects := []client.Object{
			cluster,
			kubevirtCluster,
			machine,
			kubevirtMachine,
			sshKeySecret,
			bootstrapSecret,
			bootstrapUserDataSecret,
		}

		setupClient(kubevirt.DefaultMachineFactory{}, objects)

		infraClusterMock.EXPECT().GenerateInfraClusterClient(kubevirtMachine.Spec.InfraClusterSecretRef, kubevirtMachine.Namespace, machineContext.Context).Return(fakeClient, kubevirtMachine.Namespace, nil).Times(2)

		out, err := kubevirtMachineReconciler.reconcileNormal(machineContext)

		Expect(err).ShouldNot(HaveOccurred())

		// should expect to re-enqueue while waiting for VMI to come online
		Expect(out).To(Equal(ctrl.Result{RequeueAfter: 20 * time.Second}))

		// should expect VM to be created with expected name
		vm := &kubevirtv1.VirtualMachine{}
		vmKey := client.ObjectKey{Namespace: kubevirtMachine.Namespace, Name: kubevirtMachine.Name}
		err = fakeClient.Get(gocontext.Background(), vmKey, vm)
		Expect(err).NotTo(HaveOccurred())

		machineBootstrapSecretReferenceName := machineContext.Machine.Spec.Bootstrap.DataSecretName
		machineBootstrapSecretReferenceKey := client.ObjectKey{Namespace: machineContext.Machine.GetNamespace(), Name: *machineBootstrapSecretReferenceName + "-userdata"}
		infraClusterClient, _, err := infraClusterMock.GenerateInfraClusterClient(kubevirtMachine.Spec.InfraClusterSecretRef, kubevirtMachine.Namespace, machineContext.Context)
		Expect(err).NotTo(HaveOccurred())

		bootstrapDataSecret := &corev1.Secret{}
		err = infraClusterClient.Get(gocontext.Background(), machineBootstrapSecretReferenceKey, bootstrapDataSecret)
		Expect(err).NotTo(HaveOccurred())

		Expect(bootstrapUserDataSecret.Data["userdata"]).To(Equal([]byte("shell-script")))
	})

	It("should be able to delete KubeVirt VM even when cluster objects don't exist", func() {
		controllerutil.AddFinalizer(kubevirtMachine, infrav1.MachineFinalizer)
		objects := []client.Object{
			machine,
			kubevirtMachine,
			bootstrapUserDataSecret,
			vm,
		}

		setupClient(machineFactoryMock, objects)

		machineContext = &context.MachineContext{
			Context:         gocontext.Background(),
			Machine:         machine,
			KubevirtMachine: kubevirtMachine,
			Logger:          testLogger,
		}

		infraClusterMock.EXPECT().GenerateInfraClusterClient(kubevirtMachine.Spec.InfraClusterSecretRef, kubevirtMachine.Namespace, machineContext.Context).Return(fakeClient, kubevirtMachine.Namespace, nil)

		out, err := kubevirtMachineReconciler.reconcileDelete(machineContext)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(out).To(Equal(ctrl.Result{}))

		// vm should be deleted
		vmKey := client.ObjectKey{Namespace: kubevirtMachine.Namespace, Name: kubevirtMachine.Name}
		err = fakeClient.Get(gocontext.Background(), vmKey, vm)
		Expect(err).To(HaveOccurred())

		Expect(machineContext.Machine.ObjectMeta.Finalizers).To(HaveLen(0))
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

		setupClient(kubevirt.DefaultMachineFactory{}, objects)

		infraClusterMock.EXPECT().GenerateInfraClusterClient(kubevirtMachine.Spec.InfraClusterSecretRef, kubevirtMachine.Namespace, machineContext.Context).Return(fakeClient, kubevirtMachine.Namespace, nil)

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

		setupClient(kubevirt.DefaultMachineFactory{}, objects)

		infraClusterMock.EXPECT().GenerateInfraClusterClient(kubevirtMachine.Spec.InfraClusterSecretRef, kubevirtMachine.Namespace, machineContext.Context).Return(fakeClient, kubevirtMachine.Namespace, nil)

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

		setupClient(kubevirt.DefaultMachineFactory{}, objects)

		infraClusterMock.EXPECT().GenerateInfraClusterClient(kubevirtMachine.Spec.InfraClusterSecretRef, kubevirtMachine.Namespace, machineContext.Context).Return(fakeClient, kubevirtMachine.Namespace, nil)

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

	It("should detect when VMI is marked for eviction and set FailureReason", func() {
		vmi.Status.Conditions = []kubevirtv1.VirtualMachineInstanceCondition{
			{
				Type:   kubevirtv1.VirtualMachineInstanceReady,
				Status: corev1.ConditionTrue,
			},
			{
				Type:   kubevirtv1.VirtualMachineInstanceIsMigratable,
				Status: corev1.ConditionFalse,
			},
		}
		vmi.Status.NodeName = "somenode"
		vmi.Status.EvacuationNodeName = vmi.Status.NodeName
		vmi.Status.Phase = kubevirtv1.Running

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

		setupClient(kubevirt.DefaultMachineFactory{}, objects)

		infraClusterMock.EXPECT().GenerateInfraClusterClient(kubevirtMachine.Spec.InfraClusterSecretRef, kubevirtMachine.Namespace, machineContext.Context).Return(fakeClient, kubevirtMachine.Namespace, nil)

		Expect(machineContext.KubevirtMachine.Status.Ready).To(BeFalse())

		_, err := kubevirtMachineReconciler.reconcileNormal(machineContext)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(machineContext.KubevirtMachine.Status.FailureReason).ToNot(BeNil())
		Expect(machineContext.KubevirtMachine.Status.FailureMessage).ToNot(BeNil())
		Expect(*machineContext.KubevirtMachine.Status.FailureMessage).To(Equal("The Machine's VM pod is marked for eviction due to infra node drain."))
	})

	It("should detect when VMI is down in an unrecoverable state and set FailureReason", func() {
		vmi.Status.Conditions = []kubevirtv1.VirtualMachineInstanceCondition{
			{
				Type:   kubevirtv1.VirtualMachineInstanceReady,
				Status: corev1.ConditionTrue,
			},
			{
				Type:   kubevirtv1.VirtualMachineInstanceIsMigratable,
				Status: corev1.ConditionFalse,
			},
		}
		vmi.Status.NodeName = "somenode"
		vmi.Status.EvacuationNodeName = vmi.Status.NodeName
		vmi.Status.Phase = kubevirtv1.Failed
		runStrategy := kubevirtv1.RunStrategyOnce
		vm.Spec.RunStrategy = &runStrategy

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

		setupClient(kubevirt.DefaultMachineFactory{}, objects)

		infraClusterMock.EXPECT().GenerateInfraClusterClient(kubevirtMachine.Spec.InfraClusterSecretRef, kubevirtMachine.Namespace, machineContext.Context).Return(fakeClient, kubevirtMachine.Namespace, nil)

		Expect(machineContext.KubevirtMachine.Status.Ready).To(BeFalse())

		_, err := kubevirtMachineReconciler.reconcileNormal(machineContext)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(machineContext.KubevirtMachine.Status.FailureReason).ToNot(BeNil())
		Expect(machineContext.KubevirtMachine.Status.FailureMessage).ToNot(BeNil())
		Expect(*machineContext.KubevirtMachine.Status.FailureMessage).To(Equal("VMI has reached a permanent finalized state"))
	})

	Context("update kubevirt machine conditions correctly", func() {
		It("adds a failed VMProvisionedCondition with reason WaitingForClusterInfrastructureReason when the infrastructure is not ready", func() {
			cluster.Status.InfrastructureReady = false

			objects := []client.Object{
				cluster,
				kubevirtCluster,
				machine,
				kubevirtMachine,
				sshKeySecret,
				bootstrapSecret,
				bootstrapUserDataSecret,
			}

			setupClient(kubevirt.DefaultMachineFactory{}, objects)

			// kubevirtMachineReconciler.Client
			kubevirtMachineKey := types.NamespacedName{Namespace: kubevirtMachine.Namespace, Name: kubevirtMachine.Name}
			_, err := kubevirtMachineReconciler.Reconcile(machineContext, ctrl.Request{NamespacedName: kubevirtMachineKey})

			Expect(err).ShouldNot(HaveOccurred())

			newKubevirtMachine := &infrav1.KubevirtMachine{}
			err = kubevirtMachineReconciler.Client.Get(machineContext, kubevirtMachineKey, newKubevirtMachine)
			Expect(err).ShouldNot(HaveOccurred())

			conditions := newKubevirtMachine.GetConditions()
			Expect(conditions[1].Type).To(Equal(infrav1.VMProvisionedCondition))
			Expect(conditions[1].Reason).To(Equal(infrav1.WaitingForClusterInfrastructureReason))
		})
		Context("reconcileDelete", func() {
			It("adds a failed VMProvisionedCondition with reason DeletingReason when the kubevirtMachine is being deleted", func() {
				objects := []client.Object{
					cluster,
					kubevirtCluster,
					machine,
					kubevirtMachine,
					sshKeySecret,
					bootstrapSecret,
					bootstrapUserDataSecret,
				}

				setupClient(kubevirt.DefaultMachineFactory{}, objects)

				infraClusterMock.EXPECT().GenerateInfraClusterClient(kubevirtMachine.Spec.InfraClusterSecretRef, kubevirtMachine.Namespace, machineContext.Context).Return(fakeClient, kubevirtMachine.Namespace, nil)
				// kubevirtMachineReconciler.Client
				kubevirtMachineKey := types.NamespacedName{Namespace: kubevirtMachine.Namespace, Name: kubevirtMachine.Name}
				_, err := kubevirtMachineReconciler.reconcileDelete(machineContext)

				Expect(err).ShouldNot(HaveOccurred())

				newKubevirtMachine := &infrav1.KubevirtMachine{}
				err = kubevirtMachineReconciler.Client.Get(machineContext, kubevirtMachineKey, newKubevirtMachine)
				Expect(err).ShouldNot(HaveOccurred())

				conditions := newKubevirtMachine.GetConditions()
				Expect(conditions[1].Type).To(Equal(infrav1.VMProvisionedCondition))
				Expect(conditions[1].Reason).To(Equal(clusterv1.DeletingReason))
			})
		})
		Context("reconcileNormal", func() {
			It("adds a failed VMProvisionedCondition with reason WaitingForControlPlaneAvailableReason when the control plane is not yet available", func() {
				machine.Spec.Bootstrap.DataSecretName = nil
				delete(machine.ObjectMeta.Labels, clusterv1.MachineControlPlaneLabelName)
				conditions.MarkFalse(cluster, clusterv1.ControlPlaneInitializedCondition, "nonce", clusterv1.ConditionSeverityInfo, "")

				objects := []client.Object{
					cluster,
					kubevirtCluster,
					machine,
					kubevirtMachine,
					sshKeySecret,
					bootstrapSecret,
					bootstrapUserDataSecret,
				}

				setupClient(kubevirt.DefaultMachineFactory{}, objects)

				_, err := kubevirtMachineReconciler.reconcileNormal(machineContext)

				Expect(err).ShouldNot(HaveOccurred())

				conditions := machineContext.KubevirtMachine.GetConditions()
				Expect(conditions[0].Type).To(Equal(infrav1.VMProvisionedCondition))
				Expect(conditions[0].Reason).To(Equal(clusterv1.WaitingForControlPlaneAvailableReason))
			})
			It("adds a failed VMProvisionedCondition with reason WaitingForBootstrapDataReason when bootstrap data is not yet available", func() {
				machine.Spec.Bootstrap.DataSecretName = nil
				delete(machine.ObjectMeta.Labels, clusterv1.MachineControlPlaneLabelName)
				conditions.MarkTrue(cluster, clusterv1.ControlPlaneInitializedCondition)

				objects := []client.Object{
					cluster,
					kubevirtCluster,
					machine,
					kubevirtMachine,
					sshKeySecret,
					bootstrapSecret,
					bootstrapUserDataSecret,
				}

				setupClient(kubevirt.DefaultMachineFactory{}, objects)

				_, err := kubevirtMachineReconciler.reconcileNormal(machineContext)

				Expect(err).ShouldNot(HaveOccurred())

				conditions := machineContext.KubevirtMachine.GetConditions()
				Expect(conditions[0].Type).To(Equal(infrav1.VMProvisionedCondition))
				Expect(conditions[0].Reason).To(Equal(infrav1.WaitingForBootstrapDataReason))
			})
			It("adds a failed VMProvisionedCondition with reason WaitingForBootstrapDataReason when failng to get bootstrap data secret", func() {
				objects := []client.Object{
					cluster,
					kubevirtCluster,
					machine,
					kubevirtMachine,
					sshKeySecret,
				}

				setupClient(kubevirt.DefaultMachineFactory{}, objects)

				infraClusterMock.EXPECT().GenerateInfraClusterClient(kubevirtMachine.Spec.InfraClusterSecretRef, kubevirtMachine.Namespace, machineContext.Context).Return(fakeClient, kubevirtMachine.Namespace, nil)

				_, err := kubevirtMachineReconciler.reconcileNormal(machineContext)
				Expect(err).ShouldNot(BeNil())

				conditions := machineContext.KubevirtMachine.GetConditions()
				Expect(conditions[0].Type).To(Equal(infrav1.VMProvisionedCondition))
				Expect(conditions[0].Reason).To(Equal(infrav1.WaitingForBootstrapDataReason))
			})
			It("adds a succeeded VMProvisionedCondition", func() {
				vmiReadyCondition := kubevirtv1.VirtualMachineInstanceCondition{
					Type:   kubevirtv1.VirtualMachineInstanceReady,
					Status: corev1.ConditionTrue,
				}
				vmi.Status.Conditions = append(vmi.Status.Conditions, vmiReadyCondition)
				objects := []client.Object{
					cluster,
					kubevirtCluster,
					machine,
					kubevirtMachine,
					bootstrapSecret,
					bootstrapUserDataSecret,
					sshKeySecret,
					vm,
					vmi,
				}

				setupClient(machineFactoryMock, objects)

				machineMock.EXPECT().IsReady().Return(true).Times(2)
				machineMock.EXPECT().IsBootstrapped().Return(true).AnyTimes()
				machineMock.EXPECT().GenerateProviderID().Return("abc", nil).Times(1)
				machineMock.EXPECT().IsTerminal().Return(false, "", nil).Times(1)
				machineMock.EXPECT().Exists().Return(true).Times(1)
				machineMock.EXPECT().Address().Return("1.1.1.1").Times(1)
				machineMock.EXPECT().SupportsCheckingIsBootstrapped().Return(false).Times(1)
				machineFactoryMock.EXPECT().NewMachine(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(machineMock, nil).Times(1)

				infraClusterMock.EXPECT().GenerateInfraClusterClient(kubevirtMachine.Spec.InfraClusterSecretRef, kubevirtMachine.Namespace, machineContext.Context).Return(fakeClient, kubevirtMachine.Namespace, nil)

				_, err := kubevirtMachineReconciler.reconcileNormal(machineContext)
				Expect(err).ShouldNot(HaveOccurred())

				conditions := machineContext.KubevirtMachine.GetConditions()
				Expect(conditions[0].Type).To(Equal(infrav1.VMProvisionedCondition))
				Expect(conditions[0].Status).To(Equal(corev1.ConditionTrue))
			})
			It("adds a failed BootstrapExecSucceededCondition with reason BootstrapFailedReason when bootstraping is possible and failed", func() {
				vmiReadyCondition := kubevirtv1.VirtualMachineInstanceCondition{
					Type:   kubevirtv1.VirtualMachineInstanceReady,
					Status: corev1.ConditionTrue,
				}
				vmi.Status.Conditions = append(vmi.Status.Conditions, vmiReadyCondition)
				vmi.Status.Interfaces = []kubevirtv1.VirtualMachineInstanceNetworkInterface{

					{
						IP: "1.1.1.1",
					},
				}
				sshKeySecret.Data["pub"] = []byte("shell")

				objects := []client.Object{
					cluster,
					kubevirtCluster,
					machine,
					kubevirtMachine,
					bootstrapSecret,
					bootstrapUserDataSecret,
					sshKeySecret,
					vm,
					vmi,
				}

				machineMock.EXPECT().IsTerminal().Return(false, "", nil).Times(1)
				machineMock.EXPECT().Exists().Return(true).Times(1)
				machineMock.EXPECT().Create(nil).Return(nil).AnyTimes()
				machineMock.EXPECT().IsReady().Return(true).Times(1)
				machineMock.EXPECT().Address().Return("1.1.1.1").Times(1)
				machineMock.EXPECT().GenerateProviderID().Return("abc", nil).AnyTimes()
				machineMock.EXPECT().SupportsCheckingIsBootstrapped().Return(true)
				machineMock.EXPECT().IsBootstrapped().Return(false)

				machineFactoryMock.EXPECT().NewMachine(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(machineMock, nil).Times(1)

				setupClient(machineFactoryMock, objects)

				infraClusterMock.EXPECT().GenerateInfraClusterClient(kubevirtMachine.Spec.InfraClusterSecretRef, kubevirtMachine.Namespace, machineContext.Context).Return(fakeClient, kubevirtMachine.Namespace, nil)

				_, err := kubevirtMachineReconciler.reconcileNormal(machineContext)
				Expect(err).ShouldNot(HaveOccurred())

				conditions := machineContext.KubevirtMachine.GetConditions()

				Expect(conditions[0].Type).To(Equal(infrav1.BootstrapExecSucceededCondition))
				Expect(conditions[0].Reason).To(Equal(infrav1.BootstrapFailedReason))
			})

			It("adds a succeeded BootstrapExecSucceededCondition", func() {
				vmiReadyCondition := kubevirtv1.VirtualMachineInstanceCondition{
					Type:   kubevirtv1.VirtualMachineInstanceReady,
					Status: corev1.ConditionTrue,
				}
				vmi.Status.Conditions = append(vmi.Status.Conditions, vmiReadyCondition)
				vmi.Status.Interfaces = []kubevirtv1.VirtualMachineInstanceNetworkInterface{

					{
						IP: "1.1.1.1",
					},
				}
				sshKeySecret.Data["pub"] = []byte("shell")

				objects := []client.Object{
					cluster,
					kubevirtCluster,
					machine,
					kubevirtMachine,
					bootstrapSecret,
					bootstrapUserDataSecret,
					sshKeySecret,
					vm,
					vmi,
				}

				machineMock.EXPECT().IsTerminal().Return(false, "", nil).Times(1)
				machineMock.EXPECT().Exists().Return(true).Times(1)
				machineMock.EXPECT().IsReady().Return(true).Times(2)
				machineMock.EXPECT().Address().Return("1.1.1.1").Times(1)
				machineMock.EXPECT().GenerateProviderID().Return("abc", nil).Times(1)
				machineMock.EXPECT().SupportsCheckingIsBootstrapped().Return(true)
				machineMock.EXPECT().IsBootstrapped().Return(true)

				machineFactoryMock.EXPECT().NewMachine(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(machineMock, nil).Times(1)

				setupClient(machineFactoryMock, objects)

				infraClusterMock.EXPECT().GenerateInfraClusterClient(kubevirtMachine.Spec.InfraClusterSecretRef, kubevirtMachine.Namespace, machineContext.Context).Return(fakeClient, kubevirtMachine.Namespace, nil)

				_, err := kubevirtMachineReconciler.reconcileNormal(machineContext)
				Expect(err).ShouldNot(HaveOccurred())

				conditions := machineContext.KubevirtMachine.GetConditions()

				Expect(conditions[0].Type).To(Equal(infrav1.BootstrapExecSucceededCondition))
				Expect(conditions[0].Status).To(Equal(corev1.ConditionTrue))
			})
		})
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

		setupClient(kubevirt.DefaultMachineFactory{}, objects)

		infraClusterMock.EXPECT().GenerateInfraClusterClient(kubevirtMachine.Spec.InfraClusterSecretRef, kubevirtMachine.Namespace, machineContext.Context).Return(fakeClient, kubevirtMachine.Namespace, nil)

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

		setupClient(kubevirt.DefaultMachineFactory{}, objects)

		infraClusterMock.EXPECT().GenerateInfraClusterClient(kubevirtMachine.Spec.InfraClusterSecretRef, kubevirtMachine.Namespace, machineContext.Context).Return(fakeClient, kubevirtMachine.Namespace, nil)

		Expect(machineContext.KubevirtMachine.Status.Ready).To(BeTrue())
		out, err := kubevirtMachineReconciler.reconcileNormal(machineContext)
		Expect(machineContext.KubevirtMachine.Status.Ready).To(BeFalse())
		Expect(err).ToNot(HaveOccurred())
		Expect(out).To(Equal(ctrl.Result{RequeueAfter: 20 * time.Second}))
	})
})

var _ = Describe("updateNodeProviderID", func() {
	var (
		workloadClusterMock *workloadclustermock.MockWorkloadCluster
		infraClusterMock    *infraclustermock.MockInfraCluster
		testLogger          = ctrl.Log.WithName("test")
		expectedProviderId  = "aa-66@test"
	)

	BeforeEach(func() {
		mockCtrl = gomock.NewController(GinkgoT())
		workloadClusterMock = workloadclustermock.NewMockWorkloadCluster(mockCtrl)
		infraClusterMock = infraclustermock.NewMockInfraCluster(mockCtrl)

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
