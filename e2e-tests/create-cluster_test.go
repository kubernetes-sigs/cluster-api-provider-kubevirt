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
	"k8s.io/apimachinery/pkg/util/rand"
	kubevirtv1 "kubevirt.io/api/core/v1"
	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha1"
	tests "sigs.k8s.io/cluster-api-provider-kubevirt/e2e-tests"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
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
		Expect(err).To(BeNil())

		manifestsFile = filepath.Join(tmpDir, "manifests.yaml")

		cfg, err := config.GetConfig()
		Expect(err).To(BeNil())
		k8sclient, err = client.New(cfg, client.Options{})
		Expect(err).To(BeNil())

		clusterv1.AddToScheme(k8sclient.Scheme())
		infrav1.AddToScheme(k8sclient.Scheme())
		kubevirtv1.AddToScheme(k8sclient.Scheme())

		namespace = "e2e-test-create-cluster-" + rand.String(6)

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}

		err = k8sclient.Create(context.Background(), ns)
		Expect(err).To(BeNil())
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
			k8sclient.Delete(context.Background(), ns)
			os.RemoveAll(tmpDir)
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
		Expect(err).To(BeNil())

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
		}, 5*time.Minute, 5*time.Second).Should(BeNil(), "kubevirt machines should have bootstrap succeeded condition")

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
		}, 5*time.Minute, 5*time.Second).Should(BeNil(), "waiting for expected readiness.")
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
		}, 10*time.Minute, 5*time.Second).Should(BeNil(), "cluster should have control plane initialized")

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
		}, 10*time.Minute, 5*time.Second).Should(BeNil(), "cluster should have control plane initialized")
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
		Expect(err).To(BeNil())

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
		Expect(err).To(BeNil())

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
		Expect(err).To(BeNil())

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
		Expect(err).To(BeNil())

		By("Expecting a KubevirtMachine to revert back to ready=false while VM restarts")
		waitForMachineReadiness(1, 1)

		By("Expecting both KubevirtMachines stabilize to a ready=true again.")
		waitForMachineReadiness(2, 0)
	})
})
