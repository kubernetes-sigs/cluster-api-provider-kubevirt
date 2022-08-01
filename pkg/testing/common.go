package testing

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubevirtv1 "kubevirt.io/api/core/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"

	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha1"
)

func NewCluster(clusterName string, kubevirtCluster *infrav1.KubevirtCluster) *clusterv1.Cluster {
	cluster := &clusterv1.Cluster{
		TypeMeta: metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{
			Name: clusterName,
		},
	}
	if kubevirtCluster != nil {
		cluster.Spec.InfrastructureRef = &corev1.ObjectReference{
			Name:       kubevirtCluster.Name,
			Namespace:  kubevirtCluster.Namespace,
			Kind:       kubevirtCluster.Kind,
			APIVersion: kubevirtCluster.GroupVersionKind().GroupVersion().String(),
		}
	}
	return cluster
}

func NewKubevirtCluster(clusterName, kubevirtName string) *infrav1.KubevirtCluster {
	return &infrav1.KubevirtCluster{
		TypeMeta: metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{
			Name: kubevirtName,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: clusterv1.GroupVersion.String(),
					Kind:       "Cluster",
					Name:       clusterName,
				},
			},
		},
	}
}

func NewKubevirtMachine(kubevirtMachineName, machineName string) *infrav1.KubevirtMachine {
	return &infrav1.KubevirtMachine{
		TypeMeta: metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{
			Name:            kubevirtMachineName,
			ResourceVersion: "1",
			Finalizers:      []string{infrav1.MachineFinalizer},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: clusterv1.GroupVersion.String(),
					Kind:       "Machine",
					Name:       machineName,
				},
			},
		},
		Spec: infrav1.KubevirtMachineSpec{
			VirtualMachineTemplate: infrav1.VirtualMachineTemplateSpec{

				Spec: kubevirtv1.VirtualMachineSpec{
					Template: &kubevirtv1.VirtualMachineInstanceTemplateSpec{},
				},
			},
		},
		Status: infrav1.KubevirtMachineStatus{},
	}
}

func NewMachine(clusterName, machineName string, kubevirtMachine *infrav1.KubevirtMachine) *clusterv1.Machine {
	fakeDataSecretName := "fakeDataSecretName"
	machine := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name: machineName,
			Labels: map[string]string{
				clusterv1.ClusterLabelName: clusterName,
			},
		},
		Spec: clusterv1.MachineSpec{
			Bootstrap: clusterv1.Bootstrap{
				DataSecretName: &fakeDataSecretName,
			},
		},
	}
	if kubevirtMachine != nil {
		machine.Spec.InfrastructureRef = corev1.ObjectReference{
			Name:       kubevirtMachine.Name,
			Namespace:  kubevirtMachine.Namespace,
			Kind:       kubevirtMachine.Kind,
			APIVersion: kubevirtMachine.GroupVersionKind().GroupVersion().String(),
		}
	}
	return machine
}

func NewVirtualMachineInstance(kubevirtMachine *infrav1.KubevirtMachine) *kubevirtv1.VirtualMachineInstance {
	return &kubevirtv1.VirtualMachineInstance{
		TypeMeta: metav1.TypeMeta{
			Kind:       "VirtualMachineInstance",
			APIVersion: "kubevirt.io/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      kubevirtMachine.Name,
			Namespace: kubevirtMachine.Namespace,
		},
		Status: kubevirtv1.VirtualMachineInstanceStatus{
			Interfaces: []kubevirtv1.VirtualMachineInstanceNetworkInterface{
				{IP: "1.1.1.1"},
			},
		},
	}
}

// NewExternalVirtualMachineInstance instantiates a new external VirtualMachineInstance; i.e. one in a specified
// namespace that might differ from the kubevirtMachine one.
func NewExternalVirtualMachineInstance(kubevirtMachine *infrav1.KubevirtMachine, namespace string) *kubevirtv1.VirtualMachineInstance {
	return &kubevirtv1.VirtualMachineInstance{
		TypeMeta: metav1.TypeMeta{
			Kind:       "VirtualMachineInstance",
			APIVersion: "kubevirt.io/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      kubevirtMachine.Name,
			Namespace: namespace,
		},
		Status: kubevirtv1.VirtualMachineInstanceStatus{
			Interfaces: []kubevirtv1.VirtualMachineInstanceNetworkInterface{
				{IP: "1.1.1.1"},
			},
		},
	}
}

func NewVirtualMachine(vmi *kubevirtv1.VirtualMachineInstance) *kubevirtv1.VirtualMachine {
	return &kubevirtv1.VirtualMachine{
		TypeMeta: metav1.TypeMeta{
			Kind:       "VirtualMachine",
			APIVersion: "kubevirt.io/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      vmi.Name,
			Namespace: vmi.Namespace,
		},
	}
}

func NewBootstrapDataSecret(userData []byte) *corev1.Secret {
	s := &corev1.Secret{}
	s.Data = make(map[string][]byte)
	s.Data["userdata"] = userData
	return s
}

// SetupScheme setups the scheme for a fake client.
func SetupScheme() *runtime.Scheme {
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
