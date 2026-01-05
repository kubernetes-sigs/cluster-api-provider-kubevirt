package e2e_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta1"         //nolint SA1019
	"sigs.k8s.io/cluster-api/util/deprecated/v1beta1/conditions" //nolint SA1019
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func waitForV1beta1ControlPlane(ctx context.Context, k8sclient client.Client, namespace string) {
	By("Waiting on cluster's control plane to initialize")
	Eventually(func(g Gomega) {
		cluster := &clusterv1.Cluster{}
		key := client.ObjectKey{Namespace: namespace, Name: "kvcluster"}
		g.Expect(k8sclient.Get(ctx, key, cluster)).To(Succeed())
		g.Expect(conditions.IsTrue(cluster, clusterv1.ControlPlaneInitializedCondition)).To(
			BeTrue(),
			"still waiting on controlPlaneReady condition to be true",
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
		g.Expect(conditions.IsTrue(cluster, clusterv1.ControlPlaneReadyCondition)).To(
			BeTrue(),
			"still waiting on controlPlaneInitialized condition to be true",
		)
	}).WithOffset(1).
		WithTimeout(15*time.Minute).
		WithPolling(5*time.Second).
		Should(Succeed(), "cluster should have control plane initialized")
}

func postDefaultMHCV1Beta1(ctx context.Context, k8sclient client.Client, namespace string, clusterName string) {
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

	Expect(k8sclient.Create(ctx, mhc)).To(Succeed())
}

func deleteV1Beta1Cluster(ctx context.Context, k8sclient client.Client, namespace string, clusterName string) {
	GinkgoHelper()

	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      clusterName,
		},
	}
	DeleteAndWait(ctx, k8sclient, cluster, 120)
}
