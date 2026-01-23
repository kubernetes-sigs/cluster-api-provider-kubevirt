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

package kubevirt

import (
	gocontext "context"
	"fmt"
	"time"

	"github.com/golang/mock/gomock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	kubevirtv1 "kubevirt.io/api/core/v1"
	cdiv1 "kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha1"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/context"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/ssh"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/testing"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/workloadcluster/mock"
)

var (
	clusterName         = "test-cluster"
	kubevirtClusterName = "test-kubevirt-cluster"
	kubevirtCluster     = testing.NewKubevirtCluster(clusterName, kubevirtClusterName)
	cluster             = testing.NewCluster(clusterName, kubevirtCluster)

	sshKey = "ssh-rsa 1234"

	machineName         = "test-machine"
	kubevirtMachineName = "test-kubevirt-machine"
	kubevirtMachine     = testing.NewKubevirtMachine(kubevirtMachineName, machineName)
	machine             = testing.NewMachine(clusterName, machineName, kubevirtMachine)

	bootstrapDataSecret = testing.NewBootstrapDataSecret([]byte(fmt.Sprintf("#cloud-config\n\n%s\n", sshKey)))

	logger = zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)).WithName("machine_test")

	fakeClient            client.Client
	fakeVMCommandExecutor FakeVMCommandExecutor
)

var _ = Describe("Without KubeVirt VM running", func() {
	var machineContext *context.MachineContext
	namespace := kubevirtMachine.Namespace
	virtualMachineInstance := testing.NewVirtualMachineInstance(kubevirtMachine)
	virtualMachine := testing.NewVirtualMachine(virtualMachineInstance)

	BeforeEach(func() {
		kubevirtMachine.Spec.BootstrapCheckSpec = v1alpha1.VirtualMachineBootstrapCheckSpec{}

		machineContext = &context.MachineContext{
			Context:             gocontext.TODO(),
			Cluster:             cluster,
			KubevirtCluster:     kubevirtCluster,
			Machine:             machine,
			KubevirtMachine:     kubevirtMachine,
			BootstrapDataSecret: bootstrapDataSecret,
			Logger:              logger,
		}

		objects := []client.Object{
			cluster,
			kubevirtCluster,
			machine,
			kubevirtMachine,
		}

		fakeClient = fake.NewClientBuilder().WithScheme(testing.SetupScheme()).WithObjects(objects...).Build()

		fakeVMCommandExecutor = FakeVMCommandExecutor{false}
	})

	AfterEach(func() {})

	It("NewMachine should have client and machineContext set, but vmiInstance equal nil", func() {
		externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte{})
		Expect(err).NotTo(HaveOccurred())
		Expect(externalMachine.client).To(Equal(fakeClient))
		Expect(externalMachine.machineContext).To(Equal(machineContext))
		Expect(externalMachine.vmiInstance).To(BeNil())
	})

	It("Exists should return false", func() {
		externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte{})
		Expect(err).NotTo(HaveOccurred())
		Expect(externalMachine.Exists()).To(BeFalse())
	})

	It("Address should return ''", func() {
		externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte{})
		Expect(err).NotTo(HaveOccurred())
		Expect(externalMachine.Address()).To(Equal(""))
	})

	It("Addresses should return empty slice when VMI is nil", func() {
		externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte{})
		Expect(err).NotTo(HaveOccurred())
		Expect(externalMachine.Addresses()).To(BeEmpty())
	})

	It("IsReady should return false", func() {
		externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte{})
		Expect(err).NotTo(HaveOccurred())
		Expect(externalMachine.IsReady()).To(BeFalse())
	})

	It("default mode: IsBootstrapped should return false", func() {
		externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte{})
		Expect(err).NotTo(HaveOccurred())
		Expect(externalMachine.IsBootstrapped()).To(BeFalse())
	})

	It("ssh mode: IsBootstrapped return false", func() {
		externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte{})
		externalMachine.machineContext.KubevirtMachine.Spec.BootstrapCheckSpec.CheckStrategy = "ssh"
		Expect(err).NotTo(HaveOccurred())
		Expect(externalMachine.IsBootstrapped()).To(BeFalse())
	})

	It("none mode: IsBootstrapped should be forced to be true", func() {
		externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte{})
		externalMachine.machineContext.KubevirtMachine.Spec.BootstrapCheckSpec.CheckStrategy = "none"
		Expect(err).NotTo(HaveOccurred())
		Expect(externalMachine.IsBootstrapped()).To(BeTrue())
	})

	It("invalid mode: IsBootstrapped should return false", func() {
		externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte{})
		externalMachine.machineContext.KubevirtMachine.Spec.BootstrapCheckSpec.CheckStrategy = "impossible-invalid-input"
		Expect(err).NotTo(HaveOccurred())
		Expect(externalMachine.IsBootstrapped()).To(BeFalse())
	})

	It("SupportsCheckingIsBootstrapped should return false", func() {
		externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte{})
		Expect(err).NotTo(HaveOccurred())
		Expect(externalMachine.SupportsCheckingIsBootstrapped()).To(BeFalse())
	})

	It("GenerateProviderID should fail", func() {
		externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte{})
		Expect(err).NotTo(HaveOccurred())
		providerId, err := externalMachine.GenerateProviderID()
		Expect(err).To(HaveOccurred())
		Expect(providerId).To(Equal(""))
	})

	It("Create should create VM, but not VMI", func() {
		externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte{})
		Expect(err).NotTo(HaveOccurred())

		// read the vm before creation
		validateVMNotExist(virtualMachine, fakeClient, machineContext)

		Expect(externalMachine.Create(machineContext.Context)).To(Succeed())

		// read the vm before creation
		validateVMExist(virtualMachine, fakeClient, machineContext)
	})

	It("Create should create VM if it doesn't exist", func() {
		externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte{})
		Expect(err).NotTo(HaveOccurred())

		// read the vm before creation
		validateVMNotExist(virtualMachine, fakeClient, machineContext)

		Expect(externalMachine.Create(machineContext.Context)).To(Succeed())

		// read the new created vm
		validateVMExist(virtualMachine, fakeClient, machineContext)
	})

	It("Delete should be lenient if VM doesn't exist", func() {
		externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte{})
		Expect(err).NotTo(HaveOccurred())
		validateVMNotExist(virtualMachine, fakeClient, machineContext)

		Expect(externalMachine.Delete()).To(Succeed())
	})
})

