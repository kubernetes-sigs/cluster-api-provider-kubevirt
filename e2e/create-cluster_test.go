package e2e_test

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/pflag"
	corev1 "k8s.io/api/core/v1"
	policy "k8s.io/api/policy/v1beta1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/pointer"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"kubevirt.io/client-go/kubecli"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/kind/pkg/cluster/constants"

	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha1"
)

var virtClient kubecli.KubevirtClient

const testFinalizer = "holdForTestFinalizer"

var _ = Describe("CreateCluster", func() {

	var tmpDir string
	var k8sclient client.Client
	var manifestsFile string
	var tenantKubeconfigFile string
	var namespace string
	var tenantAccessor tenantClusterAccess

	calicoManifestsUrl := "https://docs.projectcalico.org/v3.21/manifests/calico.yaml"

	BeforeEach(func() {
		var err error

		tmpDir, err = ioutil.TempDir(WorkingDir, "creation-tests")
		Expect(err).ToNot(HaveOccurred())

		manifestsFile = filepath.Join(tmpDir, "manifests.yaml")
		tenantKubeconfigFile = filepath.Join(tmpDir, "tenant-kubeconfig.yaml")

		cfg, err := config.GetConfig()
		Expect(err).ToNot(HaveOccurred())
		k8sclient, err = client.New(cfg, client.Options{})
		Expect(err).ToNot(HaveOccurred())
		clientConfig := kubecli.DefaultClientConfig(&pflag.FlagSet{})
		virtClient, err = kubecli.GetKubevirtClientFromClientConfig(clientConfig)
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

		tenantAccessor = newTenantClusterAccess(namespace, tenantKubeconfigFile)
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

		_ = tenantAccessor.stopForwardingTenantAPI()

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
		}).WithOffset(1).
			WithTimeout(10*time.Minute).
			WithPolling(5*time.Second).
			Should(Succeed(), "kubevirt machines should have bootstrap succeeded condition")

	}

	markExternalKubeVirtClusterReady := func(clusterName string, namespace string) {
		By("Ensuring no other controller is managing the kvcluster's status")
		Consistently(func(g Gomega) error {
			kvCluster := &infrav1.KubevirtCluster{}
			key := client.ObjectKey{Namespace: namespace, Name: clusterName}
			err := k8sclient.Get(context.Background(), key, kvCluster)
			g.Expect(err).ToNot(HaveOccurred())

			g.Expect(kvCluster.Finalizers).To(BeEmpty())
			g.Expect(kvCluster.Status.Ready).To(BeFalse())
			g.Expect(kvCluster.Status.FailureDomains).To(BeEmpty())
			g.Expect(kvCluster.Status.Conditions).To(BeEmpty())

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
		}).WithOffset(1).
			WithTimeout(5*time.Minute).
			WithPolling(5*time.Second).
			Should(Succeed(), "waiting for expected readiness.")
	}

	waitForTenantPods := func() {

		By(fmt.Sprintf("Perform Port Forward using controlplane vmi in namespace %s", namespace))
		err := tenantAccessor.startForwardingTenantAPI()
		Expect(err).ToNot(HaveOccurred())

		By("Create client to access the tenant cluster")
		clientSet, err := tenantAccessor.generateClient()
		Expect(err).ToNot(HaveOccurred())

		Eventually(func(g Gomega) error {
			podList, err := clientSet.CoreV1().Pods("kube-system").List(context.Background(), metav1.ListOptions{})
			g.Expect(err).ToNot(HaveOccurred())

			offlinePodList := []string{}
			for _, pod := range podList.Items {
				if pod.Status.Phase != corev1.PodRunning {
					offlinePodList = append(offlinePodList, pod.Name)
				}
			}

			if len(offlinePodList) > 0 {

				return fmt.Errorf("Waiting on tenant pods [%v] to reach a Running phase", offlinePodList)
			}
			return nil
		}).WithOffset(1).
			WithTimeout(8*time.Minute).
			WithPolling(5*time.Second).
			Should(Succeed(), "waiting for pods to hit Running phase.")

	}

	waitForTenantAccess := func(numExpectedNodes int) *kubernetes.Clientset {
		By(fmt.Sprintf("Perform Port Forward using controlplane vmi in namespace %s", namespace))
		err := tenantAccessor.startForwardingTenantAPI()
		Expect(err).ToNot(HaveOccurred())

		By("Create client to access the tenant cluster")
		clientSet, err := tenantAccessor.generateClient()
		Expect(err).ToNot(HaveOccurred())

		Eventually(func() error {

			nodeList, err := clientSet.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			if len(nodeList.Items) != numExpectedNodes {
				return fmt.Errorf("expecting tenant cluster to have %d nodes", len(nodeList.Items))
			}

			return nil
		}).WithOffset(1).
			WithTimeout(5*time.Minute).
			WithPolling(5*time.Second).
			Should(Succeed(), "waiting for expected readiness.")

		return clientSet
	}

	waitForNodeReadiness := func() *kubernetes.Clientset {
		By(fmt.Sprintf("Perform Port Forward using controlplane vmi in namespace %s", namespace))
		err := tenantAccessor.startForwardingTenantAPI()
		Expect(err).ToNot(HaveOccurred())

		By("Create client to access the tenant cluster")
		clientSet, err := tenantAccessor.generateClient()
		Expect(err).ToNot(HaveOccurred())

		Eventually(func(g Gomega) error {

			nodeList, err := clientSet.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
			g.Expect(err).ToNot(HaveOccurred())

			for _, node := range nodeList.Items {

				ready := false
				networkAvailable := false
				for _, cond := range node.Status.Conditions {
					if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
						ready = true
					} else if cond.Type == corev1.NodeNetworkUnavailable && cond.Status == corev1.ConditionFalse {
						networkAvailable = true
					}
				}

				if !ready {
					return fmt.Errorf("waiting on node %s to become ready", node.Name)
				} else if !networkAvailable {
					return fmt.Errorf("waiting on node %s to have network availablity", node.Name)
				}
			}

			return nil
		}).WithOffset(1).
			WithTimeout(8*time.Minute).
			WithPolling(5*time.Second).
			Should(Succeed(), "ensure healthy nodes.")

		return clientSet
	}

	installCalicoCNI := func() {
		cmd := exec.Command(KubectlPath, "--kubeconfig", tenantKubeconfigFile, "--insecure-skip-tls-verify", "--server", fmt.Sprintf("https://localhost:%d", tenantAccessor.getLocalPort()), "apply", "-f", calicoManifestsUrl)
		RunCmd(cmd)
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
		Eventually(func(g Gomega) error {
			cluster := &clusterv1.Cluster{}
			key := client.ObjectKey{Namespace: namespace, Name: "kvcluster"}
			err := k8sclient.Get(context.Background(), key, cluster)
			if err != nil {
				return err
			}

			if !conditions.IsTrue(cluster, clusterv1.ControlPlaneInitializedCondition) {
				return fmt.Errorf("still waiting on controlPlaneReady condition to be true")
			}

			return nil
		}).WithOffset(1).
			WithTimeout(20*time.Minute).
			WithPolling(5*time.Second).
			Should(Succeed(), "cluster should have control plane initialized")

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
		}).WithOffset(1).
			WithTimeout(15*time.Minute).
			WithPolling(5*time.Second).
			Should(Succeed(), "cluster should have control plane initialized")
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
		vmiList, err := virtClient.VirtualMachineInstance(namespace).List(&metav1.ListOptions{})
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

		By("Waiting for getting access to the tenant cluster")
		waitForTenantAccess(2)

		By("posting calico CNI manifests to the guest cluster and waiting for network")
		installCalicoCNI()

		By("Waiting for node readiness")
		waitForNodeReadiness()

		By("waiting all tenant Pods to be Ready")
		waitForTenantPods()
	})

	It("creates a simple cluster with ephemeral VMs with Passt", Label("ephemeralVMs"), func() {
		By("generating cluster manifests from example template")
		cmd := exec.Command(ClusterctlPath, "generate", "cluster", "kvcluster",
			"--target-namespace", namespace,
			"--kubernetes-version", os.Getenv("TENANT_CLUSTER_KUBERNETES_VERSION"),
			"--control-plane-machine-count=1",
			"--worker-machine-count=1",
			"--from", "templates/cluster-template-passt-kccm.yaml")
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

		By("Waiting for getting access to the tenant cluster")
		waitForTenantAccess(2)

		By("posting calico CNI manifests to the guest cluster and waiting for network")
		installCalicoCNI()

		By("Waiting for node readiness")
		waitForNodeReadiness()

		By("waiting all tenant Pods to be Ready")
		waitForTenantPods()
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

		By("Waiting for getting access to the tenant cluster")
		waitForTenantAccess(2)

		By("creating machine health check")
		postDefaultMHC("kvcluster")

		// trigger remediation by marking a running VMI as being in a failed state
		By("Selecting a worker node to remediate")
		chosenVMI := chooseWorkerVMI()

		By("Setting terminal state on VMI")
		kvmName, ok := chosenVMI.Labels["capk.cluster.x-k8s.io/kubevirt-machine-name"]
		Expect(ok).To(BeTrue())

		chosenVMI.Labels[infrav1.KubevirtMachineVMTerminalLabel] = "marked-terminal-by-func-test"
		chosenVMI, err = virtClient.VirtualMachineInstance(namespace).Update(chosenVMI)
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

		By("Waiting for getting access to the tenant cluster")
		waitForTenantAccess(2)

		By("posting calico CNI manifests to the guest cluster and waiting for network")
		installCalicoCNI()

		By("Waiting for node readiness")
		waitForNodeReadiness()

		By("waiting all tenant Pods to be Ready")
		waitForTenantPods()

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

		By("Waiting for getting access to the tenant cluster")
		waitForTenantAccess(2)

		By("creating machine health check")
		postDefaultMHC("kvcluster")

		// trigger remediation by putting the VMI in a permanent stopped state
		By("Selecting new worker node to remediate")
		chosenVMI := chooseWorkerVMI()

		By("Setting VM to runstrategy once")
		chosenVM, err := virtClient.VirtualMachine(chosenVMI.Namespace).Get(chosenVMI.Name, &metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())

		once := kubevirtv1.RunStrategyOnce
		chosenVM.Spec.RunStrategy = &once

		_, err = virtClient.VirtualMachine(namespace).Update(chosenVM)
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

		By("Waiting for getting access to the tenant cluster")
		waitForTenantAccess(2)

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

		By("Waiting for getting access to the tenant cluster")
		waitForTenantAccess(2)

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

		By("Waiting for getting access to the tenant cluster")
		waitForTenantAccess(2)

		By("Selecting a worker node to restart")
		vmiList, err := virtClient.VirtualMachineInstance(namespace).List(&metav1.ListOptions{})
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
		err = virtClient.VirtualMachineInstance(namespace).Delete(chosenVMI.Name, &metav1.DeleteOptions{})
		Expect(err).ToNot(HaveOccurred())

		By("Expecting a KubevirtMachine to revert back to ready=false while VM restarts")
		waitForMachineReadiness(1, 1)

		By("Expecting both KubevirtMachines stabilize to a ready=true again.")
		waitForMachineReadiness(2, 0)

		By("Waiting for getting access to the tenant cluster")
		clientSet := waitForTenantAccess(2)

		By("posting calico CNI manifests to the guest cluster and waiting for network")
		installCalicoCNI()

		By("Waiting for node readiness")
		waitForNodeReadiness()

		By("waiting all tenant Pods to be Ready")
		waitForTenantPods()

		vmiName := chosenVMI.Name

		By("read the VMI again after it was recreated")
		recreatedVMI := getRecreatedVMI(vmiName, namespace, chosenVMI.GetUID())

		Expect(*recreatedVMI.Spec.EvictionStrategy).Should(Equal(kubevirtv1.EvictionStrategyExternal))

		By("Set a testFinalizer to hold the VMI deletion so we could query it after eviction")
		recreatedVMI = addFinalizerFromVMI(recreatedVMI.Name, namespace)

		By("Get VMI's pod")
		pod := getVMIPod(recreatedVMI)

		By("Try to evict the VMI pod; should fail, but trigger the VMI draining")
		evictNode(pod)

		By("wait for a VMI to be marked for deletion")
		waitForVMIDraining(vmiName, namespace)

		By("remove the test finalizer")
		removeFinalizerFromVMI(recreatedVMI)

		By("wait for a new VMI to be created")
		getRecreatedVMI(vmiName, namespace, recreatedVMI.GetUID())

		By("Read the worker node from the tenant cluster, and validate its IP")
		validateNewNodeIP(clientSet, vmiName, namespace)
	})

	// This test will create a tenant cluster from `templates/cluster-template-ext-infra.yaml` template.
	// 1. generate secret in 'capk-system' namespace, containing external infra kubeconfig and namespace data
	// (note, the kubeconfig actually points to the same cluster, which is okay for the sake of the test)
	// 2. generate tenant cluster yaml manifests with `clusterctl generate cluster` command
	// 3. apply generated tenant cluster yaml manifests
	// 4. verify that tenant cluster machines booted and bootstrapped
	// 5. verify that tenant cluster control plane came up successfully
	It("should create a simple tenant cluster on external infrastructure", func() {
		By("generating a secret with external infrastructure kubeconfig and namespace")
		kubeconfig, err := os.ReadFile(os.Getenv("KUBECONFIG"))
		Expect(err).ToNot(HaveOccurred())
		// replace api server url with default server value, so it's routable from CAPK pod
		kubeconfigStr := string(kubeconfig)
		m := regexp.MustCompile("(?m:(.*?server:).*$)")
		kubeconfigStr = m.ReplaceAllString(kubeconfigStr, "${1} https://kubernetes.default")
		externalInfraSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "external-infra-kubeconfig",
				Namespace: "capk-system",
			},
			Data: map[string][]byte{
				"namespace":  []byte(namespace),
				"kubeconfig": []byte(kubeconfigStr),
			},
		}
		err = k8sclient.Create(context.Background(), externalInfraSecret)
		Expect(err).ToNot(HaveOccurred())

		By("generating cluster manifests from example template")
		cmd := exec.Command(ClusterctlPath, "generate", "cluster", "kvcluster",
			"--from", "templates/cluster-template-ext-infra.yaml",
			"--kubernetes-version", os.Getenv("TENANT_CLUSTER_KUBERNETES_VERSION"),
			"--control-plane-machine-count=1",
			"--worker-machine-count=1",
			"--target-namespace", namespace)
		stdout, _ := RunCmd(cmd)
		err = os.WriteFile(manifestsFile, stdout, 0644)
		Expect(err).ToNot(HaveOccurred())

		By("applying cluster manifests")
		cmd = exec.Command(KubectlPath, "apply", "-f", manifestsFile)
		RunCmd(cmd)

		By("waiting for machines to be ready")
		waitForMachineReadiness(2, 0)

		By("waiting for machines to bootstrap")
		waitForBootstrappedMachines()

		By("waiting for control plane")
		waitForControlPlane()
	})
})

