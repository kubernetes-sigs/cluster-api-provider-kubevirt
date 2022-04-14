package e2e_test

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
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
)

var _ = Describe("CreateCluster", func() {

	var tmpDir string
	var k8sclient client.Client
	var manifestsFile string
	var namespace string

	BeforeEach(func() {
		var err error

		tmpDir, err = ioutil.TempDir(WorkingDir, "creation-tests")
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

			DeleteAndWait(k8sclient, ns, 120)

		}()

		_ = os.RemoveAll(tmpDir)

		By("removing cluster")
		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      "kvcluster",
			},
		}
		DeleteAndWait(k8sclient, cluster, 120)
	})

	waitForBootstrappedMachines := func() {
		Eventually(func() error {
			machineList := &infrav1.KubevirtMachineList{}
			err := k8sclient.List(context.Background(), machineList, client.InNamespace(namespace))
			if err != nil {
				return err
			}

			if len(machineList.Items) == 0 {
				return fmt.Errorf("expecting a non-empty list of machines")
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

			Expect(kvCluster.Finalizers).To(BeEmpty())
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

			if len(machineList.Items) == 0 {
				return fmt.Errorf("expecting a non-empty list of machines")
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

	chooseWorkerVMI := func() *kubevirtv1.VirtualMachineInstance {
		vmiList := &kubevirtv1.VirtualMachineInstanceList{}
		err := k8sclient.List(context.Background(), vmiList, client.InNamespace(namespace))
		Expect(err).ToNot(HaveOccurred())

		var chosenVMI *kubevirtv1.VirtualMachineInstance
		for _, vmi := range vmiList.Items {
			if strings.Contains(vmi.Name, "-md-0") {
				chosenVMI = &vmi
				break
			}
		}
		Expect(chosenVMI).ToNot(BeNil())
		return chosenVMI
	}

	getVMIPod := func(vmi *kubevirtv1.VirtualMachineInstance) *corev1.Pod {

		podList := &corev1.PodList{}
		err := k8sclient.List(context.Background(), podList, client.InNamespace(namespace))
		Expect(err).ToNot(HaveOccurred())

		var chosenPod *corev1.Pod
		for _, pod := range podList.Items {
			if pod.Status.Phase != corev1.PodRunning {
				continue
			}

			vmName, ok := pod.Labels["kubevirt.io/vm"]
			if !ok {
				continue
			} else if vmName == vmi.Name {
				chosenPod = &pod
				break
			}
		}
		Expect(chosenPod).ToNot(BeNil())
		return chosenPod
	}

	waitForRemoval := func(obj client.Object, timeoutSeconds uint) {

		key := client.ObjectKeyFromObject(obj)
		Eventually(func() error {
			err := k8sclient.Get(context.Background(), key, obj)
			if err != nil && k8serrors.IsNotFound(err) {
				return nil
			} else if err != nil {
				return err
			}

			return fmt.Errorf("waiting on object %s to be deleted", key)
		}, time.Duration(timeoutSeconds)*time.Second, 1*time.Second).Should(BeNil())

	}

	postDefaultMHC := func(clusterName string) {
		maxUnhealthy := intstr.FromString("100%")
		mhc := &clusterv1.MachineHealthCheck{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "testmhc",
				Namespace: namespace,
			},
			Spec: clusterv1.MachineHealthCheckSpec{
				ClusterName: clusterName,
				Selector: metav1.LabelSelector{
					MatchLabels: map[string]string{
						"cluster.x-k8s.io/cluster-name": clusterName,
					},
				},
				MaxUnhealthy: &maxUnhealthy,

				UnhealthyConditions: []clusterv1.UnhealthyCondition{
					{
						Type:   corev1.NodeReady,
						Status: corev1.ConditionFalse,
						Timeout: metav1.Duration{
							Duration: 5 * time.Minute,
						},
					},
					{
						Type:   corev1.NodeReady,
						Status: corev1.ConditionUnknown,
						Timeout: metav1.Duration{
							Duration: 5 * time.Minute,
						},
					},
				},
				NodeStartupTimeout: &metav1.Duration{
					Duration: 10 * time.Minute,
				},
			},
		}

		err := k8sclient.Create(context.Background(), mhc)
		Expect(err).ToNot(HaveOccurred())
	}

	It("creates a simple cluster with ephemeral VMs", Label("ephemeralVMs"), func() {
		By("generating cluster manifests from example template")
		cmd := exec.Command(ClusterctlPath, "generate", "cluster", "kvcluster",
			"--target-namespace", namespace,
			"--kubernetes-version", os.Getenv("TENANT_CLUSTER_KUBERNETES_VERSION"),
			"--control-plane-machine-count=1",
			"--worker-machine-count=1",
			"--from", "templates/cluster-template.yaml")
		stdout, _ := RunCmd(cmd)
		err := os.WriteFile(manifestsFile, stdout, 0644)
		Expect(err).ToNot(HaveOccurred())

		By("posting cluster manifests example template")
		cmd = exec.Command(KubectlPath, "apply", "-f", manifestsFile)
		RunCmd(cmd)

		By("Waiting for control plane")
		waitForControlPlane()

		By("Waiting on kubevirt machines to bootstrap")
		waitForBootstrappedMachines()

		By("Waiting on kubevirt machines to be ready")
		waitForMachineReadiness(2, 0)
	})

	It("should remediate a running VMI marked as being in a terminal state", Label("ephemeralVMs"), func() {
		By("generating cluster manifests from example template")
		cmd := exec.Command(ClusterctlPath, "generate", "cluster", "kvcluster",
			"--target-namespace", namespace,
			"--kubernetes-version", os.Getenv("TENANT_CLUSTER_KUBERNETES_VERSION"),
			"--control-plane-machine-count=1",
			"--worker-machine-count=1",
			"--from", "templates/cluster-template.yaml")
		stdout, _ := RunCmd(cmd)
		err := os.WriteFile(manifestsFile, stdout, 0644)
		Expect(err).ToNot(HaveOccurred())

		By("posting cluster manifests example template")
		cmd = exec.Command(KubectlPath, "apply", "-f", manifestsFile)
		RunCmd(cmd)

		By("Waiting for control plane")
		waitForControlPlane()

		By("Waiting on kubevirt machines to bootstrap")
		waitForBootstrappedMachines()

		By("Waiting on kubevirt machines to be ready")
		waitForMachineReadiness(2, 0)

		By("creating machine health check")
		postDefaultMHC("kvcluster")

		// trigger remediation by marking a running VMI as being in a failed state
		By("Selecting a worker node to remediate")
		chosenVMI := chooseWorkerVMI()

		By("Setting terminal state on VMI")
		kvmName, ok := chosenVMI.Labels["capk.cluster.x-k8s.io/kubevirt-machine-name"]
		Expect(ok).To(BeTrue())

		chosenVMI.Labels[infrav1.KubevirtMachineVMTerminalLabel] = "marked-terminal-by-func-test"
		err = k8sclient.Update(context.Background(), chosenVMI)
		Expect(err).ToNot(HaveOccurred())

		By("Wait for KubeVirtMachine is deleted due to remediation")
		chosenKVM := &infrav1.KubevirtMachine{
			ObjectMeta: metav1.ObjectMeta{
				Name:      kvmName,
				Namespace: chosenVMI.Namespace,
			},
		}
		waitForRemoval(chosenKVM, 180)

		By("Waiting on kubevirt new machines to be ready after remediation")
		waitForMachineReadiness(2, 0)

	})

	It("should remediate failed unrecoverable VMI ", Label("ephemeralVMs"), func() {

		By("generating cluster manifests from example template")
		cmd := exec.Command(ClusterctlPath, "generate", "cluster", "kvcluster",
			"--target-namespace", namespace,
			"--kubernetes-version", os.Getenv("TENANT_CLUSTER_KUBERNETES_VERSION"),
			"--control-plane-machine-count=1",
			"--worker-machine-count=1",
			"--from", "templates/cluster-template.yaml")
		stdout, _ := RunCmd(cmd)
		err := os.WriteFile(manifestsFile, stdout, 0644)
		Expect(err).ToNot(HaveOccurred())

		By("posting cluster manifests example template")
		cmd = exec.Command(KubectlPath, "apply", "-f", manifestsFile)
		RunCmd(cmd)

		By("Waiting for control plane")
		waitForControlPlane()

		By("Waiting on kubevirt machines to bootstrap")
		waitForBootstrappedMachines()

		By("Waiting on kubevirt machines to be ready")
		waitForMachineReadiness(2, 0)

		By("creating machine health check")
		postDefaultMHC("kvcluster")

		// trigger remediation by putting the VMI in a permanent stopped state
		By("Selecting new worker node to remediate")
		chosenVMI := chooseWorkerVMI()

		By("Setting VM to runstrategy once")
		chosenVM := &kubevirtv1.VirtualMachine{
			ObjectMeta: metav1.ObjectMeta{
				Name:      chosenVMI.Name,
				Namespace: chosenVMI.Namespace,
			},
		}
		key := client.ObjectKey{Namespace: chosenVM.Namespace, Name: chosenVM.Name}
		err = k8sclient.Get(context.Background(), key, chosenVM)
		Expect(err).ToNot(HaveOccurred())

		once := kubevirtv1.RunStrategyOnce
		chosenVM.Spec.RunStrategy = &once

		err = k8sclient.Update(context.Background(), chosenVM)
		Expect(err).ToNot(HaveOccurred())

		By("killing the chosen VMI's pod")
		chosenPod := getVMIPod(chosenVMI)
		DeleteAndWait(k8sclient, chosenPod, 180)

		By("Wait for KubeVirtMachine is deleted due to remediation")
		kvmName, ok := chosenVMI.Labels["capk.cluster.x-k8s.io/kubevirt-machine-name"]
		Expect(ok).To(BeTrue())
		chosenKVM := &infrav1.KubevirtMachine{
			ObjectMeta: metav1.ObjectMeta{
				Name:      kvmName,
				Namespace: chosenVMI.Namespace,
			},
		}
		waitForRemoval(chosenKVM, 10*60)

		By("Waiting on kubevirt new machines to be ready after remediation")
		waitForMachineReadiness(2, 0)

	})

	It("creates a simple externally managed cluster ephemeral VMs", Label("ephemeralVMs", "externallyManaged"), func() {
		By("generating cluster manifests from example template")
		cmd := exec.Command(ClusterctlPath, "generate", "cluster", "kvcluster",
			"--target-namespace", namespace,
			"--kubernetes-version", os.Getenv("TENANT_CLUSTER_KUBERNETES_VERSION"),
			"--control-plane-machine-count=1",
			"--worker-machine-count=1",
			"--from", "templates/cluster-template.yaml")
		stdout, _ := RunCmd(cmd)

		modifiedStdOut := injectKubevirtClusterExternallyManagedAnnotation(string(stdout))

		err := os.WriteFile(manifestsFile, []byte(modifiedStdOut), 0644)
		Expect(err).ToNot(HaveOccurred())

		By("posting cluster manifests example template")
		cmd = exec.Command(KubectlPath, "apply", "-f", manifestsFile)
		RunCmd(cmd)

		By("marking kubevirt cluster as ready to imitate an externally managed cluster")
		markExternalKubeVirtClusterReady("kvcluster", namespace)

		By("Waiting for control plane")
		waitForControlPlane()

		By("Waiting on kubevirt machines to be ready")
		waitForMachineReadiness(2, 0)

		By("Waiting for all tenant nodes to get provider id")
		waitForNodeUpdate()

		By("Ensuring cluster teardown works without race conditions by deleting namespace")
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}
		DeleteAndWait(k8sclient, ns, 120)

	})

	It("creates a simple cluster with persistent VMs", Label("persistentVMs"), func() {
		By("generating cluster manifests from example template")
		cmd := exec.Command(ClusterctlPath, "generate", "cluster", "kvcluster",
			"--target-namespace", namespace,
			"--kubernetes-version", os.Getenv("TENANT_CLUSTER_KUBERNETES_VERSION"),
			"--control-plane-machine-count=1",
			"--worker-machine-count=1",
			"--from", "templates/cluster-template-persistent-storage.yaml")
		stdout, _ := RunCmd(cmd)
		err := os.WriteFile(manifestsFile, stdout, 0644)
		Expect(err).ToNot(HaveOccurred())

		By("posting cluster manifests example template")
		cmd = exec.Command(KubectlPath, "apply", "-f", manifestsFile)
		RunCmd(cmd)

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