var _ = Describe("With KubeVirt VM running", func() {
	var machineContext *context.MachineContext
	namespace := kubevirtMachine.Namespace
	virtualMachineInstance := testing.NewVirtualMachineInstance(kubevirtMachine)
	virtualMachine := testing.NewVirtualMachine(virtualMachineInstance)

	BeforeEach(func() {
		kubevirtMachine.Spec.BootstrapCheckSpec = v1alpha1.VirtualMachineBootstrapCheckSpec{}

		machineContext = &context.MachineContext{
			Context:             gocontext.TODO(),
			Cluster:             cluster,
			KubevirtCluster:     kubevirtCluster,
			Machine:             machine,
			KubevirtMachine:     kubevirtMachine,
			BootstrapDataSecret: bootstrapDataSecret,
			Logger:              logger,
		}

		virtualMachineInstance.Status.Conditions = []kubevirtv1.VirtualMachineInstanceCondition{
			{
				Type:   kubevirtv1.VirtualMachineInstanceReady,
				Status: corev1.ConditionTrue,
			},
		}

		fakeVMCommandExecutor = FakeVMCommandExecutor{true}
	})
	JustBeforeEach(func() {
		objects := []client.Object{
			cluster,
			kubevirtCluster,
			machine,
			kubevirtMachine,
			virtualMachineInstance,
			virtualMachine,
		}
		fakeClient = fake.NewClientBuilder().WithScheme(testing.SetupScheme()).WithObjects(objects...).Build()
	})

	AfterEach(func() {})

	It("NewMachine should have all client, machineContext and vmiInstance NOT nil", func() {
		externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte(sshKey))
		Expect(err).NotTo(HaveOccurred())
		Expect(externalMachine.client).ToNot(BeNil())
		Expect(externalMachine.machineContext).To(Equal(machineContext))
		Expect(externalMachine.vmiInstance).ToNot(BeNil())
	})

	It("Exists should return true", func() {
		externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte(sshKey))
		Expect(err).NotTo(HaveOccurred())
		Expect(externalMachine.Exists()).To(BeTrue())
	})

	It("Address should return non-empty IP", func() {
		externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte(sshKey))
		Expect(err).NotTo(HaveOccurred())
		Expect(externalMachine.Address()).To(Equal(virtualMachineInstance.Status.Interfaces[0].IP))
	})

	It("Addresses should return all IPs from interfaces", func() {
		// Set up dual-stack IPs on the interface
		virtualMachineInstance.Status.Interfaces[0].IPs = []string{"1.1.1.1", "2001:db8::1"}
		fakeClient = fake.NewClientBuilder().WithScheme(testing.SetupScheme()).WithRuntimeObjects(cluster, kubevirtMachine, virtualMachine, virtualMachineInstance, bootstrapDataSecret).Build()

		externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte(sshKey))
		Expect(err).NotTo(HaveOccurred())
		addresses := externalMachine.Addresses()
		Expect(addresses).To(HaveLen(2))
		Expect(addresses).To(ContainElement("1.1.1.1"))
		Expect(addresses).To(ContainElement("2001:db8::1"))
	})

	It("Addresses should return IPs from multiple interfaces", func() {
		// Set up multiple interfaces with IPs
		virtualMachineInstance.Status.Interfaces = []kubevirtv1.VirtualMachineInstanceNetworkInterface{
			{IP: "1.1.1.1", IPs: []string{"1.1.1.1", "2001:db8::1"}},
			{IP: "10.0.0.1", IPs: []string{"10.0.0.1", "2001:db8::2"}},
		}
		fakeClient = fake.NewClientBuilder().WithScheme(testing.SetupScheme()).WithRuntimeObjects(cluster, kubevirtMachine, virtualMachine, virtualMachineInstance, bootstrapDataSecret).Build()

		externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte(sshKey))
		Expect(err).NotTo(HaveOccurred())
		addresses := externalMachine.Addresses()
		Expect(addresses).To(HaveLen(4))
		Expect(addresses).To(ContainElement("1.1.1.1"))
		Expect(addresses).To(ContainElement("2001:db8::1"))
		Expect(addresses).To(ContainElement("10.0.0.1"))
		Expect(addresses).To(ContainElement("2001:db8::2"))
	})

	It("IsReady should return true", func() {
		externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte(sshKey))
		Expect(err).NotTo(HaveOccurred())
		Expect(externalMachine.IsReady()).To(BeTrue())
	})

	It("default mode: IsBootstrapped should return true", func() {
		externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte(sshKey))
		Expect(err).NotTo(HaveOccurred())
		Expect(externalMachine.IsBootstrapped()).To(BeTrue())
	})

	It("ssh mode: IsBootstrapped return true", func() {
		externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte(sshKey))
		externalMachine.machineContext.KubevirtMachine.Spec.BootstrapCheckSpec.CheckStrategy = "ssh"
		Expect(err).NotTo(HaveOccurred())
		Expect(externalMachine.IsBootstrapped()).To(BeTrue())
	})

	It("none mode: IsBootstrapped should be forced to be true", func() {
		externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte(sshKey))
		externalMachine.machineContext.KubevirtMachine.Spec.BootstrapCheckSpec.CheckStrategy = "none"
		Expect(err).NotTo(HaveOccurred())
		Expect(externalMachine.IsBootstrapped()).To(BeTrue())
	})

	It("invalid mode: IsBootstrapped should return false", func() {
		externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte(sshKey))
		externalMachine.machineContext.KubevirtMachine.Spec.BootstrapCheckSpec.CheckStrategy = "impossible-invalid-input"
		Expect(err).NotTo(HaveOccurred())
		Expect(externalMachine.IsBootstrapped()).To(BeFalse())
	})

	It("SupportsCheckingIsBootstrapped should return true", func() {
		externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte(sshKey))
		Expect(err).NotTo(HaveOccurred())
		Expect(externalMachine.SupportsCheckingIsBootstrapped()).To(BeTrue())
	})

	It("GenerateProviderID should succeed", func() {
		expectedProviderId := fmt.Sprintf("kubevirt://%s", kubevirtMachineName)

		externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte(sshKey))
		Expect(err).NotTo(HaveOccurred())
		providerId, err := externalMachine.GenerateProviderID()
		Expect(err).ToNot(HaveOccurred())
		Expect(providerId).To(Equal(expectedProviderId))
	})

	It("Delete should succeed", func() {
		externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte{})
		Expect(err).NotTo(HaveOccurred())
		validateVMExist(virtualMachine, fakeClient, machineContext)

		Expect(externalMachine.Delete()).To(Succeed())
		validateVMNotExist(virtualMachine, fakeClient, machineContext)
	})

	Context("test DrainNodeIfNeeded", func() {
		const nodeName = "control-plane1"

		var (
			wlCluster *mock.MockWorkloadCluster
		)

		BeforeEach(func() {
			virtualMachineInstance = testing.NewVirtualMachineInstance(kubevirtMachine)
			strategy := kubevirtv1.EvictionStrategyExternal
			virtualMachineInstance.Spec.EvictionStrategy = &strategy
			virtualMachineInstance.Status.EvacuationNodeName = nodeName

			if kubevirtMachine.Annotations == nil {
				kubevirtMachine.Annotations = make(map[string]string)
			}

			mockCtrl := gomock.NewController(GinkgoT())
			wlCluster = mock.NewMockWorkloadCluster(mockCtrl)
		})

		When("VMI is not evicted", func() {
			BeforeEach(func() {
				virtualMachineInstance.Spec.EvictionStrategy = nil
				virtualMachineInstance.Status.EvacuationNodeName = ""
			})

			It("Should do nothing", func() {
				wlCluster.EXPECT().GenerateWorkloadClusterK8sClient(gomock.Any()).Times(0)

				externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte(sshKey))
				Expect(err).NotTo(HaveOccurred())

				requeueDuration, err := externalMachine.DrainNodeIfNeeded(wlCluster)
				Expect(err).NotTo(HaveOccurred())
				Expect(requeueDuration).Should(BeZero())

				vmi := &kubevirtv1.VirtualMachineInstance{}
				err = fakeClient.Get(gocontext.Background(), client.ObjectKey{Namespace: virtualMachineInstance.Namespace, Name: virtualMachineInstance.Name}, vmi)
				Expect(err).ToNot(HaveOccurred())
				Expect(vmi).ToNot(BeNil())
			})
		})

		When("VMI is already deleted", func() {
			BeforeEach(func() {
				deletionTimeStamp := metav1.NewTime(time.Now().UTC().Add(-5 * time.Second))
				virtualMachineInstance.DeletionTimestamp = &deletionTimeStamp
				virtualMachineInstance.Finalizers = []string{"fake/finalizer"}
			})

			It("Should do nothing", func() {
				wlCluster.EXPECT().GenerateWorkloadClusterK8sClient(gomock.Any()).Times(0)

				externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte(sshKey))
				Expect(err).NotTo(HaveOccurred())

				requeueDuration, err := externalMachine.DrainNodeIfNeeded(wlCluster)
				Expect(err).NotTo(HaveOccurred())
				Expect(requeueDuration).Should(BeZero())

				vmi := &kubevirtv1.VirtualMachineInstance{}
				err = fakeClient.Get(gocontext.Background(), client.ObjectKey{Namespace: virtualMachineInstance.Namespace, Name: virtualMachineInstance.Name}, vmi)
				Expect(err).ToNot(HaveOccurred())
				Expect(vmi).ToNot(BeNil())
			})
		})

		When("VMI is already deleted, but the grace period annotation is still there", func() {
			BeforeEach(func() {
				deletionTimeStamp := metav1.NewTime(time.Now().UTC().Add(-5 * time.Second))
				virtualMachineInstance.DeletionTimestamp = &deletionTimeStamp
				virtualMachineInstance.Finalizers = []string{"fake/finalizer"}

				graceTime := time.Now().UTC().Add(-5 * time.Minute).Format(time.RFC3339)
				kubevirtMachine.Annotations[v1alpha1.VmiDeletionGraceTime] = graceTime
			})

			It("Should remove the grace period annotation", func() {
				wlCluster.EXPECT().GenerateWorkloadClusterK8sClient(gomock.Any()).Times(0)

				externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte(sshKey))
				Expect(err).NotTo(HaveOccurred())

				requeueDuration, err := externalMachine.DrainNodeIfNeeded(wlCluster)
				Expect(err).NotTo(HaveOccurred())
				Expect(requeueDuration).Should(BeZero())

				vmi := &kubevirtv1.VirtualMachineInstance{}
				err = fakeClient.Get(gocontext.Background(), client.ObjectKey{Namespace: virtualMachineInstance.Namespace, Name: virtualMachineInstance.Name}, vmi)
				Expect(err).ToNot(HaveOccurred())
				Expect(vmi).ToNot(BeNil())

				machine := &v1alpha1.KubevirtMachine{}
				err = fakeClient.Get(gocontext.Background(), client.ObjectKey{Namespace: kubevirtMachine.Namespace, Name: kubevirtMachine.Name}, machine)
				Expect(err).ToNot(HaveOccurred())
				Expect(machine).ToNot(BeNil())
				Expect(machine.Annotations).ToNot(HaveKey(v1alpha1.VmiDeletionGraceTime))
			})
		})

		When("VMI is missing, but the grace period annotation is still there", func() {
			BeforeEach(func() {
				graceTime := time.Now().UTC().Add(-5 * time.Minute).Format(time.RFC3339)
				kubevirtMachine.Annotations[v1alpha1.VmiDeletionGraceTime] = graceTime
			})

			It("Should remove the grace period annotation", func() {
				wlCluster.EXPECT().GenerateWorkloadClusterK8sClient(gomock.Any()).Times(0)

				externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte(sshKey))
				Expect(err).NotTo(HaveOccurred())
				externalMachine.vmiInstance = nil

				requeueDuration, err := externalMachine.DrainNodeIfNeeded(wlCluster)
				Expect(err).NotTo(HaveOccurred())
				Expect(requeueDuration).Should(BeZero())

				vmi := &kubevirtv1.VirtualMachineInstance{}
				err = fakeClient.Get(gocontext.Background(), client.ObjectKey{Namespace: virtualMachineInstance.Namespace, Name: virtualMachineInstance.Name}, vmi)
				Expect(err).ToNot(HaveOccurred())
				Expect(vmi).ToNot(BeNil())

				machine := &v1alpha1.KubevirtMachine{}
				err = fakeClient.Get(gocontext.Background(), client.ObjectKey{Namespace: kubevirtMachine.Namespace, Name: kubevirtMachine.Name}, machine)
				Expect(err).ToNot(HaveOccurred())
				Expect(machine).ToNot(BeNil())
				Expect(machine.Annotations).ToNot(HaveKey(v1alpha1.VmiDeletionGraceTime))
			})
		})

		When("grace not expired (wrap for BeforeEach)", func() {
			BeforeEach(func() {
				graceTime := time.Now().UTC().Add(5 * time.Minute).Format(time.RFC3339)
				kubevirtMachine.Annotations[v1alpha1.VmiDeletionGraceTime] = graceTime
			})

			It("Should drain the node", func() {
				node := &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: nodeName,
					},
				}

				Expect(k8sfake.AddToScheme(setupRemoteScheme())).ToNot(HaveOccurred())
				cl := k8sfake.NewSimpleClientset(node)

				wlCluster.EXPECT().GenerateWorkloadClusterK8sClient(gomock.Any()).Return(cl, nil).Times(1)

				externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte(sshKey))
				Expect(err).NotTo(HaveOccurred())

				requeueDuration, err := externalMachine.DrainNodeIfNeeded(wlCluster)
				Expect(err).NotTo(HaveOccurred())
				Expect(requeueDuration).To(Equal(time.Duration(10 * time.Second)))

				vmi := &kubevirtv1.VirtualMachineInstance{}
				err = fakeClient.Get(gocontext.Background(), client.ObjectKey{Namespace: virtualMachineInstance.Namespace, Name: virtualMachineInstance.Name}, vmi)
				Expect(err).To(HaveOccurred())
				Expect(apierrors.IsNotFound(err)).To(BeTrue())

				machine := &v1alpha1.KubevirtMachine{}
				err = fakeClient.Get(gocontext.Background(), client.ObjectKey{Namespace: kubevirtMachine.Namespace, Name: kubevirtMachine.Name}, machine)
				Expect(err).ToNot(HaveOccurred())
				Expect(machine).ToNot(BeNil())
				Expect(machine.Annotations).ToNot(HaveKey(v1alpha1.VmiDeletionGraceTime))
			})
		})

		When("grace not expired, drain fails (wrap for BeforeEach)", func() {
			BeforeEach(func() {
				graceTime := time.Now().UTC().Add(5 * time.Minute).Format(time.RFC3339)
				kubevirtMachine.Annotations[v1alpha1.VmiDeletionGraceTime] = graceTime
			})

			It("Should not drain the node", func() {
				node := &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: nodeName,
					},
				}

				Expect(k8sfake.AddToScheme(setupRemoteScheme())).ToNot(HaveOccurred())
				cl := k8sfake.NewSimpleClientset(node)

				fakeErr := errors.New("fake error: can't get node")
				cl.PrependReactor("get", "nodes", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, nil, fakeErr
				})

				wlCluster.EXPECT().GenerateWorkloadClusterK8sClient(gomock.Any()).Return(cl, nil).Times(1)

				externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte(sshKey))
				Expect(err).ToNot(HaveOccurred())

				requeueDuration, err := externalMachine.DrainNodeIfNeeded(wlCluster)
				Expect(err).To(HaveOccurred())
				Expect(err).To(MatchError(fakeErr))
				Expect(requeueDuration).To(Equal(time.Duration(0)))

				By("VMI should not be deleted")
				vmi := &kubevirtv1.VirtualMachineInstance{}
				err = fakeClient.Get(gocontext.Background(), client.ObjectKey{Namespace: virtualMachineInstance.Namespace, Name: virtualMachineInstance.Name}, vmi)
				Expect(err).ToNot(HaveOccurred())
				Expect(vmi).ToNot(BeNil())

				By("the grace period annotation still exists")
				machine := &v1alpha1.KubevirtMachine{}
				err = fakeClient.Get(gocontext.Background(), client.ObjectKey{Namespace: kubevirtMachine.Namespace, Name: kubevirtMachine.Name}, machine)
				Expect(err).ToNot(HaveOccurred())
				Expect(machine).ToNot(BeNil())
				Expect(machine.Annotations).To(HaveKey(v1alpha1.VmiDeletionGraceTime))
			})
		})

		When("grace period expired (wrap for BeforeEach)", func() {
			BeforeEach(func() {
				graceTime := time.Now().UTC().Format(time.RFC3339)
				kubevirtMachine.Annotations[v1alpha1.VmiDeletionGraceTime] = graceTime
			})

			It("Should delete the VMI after grace period", func() {
				wlCluster.EXPECT().GenerateWorkloadClusterK8sClient(gomock.Any()).Times(0)

				externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte(sshKey))
				Expect(err).NotTo(HaveOccurred())

				requeueDuration, err := externalMachine.DrainNodeIfNeeded(nil)
				Expect(err).NotTo(HaveOccurred())
				Expect(requeueDuration).Should(Equal(10 * time.Second))

				vmi := &kubevirtv1.VirtualMachineInstance{}
				err = fakeClient.Get(gocontext.Background(), client.ObjectKey{Namespace: virtualMachineInstance.Namespace, Name: virtualMachineInstance.Name}, vmi)
				Expect(err).To(HaveOccurred())
				Expect(apierrors.IsNotFound(err)).To(BeTrue())

				machine := &v1alpha1.KubevirtMachine{}
				err = fakeClient.Get(gocontext.Background(), client.ObjectKey{Namespace: kubevirtMachine.Namespace, Name: kubevirtMachine.Name}, machine)
				Expect(err).ToNot(HaveOccurred())
				Expect(machine).ToNot(BeNil())
				Expect(machine.Annotations).ToNot(HaveKey(v1alpha1.VmiDeletionGraceTime))
			})
		})
	})
})