func waitForVMIDraining(vmiName, namespace string) {
	var vmi *kubevirtv1.VirtualMachineInstance
	var err error

	By("wait for VMI is marked for deletion")
	Eventually(func(g Gomega) bool {
		vmi, err = virtClient.VirtualMachineInstance(namespace).Get(vmiName, &metav1.GetOptions{})
		g.Expect(err).ShouldNot(HaveOccurred())

		vmiDebugPrintout(vmi)

		g.Expect(vmi.Status.EvacuationNodeName).ShouldNot(BeEmpty())
		g.Expect(vmi.DeletionTimestamp).ShouldNot(BeNil())

		return true
	}).WithOffset(1).
		WithTimeout(time.Minute * 2).
		WithPolling(time.Second).
		Should(BeTrue())
}

func evictNode(pod *corev1.Pod) {

	err := virtClient.CoreV1().Pods(pod.Namespace).EvictV1beta1(context.Background(), &policy.Eviction{
		ObjectMeta: metav1.ObjectMeta{
			Name: pod.Name,
		},
		DeleteOptions: &metav1.DeleteOptions{
			GracePeriodSeconds: pointer.Int64(60 * 10), // 10 minutes
		},
	})

	ExpectWithOffset(1, k8serrors.IsTooManyRequests(err)).To(BeTrue(), "should return TooManyRequests error; got %v instead", err)
}

