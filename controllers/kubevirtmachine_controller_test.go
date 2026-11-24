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

	"github.com/go-logr/logr"
	"github.com/golang/mock/gomock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	kubevirtv1 "kubevirt.io/api/core/v1"

	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/kubevirt"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	capierrors "sigs.k8s.io/cluster-api/errors" //nolint:staticcheck // deprecated but no replacement yet
	"sigs.k8s.io/cluster-api/util/conditions"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	machinemocks "sigs.k8s.io/cluster-api-provider-kubevirt/pkg/kubevirt/mock"

	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha1"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/annotationfilter"
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

var _ = Describe("KubevirtMachine event mapping", func() {

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
			kubevirtMachine,
			anotherMachine,
			anotherKubevirtMachine,
			// add one more machine without corresponding kubevirt machine, to test that no request is created for it
			testing.NewMachine(clusterName, "machine-without-corresponding-kubevirt-machine", nil),
		}
		fakeClient = fake.NewClientBuilder().WithScheme(testing.SetupScheme()).WithObjects(objects...).
			WithIndex(&infrav1.KubevirtMachine{}, networkDataSecretRefIndex, networkDataSecretRefIndexFunc).
			Build()
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

	It("should enqueue machines referencing a changed network data secret", func() {
		kubevirtMachine.Spec.CloudInit = &infrav1.CloudInitSpec{
			DataSource:           infrav1.CloudInitDataSourceNoCloud,
			NetworkDataSecretRef: &corev1.LocalObjectReference{Name: "network-data"},
		}
		Expect(fakeClient.Update(ctx, kubevirtMachine)).To(Succeed())

		// A machine with inline network data references no secret, whatever the secret is called
		inlineNetworkData := "version: 2"
		anotherKubevirtMachine.Spec.CloudInit = &infrav1.CloudInitSpec{
			DataSource:  infrav1.CloudInitDataSourceNoCloud,
			NetworkData: &inlineNetworkData,
		}
		Expect(fakeClient.Update(ctx, anotherKubevirtMachine)).To(Succeed())

		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: kubevirtMachine.Namespace, Name: "network-data"}}
		requests := kubevirtMachineReconciler.SecretToKubevirtMachines(ctx, secret)
		Expect(requests).To(ConsistOf(ctrl.Request{NamespacedName: client.ObjectKeyFromObject(kubevirtMachine)}))

		unrelated := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: kubevirtMachine.Namespace, Name: "unrelated"}}
		Expect(kubevirtMachineReconciler.SecretToKubevirtMachines(ctx, unrelated)).To(BeEmpty())
	})

	DescribeTable("should let only the secrets that can hold network data through to the machine lookup",
		func(data map[string][]byte, expected bool) {
			secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "some-secret"}, Data: data}
			Expect(hasNetworkDataKey(secret)).To(Equal(expected))
		},
		Entry("the networkdata key", map[string][]byte{"networkdata": []byte("version: 2")}, true),
		Entry("the networkData key", map[string][]byte{"networkData": []byte("version: 2")}, true),
		Entry("a network data key with an empty value, which a machine may be waiting on", map[string][]byte{"networkdata": {}}, true),
		Entry("a bootstrap data secret", map[string][]byte{"value": []byte("shell-script")}, false),
		Entry("a secret without data", nil, false),
	)

	It("should not take an object that is not a secret for a network data secret", func() {
		Expect(hasNetworkDataKey(kubevirtMachine)).To(BeFalse())
	})

	It("should panic when kubevirt cluster is not specified.", func() {
		if err := recover(); err != nil {
			Expect(kubevirtMachineReconciler.KubevirtClusterToKubevirtMachines(ctx, cluster)).To(Panic())
		}
	})
})