var _ = Describe("util functions", func() {
	var machineContext *context.MachineContext

	BeforeEach(func() {
		machineContext = &context.MachineContext{
			Context:             gocontext.TODO(),
			Cluster:             cluster,
			KubevirtCluster:     kubevirtCluster,
			Machine:             machine,
			KubevirtMachine:     kubevirtMachine.DeepCopy(),
			BootstrapDataSecret: bootstrapDataSecret,
			Logger:              logger,
		}
	})

	It("GenerateProviderID should succeed", func() {
		dataVolumeTemplates := []kubevirtv1.DataVolumeTemplateSpec{
			{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      map[string]string{"my": "label"},
					Annotations: map[string]string{"my": "annotation"},
					Name:        "dv1",
				},
			},
		}
		volumes := []kubevirtv1.Volume{
			{
				Name: "test1",
				VolumeSource: kubevirtv1.VolumeSource{
					DataVolume: &kubevirtv1.DataVolumeSource{
						Name: "dv1",
					},
				},
			},
		}

		machineContext.KubevirtMachine.Spec.VirtualMachineTemplate.Spec.DataVolumeTemplates = dataVolumeTemplates
		machineContext.KubevirtMachine.Spec.VirtualMachineTemplate.Spec.Template.Spec.Volumes = volumes

		newVM := newVirtualMachineFromKubevirtMachine(machineContext, "default")

		Expect(newVM.Spec.DataVolumeTemplates[0].ObjectMeta.Name).To(Equal(kubevirtMachineName + "-dv1"))
		Expect(newVM.Spec.Template.Spec.Volumes[0].VolumeSource.DataVolume.Name).To(Equal(kubevirtMachineName + "-dv1"))
	})
})

