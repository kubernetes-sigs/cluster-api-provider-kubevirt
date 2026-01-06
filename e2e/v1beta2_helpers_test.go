package e2e_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func waitForV1beta2ControlPlane(ctx context.Context, k8sclient client.Client, namespace string) {
	By("Waiting on cluster's control plane to initialize")
	Eventually(func(g Gomega) {
		cluster := &clusterv1.Cluster{}
		key := client.ObjectKey{Namespace: namespace, Name: "kvcluster"}
		g.Expect(k8sclient.Get(ctx, key, cluster)).To(Succeed())
		g.Expect(ptr.Equal(cluster.Status.Initialization.ControlPlaneInitialized, ptr.To(true))).To(
			BeTrue(),
			"still waiting on controlPlaneInitialized condition to be true",
		)
	}).WithOffset(1).
		WithTimeout(20*time.Minute).
		WithPolling(5*time.Second).
		Should(Succeed(), "cluster should have control plane initialized")

	By("Waiting on cluster's control plane to be ready")
	Eventually(func(g Gomega) {
		cluster := &clusterv1.Cluster{}
		key := client.ObjectKey{Namespace: namespace, Name: "kvcluster"}
		g.Expect(k8sclient.Get(ctx, key, cluster)).To(Succeed())
		g.Expect(conditions.IsTrue(cluster, clusterv1.ClusterControlPlaneAvailableCondition)).To(
			BeTrue(),
			"still waiting on controlplaneAvailable condition to be true",
		)
	}).WithOffset(1).
		WithTimeout(15*time.Minute).
		WithPolling(5*time.Second).
		Should(Succeed(), "cluster should have control plane available")
}

func postDefaultMHCV1Beta2(ctx context.Context, k8sclient client.Client, namespace string, clusterName string) {
	GinkgoHelper()
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
			Checks: clusterv1.MachineHealthCheckChecks{
				UnhealthyNodeConditions: []clusterv1.UnhealthyNodeCondition{
					{
						Type:           corev1.NodeReady,
						Status:         corev1.ConditionFalse,
						TimeoutSeconds: ptr.To(int32(300)),
					},
					{
						Type:           corev1.NodeReady,
						Status:         corev1.ConditionUnknown,
						TimeoutSeconds: ptr.To(int32(300)),
					},
				},
			},
			Remediation: clusterv1.MachineHealthCheckRemediation{
				TriggerIf: clusterv1.MachineHealthCheckRemediationTriggerIf{
					UnhealthyLessThanOrEqualTo: &maxUnhealthy,
				},
			},
		},
	}

	Expect(k8sclient.Create(ctx, mhc)).To(Succeed())
}

func deleteV1Beta2Cluster(ctx context.Context, k8sclient client.Client, namespace string, clusterName string) {
	GinkgoHelper()

	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      clusterName,
		},
	}
	DeleteAndWait(ctx, k8sclient, cluster, 120)
}
