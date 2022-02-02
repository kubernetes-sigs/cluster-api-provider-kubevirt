package e2e_tests_test

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/rand"
	kubevirtv1 "kubevirt.io/api/core/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/kind/pkg/cluster/constants"

	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha1"
	tests "sigs.k8s.io/cluster-api-provider-kubevirt/e2e-tests"
)

var _ = Describe("CreateCluster", func() {

	var tmpDir string
	var k8sclient client.Client
	var manifestsFile string
	var namespace string

	BeforeEach(func() {
		var err error

		Expect(tests.KubectlPath).ToNot(Equal(""))
		Expect(tests.ClusterctlPath).ToNot(Equal(""))
		Expect(tests.WorkingDir).ToNot(Equal(""))

		tmpDir, err = ioutil.TempDir(tests.WorkingDir, "creation-tests")
		Expect(err).ToNot(HaveOccurred())

		manifestsFile = filepath.Join(tmpDir, "manifests.yaml")

		cfg, err := config.GetConfig()
		Expect(err).ToNot(HaveOccurred())
		k8sclient, err = client.New(cfg, client.Options{})
		Expect(err).ToNot(HaveOccurred())

		_ = clusterv1.AddToScheme(k8sclient.Scheme())
		_ = infrav1.AddToScheme(k8sclient.Scheme())
		_ = kubevirtv1.AddToScheme(k8sclient.Scheme())

		namespace = "e2e-test-create-cluster-" + rand.String(6)

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}

		err = k8sclient.Create(context.Background(), ns)
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		defer func() {
			// Best effort cleanup of remaining artifacts by deleting namespace
			By("removing namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			_ = k8sclient.Delete(context.Background(), ns)
			_ = os.RemoveAll(tmpDir)
		}()

		// Typically the machine deployment and machines should not need to get removed before
		// the Cluster object. However we have a bug today that prevents proper tear down
		// of the cluster unless the machines are removed first.
		//
		// Tracking this issue here: https://github.com/kubernetes-sigs/cluster-api-provider-kubevirt/issues/65
		// TODO remove the logic that delets the machine and kubevirt machines once this issue is resolved.
		By("removing machine deployment")
		machineDeployment := &clusterv1.MachineDeployment{}
		key := client.ObjectKey{Namespace: namespace, Name: "kvcluster-md-0"}
		tests.DeleteAndWait(k8sclient, machineDeployment, key, 120)

		By("removing all kubevirt machines")
		machineList := &infrav1.KubevirtMachineList{}
		err := k8sclient.List(context.Background(), machineList, client.InNamespace(namespace))
		Expect(err).ToNot(HaveOccurred())

		for _, machine := range machineList.Items {
			key := client.ObjectKey{Namespace: namespace, Name: machine.Name}
			tests.DeleteAndWait(k8sclient, &machine, key, 120)
		}

		By("removing cluster")
		cluster := &clusterv1.Cluster{}
		key = client.ObjectKey{Namespace: namespace, Name: "kvcluster"}
		tests.DeleteAndWait(k8sclient, cluster, key, 120)

	})

	waitForBootstrappedMachines := func() {
		Eventually(func() error {
			machineList := &infrav1.KubevirtMachineList{}
			err := k8sclient.List(context.Background(), machineList, client.InNamespace(namespace))
			if err != nil {
				return err
			}

			for _, machine := range machineList.Items {
				if !conditions.IsTrue(&machine, infrav1.BootstrapExecSucceededCondition) {
					return fmt.Errorf("still waiting on a kubevirt machine with bootstrap succeeded condition")
				}
			}
			return nil
		}, 5*time.Minute, 5*time.Second).Should(Succeed(), "kubevirt machines should have bootstrap succeeded condition")

	}

	markExternalKubeVirtClusterReady := func(clusterName string, namespace string) {
		By("Ensuring no other controller is managing the kvcluster's status")
		Consistently(func() error {
			kvCluster := &infrav1.KubevirtCluster{}
			key := client.ObjectKey{Namespace: namespace, Name: clusterName}
			err := k8sclient.Get(context.Background(), key, kvCluster)
			Expect(err).ToNot(HaveOccurred())

			Expect(kvCluster.Status.Ready).To(BeFalse())
			Expect(kvCluster.Status.FailureDomains).To(BeEmpty())
			Expect(kvCluster.Status.Conditions).To(BeEmpty())

			return nil
		}, 30*time.Second, 5*time.Second).Should(Succeed())

		By("Setting creating load balancer on kvcluster object")

		lbService := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      clusterName + "-lb",
			},
			Spec: corev1.ServiceSpec{
				Type: corev1.ServiceTypeClusterIP,
				Ports: []corev1.ServicePort{
					{
						Port:       6443,
						Protocol:   corev1.ProtocolTCP,
						TargetPort: intstr.FromInt(6443),
					},
				},
				Selector: map[string]string{
					"cluster.x-k8s.io/role":         constants.ControlPlaneNodeRoleValue,
					"cluster.x-k8s.io/cluster-name": clusterName,
				},
			},
		}
		err := k8sclient.Create(context.Background(), lbService)
		Expect(err).ToNot(HaveOccurred())

		By("getting IP of load balancer")

		lbIP := ""
		Eventually(func() error {
			updatedLB := &corev1.Service{}
			key := client.ObjectKey{Namespace: namespace, Name: lbService.Name}
			err := k8sclient.Get(context.Background(), key, updatedLB)
			if err != nil {
				return err
			}

			if len(updatedLB.Spec.ClusterIP) == 0 {
				return fmt.Errorf("still waiting on lb ip")
			}

			lbIP = updatedLB.Spec.ClusterIP
			return nil
		}, 30*time.Second, 5*time.Second).Should(Succeed(), "lb should have provided an ip")

		By("Setting ready=true on kvcluster object")
		kvCluster := &infrav1.KubevirtCluster{}
		key := client.ObjectKey{Namespace: namespace, Name: clusterName}
		err = k8sclient.Get(context.Background(), key, kvCluster)
		Expect(err).ToNot(HaveOccurred())

		kvCluster.Spec.ControlPlaneEndpoint = infrav1.APIEndpoint{
			Host: lbIP,
			Port: 6443,
		}
		err = k8sclient.Update(context.Background(), kvCluster)
		Expect(err).ToNot(HaveOccurred())

		conditions.MarkTrue(kvCluster, infrav1.LoadBalancerAvailableCondition)
		kvCluster.Status.Ready = true

		err = k8sclient.Status().Update(context.Background(), kvCluster)
		Expect(err).ToNot(HaveOccurred())
		Expect(kvCluster.Status.Ready).To(BeTrue())
	}

	waitForMachineReadiness := func(numExpectedReady int, numExpectedNotReady int) {
		Eventually(func() error {

			readyCount := 0
			notReadyCount := 0

			machineList := &infrav1.KubevirtMachineList{}
			err := k8sclient.List(context.Background(), machineList, client.InNamespace(namespace))
			if err != nil {
				return err
			}

			for _, machine := range machineList.Items {
				if machine.Status.Ready {
					readyCount++
				} else {
					notReadyCount++
				}
			}

			if readyCount != numExpectedReady {
				return fmt.Errorf("Expected %d ready, but got %d", numExpectedReady, readyCount)
			} else if notReadyCount != numExpectedNotReady {
				return fmt.Errorf("Expected %d not ready, but got %d", numExpectedNotReady, notReadyCount)
			}

			return nil
		}, 5*time.Minute, 5*time.Second).Should(Succeed(), "waiting for expected readiness.")
	}

	waitForNodeUpdate := func() {
		Eventually(func() error {
			machineList := &infrav1.KubevirtMachineList{}
			err := k8sclient.List(context.Background(), machineList, client.InNamespace(namespace))
			if err != nil {
				return err
			}

			for _, machine := range machineList.Items {
				if !machine.Status.NodeUpdated {
					return fmt.Errorf("Still waiting on machine %s to have node provider id updated", machine.Name)
				}
			}

			return nil
		}, 5*time.Minute, 5*time.Second).Should(Succeed(), "waiting for expected readiness.")
	}

	waitForControlPlane := func() {
		By("Waiting on cluster's control plane to initialize")
		Eventually(func() error {
			cluster := &clusterv1.Cluster{}
			key := client.ObjectKey{Namespace: namespace, Name: "kvcluster"}
			err := k8sclient.Get(context.Background(), key, cluster)
			if err != nil {
				return err
			}

			if !conditions.IsTrue(cluster, clusterv1.ControlPlaneInitializedCondition) {
				return fmt.Errorf("still waiting on controlPlaneInitialized condition to be true")
			}

			return nil
		}, 10*time.Minute, 5*time.Second).Should(Succeed(), "cluster should have control plane initialized")

		By("Waiting on cluster's control plane to be ready")
		Eventually(func() error {
			cluster := &clusterv1.Cluster{}
			key := client.ObjectKey{Namespace: namespace, Name: "kvcluster"}
			err := k8sclient.Get(context.Background(), key, cluster)
			if err != nil {
				return err
			}

			if !conditions.IsTrue(cluster, clusterv1.ControlPlaneReadyCondition) {
				return fmt.Errorf("still waiting on controlPlaneInitialized condition to be true")
			}

			return nil
		}, 10*time.Minute, 5*time.Second).Should(Succeed(), "cluster should have control plane initialized")
	}

	injectKubevirtClusterExternallyManagedAnnotation := func(yamlStr string) string {
		strs := strings.Split(yamlStr, "\n")

		labelInjection := `  annotations:
    cluster.x-k8s.io/managed-by: external
`
		newString := ""
		for i, s := range strs {
			if strings.Contains(s, "metadata:") && i > 0 && strings.Contains(strs[i-1], "kind: KubevirtCluster") {
				newString = newString + s + "\n" + labelInjection
			} else {
				newString = newString + s + "\n"
			}
		}

		return newString
	}

	It("creating a simple cluster with ephemeral VMs", func() {
		By("generating cluster manifests from example template")
		cmd := exec.Command(tests.ClusterctlPath, "generate", "cluster", "kvcluster", "--target-namespace", namespace, "--kubernetes-version", "v1.21.0", "--control-plane-machine-count=1", "--worker-machine-count=1", "--from", "templates/cluster-template.yaml")
		cmd.Env = append(os.Environ(),
			"NODE_VM_IMAGE_TEMPLATE=quay.io/kubevirtci/fedora-kubeadm:35",
			"IMAGE_REPO=k8s.gcr.io",
			"CRI_PATH=/var/run/crio/crio.sock",
		)
		stdout, _ := tests.RunCmd(cmd)
		err := os.WriteFile(manifestsFile, stdout, 0644)
		Expect(err).ToNot(HaveOccurred())

		By("posting cluster manifests example template")
		cmd = exec.Command(tests.KubectlPath, "apply", "-f", manifestsFile)
		tests.RunCmd(cmd)

		By("Waiting for control plane")
		waitForControlPlane()

		By("Waiting on kubevirt machines to bootstrap")
		waitForBootstrappedMachines()

		By("Waiting on kubevirt machines to be ready")
		waitForMachineReadiness(2, 0)
	})

	It("creating a simple externally managed cluster ephemeral VMs", func() {
		By("generating cluster manifests from example template")
		cmd := exec.Command(tests.ClusterctlPath, "generate", "cluster", "kvcluster", "--target-namespace", namespace, "--kubernetes-version", "v1.21.0", "--control-plane-machine-count=1", "--worker-machine-count=1", "--from", "templates/cluster-template.yaml")
		cmd.Env = append(os.Environ(),
			"NODE_VM_IMAGE_TEMPLATE=quay.io/kubevirtci/fedora-kubeadm:35",
			"IMAGE_REPO=k8s.gcr.io",
			"CRI_PATH=/var/run/crio/crio.sock",
		)
		stdout, _ := tests.RunCmd(cmd)

		modifiedStdOut := injectKubevirtClusterExternallyManagedAnnotation(string(stdout))

		err := os.WriteFile(manifestsFile, []byte(modifiedStdOut), 0644)
		Expect(err).ToNot(HaveOccurred())

		By("posting cluster manifests example template")
		cmd = exec.Command(tests.KubectlPath, "apply", "-f", manifestsFile)
		tests.RunCmd(cmd)

		By("marking kubevirt cluster as ready to imitate an externally managed cluster")
		markExternalKubeVirtClusterReady("kvcluster", namespace)

		By("Waiting for control plane")
		waitForControlPlane()

		By("Waiting on kubevirt machines to be ready")
		waitForMachineReadiness(2, 0)

		By("Waiting for all tenant nodes to get provider id")
		waitForNodeUpdate()
	})

	It("creating a simple cluster with persistent VMs", func() {
		By("generating cluster manifests from example template")
		cmd := exec.Command(tests.ClusterctlPath, "generate", "cluster", "kvcluster", "--target-namespace", namespace, "--kubernetes-version", "v1.21.0", "--control-plane-machine-count=1", "--worker-machine-count=1", "--from", "templates/cluster-template-persistent-storage.yaml")
		cmd.Env = append(os.Environ(),
			"NODE_VM_IMAGE_TEMPLATE=quay.io/kubevirtci/fedora-kubeadm:35",
			"IMAGE_REPO=k8s.gcr.io",
			"CRI_PATH=/var/run/crio/crio.sock",
		)
		stdout, _ := tests.RunCmd(cmd)
		err := os.WriteFile(manifestsFile, stdout, 0644)
		Expect(err).ToNot(HaveOccurred())

		By("posting cluster manifests example template")
		cmd = exec.Command(tests.KubectlPath, "apply", "-f", manifestsFile)
		tests.RunCmd(cmd)

		By("Waiting for control plane")
		waitForControlPlane()

		By("Waiting on kubevirt machines to bootstrap")
		waitForBootstrappedMachines()

		By("Waiting on kubevirt machines to be ready")
		waitForMachineReadiness(2, 0)

		By("Selecting a worker node to restart")
		vmiList := &kubevirtv1.VirtualMachineInstanceList{}
		err = k8sclient.List(context.Background(), vmiList, client.InNamespace(namespace))
		Expect(err).ToNot(HaveOccurred())

		var chosenVMI *kubevirtv1.VirtualMachineInstance
		for _, vmi := range vmiList.Items {
			if strings.Contains(vmi.Name, "-md-0") {
				chosenVMI = &vmi
				break
			}
		}
		Expect(chosenVMI).ToNot(BeNil())

		By(fmt.Sprintf("By restarting worker node hosted in vmi %s", chosenVMI.Name))
		err = k8sclient.Delete(context.Background(), chosenVMI)
		Expect(err).ToNot(HaveOccurred())

		By("Expecting a KubevirtMachine to revert back to ready=false while VM restarts")
		waitForMachineReadiness(1, 1)

		By("Expecting both KubevirtMachines stabilize to a ready=true again.")
		waitForMachineReadiness(2, 0)
	})
})