var _ = Describe("With KubeVirt VM running externally", func() {
	var machineContext *context.MachineContext
	namespace := "external"
	virtualMachineInstance := testing.NewExternalVirtualMachineInstance(kubevirtMachine, namespace)
	virtualMachine := testing.NewVirtualMachine(virtualMachineInstance)

	BeforeEach(func() {
		kubevirtMachine.Spec.BootstrapCheckSpec = v1alpha1.VirtualMachineBootstrapCheckSpec{}

		machineContext = &context.MachineContext{
			Context:             gocontext.TODO(),
			Cluster:             cluster,
			KubevirtCluster:     kubevirtCluster,
			Machine:             machine,
			KubevirtMachine:     kubevirtMachine,
			BootstrapDataSecret: bootstrapDataSecret,
			Logger:              logger,
		}

		virtualMachineInstance.Status.Conditions = []kubevirtv1.VirtualMachineInstanceCondition{
			{
				Type:   kubevirtv1.VirtualMachineInstanceReady,
				Status: corev1.ConditionTrue,
			},
		}
		objects := []client.Object{
			cluster,
			kubevirtCluster,
			machine,
			kubevirtMachine,
			virtualMachineInstance,
			virtualMachine,
		}

		fakeClient = fake.NewClientBuilder().WithScheme(testing.SetupScheme()).WithObjects(objects...).Build()

		fakeVMCommandExecutor = FakeVMCommandExecutor{true}
	})

	AfterEach(func() {})

	It("NewMachine should have all client, machineContext and vmiInstance NOT nil", func() {
		externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte(sshKey))
		Expect(err).NotTo(HaveOccurred())
		Expect(externalMachine.client).ToNot(BeNil())
		Expect(externalMachine.machineContext).To(Equal(machineContext))
		Expect(externalMachine.vmiInstance).ToNot(BeNil())
	})

	It("Exists should return true", func() {
		externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte(sshKey))
		Expect(err).NotTo(HaveOccurred())
		Expect(externalMachine.Exists()).To(BeTrue())
	})

	It("Address should return non-empty IP", func() {
		externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte(sshKey))
		Expect(err).NotTo(HaveOccurred())
		Expect(externalMachine.Address()).To(Equal(virtualMachineInstance.Status.Interfaces[0].IP))
	})

	It("IsReady should return true", func() {
		externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte(sshKey))
		Expect(err).NotTo(HaveOccurred())
		Expect(externalMachine.IsReady()).To(BeTrue())
	})

	It("default mode: IsBootstrapped should return true", func() {
		externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte(sshKey))
		Expect(err).NotTo(HaveOccurred())
		Expect(externalMachine.IsBootstrapped()).To(BeTrue())
	})

	It("ssh mode: IsBootstrapped return true", func() {
		externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte(sshKey))
		externalMachine.machineContext.KubevirtMachine.Spec.BootstrapCheckSpec.CheckStrategy = "ssh"
		Expect(err).NotTo(HaveOccurred())
		Expect(externalMachine.IsBootstrapped()).To(BeTrue())
	})

	It("none mode: IsBootstrapped should be forced to be true", func() {
		externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte(sshKey))
		externalMachine.machineContext.KubevirtMachine.Spec.BootstrapCheckSpec.CheckStrategy = "none"
		Expect(err).NotTo(HaveOccurred())
		Expect(externalMachine.IsBootstrapped()).To(BeTrue())
	})

	It("invalid mode: IsBootstrapped should return false", func() {
		externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte(sshKey))
		externalMachine.machineContext.KubevirtMachine.Spec.BootstrapCheckSpec.CheckStrategy = "impossible-invalid-input"
		Expect(err).NotTo(HaveOccurred())
		Expect(externalMachine.IsBootstrapped()).To(BeFalse())
	})

	It("SupportsCheckingIsBootstrapped should return true", func() {
		externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte(sshKey))
		Expect(err).NotTo(HaveOccurred())
		Expect(externalMachine.SupportsCheckingIsBootstrapped()).To(BeTrue())
	})

	It("GenerateProviderID should succeed", func() {
		expectedProviderId := fmt.Sprintf("kubevirt://%s", kubevirtMachineName)

		externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte(sshKey))
		Expect(err).NotTo(HaveOccurred())
		providerId, err := externalMachine.GenerateProviderID()
		Expect(err).ToNot(HaveOccurred())
		Expect(providerId).To(Equal(expectedProviderId))
	})

	It("Delete should succeed", func() {
		externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte{})
		Expect(err).NotTo(HaveOccurred())
		validateVMExist(virtualMachine, fakeClient, machineContext)

		Expect(externalMachine.Delete()).To(Succeed())
		validateVMNotExist(virtualMachine, fakeClient, machineContext)
	})
})

