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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubevirtv1 "kubevirt.io/api/core/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha1"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/context"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/ssh"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/testing"
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

	virtualMachineInstance = testing.NewVirtualMachineInstance(kubevirtMachine)
	virtualMachine         = testing.NewVirtualMachine(virtualMachineInstance)

	bootstrapDataSecret = testing.NewBootstrapDataSecret([]byte(fmt.Sprintf("#cloud-config\n\n%s\n", sshKey)))

	logger = zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)).WithName("machine_test")

	fakeClient            client.Client
	fakeVMCommandExecutor FakeVMCommandExecutor
)

var _ = Describe("Without KubeVirt VM running", func() {
	var machineContext *context.MachineContext

	BeforeEach(func() {
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

		fakeClient = fake.NewClientBuilder().WithScheme(setupScheme()).WithObjects(objects...).Build()

		fakeVMCommandExecutor = FakeVMCommandExecutor{false}
	})

	AfterEach(func() {})

	It("NewMachine should have client and machineContext set, but vmiInstance equal nil", func() {
		externalMachine, err := defaultTestMachine(machineContext, fakeClient, fakeVMCommandExecutor, []byte{})
		Expect(err).NotTo(HaveOccurred())
		Expect(externalMachine.client).To(Equal(fakeClient))
		Expect(externalMachine.machineContext).To(Equal(machineContext))
		Expect(externalMachine.vmiInstance).To(BeNil())
	})

	It("Exists should return false", func() {
		externalMachine, err := defaultTestMachine(machineContext, fakeClient, fakeVMCommandExecutor, []byte{})
		Expect(err).NotTo(HaveOccurred())
		Expect(externalMachine.Exists()).To(BeFalse())
	})

	It("Address should return ''", func() {
		externalMachine, err := defaultTestMachine(machineContext, fakeClient, fakeVMCommandExecutor, []byte{})
		Expect(err).NotTo(HaveOccurred())
		Expect(externalMachine.Address()).To(Equal(""))
	})

	It("IsReady should return false", func() {
		externalMachine, err := defaultTestMachine(machineContext, fakeClient, fakeVMCommandExecutor, []byte{})
		Expect(err).NotTo(HaveOccurred())
		Expect(externalMachine.IsReady()).To(BeFalse())
	})

	It("IsBootstrapped should return false", func() {
		externalMachine, err := defaultTestMachine(machineContext, fakeClient, fakeVMCommandExecutor, []byte{})
		Expect(err).NotTo(HaveOccurred())
		Expect(externalMachine.IsBootstrapped()).To(BeFalse())
	})

	It("SupportsCheckingIsBootstrapped should return false", func() {
		externalMachine, err := defaultTestMachine(machineContext, fakeClient, fakeVMCommandExecutor, []byte{})
		Expect(err).NotTo(HaveOccurred())
		Expect(externalMachine.SupportsCheckingIsBootstrapped()).To(BeFalse())
	})

	It("GenerateProviderID should fail", func() {
		externalMachine, err := defaultTestMachine(machineContext, fakeClient, fakeVMCommandExecutor, []byte{})
		Expect(err).NotTo(HaveOccurred())
		providerId, err := externalMachine.GenerateProviderID()
		Expect(err).To(HaveOccurred())
		Expect(providerId).To(Equal(""))
	})

	It("Create should create VM, but not VMI", func() {
		externalMachine, err := defaultTestMachine(machineContext, fakeClient, fakeVMCommandExecutor, []byte{})
		Expect(err).NotTo(HaveOccurred())

		// read the vm before creation
		validateVMNotExist(fakeClient, machineContext)

		err = externalMachine.Create(machineContext.Context)
		Expect(err).NotTo(HaveOccurred())

		// read the vm before creation
		validateVMExist(fakeClient, machineContext)
	})

	It("Create should create VM if it doesn't exist", func() {
		externalMachine, err := defaultTestMachine(machineContext, fakeClient, fakeVMCommandExecutor, []byte{})
		Expect(err).NotTo(HaveOccurred())

		// read the vm before creation
		validateVMNotExist(fakeClient, machineContext)

		err = externalMachine.Create(machineContext.Context)
		Expect(err).NotTo(HaveOccurred())

		// read the new created vm
		validateVMExist(fakeClient, machineContext)
	})
})