func getRecreatedVMI(vmiName string, namespace string, originalUID types.UID) *kubevirtv1.VirtualMachineInstance {
	var (
		vmi *kubevirtv1.VirtualMachineInstance
		err error
	)
	Eventually(func(g Gomega) types.UID {
		vmi, err = virtClient.VirtualMachineInstance(namespace).Get(vmiName, &metav1.GetOptions{})
		g.Expect(err).ShouldNot(HaveOccurred())
		g.Expect(vmi).ShouldNot(BeNil())

		vmiDebugPrintout(vmi)

		g.Expect(vmi.Status.EvacuationNodeName).Should(BeEmpty())
		g.Expect(vmi.DeletionTimestamp).Should(BeNil())

		return vmi.GetUID()

	}).WithOffset(1).
		WithTimeout(time.Minute * 5).
		WithPolling(time.Second * 5).
		ShouldNot(Equal(originalUID)) // make sure that a new VMI was created

	return vmi
}

func validateNewNodeIP(cl *kubernetes.Clientset, vmiName, namespace string) {
	Eventually(func(g Gomega) bool {
		// reading the node and the VMI again and again, because it takes time to the IPs to be synchronized
		node, err := cl.CoreV1().Nodes().Get(context.Background(), vmiName, metav1.GetOptions{})
		g.Expect(err).ToNot(HaveOccurred())

		var nodeIp string
		for _, address := range node.Status.Addresses {
			if address.Type == "InternalIP" {
				nodeIp = address.Address
			}
		}

		g.Expect(nodeIp).ShouldNot(BeEmpty(), "node's IP is not set")

		vmi, err := virtClient.VirtualMachineInstance(namespace).Get(vmiName, &metav1.GetOptions{})

		g.Expect(err).ShouldNot(HaveOccurred())
		g.Expect(vmi).ShouldNot(BeNil())

		for _, ifs := range vmi.Status.Interfaces {
			for _, ip := range ifs.IPs {
				if ip == nodeIp {
					return true
				}
			}
		}
		return false

	}).WithTimeout(5 * time.Minute).
		WithOffset(1).
		WithPolling(10 * time.Second).
		Should(BeTrue())
}