var _ = Describe("util functions", func() {
	var machineContext *context.MachineContext

	BeforeEach(func() {
		machineContext = &context.MachineContext{
			Context:             gocontext.TODO(),
			Cluster:             cluster,
			KubevirtCluster:     kubevirtCluster,
			Machine:             machine,
			KubevirtMachine:     kubevirtMachine.DeepCopy(),
			BootstrapDataSecret: bootstrapDataSecret,
			Logger:              logger,
		}
	})

	It("GenerateProviderID should succeed", func() {
		dataVolumeTemplates := []kubevirtv1.DataVolumeTemplateSpec{
			{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      map[string]string{"my": "label"},
					Annotations: map[string]string{"my": "annotation"},
					Name:        "dv1",
				},
			},
		}
		volumes := []kubevirtv1.Volume{
			{
				Name: "test1",
				VolumeSource: kubevirtv1.VolumeSource{
					DataVolume: &kubevirtv1.DataVolumeSource{
						Name: "dv1",
					},
				},
			},
		}

		machineContext.KubevirtMachine.Spec.VirtualMachineTemplate.Spec.DataVolumeTemplates = dataVolumeTemplates
		machineContext.KubevirtMachine.Spec.VirtualMachineTemplate.Spec.Template.Spec.Volumes = volumes

		newVM := newVirtualMachineFromKubevirtMachine(machineContext, "default")

		Expect(newVM.Spec.DataVolumeTemplates[0].ObjectMeta.Name).To(Equal(kubevirtMachineName + "-dv1"))
		Expect(newVM.Spec.Template.Spec.Volumes[0].VolumeSource.DataVolume.Name).To(Equal(kubevirtMachineName + "-dv1"))
	})
})