var _ = Describe("KubevirtMachine reconciliation with excluded annotations", func() {
	It("should not delete a VM or remove a finalizer", func(ctx gocontext.Context) {
		kubevirtMachine := testing.NewKubevirtMachine("excluded-machine", "excluded-owner-machine")
		kubevirtMachine.Annotations = map[string]string{
			"example.com/exclude": "true",
		}
		controllerutil.AddFinalizer(kubevirtMachine, infrav1.MachineFinalizer)

		virtualMachine := &kubevirtv1.VirtualMachine{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: kubevirtMachine.Namespace,
				Name:      kubevirtMachine.Name,
			},
		}
		fakeClient := fake.NewClientBuilder().
			WithScheme(testing.SetupScheme()).
			WithObjects(kubevirtMachine, virtualMachine).
			Build()
		reconciler := KubevirtMachineReconciler{
			Client:              fakeClient,
			ExcludedAnnotations: annotationfilter.New([]string{"example.com/exclude"}),
		}

		result, err := reconciler.Reconcile(ctx, ctrl.Request{
			NamespacedName: types.NamespacedName{
				Namespace: kubevirtMachine.Namespace,
				Name:      kubevirtMachine.Name,
			},
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(ctrl.Result{}))
		Expect(fakeClient.Get(ctx, client.ObjectKeyFromObject(virtualMachine), virtualMachine)).To(Succeed())

		updatedMachine := &infrav1.KubevirtMachine{}
		Expect(fakeClient.Get(ctx, client.ObjectKeyFromObject(kubevirtMachine), updatedMachine)).To(Succeed())
		Expect(updatedMachine.Finalizers).To(ContainElement(infrav1.MachineFinalizer))
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
		fakeRecorder       *events.FakeRecorder
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

	setupClientWithInterceptors := func(machineFactory kubevirt.MachineFactory, objects []client.Object, interceptorFuncs interceptor.Funcs) {
		machineContext = &context.MachineContext{
			Context:         logr.NewContext(gocontext.Background(), GinkgoLogr),
			Cluster:         cluster,
			KubevirtCluster: kubevirtCluster,
			Machine:         machine,
			KubevirtMachine: kubevirtMachine,
			Logger:          testLogger,
		}

		fakeClient = fake.NewClientBuilder().WithScheme(testing.SetupScheme()).WithObjects(objects...).WithStatusSubresource(objects...).WithInterceptorFuncs(interceptorFuncs).Build()
		fakeRecorder = events.NewFakeRecorder(16)
		kubevirtMachineReconciler = KubevirtMachineReconciler{
			Client:          fakeClient,
			WorkloadCluster: workloadClusterMock,
			InfraCluster:    infraClusterMock,
			MachineFactory:  machineFactory,
			Recorder:        fakeRecorder,
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

		// Should have created the userdata secret
		machineBootstrapSecretReferenceName := machineContext.Machine.Spec.Bootstrap.DataSecretName
		machineBootstrapSecretReferenceKey := client.ObjectKey{Namespace: machineContext.Machine.GetNamespace(), Name: *machineBootstrapSecretReferenceName + "-userdata"}
		bootstrapDataSecret := &corev1.Secret{}
		Expect(
			fakeClient.Get(gocontext.Background(), machineBootstrapSecretReferenceKey, bootstrapDataSecret),
		).To(Succeed())
		Expect(bootstrapDataSecret.Data).To(HaveKeyWithValue("userdata", []byte("shell-script")))
		Expect(bootstrapDataSecret.Labels).To(HaveLen(1))
		Expect(bootstrapDataSecret.Labels).To(HaveKeyWithValue("hello", "world"))

		// Without a networkDataSecretRef, the config drive must not reference network data
		Expect(bootstrapDataSecret.Data).ShouldNot(HaveKey("networkdata"))
		Expect(cloudInitConfigDrive(vm)).ShouldNot(BeNil())
		Expect(cloudInitConfigDrive(vm).NetworkDataSecretRef).To(BeNil())
	})

	Context("cloud-init network data", func() {
		const (
			configDriveNetworkData = `{"links":[{"id":"eth0","type":"phy","ethernet_mac_address":"52:54:00:00:00:01"}],"networks":[{"id":"net0","type":"ipv4","link":"eth0","ip_address":"192.0.2.10","netmask":"255.255.255.0"}]}`
			noCloudNetworkData     = "version: 2\nethernets:\n  eth0:\n    addresses: [192.0.2.10/24]\n"
			networkDataSecretName  = "network-data-secret"
		)

		var networkDataSecret *corev1.Secret

		// useNetworkDataSecret makes the machine reference a network data secret with the given content.
		useNetworkDataSecret := func(dataSource infrav1.CloudInitDataSource, data map[string][]byte) {
			networkDataSecret = &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: networkDataSecretName, Namespace: kubevirtMachine.Namespace},
				Data:       data,
			}
			kubevirtMachine.Spec.CloudInit = &infrav1.CloudInitSpec{
				DataSource:           dataSource,
				NetworkDataSecretRef: &corev1.LocalObjectReference{Name: networkDataSecretName},
			}
		}

		// setup builds the client and the reconciler, with the network data secret if there is one,
		// and lets reconcileNormal reach the infra cluster as many times as asked.
		setup := func(reconciliations int) {
			objects := []client.Object{cluster, kubevirtCluster, machine, kubevirtMachine, sshKeySecret, bootstrapSecret}
			if networkDataSecret != nil {
				objects = append(objects, networkDataSecret)
			}
			setupClient(kubevirt.DefaultMachineFactory{}, objects)
			infraClusterMock.EXPECT().GenerateInfraClusterClient(kubevirtMachine.Spec.InfraClusterSecretRef, kubevirtMachine.Namespace, machineContext.Context).Return(fakeClient, kubevirtMachine.Namespace, nil).Times(reconciliations)
		}

		userDataSecretName := func() string {
			return *machineContext.Machine.Spec.Bootstrap.DataSecretName + "-userdata"
		}

		machineNetworkDataSecretKey := func(kvMachine *infrav1.KubevirtMachine) client.ObjectKey {
			return client.ObjectKey{Namespace: kvMachine.Namespace, Name: kvMachine.Name + "-networkdata"}
		}

		getVM := func() *kubevirtv1.VirtualMachine {
			GinkgoHelper()
			createdVM := &kubevirtv1.VirtualMachine{}
			Expect(fakeClient.Get(gocontext.Background(), client.ObjectKeyFromObject(kubevirtMachine), createdVM)).To(Succeed())
			return createdVM
		}

		expectNoVM := func() {
			GinkgoHelper()
			err := fakeClient.Get(gocontext.Background(), client.ObjectKeyFromObject(kubevirtMachine), &kubevirtv1.VirtualMachine{})
			Expect(apierrors.IsNotFound(err)).To(BeTrue(), "no VM must be created without usable network data")
		}

		vmProvisionedCondition := func() *metav1.Condition {
			return conditions.Get(machineContext.KubevirtMachine, infrav1.VMProvisionedCondition)
		}

		BeforeEach(func() {
			networkDataSecret = nil
		})

		It("should copy the network data of a referenced secret into a secret of the machine's own", func() {
			useNetworkDataSecret(infrav1.CloudInitDataSourceConfigDrive, map[string][]byte{"networkdata": []byte(configDriveNetworkData)})
			setup(1)

			_, err := kubevirtMachineReconciler.reconcileNormal(machineContext)
			Expect(err).ShouldNot(HaveOccurred())

			// The user data secret, which machines can share, carries no network data
			userDataSecret := &corev1.Secret{}
			Expect(fakeClient.Get(gocontext.Background(), client.ObjectKey{Namespace: kubevirtMachine.Namespace, Name: userDataSecretName()}, userDataSecret)).To(Succeed())
			Expect(userDataSecret.Data).To(HaveKeyWithValue("userdata", []byte("shell-script")))
			Expect(userDataSecret.Data).ShouldNot(HaveKey("networkdata"))

			// The network data is in a secret named after the machine, that cannot be changed
			machineNetworkDataSecret := &corev1.Secret{}
			Expect(fakeClient.Get(gocontext.Background(), machineNetworkDataSecretKey(kubevirtMachine), machineNetworkDataSecret)).To(Succeed())
			Expect(machineNetworkDataSecret.Data).To(Equal(map[string][]byte{"networkdata": []byte(configDriveNetworkData)}))
			Expect(machineNetworkDataSecret.Immutable).To(HaveValue(BeTrue()))
			Expect(machineNetworkDataSecret.Type).To(Equal(clusterv1.ClusterSecretType))
			Expect(machineNetworkDataSecret.Labels).To(HaveKeyWithValue(clusterv1.ClusterNameLabel, clusterName))

			// The config drive references each secret for what it holds
			configDrive := cloudInitConfigDrive(getVM())
			Expect(configDrive).ShouldNot(BeNil())
			Expect(configDrive.UserDataSecretRef).To(HaveValue(Equal(corev1.LocalObjectReference{Name: userDataSecretName()})))
			Expect(configDrive.NetworkDataSecretRef).To(HaveValue(Equal(corev1.LocalObjectReference{Name: machineNetworkDataSecretKey(kubevirtMachine).Name})))
		})

		It("should attach a NoCloud data source with network data when the noCloud data source is selected", func() {
			// The camel case spelling of the key is accepted as well
			useNetworkDataSecret(infrav1.CloudInitDataSourceNoCloud, map[string][]byte{"networkData": []byte(noCloudNetworkData)})
			setup(1)

			_, err := kubevirtMachineReconciler.reconcileNormal(machineContext)
			Expect(err).ShouldNot(HaveOccurred())

			machineNetworkDataSecret := &corev1.Secret{}
			Expect(fakeClient.Get(gocontext.Background(), machineNetworkDataSecretKey(kubevirtMachine), machineNetworkDataSecret)).To(Succeed())
			Expect(machineNetworkDataSecret.Data).To(Equal(map[string][]byte{"networkdata": []byte(noCloudNetworkData)}))

			// A NoCloud volume must be attached instead of a config drive volume
			createdVM := getVM()
			Expect(cloudInitConfigDrive(createdVM)).Should(BeNil())
			noCloud := cloudInitNoCloud(createdVM)
			Expect(noCloud).ShouldNot(BeNil())
			Expect(noCloud.UserDataSecretRef).To(HaveValue(Equal(corev1.LocalObjectReference{Name: userDataSecretName()})))
			Expect(noCloud.NetworkDataSecretRef).To(HaveValue(Equal(corev1.LocalObjectReference{Name: machineNetworkDataSecretKey(kubevirtMachine).Name})))
		})

		It("should attach inline cloud-init network data to the KubeVirt VM", func() {
			networkData := noCloudNetworkData
			kubevirtMachine.Spec.CloudInit = &infrav1.CloudInitSpec{DataSource: infrav1.CloudInitDataSourceNoCloud, NetworkData: &networkData}
			setup(1)

			_, err := kubevirtMachineReconciler.reconcileNormal(machineContext)
			Expect(err).ShouldNot(HaveOccurred())

			machineNetworkDataSecret := &corev1.Secret{}
			Expect(fakeClient.Get(gocontext.Background(), machineNetworkDataSecretKey(kubevirtMachine), machineNetworkDataSecret)).To(Succeed())
			Expect(machineNetworkDataSecret.Data).To(Equal(map[string][]byte{"networkdata": []byte(noCloudNetworkData)}))

			noCloud := cloudInitNoCloud(getVM())
			Expect(noCloud).ShouldNot(BeNil())
			Expect(noCloud.NetworkDataSecretRef).To(HaveValue(Equal(corev1.LocalObjectReference{Name: machineNetworkDataSecretKey(kubevirtMachine).Name})))
		})

		It("should keep the network data of machines that share a bootstrap data secret apart", func() {
			firstNetworkData := "version: 2\nethernets:\n  eth0:\n    addresses: [192.0.2.10/24]\n"
			secondNetworkData := "version: 2\nethernets:\n  eth0:\n    addresses: [192.0.2.11/24]\n"

			kubevirtMachine.Spec.CloudInit = &infrav1.CloudInitSpec{DataSource: infrav1.CloudInitDataSourceNoCloud, NetworkData: &firstNetworkData}

			// A second machine on the same bootstrap data secret, with network data of its own
			secondKubevirtMachine := testing.NewKubevirtMachine("second-kubevirt-machine", "second-machine")
			secondKubevirtMachine.Spec.CloudInit = &infrav1.CloudInitSpec{DataSource: infrav1.CloudInitDataSourceNoCloud, NetworkData: &secondNetworkData}
			secondMachine := testing.NewMachine(clusterName, "second-machine", secondKubevirtMachine)
			secondMachine.Spec.Bootstrap.DataSecretName = &bootstrapSecretName

			// And a third one on it, with none
			thirdKubevirtMachine := testing.NewKubevirtMachine("third-kubevirt-machine", "third-machine")
			thirdMachine := testing.NewMachine(clusterName, "third-machine", thirdKubevirtMachine)
			thirdMachine.Spec.Bootstrap.DataSecretName = &bootstrapSecretName

			setupClient(kubevirt.DefaultMachineFactory{}, []client.Object{
				cluster, kubevirtCluster, sshKeySecret, bootstrapSecret,
				machine, kubevirtMachine, secondMachine, secondKubevirtMachine, thirdMachine, thirdKubevirtMachine,
			})

			contexts := []*context.MachineContext{machineContext}
			for _, other := range []struct {
				machine         *clusterv1.Machine
				kubevirtMachine *infrav1.KubevirtMachine
			}{{secondMachine, secondKubevirtMachine}, {thirdMachine, thirdKubevirtMachine}} {
				contexts = append(contexts, &context.MachineContext{
					Context:         machineContext.Context,
					Cluster:         cluster,
					KubevirtCluster: kubevirtCluster,
					Machine:         other.machine,
					KubevirtMachine: other.kubevirtMachine,
					Logger:          testLogger,
				})
			}

			// Reconciled twice over, in turn, as the controller would on each pass
			for range 2 {
				for _, machineCtx := range contexts {
					Expect(kubevirtMachineReconciler.reconcileKubevirtBootstrapSecret(machineCtx, fakeClient, kubevirtMachine.Namespace, nil)).To(Succeed())
					Expect(kubevirtMachineReconciler.reconcileKubevirtNetworkDataSecret(machineCtx, fakeClient, kubevirtMachine.Namespace)).To(Succeed())
				}
			}

			firstSecret := &corev1.Secret{}
			Expect(fakeClient.Get(gocontext.Background(), machineNetworkDataSecretKey(kubevirtMachine), firstSecret)).To(Succeed())
			Expect(firstSecret.Data).To(HaveKeyWithValue("networkdata", []byte(firstNetworkData)))

			secondSecret := &corev1.Secret{}
			Expect(fakeClient.Get(gocontext.Background(), machineNetworkDataSecretKey(secondKubevirtMachine), secondSecret)).To(Succeed())
			Expect(secondSecret.Data).To(HaveKeyWithValue("networkdata", []byte(secondNetworkData)))

			err := fakeClient.Get(gocontext.Background(), machineNetworkDataSecretKey(thirdKubevirtMachine), &corev1.Secret{})
			Expect(apierrors.IsNotFound(err)).To(BeTrue(), "a machine without network data must not get a network data secret")

			// The shared user data secret is left to the user data
			userDataSecret := &corev1.Secret{}
			Expect(fakeClient.Get(gocontext.Background(), client.ObjectKey{Namespace: kubevirtMachine.Namespace, Name: bootstrapSecretName + "-userdata"}, userDataSecret)).To(Succeed())
			Expect(userDataSecret.Data).To(Equal(map[string][]byte{"userdata": []byte("shell-script")}))
		})

		It("should keep the network data a machine was prepared with when the referenced secret changes or goes away", func() {
			useNetworkDataSecret(infrav1.CloudInitDataSourceNoCloud, map[string][]byte{"networkdata": []byte(noCloudNetworkData)})
			setup(0)

			Expect(kubevirtMachineReconciler.reconcileKubevirtNetworkDataSecret(machineContext, fakeClient, kubevirtMachine.Namespace)).To(Succeed())

			// The source changes: the machine keeps what it was prepared with
			source := &corev1.Secret{}
			Expect(fakeClient.Get(gocontext.Background(), client.ObjectKeyFromObject(networkDataSecret), source)).To(Succeed())
			source.Data["networkdata"] = []byte("version: 2\nethernets:\n  eth0:\n    addresses: [198.51.100.10/24]\n")
			Expect(fakeClient.Update(gocontext.Background(), source)).To(Succeed())
			Expect(kubevirtMachineReconciler.reconcileKubevirtNetworkDataSecret(machineContext, fakeClient, kubevirtMachine.Namespace)).To(Succeed())

			machineNetworkDataSecret := &corev1.Secret{}
			Expect(fakeClient.Get(gocontext.Background(), machineNetworkDataSecretKey(kubevirtMachine), machineNetworkDataSecret)).To(Succeed())
			Expect(machineNetworkDataSecret.Data).To(HaveKeyWithValue("networkdata", []byte(noCloudNetworkData)))

			// The source goes away, as after a "clusterctl move" that left it behind: the machine
			// that was already prepared is not disturbed
			Expect(fakeClient.Delete(gocontext.Background(), source)).To(Succeed())
			Expect(kubevirtMachineReconciler.reconcileKubevirtNetworkDataSecret(machineContext, fakeClient, kubevirtMachine.Namespace)).To(Succeed())
			Expect(fakeClient.Get(gocontext.Background(), machineNetworkDataSecretKey(kubevirtMachine), machineNetworkDataSecret)).To(Succeed())
			Expect(machineNetworkDataSecret.Data).To(HaveKeyWithValue("networkdata", []byte(noCloudNetworkData)))
		})

		It("should label a referenced network data secret for clusterctl move, and keep its other labels", func() {
			useNetworkDataSecret(infrav1.CloudInitDataSourceNoCloud, map[string][]byte{"networkdata": []byte(noCloudNetworkData)})
			networkDataSecret.Labels = map[string]string{"hello": "world"}
			setup(0)

			Expect(kubevirtMachineReconciler.reconcileKubevirtNetworkDataSecret(machineContext, fakeClient, kubevirtMachine.Namespace)).To(Succeed())

			source := &corev1.Secret{}
			Expect(fakeClient.Get(gocontext.Background(), client.ObjectKeyFromObject(networkDataSecret), source)).To(Succeed())
			Expect(source.Labels).To(Equal(map[string]string{"hello": "world", "clusterctl.cluster.x-k8s.io/move": ""}))
			Expect(source.Data).To(HaveKeyWithValue("networkdata", []byte(noCloudNetworkData)))
		})

		It("should wait, and report an error, while the referenced network data secret does not exist", func() {
			kubevirtMachine.Spec.CloudInit = &infrav1.CloudInitSpec{
				DataSource:           infrav1.CloudInitDataSourceNoCloud,
				NetworkDataSecretRef: &corev1.LocalObjectReference{Name: networkDataSecretName},
			}
			setup(1)

			out, err := kubevirtMachineReconciler.reconcileNormal(machineContext)
			Expect(err).Should(HaveOccurred())
			Expect(err.Error()).To(HavePrefix("failed to prepare cloud-init network data"))
			Expect(out).To(Equal(ctrl.Result{RequeueAfter: 10 * time.Second}))

			condition := vmProvisionedCondition()
			Expect(condition).ShouldNot(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			Expect(condition.Reason).To(Equal(infrav1.WaitingForNetworkDataReason))
			Expect(condition.Message).To(ContainSubstring(networkDataSecretName))

			// Waiting for a secret is not worth an event
			Expect(fakeRecorder.Events).To(BeEmpty())
			expectNoVM()
		})

		DescribeTable("should stop, without an error, on network data that cannot be used",
			func(dataSource infrav1.CloudInitDataSource, data map[string][]byte, expectedMessage string) {
				useNetworkDataSecret(dataSource, data)
				setup(1)

				out, err := kubevirtMachineReconciler.reconcileNormal(machineContext)

				// Nothing a retry could change: no error, no requeue
				Expect(err).ShouldNot(HaveOccurred())
				Expect(out).To(Equal(ctrl.Result{}))

				condition := vmProvisionedCondition()
				Expect(condition).ShouldNot(BeNil())
				Expect(condition.Status).To(Equal(metav1.ConditionFalse))
				Expect(condition.Reason).To(Equal(infrav1.InvalidNetworkDataReason))
				Expect(condition.Message).To(ContainSubstring(expectedMessage))

				var event string
				Expect(fakeRecorder.Events).To(Receive(&event))
				Expect(event).To(HavePrefix(corev1.EventTypeWarning + " " + infrav1.InvalidNetworkDataReason + " "))
				Expect(event).To(ContainSubstring(expectedMessage))

				err = fakeClient.Get(gocontext.Background(), machineNetworkDataSecretKey(kubevirtMachine), &corev1.Secret{})
				Expect(apierrors.IsNotFound(err)).To(BeTrue(), "unusable network data must not be copied to the infra cluster")
				expectNoVM()
			},
			Entry("a secret without a network data key", infrav1.CloudInitDataSourceNoCloud,
				map[string][]byte{"wrong-key": []byte(noCloudNetworkData)}, "is missing the networkdata key"),
			Entry("a secret with an empty network data value", infrav1.CloudInitDataSourceNoCloud,
				map[string][]byte{"networkdata": {}}, "network data is empty"),
			Entry("a config drive document that is not JSON", infrav1.CloudInitDataSourceConfigDrive,
				map[string][]byte{"networkdata": []byte(noCloudNetworkData)}, "must be a JSON object in the OpenStack network_data.json format"),
			Entry("a NoCloud document that is not a YAML mapping", infrav1.CloudInitDataSourceNoCloud,
				map[string][]byte{"networkdata": []byte("- not\n- a mapping\n")}, "must be a YAML mapping in the cloud-init network configuration format"),
		)

		It("should stop on inline network data that is not well formed, as a machine admitted without the webhook can carry", func() {
			networkData := noCloudNetworkData
			kubevirtMachine.Spec.CloudInit = &infrav1.CloudInitSpec{DataSource: infrav1.CloudInitDataSourceConfigDrive, NetworkData: &networkData}
			setup(1)

			out, err := kubevirtMachineReconciler.reconcileNormal(machineContext)
			Expect(err).ShouldNot(HaveOccurred())
			Expect(out).To(Equal(ctrl.Result{}))
			Expect(vmProvisionedCondition().Reason).To(Equal(infrav1.InvalidNetworkDataReason))
			Expect(vmProvisionedCondition().Message).To(ContainSubstring("inline networkData"))
			expectNoVM()
		})

		It("should provision the machine once unusable network data is corrected", func() {
			useNetworkDataSecret(infrav1.CloudInitDataSourceNoCloud, map[string][]byte{"wrong-key": []byte(noCloudNetworkData)})
			setup(2)

			_, err := kubevirtMachineReconciler.reconcileNormal(machineContext)
			Expect(err).ShouldNot(HaveOccurred())
			Expect(vmProvisionedCondition().Reason).To(Equal(infrav1.InvalidNetworkDataReason))
			expectNoVM()

			source := &corev1.Secret{}
			Expect(fakeClient.Get(gocontext.Background(), client.ObjectKeyFromObject(networkDataSecret), source)).To(Succeed())
			source.Data = map[string][]byte{"networkdata": []byte(noCloudNetworkData)}
			Expect(fakeClient.Update(gocontext.Background(), source)).To(Succeed())

			// The corrected secret carries the key, so the watch lets it through to the machine
			Expect(hasNetworkDataKey(source)).To(BeTrue())

			_, err = kubevirtMachineReconciler.reconcileNormal(machineContext)
			Expect(err).ShouldNot(HaveOccurred())
			Expect(cloudInitNoCloud(getVM()).NetworkDataSecretRef).To(HaveValue(Equal(corev1.LocalObjectReference{Name: machineNetworkDataSecretKey(kubevirtMachine).Name})))
		})

		It("should delete the network data secret of a machine with the machine", func() {
			useNetworkDataSecret(infrav1.CloudInitDataSourceNoCloud, map[string][]byte{"networkdata": []byte(noCloudNetworkData)})
			setup(2)

			_, err := kubevirtMachineReconciler.reconcileNormal(machineContext)
			Expect(err).ShouldNot(HaveOccurred())
			Expect(fakeClient.Get(gocontext.Background(), machineNetworkDataSecretKey(kubevirtMachine), &corev1.Secret{})).To(Succeed())

			_, err = kubevirtMachineReconciler.reconcileDelete(machineContext)
			Expect(err).ShouldNot(HaveOccurred())

			err = fakeClient.Get(gocontext.Background(), machineNetworkDataSecretKey(kubevirtMachine), &corev1.Secret{})
			Expect(apierrors.IsNotFound(err)).To(BeTrue())

			// The secret of the user is not the controller's to delete
			Expect(fakeClient.Get(gocontext.Background(), client.ObjectKeyFromObject(networkDataSecret), &corev1.Secret{})).To(Succeed())
		})
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
		machineMock.EXPECT().GenerateProviderID().Return("abc", nil).AnyTimes()
		machineMock.EXPECT().DrainNodeIfNeeded(gomock.Any()).Return(time.Duration(0), nil).AnyTimes()
		machineFactoryMock.EXPECT().NewMachine(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(machineMock, nil).Times(1)

		infraClusterMock.EXPECT().GenerateInfraClusterClient(kubevirtMachine.Spec.InfraClusterSecretRef, kubevirtMachine.Namespace, machineContext.Context).Return(fakeClient, kubevirtMachine.Namespace, nil).Times(3)

		out, err := kubevirtMachineReconciler.reconcileNormal(machineContext)

		Expect(err).ShouldNot(HaveOccurred())

		// should expect to re-enqueue while waiting for VMI to come online
		Expect(out).To(Equal(ctrl.Result{RequeueAfter: 20 * time.Second}))

		out, err = kubevirtMachineReconciler.reconcileDelete(machineContext)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(out).To(Equal(ctrl.Result{RequeueAfter: 0}))

		// Check bootstrapData secret is deleted
		machineBootstrapSecretReferenceName := machineContext.Machine.Spec.Bootstrap.DataSecretName
		machineBootstrapSecretReferenceKey := client.ObjectKey{Namespace: machineContext.Machine.GetNamespace(), Name: *machineBootstrapSecretReferenceName + "-userdata"}
		infraClusterClient, _, err := infraClusterMock.GenerateInfraClusterClient(kubevirtMachine.Spec.InfraClusterSecretRef, kubevirtMachine.Namespace, machineContext.Context)
		Expect(err).NotTo(HaveOccurred())
		bootstrapDataSecret := &corev1.Secret{}
		err = infraClusterClient.Get(gocontext.Background(), machineBootstrapSecretReferenceKey, bootstrapDataSecret)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())

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
		Expect(out).To(Equal(ctrl.Result{RequeueAfter: 0}))

		// Check finalizer is removed from machine
		Expect(machineContext.Machine.ObjectMeta.Finalizers).To(BeEmpty())
	})

	It("should update userdata correctly at KubevirtMachine reconcile", func() {
		// Get Machine
		// Get userdata secret name from machine
		// Get userdata secret and assert equality to original secret
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
		Expect(fakeClient.Get(gocontext.Background(), vmKey, vm)).To(Succeed())

		machineBootstrapSecretReferenceName := machineContext.Machine.Spec.Bootstrap.DataSecretName
		machineBootstrapSecretReferenceKey := client.ObjectKey{Namespace: machineContext.Machine.GetNamespace(), Name: *machineBootstrapSecretReferenceName + "-userdata"}
		infraClusterClient, _, err := infraClusterMock.GenerateInfraClusterClient(kubevirtMachine.Spec.InfraClusterSecretRef, kubevirtMachine.Namespace, machineContext.Context)
		Expect(err).NotTo(HaveOccurred())

		bootstrapDataSecret := &corev1.Secret{}
		Expect(
			infraClusterClient.Get(gocontext.Background(), machineBootstrapSecretReferenceKey, bootstrapDataSecret),
		).To(Succeed())

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

		// Should have created the userdata secret
		machineBootstrapSecretReferenceName := machineContext.Machine.Spec.Bootstrap.DataSecretName
		machineBootstrapSecretReferenceKey := client.ObjectKey{Namespace: kubevirtMachine.Namespace, Name: *machineBootstrapSecretReferenceName + "-userdata"}
		bootstrapDataSecret := &corev1.Secret{}
		Expect(
			fakeClient.Get(gocontext.Background(), machineBootstrapSecretReferenceKey, bootstrapDataSecret),
		).To(Succeed())
		Expect(bootstrapDataSecret.Data).To(HaveKeyWithValue("userdata", []byte("shell-script")))
		Expect(bootstrapDataSecret.Labels).To(HaveLen(1))
		Expect(bootstrapDataSecret.Labels).To(HaveKeyWithValue("hello", "world"))
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

		// Should have created the userdata secret
		machineBootstrapSecretReferenceName := machineContext.Machine.Spec.Bootstrap.DataSecretName
		machineBootstrapSecretReferenceKey := client.ObjectKey{Namespace: customNamespace, Name: *machineBootstrapSecretReferenceName + "-userdata"}
		bootstrapDataSecret := &corev1.Secret{}
		Expect(fakeClient.Get(gocontext.Background(), machineBootstrapSecretReferenceKey, bootstrapDataSecret)).To(Succeed())
		Expect(bootstrapDataSecret.Data).To(HaveKeyWithValue("userdata", []byte("shell-script")))
		Expect(bootstrapDataSecret.Labels).To(HaveLen(1))
		Expect(bootstrapDataSecret.Labels).To(HaveKeyWithValue("hello", "world"))
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

	It("When VM recovers from terminal state it should clear FailureReason and FailureMessage", func() {
		// Simulate a KubevirtMachine that previously had a terminal failure
		// (e.g., VM was stopped with RunStrategyHalted, then started back with RunStrategyAlways)
		failureErr := capierrors.UpdateMachineError
		failureMsg := "VMI has reached a permanent finalized state"
		kubevirtMachine.Status.FailureReason = &failureErr
		kubevirtMachine.Status.FailureMessage = &failureMsg

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
				IP: "10.200.36.200",
			},
		}
		vmi.Status.Phase = kubevirtv1.Running

		// VM now has RunStrategyAlways (recovered)
		runStrategy := kubevirtv1.RunStrategyAlways
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

		_, err := kubevirtMachineReconciler.reconcileNormal(machineContext)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(machineContext.KubevirtMachine.Status.FailureReason).To(BeNil())
		Expect(machineContext.KubevirtMachine.Status.FailureMessage).To(BeNil())
		Expect(machineContext.KubevirtMachine.Status.Ready).To(BeTrue())
	})

	Context("update kubevirt machine conditions correctly", func() {
		It("adds a failed VMProvisionedCondition with reason WaitingForClusterInfrastructureReason when the infrastructure is not ready", func() {
			cluster.Status.Conditions = []metav1.Condition{
				{
					Type:   string(clusterv1.InfrastructureReadyCondition),
					Status: metav1.ConditionFalse,
					Reason: "NotReady",
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
			}

			setupClient(kubevirt.DefaultMachineFactory{}, objects)

			// kubevirtMachineReconciler.Client
			kubevirtMachineKey := types.NamespacedName{Namespace: kubevirtMachine.Namespace, Name: kubevirtMachine.Name}
			_, err := kubevirtMachineReconciler.Reconcile(machineContext, ctrl.Request{NamespacedName: kubevirtMachineKey})
			Expect(err).ShouldNot(HaveOccurred())

			newKubevirtMachine := &infrav1.KubevirtMachine{}
			err = kubevirtMachineReconciler.Get(machineContext, kubevirtMachineKey, newKubevirtMachine)
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
					bootstrapUserDataSecret,
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
				delete(machine.Labels, clusterv1.MachineControlPlaneNameLabel)
				conditions.Set(cluster, metav1.Condition{
					Type:   clusterv1.ClusterControlPlaneInitializedCondition,
					Status: metav1.ConditionFalse,
					Reason: "NotInitialized",
				})

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
				Expect(conditions[0].Reason).To(Equal(infrav1.WaitingForControlPlaneAvailableReason))
			})
			It("adds a failed VMProvisionedCondition with reason WaitingForBootstrapDataReason when bootstrap data is not yet available", func() {
				machine.Spec.Bootstrap.DataSecretName = nil
				delete(machine.Labels, clusterv1.MachineControlPlaneNameLabel)
				conditions.Set(cluster, metav1.Condition{
					Type:   clusterv1.ClusterControlPlaneInitializedCondition,
					Status: metav1.ConditionTrue,
					Reason: "Initialized",
				})

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
				Expect(err).Should(HaveOccurred())

				conditions := machineContext.KubevirtMachine.GetConditions()
				Expect(conditions[0].Type).To(Equal(infrav1.VMProvisionedCondition))
				Expect(conditions[0].Reason).To(Equal(infrav1.WaitingForBootstrapDataReason))
			})

			It("adds a failed VMProvisionedCondition with reason VMCreateFailed when failing to create VM", func() {
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
				Expect(conditions[0].Status).To(Equal(metav1.ConditionFalse))
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
				machineMock.EXPECT().DrainNodeIfNeeded(gomock.Any()).Return(time.Duration(0), nil)
				machineMock.EXPECT().Addresses().Return([]string{"1.1.1.1"}).Times(1)
				machineMock.EXPECT().IsLiveMigratable().Return(false, "", "", nil).Times(1)

				machineFactoryMock.EXPECT().NewMachine(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(machineMock, nil).Times(1)

				infraClusterMock.EXPECT().GenerateInfraClusterClient(kubevirtMachine.Spec.InfraClusterSecretRef, kubevirtMachine.Namespace, machineContext.Context).Return(fakeClient, kubevirtMachine.Namespace, nil)

				_, err := kubevirtMachineReconciler.reconcileNormal(machineContext)
				Expect(err).ShouldNot(HaveOccurred())

				conds := machineContext.KubevirtMachine.GetConditions()
				Expect(conditions.Get(machineContext.KubevirtMachine, infrav1.VMLiveMigratableCondition)).ToNot(BeNil())
				Expect(conditions.Get(machineContext.KubevirtMachine, infrav1.VMLiveMigratableCondition).Status).To(Equal(metav1.ConditionFalse))
				Expect(conditions.Get(machineContext.KubevirtMachine, infrav1.VMProvisionedCondition)).ToNot(BeNil())
				Expect(conditions.Get(machineContext.KubevirtMachine, infrav1.VMProvisionedCondition).Status).To(Equal(metav1.ConditionTrue))
				Expect(conditions.Get(machineContext.KubevirtMachine, infrav1.BootstrapExecSucceededCondition)).ToNot(BeNil())
				Expect(conditions.Get(machineContext.KubevirtMachine, infrav1.BootstrapExecSucceededCondition).Status).To(Equal(metav1.ConditionTrue))
				_ = conds
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
				machineMock.EXPECT().DrainNodeIfNeeded(gomock.Any()).Return(time.Duration(0), nil)

				machineFactoryMock.EXPECT().NewMachine(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(machineMock, nil).Times(1)

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
				machineMock.EXPECT().DrainNodeIfNeeded(gomock.Any()).Return(time.Duration(0), nil)
				machineMock.EXPECT().Addresses().Return([]string{"1.1.1.1"}).Times(1)
				machineMock.EXPECT().IsLiveMigratable().Return(false, "", "", nil).Times(1)

				machineFactoryMock.EXPECT().NewMachine(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(machineMock, nil).Times(1)

				setupClient(machineFactoryMock, objects)

				infraClusterMock.EXPECT().GenerateInfraClusterClient(kubevirtMachine.Spec.InfraClusterSecretRef, kubevirtMachine.Namespace, machineContext.Context).Return(fakeClient, kubevirtMachine.Namespace, nil)

				_, err := kubevirtMachineReconciler.reconcileNormal(machineContext)
				Expect(err).ShouldNot(HaveOccurred())

				conditions := machineContext.KubevirtMachine.GetConditions()

				Expect(conditions[0].Type).To(Equal(infrav1.BootstrapExecSucceededCondition))
				Expect(conditions[0].Status).To(Equal(metav1.ConditionTrue))
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
				machineMock.EXPECT().DrainNodeIfNeeded(gomock.Any()).Return(time.Duration(0), nil)
				machineMock.EXPECT().Addresses().Return([]string{"1.1.1.1"}).Times(1)
				machineMock.EXPECT().IsLiveMigratable().Return(true, "", "", nil).Times(1)

				machineFactoryMock.EXPECT().NewMachine(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(machineMock, nil).Times(1)

				setupClient(machineFactoryMock, objects)

				infraClusterMock.EXPECT().GenerateInfraClusterClient(kubevirtMachine.Spec.InfraClusterSecretRef, kubevirtMachine.Namespace, machineContext.Context).Return(fakeClient, kubevirtMachine.Namespace, nil)

				_, err := kubevirtMachineReconciler.reconcileNormal(machineContext)
				Expect(err).ShouldNot(HaveOccurred())

				conditions := machineContext.KubevirtMachine.GetConditions()

				Expect(conditions[0].Type).To(Equal(infrav1.BootstrapExecSucceededCondition))
				Expect(conditions[0].Status).To(Equal(metav1.ConditionTrue))
				Expect(conditions[1].Type).To(Equal(infrav1.VMLiveMigratableCondition))
				Expect(conditions[1].Status).To(Equal(metav1.ConditionTrue))
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
					bootstrapUserDataSecret,
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

				machineFactoryMock.EXPECT().NewMachine(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(machineMock, nil).Times(1)

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
					bootstrapUserDataSecret,
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

				machineFactoryMock.EXPECT().NewMachine(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(machineMock, nil).Times(1)

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
			bootstrapUserDataSecret,
		}

		// test that if the source secret has changed, and there is alredy a secret exist, we still copy the source
		// to the dest.
		// the source is the bootstrap secret, the dest is the bootstrap user data secret

		setupClient(kubevirt.DefaultMachineFactory{}, objects)

		infraClusterMock.EXPECT().GenerateInfraClusterClient(kubevirtMachine.Spec.InfraClusterSecretRef, kubevirtMachine.Namespace, machineContext.Context).Return(fakeClient, kubevirtMachine.Namespace, nil)

		Expect(machineContext.KubevirtMachine.Status.Ready).To(BeTrue())
		out, err := kubevirtMachineReconciler.reconcileNormal(machineContext)
		Expect(machineContext.KubevirtMachine.Status.Ready).To(BeFalse())
		Expect(err).ToNot(HaveOccurred())
		Expect(out).To(Equal(ctrl.Result{RequeueAfter: 20 * time.Second}))
		Expect(machineContext.BootstrapDataSecret.Data["userdata"]).To(Equal(bootstrapSecret.Data["value"]))
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

// cloudInitConfigDrive returns the cloud-init config drive source of the given virtual machine,
// or nil when the virtual machine has no config drive volume.
func cloudInitConfigDrive(vm *kubevirtv1.VirtualMachine) *kubevirtv1.CloudInitConfigDriveSource {
	if vm.Spec.Template == nil {
		return nil
	}
	for _, volume := range vm.Spec.Template.Spec.Volumes {
		if volume.CloudInitConfigDrive != nil {
			return volume.CloudInitConfigDrive
		}
	}
	return nil
}

// cloudInitNoCloud returns the cloud-init NoCloud source of the given virtual machine,
// or nil when the virtual machine has no NoCloud volume.
func cloudInitNoCloud(vm *kubevirtv1.VirtualMachine) *kubevirtv1.CloudInitNoCloudSource {
	if vm.Spec.Template == nil {
		return nil
	}
	for _, volume := range vm.Spec.Template.Spec.Volumes {
		if volume.CloudInitNoCloud != nil {
			return volume.CloudInitNoCloud
		}
	}
	return nil
}