var _ = Describe("With KubeVirt VM running", func() {
	var machineContext *context.MachineContext

	BeforeEach(func() {
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

		fakeClient = fake.NewClientBuilder().WithScheme(setupScheme()).WithObjects(objects...).Build()

		fakeVMCommandExecutor = FakeVMCommandExecutor{true}
	})

	AfterEach(func() {})

	It("NewMachine should have all client, machineContext and vmiInstance NOT nil", func() {
		externalMachine, err := defaultTestMachine(machineContext, fakeClient, fakeVMCommandExecutor, []byte(sshKey))
		Expect(err).NotTo(HaveOccurred())
		Expect(externalMachine.client).ToNot(BeNil())
		Expect(externalMachine.machineContext).To(Equal(machineContext))
		Expect(externalMachine.vmiInstance).ToNot(BeNil())
	})

	It("Exists should return true", func() {
		externalMachine, err := defaultTestMachine(machineContext, fakeClient, fakeVMCommandExecutor, []byte(sshKey))
		Expect(err).NotTo(HaveOccurred())
		Expect(externalMachine.Exists()).To(BeTrue())
	})

	It("Address should return non-empty IP", func() {
		externalMachine, err := defaultTestMachine(machineContext, fakeClient, fakeVMCommandExecutor, []byte(sshKey))
		Expect(err).NotTo(HaveOccurred())
		Expect(externalMachine.Address()).To(Equal(virtualMachineInstance.Status.Interfaces[0].IP))
	})

	It("IsReady should return true", func() {
		externalMachine, err := defaultTestMachine(machineContext, fakeClient, fakeVMCommandExecutor, []byte(sshKey))
		Expect(err).NotTo(HaveOccurred())
		Expect(externalMachine.IsReady()).To(BeTrue())
	})

	It("IsBootstrapped should return true", func() {
		externalMachine, err := defaultTestMachine(machineContext, fakeClient, fakeVMCommandExecutor, []byte(sshKey))
		Expect(err).NotTo(HaveOccurred())
		Expect(externalMachine.IsBootstrapped()).To(BeTrue())
	})

	It("SupportsCheckingIsBootstrapped should return true", func() {
		externalMachine, err := defaultTestMachine(machineContext, fakeClient, fakeVMCommandExecutor, []byte(sshKey))
		Expect(err).NotTo(HaveOccurred())
		Expect(externalMachine.SupportsCheckingIsBootstrapped()).To(BeTrue())
	})

	It("GenerateProviderID should succeed", func() {
		expectedProviderId := fmt.Sprintf("kubevirt://%s", kubevirtMachineName)

		externalMachine, err := defaultTestMachine(machineContext, fakeClient, fakeVMCommandExecutor, []byte(sshKey))
		Expect(err).NotTo(HaveOccurred())
		providerId, err := externalMachine.GenerateProviderID()
		Expect(err).ToNot(HaveOccurred())
		Expect(providerId).To(Equal(expectedProviderId))
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

func validateVMNotExist(fakeClient client.Client, machineContext *context.MachineContext) {
	vm := &kubevirtv1.VirtualMachine{}
	key := client.ObjectKey{Name: virtualMachineInstance.Name, Namespace: virtualMachineInstance.Namespace}

	err := fakeClient.Get(machineContext.Context, key, vm)
	ExpectWithOffset(1, err).To(HaveOccurred())
	ExpectWithOffset(1, apierrors.IsNotFound(err)).To(BeTrue())
}

func validateVMExist(fakeClient client.Client, machineContext *context.MachineContext) {
	vm := &kubevirtv1.VirtualMachine{}
	key := client.ObjectKey{Name: virtualMachineInstance.Name, Namespace: virtualMachineInstance.Namespace}

	err := fakeClient.Get(machineContext.Context, key, vm)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	Expect(vm.Name).To(Equal(virtualMachineInstance.Name))
	Expect(vm.Namespace).To(Equal(virtualMachineInstance.Namespace))
}

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

func defaultTestMachine(ctx *context.MachineContext, client client.Client, vmExecutor FakeVMCommandExecutor, sshPubKey []byte) (*Machine, error) {

	machine, err := NewMachine(ctx, client, ctx.Cluster.Namespace, &ssh.ClusterNodeSshKeys{PublicKey: sshPubKey})

	machine.getCommandExecutor = func(fake string, fakeKeys *ssh.ClusterNodeSshKeys) ssh.VMCommandExecutor {
		return vmExecutor
	}

	return machine, err
}