var _ = Describe("with dataVolumes", func() {
	var machineContext *context.MachineContext
	namespace := kubevirtMachine.Namespace
	virtualMachineInstance := testing.NewVirtualMachineInstance(kubevirtMachine)
	virtualMachine := testing.NewVirtualMachine(virtualMachineInstance)
	dataVolume := &cdiv1.DataVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "dv-name",
			Namespace: namespace,
		},
	}

	BeforeEach(func() {
		kubevirtMachine.Spec.BootstrapCheckSpec = v1alpha1.VirtualMachineBootstrapCheckSpec{}

		machineContext = &context.MachineContext{
			Context:             gocontext.TODO(),
			Cluster:             cluster,
			KubevirtCluster:     kubevirtCluster,
			Machine:             machine,
			KubevirtMachine:     kubevirtMachine,
			BootstrapDataSecret: bootstrapDataSecret,
			Logger:              logger,
		}

		virtualMachine.Spec.DataVolumeTemplates = []kubevirtv1.DataVolumeTemplateSpec{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "dv-name"},
			},
		}

		if virtualMachine.Spec.Template == nil {
			virtualMachine.Spec.Template = &kubevirtv1.VirtualMachineInstanceTemplateSpec{}
		}
		virtualMachine.Spec.Template.Spec.Volumes = []kubevirtv1.Volume{
			{
				Name: "dv-disk",
				VolumeSource: kubevirtv1.VolumeSource{
					DataVolume: &kubevirtv1.DataVolumeSource{
						Name: "dv-name",
					},
				},
			},
		}

		fakeVMCommandExecutor = FakeVMCommandExecutor{true}
	})
	JustBeforeEach(func() {
		objects := []client.Object{
			cluster,
			kubevirtCluster,
			machine,
			kubevirtMachine,
			virtualMachineInstance,
			virtualMachine,
			dataVolume,
		}
		fakeClient = fake.NewClientBuilder().WithScheme(testing.SetupScheme()).WithObjects(objects...).Build()
	})

	It("NewMachine should have all client, machineContext and vmiInstance NOT nil", func() {
		externalMachine, err := defaultTestMachine(machineContext, namespace, fakeClient, fakeVMCommandExecutor, []byte(sshKey))
		Expect(err).NotTo(HaveOccurred())
		Expect(externalMachine.client).ToNot(BeNil())
		Expect(externalMachine.machineContext).To(Equal(machineContext))
		Expect(externalMachine.vmiInstance).ToNot(BeNil())
		Expect(externalMachine.dataVolumes).To(HaveLen(1))
		Expect(externalMachine.dataVolumes[0].Name).To(Equal(dataVolume.Name))
	})
})

