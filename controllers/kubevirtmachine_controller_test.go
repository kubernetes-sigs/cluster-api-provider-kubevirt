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
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubevirtv1 "kubevirt.io/api/core/v1"
	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha4"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1alpha4"
)

var (
	clusterName         string
	kubevirtClusterName string
	kubevirtCluster     *infrav1.KubevirtCluster
	cluster             *clusterv1.Cluster

	machineName         string
	kubevirtMachineName string
	kubevirtMachine     *infrav1.KubevirtMachine
	machine             *clusterv1.Machine

	anotherMachineName         string
	anotherKubevirtMachineName string
	anotherKubevirtMachine     *infrav1.KubevirtMachine
	anotherMachine             *clusterv1.Machine

	fakeClient                client.Client
	kubevirtMachineReconciler KubevirtMachineReconciler
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
