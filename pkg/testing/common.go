package testing

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubevirtv1 "kubevirt.io/api/core/v1"
	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha4"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1alpha4"
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
		Spec:   infrav1.KubevirtMachineSpec{},
		Status: infrav1.KubevirtMachineStatus{},
	}
}

func NewMachine(clusterName, machineName string, kubevirtMachine *infrav1.KubevirtMachine) *clusterv1.Machine {
	machine := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name: machineName,
			Labels: map[string]string{
				clusterv1.ClusterLabelName: clusterName,
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