var _ = Describe("check GetVMNotReadyReason", func() {
	DescribeTable("not-ready reason", func(vm *kubevirtv1.VirtualMachine, dv *cdiv1.DataVolume, expectedReason, expectedMsg string) {
		m := Machine{
			vmInstance: vm,
		}

		if dv != nil {
			m.dataVolumes = []*cdiv1.DataVolume{dv}
		}

		reason, msg := m.GetVMNotReadyReason()
		Expect(reason).To(Equal(expectedReason))
		Expect(msg).To(ContainSubstring(expectedMsg))
	},
		Entry("no vm instance", nil, nil, defaultCondReason, defaultCondMessage),
		Entry("no vm conditions", &kubevirtv1.VirtualMachine{}, nil, defaultCondReason, defaultCondMessage),
		Entry("vm PodScheduled condition is true", &kubevirtv1.VirtualMachine{
			Status: kubevirtv1.VirtualMachineStatus{
				Conditions: []kubevirtv1.VirtualMachineCondition{
					{
						Type:   kubevirtv1.VirtualMachineConditionType(corev1.PodScheduled),
						Status: corev1.ConditionTrue,
					},
				},
			},
		}, nil, defaultCondReason, defaultCondMessage),
		Entry("vm PodScheduled condition is false, with unknown reason", &kubevirtv1.VirtualMachine{
			Status: kubevirtv1.VirtualMachineStatus{
				Conditions: []kubevirtv1.VirtualMachineCondition{
					{
						Type:   kubevirtv1.VirtualMachineConditionType(corev1.PodScheduled),
						Status: corev1.ConditionFalse,
						Reason: "somethingElse",
					},
				},
			},
		}, nil, defaultCondReason, defaultCondMessage),
		Entry("vm PodScheduled condition is false, with 'Unschedulable' reason", &kubevirtv1.VirtualMachine{
			Status: kubevirtv1.VirtualMachineStatus{
				Conditions: []kubevirtv1.VirtualMachineCondition{
					{
						Type:    kubevirtv1.VirtualMachineConditionType(corev1.PodScheduled),
						Status:  corev1.ConditionFalse,
						Reason:  "Unschedulable",
						Message: "test message",
					},
				},
			},
		}, nil, "Unschedulable", "test message"),
		Entry("dv with Running condition; phase = Succeeded", &kubevirtv1.VirtualMachine{}, &cdiv1.DataVolume{
			Status: cdiv1.DataVolumeStatus{
				Phase: cdiv1.Succeeded,
			},
		}, defaultCondReason, defaultCondMessage),
		Entry("dv with Running condition; phase = Pending", &kubevirtv1.VirtualMachine{}, &cdiv1.DataVolume{

			Status: cdiv1.DataVolumeStatus{
				Phase: cdiv1.Pending,
			},
		}, "DVPending", "is not ready; Phase: Pending"),
		Entry("dv with Running condition; phase = Failed", &kubevirtv1.VirtualMachine{}, &cdiv1.DataVolume{
			Status: cdiv1.DataVolumeStatus{
				Phase: cdiv1.Failed,
			},
		}, "DVFailed", "is not ready; Phase: Failed"),
		Entry("dv with Running condition; phase is something else; Running condition true", &kubevirtv1.VirtualMachine{}, &cdiv1.DataVolume{
			Status: cdiv1.DataVolumeStatus{
				Phase: cdiv1.ImportInProgress,
				Conditions: []cdiv1.DataVolumeCondition{
					{
						Type:   cdiv1.DataVolumeRunning,
						Status: corev1.ConditionTrue,
					},
				},
			},
		}, "DVNotReady", "is not ready; Phase: ImportInProgress"),
		Entry("dv with Running condition; phase is something else; Running condition false; reason=Completed", &kubevirtv1.VirtualMachine{}, &cdiv1.DataVolume{
			Status: cdiv1.DataVolumeStatus{
				Phase: cdiv1.ImportInProgress,
				Conditions: []cdiv1.DataVolumeCondition{
					{
						Type:   cdiv1.DataVolumeRunning,
						Status: corev1.ConditionFalse,
						Reason: "Completed",
					},
				},
			},
		}, "DVNotReady", "import is not running"),
		Entry("dv with Running condition; phase is something else; Running condition false; reason!=Completed", &kubevirtv1.VirtualMachine{}, &cdiv1.DataVolume{
			Status: cdiv1.DataVolumeStatus{
				Phase: cdiv1.ImportInProgress,
				Conditions: []cdiv1.DataVolumeCondition{
					{
						Type:    cdiv1.DataVolumeRunning,
						Status:  corev1.ConditionFalse,
						Reason:  "SomethingElse",
						Message: "test message",
					},
				},
			},
		}, "DVNotReady", "test message"),
		Entry("dv with Running condition; phase is something else; no Running condition", &kubevirtv1.VirtualMachine{}, &cdiv1.DataVolume{
			Status: cdiv1.DataVolumeStatus{
				Phase:      cdiv1.ImportInProgress,
				Conditions: []cdiv1.DataVolumeCondition{},
			},
		}, "DVNotReady", "is not ready; Phase: ImportInProgress"),
		Entry("dv with Running condition and reason = 'ImagePullFailed'", &kubevirtv1.VirtualMachine{}, &cdiv1.DataVolume{
			Status: cdiv1.DataVolumeStatus{
				Phase: cdiv1.ImportInProgress,
				Conditions: []cdiv1.DataVolumeCondition{
					{
						Type:    cdiv1.DataVolumeRunning,
						Status:  corev1.ConditionFalse,
						Reason:  "ImagePullFailed",
						Message: "test message",
					},
				},
			},
		}, "DVImagePullFailed", "test message"),
	)
})