func vmiDebugPrintout(vmi *kubevirtv1.VirtualMachineInstance) {
	GinkgoWriter.Printf(`[Debug] VMI: {"UID": "%v", "DeletionTimestamp": "%v", "EvacuationNodeName": "%s"}`+"\n",
		vmi.UID, vmi.DeletionTimestamp, vmi.Status.EvacuationNodeName)
}

func addFinalizerFromVMI(vmiName, namespace string) *kubevirtv1.VirtualMachineInstance {
	var (
		vmi *kubevirtv1.VirtualMachineInstance
		err error
	)
	Eventually(func() error {
		vmi, err = virtClient.VirtualMachineInstance(namespace).Get(vmiName, &metav1.GetOptions{})
		if err != nil {
			return err
		}

		vmi.Finalizers = append(vmi.Finalizers, testFinalizer)
		_, err = virtClient.VirtualMachineInstance(vmi.Namespace).Update(vmi)
		return err
	}).WithOffset(1).
		WithTimeout(time.Minute).
		WithPolling(2 * time.Second).
		Should(Succeed())

	return vmi
}

func removeFinalizerFromVMI(vmi *kubevirtv1.VirtualMachineInstance) {
	vmi, err := virtClient.VirtualMachineInstance(vmi.Namespace).Get(vmi.Name, &metav1.GetOptions{})
	Expect(err).NotTo(HaveOccurred())

	index := -1
	for i, finalizer := range vmi.Finalizers {
		if finalizer == testFinalizer {
			index = i
			break
		}
	}
	ExpectWithOffset(1, index).To(BeNumerically(">=", 0))

	patch := []byte(fmt.Sprintf(`[{"op": "remove", "path": "/metadata/finalizers/%d"}]`, index))

	Eventually(func() error {
		_, err := virtClient.VirtualMachineInstance(vmi.Namespace).Patch(vmi.Name, types.JSONPatchType, patch, &metav1.PatchOptions{})
		return err
	}).WithOffset(1).
		WithTimeout(time.Minute).
		WithPolling(2 * time.Second).
		Should(Succeed())
}
