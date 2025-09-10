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
	"fmt"
	"time"

	"github.com/golang/mock/gomock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/pkg/errors"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	kubevirtv1 "kubevirt.io/api/core/v1"

	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/kubevirt"

	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/conditions"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
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

	fakeClient                client.Client
	kubevirtMachineReconciler KubevirtMachineReconciler
	fakeWorkloadClusterClient client.Client
)

var _ = Describe("KubevirtClusterToKubevirtMachines", func() {

	var ctx gocontext.Context

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
		fakeClient = fake.NewClientBuilder().WithScheme(testing.SetupScheme()).WithObjects(objects...).Build()
		kubevirtMachineReconciler = KubevirtMachineReconciler{
			Client:         fakeClient,
			MachineFactory: kubevirt.DefaultMachineFactory{},
		}

		ctx = gocontext.Background()
	})

	AfterEach(func() {})

	It("should generate requests for all Kubevirt machines in the cluster", func() {
		out := kubevirtMachineReconciler.KubevirtClusterToKubevirtMachines(ctx, kubevirtCluster)
		Expect(out).To(HaveLen(2))
		machineNames := make([]string, len(out))
		for i := range out {
			machineNames[i] = out[i].Name
		}
		Expect(machineNames).To(ConsistOf("test-machine", "another-test-machine"))
	})

	It("should panic when kubevirt cluster is not specified.", func() {
		if err := recover(); err != nil {
			Expect(kubevirtMachineReconciler.KubevirtClusterToKubevirtMachines(ctx, cluster)).To(Panic())
		}
	})
})