func validateVMNotExist(expected *kubevirtv1.VirtualMachine, fakeClient client.Client, machineContext *context.MachineContext) {
	vm := &kubevirtv1.VirtualMachine{}
	key := client.ObjectKey{Name: expected.Name, Namespace: expected.Namespace}

	err := fakeClient.Get(machineContext.Context, key, vm)
	ExpectWithOffset(1, err).To(HaveOccurred())
	ExpectWithOffset(1, apierrors.IsNotFound(err)).To(BeTrue())
}

func validateVMExist(expected *kubevirtv1.VirtualMachine, fakeClient client.Client, machineContext *context.MachineContext) {
	vm := &kubevirtv1.VirtualMachine{}
	key := client.ObjectKey{Name: expected.Name, Namespace: expected.Namespace}

	ExpectWithOffset(1, fakeClient.Get(machineContext.Context, key, vm)).To(Succeed())
	Expect(vm.Name).To(Equal(expected.Name))
	Expect(vm.Namespace).To(Equal(expected.Namespace))
}

type FakeVMCommandExecutor struct {
	isVMRunning bool
}

// vmExecute runs command inside a VM, via SSH, and returns the command output.
func (e FakeVMCommandExecutor) ExecuteCommand(command string) (string, error) {
	if !e.isVMRunning {
		return "", errors.New("VM not found")
	}

	switch command {
	case "hostname":
		return kubevirtMachineName, nil
	case "cat /run/cluster-api/bootstrap-success.complete":
		return "success", nil
	default:
		return "", errors.New("unexpected input argument")
	}
}

func defaultTestMachine(ctx *context.MachineContext, namespace string, client client.Client, vmExecutor FakeVMCommandExecutor, sshPubKey []byte) (*Machine, error) {

	machine, err := NewMachine(ctx, client, namespace, &ssh.ClusterNodeSshKeys{PublicKey: sshPubKey})

	machine.getCommandExecutor = func(fake string, fakeKeys *ssh.ClusterNodeSshKeys) ssh.VMCommandExecutor {
		return vmExecutor
	}

	return machine, err
}

func setupRemoteScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		panic(err)
	}
	return s
}