var _ = Describe("utility functions", func() {

	DescribeTable("capk user",
		func(userData []byte, sshAuthorizedKey string, expectedOrNil []byte) {
			actual, modified, err := addCapkUserToCloudInitConfig(userData, []byte(sshAuthorizedKey))
			Expect(err).ShouldNot(HaveOccurred())
			if expectedOrNil == nil {
				Expect(modified).To(BeFalse())
				Expect(string(actual)).To(Equal(string(userData)))
			} else {
				Expect(modified).To(BeTrue())
				Expect(string(actual)).To(Equal(string(expectedOrNil)))
			}
		},
		Entry(
			"should be added to cloud-init config",
			[]byte(`## template: jinja
#cloud-config

write_files:
-   path: /etc/kubernetes/pki/ca.crt
    owner: root:root
    permissions: '0640'

-   path: /run/cluster-api/placeholder
    owner: root:root
    permissions: '0640'
    content: "This placeholder file is used ..."
users:
  - name: johndoe
    group: users
runcmd:
  - 'kubeadm init --config /run/kubeadm/kubeadm.yaml  && echo success > /run/cluster-api/bootstrap-success.complete'
`),
			"sha-rsa 5678",
			[]byte(`## template: jinja
#cloud-config

write_files:
    - path: /etc/kubernetes/pki/ca.crt
      owner: root:root
      permissions: '0640'
    - path: /run/cluster-api/placeholder
      owner: root:root
      permissions: '0640'
      content: "This placeholder file is used ..."
users:
    - name: johndoe
      group: users
    - name: capk
      gecos: CAPK User
      sudo: ALL=(ALL) NOPASSWD:ALL
      groups: users, admin
      ssh_authorized_keys:
        - sha-rsa 5678
runcmd:
    - 'kubeadm init --config /run/kubeadm/kubeadm.yaml  && echo success > /run/cluster-api/bootstrap-success.complete'
`),
		),
		Entry(
			"should be overridden when already in cloud-init config",
			[]byte(`## template: jinja
#cloud-config
users:
  - name: capk
    group: users
runcmd:
  - 'kubeadm init --config /run/kubeadm/kubeadm.yaml  && echo success > /run/cluster-api/bootstrap-success.complete'
`),
			"sha-rsa 5678",
			[]byte(`## template: jinja
#cloud-config
users:
    - name: capk
      gecos: CAPK User
      sudo: ALL=(ALL) NOPASSWD:ALL
      groups: users, admin
      ssh_authorized_keys:
        - sha-rsa 5678
runcmd:
    - 'kubeadm init --config /run/kubeadm/kubeadm.yaml  && echo success > /run/cluster-api/bootstrap-success.complete'
`),
		),
		Entry("should not be added to non cloud-init config", []byte("hello: world"), "sha-rsa 5678", nil),
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
				Name:   sshKeySecretName,
				Labels: map[string]string{"hello": "world"},
			},
			Data: map[string][]byte{
				"pub": []byte("sha-rsa 1234"),
				"key": []byte("sha-rsa 5678"),
			},
		}

		bootstrapSecret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:   bootstrapSecretName,
				Labels: map[string]string{"hello": "world"},
			},
			Data: map[string][]byte{
				"value": []byte("shell-script"),
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

	setupClientWithInterceptors := func(machineFactory kubevirt.MachineFactory, objects []client.Object, interceptorFuncs interceptor.Funcs) {
		machineContext = &context.MachineContext{
			Context:         gocontext.Background(),
			Cluster:         cluster,
			KubevirtCluster: kubevirtCluster,
			Machine:         machine,
			KubevirtMachine: kubevirtMachine,
			Logger:          testLogger,
		}

		fakeClient = fake.NewClientBuilder().WithScheme(testing.SetupScheme()).WithObjects(objects...).WithStatusSubresource(objects...).WithInterceptorFuncs(interceptorFuncs).Build()
		kubevirtMachineReconciler = KubevirtMachineReconciler{
			Client:          fakeClient,
			WorkloadCluster: workloadClusterMock,
			InfraCluster:    infraClusterMock,
			MachineFactory:  machineFactory,
		}
	}

	setupClient := func(machineFactory kubevirt.MachineFactory, objects []client.Object) {

		setupClientWithInterceptors(machineFactory, objects, interceptor.Funcs{})

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
		Expect(fakeClient.Get(gocontext.Background(), vmKey, vm)).To(Succeed())

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
		}

		setupClient(machineFactoryMock, objects)

		machineMock.EXPECT().IsTerminal().Return(false, "", nil).Times(1)
		machineMock.EXPECT().Exists().Return(true).Times(1)
		machineMock.EXPECT().IsReady().Return(false).AnyTimes()
		machineMock.EXPECT().Address().Return("1.1.1.1").AnyTimes()
		machineMock.EXPECT().SupportsCheckingIsBootstrapped().Return(false).AnyTimes()
		machineMock.EXPECT().GenerateProviderID().Return("abc", nil).AnyTimes()
		machineMock.EXPECT().GenerateProviderID().Return("abc", nil).AnyTimes()
		machineMock.EXPECT().DrainNodeIfNeeded(gomock.Any()).Return(time.Duration(0), nil).AnyTimes()
		machineFactoryMock.EXPECT().NewMachine(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(machineMock, nil).Times(1)

		infraClusterMock.EXPECT().GenerateInfraClusterClient(kubevirtMachine.Spec.InfraClusterSecretRef, kubevirtMachine.Namespace, machineContext.Context).Return(fakeClient, kubevirtMachine.Namespace, nil).Times(3)

		out, err := kubevirtMachineReconciler.reconcileNormal(machineContext)

		Expect(err).ShouldNot(HaveOccurred())

		// should expect to re-enqueue while waiting for VMI to come online
		Expect(out).To(Equal(ctrl.Result{RequeueAfter: 20 * time.Second}))

		out, err = kubevirtMachineReconciler.reconcileDelete(machineContext)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(out).To(Equal(ctrl.Result{Requeue: false, RequeueAfter: 0}))


		// Check finalizer is removed from machine
		Expect(machineContext.Machine.ObjectMeta.Finalizers).To(BeEmpty())
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

		// Check finalizer is removed from machine
		Expect(machineContext.Machine.ObjectMeta.Finalizers).To(BeEmpty())
	})


	It("should be able to delete KubeVirt VM even when cluster objects don't exist", func() {
		controllerutil.AddFinalizer(kubevirtMachine, infrav1.MachineFinalizer)
		objects := []client.Object{
			machine,
			kubevirtMachine,
			vm,
		}

		setupClient(machineFactoryMock, objects)

		// Set the VM status so the deletion logic knows about the VM
		kubevirtMachine.Status.VirtualMachine = &kubevirtMachineName

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

		Expect(machineContext.Machine.ObjectMeta.Finalizers).To(BeEmpty())
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
		Expect(fakeClient.Get(gocontext.Background(), vmKey, vm)).To(Succeed())

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
		Expect(fakeClient.Get(gocontext.Background(), vmKey, vm)).To(Succeed())

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
			{
				Type:   kubevirtv1.VirtualMachineInstanceIsMigratable,
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
		Expect(fakeClient.Get(gocontext.Background(), vmKey, vm)).To(Succeed())

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
				}

			setupClient(kubevirt.DefaultMachineFactory{}, objects)

			// kubevirtMachineReconciler.Client
			kubevirtMachineKey := types.NamespacedName{Namespace: kubevirtMachine.Namespace, Name: kubevirtMachine.Name}
			_, err := kubevirtMachineReconciler.Reconcile(machineContext, ctrl.Request{NamespacedName: kubevirtMachineKey})
			Expect(err).ShouldNot(HaveOccurred())

			newKubevirtMachine := &infrav1.KubevirtMachine{}
			err = kubevirtMachineReconciler.Client.Get(machineContext, kubevirtMachineKey, newKubevirtMachine)
			Expect(
				err,
			).To(Succeed())

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
						}

				setupClient(kubevirt.DefaultMachineFactory{}, objects)

				infraClusterMock.EXPECT().GenerateInfraClusterClient(kubevirtMachine.Spec.InfraClusterSecretRef, kubevirtMachine.Namespace, machineContext.Context).Return(fakeClient, kubevirtMachine.Namespace, nil)
				// kubevirtMachineReconciler.Client
				kubevirtMachineKey := types.NamespacedName{Namespace: kubevirtMachine.Namespace, Name: kubevirtMachine.Name}
				_, err := kubevirtMachineReconciler.reconcileDelete(machineContext)

				Expect(err).ShouldNot(HaveOccurred())

				newKubevirtMachine := &infrav1.KubevirtMachine{}
				Expect(kubevirtMachineReconciler.Client.Get(machineContext, kubevirtMachineKey, newKubevirtMachine)).To(Succeed())

				conditions := newKubevirtMachine.GetConditions()
				Expect(conditions).To(HaveLen(2))
				Expect(conditions[1].Type).To(Equal(infrav1.VMProvisionedCondition))
				Expect(conditions[1].Reason).To(Equal(clusterv1.DeletingReason))
			})
		})
		Context("reconcileNormal", func() {
			It("adds a failed VMProvisionedCondition with reason WaitingForControlPlaneAvailableReason when the control plane is not yet available", func() {
				machine.Spec.Bootstrap.DataSecretName = nil
				delete(machine.ObjectMeta.Labels, clusterv1.MachineControlPlaneNameLabel)
				conditions.MarkFalse(cluster, clusterv1.ControlPlaneInitializedCondition, "nonce", clusterv1.ConditionSeverityInfo, "")

				objects := []client.Object{
					cluster,
					kubevirtCluster,
					machine,
					kubevirtMachine,
					sshKeySecret,
					bootstrapSecret,
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
				delete(machine.ObjectMeta.Labels, clusterv1.MachineControlPlaneNameLabel)
				conditions.MarkTrue(cluster, clusterv1.ControlPlaneInitializedCondition)

				objects := []client.Object{
					cluster,
					kubevirtCluster,
					machine,
					kubevirtMachine,
					sshKeySecret,
					bootstrapSecret,
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
				Expect(err).Should(HaveOccurred())

				conditions := machineContext.KubevirtMachine.GetConditions()
				Expect(conditions[0].Type).To(Equal(infrav1.VMProvisionedCondition))
				Expect(conditions[0].Reason).To(Equal(infrav1.WaitingForBootstrapDataReason))
			})

			It("adds a failed VMProvisionedCondition with reason VMCreateFailed when failng to create VM", func() {
				objects := []client.Object{
					cluster,
					kubevirtCluster,
					machine,
					kubevirtMachine,
					sshKeySecret,
					bootstrapSecret,
				}

				injectErr := interceptor.Funcs{
					Create: func(ctx gocontext.Context, client client.WithWatch, obj client.Object, opts ...client.CreateOption) error {

						_, ok := obj.(*kubevirtv1.VirtualMachine)
						if ok {
							return errors.New("vm create error")
						}
						return nil
					},
				}

				setupClientWithInterceptors(kubevirt.DefaultMachineFactory{}, objects, injectErr)

				infraClusterMock.EXPECT().GenerateInfraClusterClient(kubevirtMachine.Spec.InfraClusterSecretRef, kubevirtMachine.Namespace, machineContext.Context).Return(fakeClient, kubevirtMachine.Namespace, nil)

				_, err := kubevirtMachineReconciler.reconcileNormal(machineContext)

				Expect(err).Should(HaveOccurred())

				// should expect condition
				conditions := machineContext.KubevirtMachine.GetConditions()
				Expect(conditions[0].Type).To(Equal(infrav1.VMProvisionedCondition))
				Expect(conditions[0].Status).To(Equal(corev1.ConditionFalse))
				Expect(conditions[0].Reason).To(Equal(infrav1.VMCreateFailedReason))
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
				machineMock.EXPECT().DrainNodeIfNeeded(gomock.Any()).Return(time.Duration(0), nil)
				machineMock.EXPECT().IsLiveMigratable().Return(false, "", "", nil).Times(1)

				machineFactoryMock.EXPECT().NewMachine(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(machineMock, nil).Times(1)

				infraClusterMock.EXPECT().GenerateInfraClusterClient(kubevirtMachine.Spec.InfraClusterSecretRef, kubevirtMachine.Namespace, machineContext.Context).Return(fakeClient, kubevirtMachine.Namespace, nil)

				_, err := kubevirtMachineReconciler.reconcileNormal(machineContext)
				Expect(err).ShouldNot(HaveOccurred())

				conditions := machineContext.KubevirtMachine.GetConditions()
				Expect(conditions[0].Type).To(Equal(infrav1.VMLiveMigratableCondition))
				Expect(conditions[0].Status).To(Equal(corev1.ConditionFalse))
				Expect(conditions[1].Type).To(Equal(infrav1.VMProvisionedCondition))
				Expect(conditions[1].Status).To(Equal(corev1.ConditionTrue))
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
				machineMock.EXPECT().DrainNodeIfNeeded(gomock.Any()).Return(time.Duration(0), nil)

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
				machineMock.EXPECT().DrainNodeIfNeeded(gomock.Any()).Return(time.Duration(0), nil)
				machineMock.EXPECT().IsLiveMigratable().Return(false, "", "", nil).Times(1)

				machineFactoryMock.EXPECT().NewMachine(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(machineMock, nil).Times(1)

				setupClient(machineFactoryMock, objects)

				infraClusterMock.EXPECT().GenerateInfraClusterClient(kubevirtMachine.Spec.InfraClusterSecretRef, kubevirtMachine.Namespace, machineContext.Context).Return(fakeClient, kubevirtMachine.Namespace, nil)

				_, err := kubevirtMachineReconciler.reconcileNormal(machineContext)
				Expect(err).ShouldNot(HaveOccurred())

				conditions := machineContext.KubevirtMachine.GetConditions()

				Expect(conditions[0].Type).To(Equal(infrav1.BootstrapExecSucceededCondition))
				Expect(conditions[0].Status).To(Equal(corev1.ConditionTrue))
			})

			It("adds a succeeded VMLiveMigratableCondition", func() {
				vmiReadyCondition := kubevirtv1.VirtualMachineInstanceCondition{
					Type:   kubevirtv1.VirtualMachineInstanceReady,
					Status: corev1.ConditionTrue,
				}
				vmiLiveMigratableCondition := kubevirtv1.VirtualMachineInstanceCondition{
					Type:   kubevirtv1.VirtualMachineInstanceIsMigratable,
					Status: corev1.ConditionTrue,
				}
				vmi.Status.Conditions = append(vmi.Status.Conditions, vmiReadyCondition)
				vmi.Status.Conditions = append(vmi.Status.Conditions, vmiLiveMigratableCondition)
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
				machineMock.EXPECT().DrainNodeIfNeeded(gomock.Any()).Return(time.Duration(0), nil)
				machineMock.EXPECT().IsLiveMigratable().Return(true, "", "", nil).Times(1)

				machineFactoryMock.EXPECT().NewMachine(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(machineMock, nil).Times(1)

				setupClient(machineFactoryMock, objects)

				infraClusterMock.EXPECT().GenerateInfraClusterClient(kubevirtMachine.Spec.InfraClusterSecretRef, kubevirtMachine.Namespace, machineContext.Context).Return(fakeClient, kubevirtMachine.Namespace, nil)

				_, err := kubevirtMachineReconciler.reconcileNormal(machineContext)
				Expect(err).ShouldNot(HaveOccurred())

				conditions := machineContext.KubevirtMachine.GetConditions()

				Expect(conditions[0].Type).To(Equal(infrav1.BootstrapExecSucceededCondition))
				Expect(conditions[0].Status).To(Equal(corev1.ConditionTrue))
				Expect(conditions[1].Type).To(Equal(infrav1.VMLiveMigratableCondition))
				Expect(conditions[1].Status).To(Equal(corev1.ConditionTrue))
			})

			It("should requeue on node draining", func() {
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
							sshKeySecret,
					vm,
					vmi,
				}

				const requeueDurationSeconds = 3
				machineMock.EXPECT().IsTerminal().Return(false, "", nil).Times(1)
				machineMock.EXPECT().Exists().Return(true).Times(1)
				machineMock.EXPECT().IsReady().Return(true).Times(1)
				machineMock.EXPECT().Address().Return("1.1.1.1").Times(1)
				machineMock.EXPECT().DrainNodeIfNeeded(gomock.Any()).Return(time.Second*requeueDurationSeconds, nil).Times(1)

				machineFactoryMock.EXPECT().NewMachine(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(machineMock, nil).Times(1)

				setupClient(machineFactoryMock, objects)

				infraClusterMock.EXPECT().GenerateInfraClusterClient(kubevirtMachine.Spec.InfraClusterSecretRef, kubevirtMachine.Namespace, machineContext.Context).Return(fakeClient, kubevirtMachine.Namespace, nil)

				res, err := kubevirtMachineReconciler.reconcileNormal(machineContext)
				Expect(err).ShouldNot(HaveOccurred())

				Expect(res.RequeueAfter).To(Equal(time.Second * requeueDurationSeconds))
			})

			It("should requeue on node draining error + requeue duration", func() {
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
							sshKeySecret,
					vm,
					vmi,
				}

				const requeueDurationSeconds = 3
				machineMock.EXPECT().IsTerminal().Return(false, "", nil).Times(1)
				machineMock.EXPECT().Exists().Return(true).Times(1)
				machineMock.EXPECT().IsReady().Return(true).Times(1)
				machineMock.EXPECT().Address().Return("1.1.1.1").Times(1)
				machineMock.EXPECT().DrainNodeIfNeeded(gomock.Any()).Return(time.Second*requeueDurationSeconds, fmt.Errorf("mock error")).Times(1)

				machineFactoryMock.EXPECT().NewMachine(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(machineMock, nil).Times(1)

				setupClient(machineFactoryMock, objects)

				infraClusterMock.EXPECT().GenerateInfraClusterClient(kubevirtMachine.Spec.InfraClusterSecretRef, kubevirtMachine.Namespace, machineContext.Context).Return(fakeClient, kubevirtMachine.Namespace, nil)

				res, err := kubevirtMachineReconciler.reconcileNormal(machineContext)
				Expect(err).Should(HaveOccurred())
				Expect(errors.Unwrap(err).Error()).Should(ContainSubstring("failed to drain node: mock error"))

				Expect(res.RequeueAfter).To(Equal(time.Second * requeueDurationSeconds))
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
		}

		setupClient(kubevirt.DefaultMachineFactory{}, objects)

		infraClusterMock.EXPECT().GenerateInfraClusterClient(kubevirtMachine.Spec.InfraClusterSecretRef, kubevirtMachine.Namespace, machineContext.Context).Return(fakeClient, kubevirtMachine.Namespace, nil)

		Expect(machineContext.KubevirtMachine.Status.Ready).To(BeTrue())
		out, err := kubevirtMachineReconciler.reconcileNormal(machineContext)
		Expect(machineContext.KubevirtMachine.Status.Ready).To(BeFalse())
		Expect(err).ToNot(HaveOccurred())
		Expect(out).To(Equal(ctrl.Result{RequeueAfter: 20 * time.Second}))
	})

	It("should fetch the latest bootstrap secret and update the machine context if changed", func() {
		kubevirtMachine.Status.Ready = true
		bootstrapSecret.Data["value"] = append(bootstrapSecret.Data["value"], []byte(" some change")...)

		objects := []client.Object{
			cluster,
			kubevirtCluster,
			machine,
			kubevirtMachine,
			sshKeySecret,
			bootstrapSecret,
		}

		// test that if the source secret has changed, the VM will be updated

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
		fakeClient = fake.NewClientBuilder().WithScheme(testing.SetupScheme()).WithObjects(objects...).Build()
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
		fakeWorkloadClusterClient = fake.NewClientBuilder().WithScheme(testing.SetupScheme()).WithObjects(workloadClusterObjects...).Build()
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
		Expect(
			fakeWorkloadClusterClient.Get(machineContext, workloadClusterNodeKey, workloadClusterNode),
		).To(Succeed())
		Expect(workloadClusterNode.Spec.ProviderID).To(Equal(expectedProviderId))
		Expect(kubevirtMachine.Status.NodeUpdated).To(BeTrue())
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
		Expect(
			fakeWorkloadClusterClient.Get(machineContext, workloadClusterNodeKey, workloadClusterNode),
		).To(Succeed())
		Expect(workloadClusterNode.Spec.ProviderID).NotTo(Equal(expectedProviderId))
		Expect(kubevirtMachine.Status.NodeUpdated).To(BeFalse())
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
		Expect(
			fakeWorkloadClusterClient.Get(machineContext, workloadClusterNodeKey, workloadClusterNode),
		).To(Succeed())
		Expect(workloadClusterNode.Spec.ProviderID).NotTo(Equal(expectedProviderId))
		Expect(kubevirtMachine.Status.NodeUpdated).To(BeFalse())
	})
})

var _ = Describe("VM Pool functionality", func() {
	var (
		mockCtrl                *gomock.Controller
		workloadClusterMock     *workloadclustermock.MockWorkloadCluster
		infraClusterMock        *infraclustermock.MockInfraCluster
		testLogger              = ctrl.Log.WithName("test")
		kubevirtMachineTemplate *infrav1.KubevirtMachineTemplate
		machineSet              *clusterv1.MachineSet
	)

	BeforeEach(func() {
		mockCtrl = gomock.NewController(GinkgoT())
		workloadClusterMock = workloadclustermock.NewMockWorkloadCluster(mockCtrl)
		infraClusterMock = infraclustermock.NewMockInfraCluster(mockCtrl)

		clusterName = "test-cluster"
		machineName = "test-machine"
		kubevirtMachineName = "test-kubevirt-machine"
		kubevirtMachine = testing.NewKubevirtMachine(kubevirtMachineName, machineName)
		kubevirtCluster = testing.NewKubevirtCluster(clusterName, machineName)
		cluster = testing.NewCluster(clusterName, kubevirtCluster)
		machine = testing.NewMachine(clusterName, machineName, kubevirtMachine)

		// Add MachineSet label to the machine
		if machine.Labels == nil {
			machine.Labels = make(map[string]string)
		}
		machine.Labels[clusterv1.MachineSetNameLabel] = "test-machineset"

		kubevirtMachineTemplate = &infrav1.KubevirtMachineTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-template",
				Namespace: machine.Namespace, // Use same namespace as machine
			},
			Spec: infrav1.KubevirtMachineTemplateSpec{
				VirtualMachinePool: []infrav1.VirtualMachinePoolEntry{
					{
						Name: "pool-vm-01",
						CloudInitNetworkData: func() *string {
							s := "network:\n  version: 2\n  ethernets:\n    eth0:\n      dhcp4: true\n"
							return &s
						}(),
					},
					{
						Name: "pool-vm-02",
						CloudInitNetworkData: func() *string {
							s := "network:\n  version: 2\n  ethernets:\n    eth0:\n      dhcp4: true\n"
							return &s
						}(),
					},
					{
						Name: "pool-vm-03",
						CloudInitNetworkData: func() *string {
							s := "network:\n  version: 2\n  ethernets:\n    eth0:\n      dhcp4: true\n"
							return &s
						}(),
					},
				},
			},
		}

		machineSet = &clusterv1.MachineSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-machineset",
				Namespace: machine.Namespace, // Use same namespace as machine
			},
			Spec: clusterv1.MachineSetSpec{
				Template: clusterv1.MachineTemplateSpec{
					Spec: clusterv1.MachineSpec{
						InfrastructureRef: corev1.ObjectReference{
							APIVersion: "infrastructure.cluster.x-k8s.io/v1alpha1",
							Kind:       "KubevirtMachineTemplate",
							Name:       "test-template",
						},
					},
				},
			},
		}

		objects := []client.Object{
			cluster,
			kubevirtCluster,
			machine,
			kubevirtMachine,
			kubevirtMachineTemplate,
			machineSet,
		}
		fakeClient = fake.NewClientBuilder().WithScheme(testing.SetupScheme()).WithObjects(objects...).Build()
		kubevirtMachineReconciler = KubevirtMachineReconciler{
			Client:          fakeClient,
			WorkloadCluster: workloadClusterMock,
			InfraCluster:    infraClusterMock,
			MachineFactory:  kubevirt.DefaultMachineFactory{},
		}
	})

	Describe("getKubevirtMachineTemplate", func() {
		It("should retrieve template from MachineSet", func() {
			// Verify MachineSet exists first
			existingMachineSet := &clusterv1.MachineSet{}
			machineSetKey := types.NamespacedName{Name: "test-machineset", Namespace: machine.Namespace}
			Expect(fakeClient.Get(gocontext.Background(), machineSetKey, existingMachineSet)).To(Succeed())

			// Add owner reference to make machine owned by MachineSet
			machine.OwnerReferences = []metav1.OwnerReference{
				{
					APIVersion: clusterv1.GroupVersion.String(),
					Kind:       "MachineSet",
					Name:       "test-machineset",
					UID:        "test-uid",
				},
			}

			// Update the machine in the fake client with owner references
			Expect(fakeClient.Update(gocontext.Background(), machine)).To(Succeed())

			template, err := kubevirtMachineReconciler.getKubevirtMachineTemplate(gocontext.Background(), machine)
			Expect(err).ShouldNot(HaveOccurred())
			Expect(template).ToNot(BeNil())
			Expect(template.Name).To(Equal("test-template"))
			Expect(len(template.Spec.VirtualMachinePool)).To(Equal(3))
		})

		It("should return nil for standalone machine", func() {
			// No owner references = standalone machine
			machine.OwnerReferences = []metav1.OwnerReference{}

			template, err := kubevirtMachineReconciler.getKubevirtMachineTemplate(gocontext.Background(), machine)
			Expect(err).ShouldNot(HaveOccurred())
			Expect(template).To(BeNil())
		})

		It("should return error when MachineSet not found", func() {
			machine.OwnerReferences = []metav1.OwnerReference{
				{
					APIVersion: clusterv1.GroupVersion.String(),
					Kind:       "MachineSet",
					Name:       "non-existent-machineset",
					UID:        "test-uid",
				},
			}

			// Update the machine in the fake client with owner references
			Expect(fakeClient.Update(gocontext.Background(), machine)).To(Succeed())

			template, err := kubevirtMachineReconciler.getKubevirtMachineTemplate(gocontext.Background(), machine)
			Expect(err).Should(HaveOccurred())
			Expect(template).To(BeNil())
		})
	})

	Describe("selectVMFromPool", func() {
		var machineContext *context.MachineContext

		BeforeEach(func() {
			machineContext = &context.MachineContext{
				Context:         gocontext.Background(),
				Cluster:         cluster,
				KubevirtCluster: kubevirtCluster,
				Machine:         machine,
				KubevirtMachine: kubevirtMachine,
				Logger:          testLogger,
			}
		})

		It("should return nil for empty pool", func() {
			emptyTemplate := &infrav1.KubevirtMachineTemplate{
				Spec: infrav1.KubevirtMachineTemplateSpec{
					VirtualMachinePool: []infrav1.VirtualMachinePoolEntry{},
				},
			}

			selectedVM, err := kubevirtMachineReconciler.selectVMFromPool(gocontext.Background(), machineContext, emptyTemplate)
			Expect(err).ShouldNot(HaveOccurred())
			Expect(selectedVM).To(BeNil())
		})

		It("should select first available VM", func() {
			selectedVM, err := kubevirtMachineReconciler.selectVMFromPool(gocontext.Background(), machineContext, kubevirtMachineTemplate)
			Expect(err).ShouldNot(HaveOccurred())
			Expect(selectedVM).ToNot(BeNil())
			Expect(selectedVM.Name).To(SatisfyAny(Equal("pool-vm-01"), Equal("pool-vm-02"), Equal("pool-vm-03")))

			// Verify annotation was added
			Expect(kubevirtMachine.Status.VirtualMachine).ToNot(BeNil())
			Expect(*kubevirtMachine.Status.VirtualMachine).To(Equal(selectedVM.Name))
		})

		It("should skip VMs already in use by same MachineSet", func() {
			// Create another KubevirtMachine in the same MachineSet that's using pool-vm-01
			usedMachine := &infrav1.KubevirtMachine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "used-machine",
					Namespace: "default",
					Labels: map[string]string{
						clusterv1.MachineSetNameLabel: "test-machineset",
					},
				},
				Status: infrav1.KubevirtMachineStatus{
					VirtualMachine: ptr.To("pool-vm-01"),
				},
			}

			// Add to fake client
			Expect(fakeClient.Create(gocontext.Background(), usedMachine)).To(Succeed())

			selectedVM, err := kubevirtMachineReconciler.selectVMFromPool(gocontext.Background(), machineContext, kubevirtMachineTemplate)
			Expect(err).ShouldNot(HaveOccurred())
			Expect(selectedVM).ToNot(BeNil())
			Expect(selectedVM.Name).To(Equal("pool-vm-02")) // Should skip pool-vm-01 and select pool-vm-02
		})

		It("should not consider VMs used by different MachineSet", func() {
			// Keep just one pool entry
			kubevirtMachineTemplate.Spec.VirtualMachinePool = []infrav1.VirtualMachinePoolEntry{
				kubevirtMachineTemplate.Spec.VirtualMachinePool[0],
			}
			// Create another KubevirtMachine in a DIFFERENT MachineSet that's using pool-vm-01
			differentMachineSetMachine := &infrav1.KubevirtMachine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "different-machineset-machine",
					Namespace: "default",
					Labels: map[string]string{
						clusterv1.MachineSetNameLabel: "different-machineset", // Different MachineSet
					},
				},
				Status: infrav1.KubevirtMachineStatus{
					VirtualMachine: ptr.To("pool-vm-01"),
				},
			}

			// Add to fake client
			Expect(fakeClient.Create(gocontext.Background(), differentMachineSetMachine)).To(Succeed())

			selectedVM, err := kubevirtMachineReconciler.selectVMFromPool(gocontext.Background(), machineContext, kubevirtMachineTemplate)
			Expect(err).ShouldNot(HaveOccurred())
			Expect(selectedVM).ToNot(BeNil())
			Expect(selectedVM.Name).To(Equal("pool-vm-01")) // Should still select pool-vm-01 because it's used by different MachineSet
		})

		It("should return error when all VMs are in use", func() {
			// Create KubevirtMachines for all VMs in the pool
			for i, vm := range kubevirtMachineTemplate.Spec.VirtualMachinePool {
				usedMachine := &infrav1.KubevirtMachine{
					ObjectMeta: metav1.ObjectMeta{
						Name:      fmt.Sprintf("used-machine-%d", i),
						Namespace: "default",
						Labels: map[string]string{
							clusterv1.MachineSetNameLabel: "test-machineset",
						},
					},
					Status: infrav1.KubevirtMachineStatus{
						VirtualMachine: ptr.To(vm.Name),
					},
				}
				Expect(fakeClient.Create(gocontext.Background(), usedMachine)).To(Succeed())
			}

			selectedVM, err := kubevirtMachineReconciler.selectVMFromPool(gocontext.Background(), machineContext, kubevirtMachineTemplate)
			Expect(err).Should(HaveOccurred())
			Expect(selectedVM).To(BeNil())
			Expect(err.Error()).To(ContainSubstring("no available VMs in the pool"))
		})

		It("should return nil for machine without MachineSet label", func() {
			// Remove MachineSet label
			delete(machine.Labels, clusterv1.MachineSetNameLabel)

			selectedVM, err := kubevirtMachineReconciler.selectVMFromPool(gocontext.Background(), machineContext, kubevirtMachineTemplate)
			Expect(err).ShouldNot(HaveOccurred())
			Expect(selectedVM).To(BeNil())
		})
	})


	Describe("cleanupPoolResources", func() {
		var (
			machineContext     *context.MachineContext
			infraClusterClient client.Client
			vmNamespace        string
		)

		BeforeEach(func() {
			machineContext = &context.MachineContext{
				Context:         gocontext.Background(),
				Cluster:         cluster,
				KubevirtCluster: kubevirtCluster,
				Machine:         machine,
				KubevirtMachine: kubevirtMachine,
				Logger:          testLogger,
			}
			infraClusterClient = fakeClient
			vmNamespace = "default"
		})

		It("should do nothing when no pool annotation exists", func() {
			err := kubevirtMachineReconciler.cleanupPoolResources(gocontext.Background(), machineContext, infraClusterClient, vmNamespace)
			Expect(err).ShouldNot(HaveOccurred())
		})

		It("should remove pool VM assignment", func() {
			vmName := "pool-vm-01"

			// Add pool VM assignment
			kubevirtMachine.Status.VirtualMachine = &vmName

			err := kubevirtMachineReconciler.cleanupPoolResources(gocontext.Background(), machineContext, infraClusterClient, vmNamespace)
			Expect(err).ShouldNot(HaveOccurred())

			// Verify status vm name was removed
			Expect(kubevirtMachine.Status.VirtualMachine).To(BeNil())
		})

		It("should handle cleanup with no pool VM assignment", func() {
			// No pool VM assignment
			kubevirtMachine.Status.VirtualMachine = nil

			err := kubevirtMachineReconciler.cleanupPoolResources(gocontext.Background(), machineContext, infraClusterClient, vmNamespace)
			Expect(err).ShouldNot(HaveOccurred())

			// Verify no errors occurred
			Expect(kubevirtMachine.Status.VirtualMachine).To(BeNil())
		})
	})
})

var _ = Describe("VM Pool integration tests", func() {
	var (
		infraClusterMock *infraclustermock.MockInfraCluster
		machineContext   *context.MachineContext
		testLogger       = ctrl.Log.WithName("test")
	)

	BeforeEach(func() {
		mockCtrl = gomock.NewController(GinkgoT())
		infraClusterMock = infraclustermock.NewMockInfraCluster(mockCtrl)

		clusterName = "test-cluster"
		machineName = "test-machine"
		kubevirtMachineName = "test-kubevirt-machine"
		kubevirtMachine = testing.NewKubevirtMachine(kubevirtMachineName, machineName)
		kubevirtCluster = testing.NewKubevirtCluster(clusterName, machineName)
		cluster = testing.NewCluster(clusterName, kubevirtCluster)
		machine = testing.NewMachine(clusterName, machineName, kubevirtMachine)

		// Add MachineSet label to the machine
		if machine.Labels == nil {
			machine.Labels = make(map[string]string)
		}
		machine.Labels[clusterv1.MachineSetNameLabel] = "worker-machineset"

		machineContext = &context.MachineContext{
			Context:         gocontext.Background(),
			Cluster:         cluster,
			KubevirtCluster: kubevirtCluster,
			Machine:         machine,
			KubevirtMachine: kubevirtMachine,
			Logger:          testLogger,
		}

		// Create template with VM pool
		kubevirtMachineTemplate := &infrav1.KubevirtMachineTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "worker-template",
				Namespace: "default",
			},
			Spec: infrav1.KubevirtMachineTemplateSpec{
				VirtualMachinePool: []infrav1.VirtualMachinePoolEntry{
					{
						Name: "worker-vm-01",
						CloudInitNetworkData: func() *string {
							s := "network:\n  version: 2\n  ethernets:\n    eth0:\n      dhcp4: true\n"
							return &s
						}(),
					},
					{
						Name: "worker-vm-02",
						CloudInitNetworkData: func() *string {
							s := "network:\n  version: 2\n  ethernets:\n    eth0:\n      dhcp4: true\n"
							return &s
						}(),
					},
				},
			},
		}

		// Create MachineSet that references the template
		machineSet := &clusterv1.MachineSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "worker-machineset",
				Namespace: "default",
			},
			Spec: clusterv1.MachineSetSpec{
				Template: clusterv1.MachineTemplateSpec{
					Spec: clusterv1.MachineSpec{
						InfrastructureRef: corev1.ObjectReference{
							APIVersion: "infrastructure.cluster.x-k8s.io/v1alpha1",
							Kind:       "KubevirtMachineTemplate",
							Name:       "worker-template",
						},
					},
				},
			},
		}

		// Add owner reference to make machine owned by MachineSet
		machine.OwnerReferences = []metav1.OwnerReference{
			{
				APIVersion: clusterv1.GroupVersion.String(),
				Kind:       "MachineSet",
				Name:       "worker-machineset",
				UID:        "test-uid",
			},
		}

		objects := []client.Object{
			cluster,
			kubevirtCluster,
			machine,
			kubevirtMachine,
			kubevirtMachineTemplate,
			machineSet,
		}
		fakeClient = fake.NewClientBuilder().WithScheme(testing.SetupScheme()).WithObjects(objects...).Build()
		kubevirtMachineReconciler = KubevirtMachineReconciler{
			Client:         fakeClient,
			InfraCluster:   infraClusterMock,
			MachineFactory: kubevirt.DefaultMachineFactory{},
		}
	})

	It("should successfully select VM from pool", func() {
		// This test verifies the VM pool selection logic

		// Get the template that was created in BeforeEach
		template := &infrav1.KubevirtMachineTemplate{}
		templateKey := types.NamespacedName{Name: "worker-template", Namespace: "default"}
		Expect(fakeClient.Get(gocontext.Background(), templateKey, template)).To(Succeed())

		// Test VM selection
		selectedVM, err := kubevirtMachineReconciler.selectVMFromPool(gocontext.Background(), machineContext, template)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(selectedVM).ToNot(BeNil())
		Expect(selectedVM.Name).To(SatisfyAny(Equal("worker-vm-01"), Equal("worker-vm-02"))) // First available VM

		// Verify status vm name was added
		Expect(kubevirtMachine.Status.VirtualMachine).ToNot(BeNil())
		Expect(*kubevirtMachine.Status.VirtualMachine).To(Equal(selectedVM.Name))

	})

	It("should handle VM pool exhaustion during selection", func() {
		// Create KubevirtMachines that use all VMs in the pool
		for i := range 2 {
			usedMachine := &infrav1.KubevirtMachine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("used-machine-%d", i),
					Namespace: "default",
					Labels: map[string]string{
						clusterv1.MachineSetNameLabel: "worker-machineset",
					},
				},
				Status: infrav1.KubevirtMachineStatus{
					VirtualMachine: ptr.To(fmt.Sprintf("worker-vm-0%d", i+1)),
				},
			}
			Expect(fakeClient.Create(gocontext.Background(), usedMachine)).To(Succeed())
		}

		// Get the template that was created in BeforeEach
		template := &infrav1.KubevirtMachineTemplate{}
		templateKey := types.NamespacedName{Name: "worker-template", Namespace: "default"}
		Expect(fakeClient.Get(gocontext.Background(), templateKey, template)).To(Succeed())

		// Attempt VM selection - should fail due to pool exhaustion
		selectedVM, err := kubevirtMachineReconciler.selectVMFromPool(gocontext.Background(), machineContext, template)

		// Should fail with pool exhaustion error
		Expect(err).Should(HaveOccurred())
		Expect(selectedVM).To(BeNil())
		Expect(err.Error()).To(ContainSubstring("no available VMs in the pool"))
	})
})
