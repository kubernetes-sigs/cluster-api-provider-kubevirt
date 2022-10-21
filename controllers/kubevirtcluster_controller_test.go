package controllers_test

import (
	goContext "context"
	"time"

	"github.com/golang/mock/gomock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	. "sigs.k8s.io/controller-runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha1"
	"sigs.k8s.io/cluster-api-provider-kubevirt/controllers"
	infraclustermock "sigs.k8s.io/cluster-api-provider-kubevirt/pkg/infracluster/mock"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/testing"
)

var (
	mockCtrl                  *gomock.Controller
	clusterName               string
	kubevirtClusterName       string
	kubevirtCluster           *infrav1.KubevirtCluster
	cluster                   *clusterv1.Cluster
	fakeClient                client.Client
	kubevirtClusterReconciler controllers.KubevirtClusterReconciler
	fakeContext               = goContext.TODO()
	testLogger                = ctrl.Log.WithName("test")
	infraClusterMock          *infraclustermock.MockInfraCluster
)

var _ = Describe("Reconcile", func() {

	BeforeEach(func() {
		mockCtrl = gomock.NewController(GinkgoT())
		infraClusterMock = infraclustermock.NewMockInfraCluster(mockCtrl)
	})

	setupClient := func(objects []client.Object) {
		fakeClient = fake.NewClientBuilder().WithScheme(testing.SetupScheme()).WithObjects(objects...).Build()
		kubevirtClusterReconciler = controllers.KubevirtClusterReconciler{
			Client:       fakeClient,
			InfraCluster: infraClusterMock,
			Log:          testLogger,
		}
	}

	Context("reconcile generic cluster", func() {
		BeforeEach(func() {
			clusterName = "test-cluster"
			kubevirtClusterName = "test-kubevirt-cluster"
			kubevirtCluster = testing.NewKubevirtCluster(kubevirtClusterName, kubevirtClusterName)
			cluster = testing.NewCluster(kubevirtClusterName, kubevirtCluster)
			objects := []client.Object{
				cluster,
				kubevirtCluster,
			}
			fakeClient = fake.NewClientBuilder().WithScheme(testing.SetupScheme()).WithObjects(objects...).Build()
		})

		AfterEach(func() {})

		It("should create cluster", func() {
			objects := []client.Object{
				cluster,
				kubevirtCluster,
			}
			setupClient(objects)
			infraClusterMock.EXPECT().GenerateInfraClusterClient(gomock.Any(), gomock.Any(), gomock.Any()).Return(fakeClient, kubevirtCluster.Namespace, nil)

			result, err := kubevirtClusterReconciler.Reconcile(fakeContext, Request{
				NamespacedName: client.ObjectKey{
					Namespace: kubevirtCluster.Namespace,
					Name:      kubevirtCluster.Name,
				},
			})
			Expect(err).ShouldNot(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero())
			Expect(result.Requeue).To(BeFalse())
		})

		It("should not create cluster when namespace and kubevirtCluster is not specified", func() {
			result, err := kubevirtClusterReconciler.Reconcile(fakeContext, Request{
				NamespacedName: client.ObjectKey{
					Namespace: "",
					Name:      "",
				},
			})
			Expect(err).ShouldNot(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero())
			Expect(result.Requeue).To(BeFalse())
		})

		It("should not create cluster when invalid namespace and kubevirtCluster is specified", func() {
			result, err := kubevirtClusterReconciler.Reconcile(fakeContext, Request{
				NamespacedName: client.ObjectKey{
					Namespace: "Invalid Namespace",
					Name:      "Invalid Name",
				},
			})
			Expect(err).ShouldNot(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero())
			Expect(result.Requeue).To(BeFalse())
		})
	})

	Context("reconcile a cluster with finalizer set", func() {
		BeforeEach(func() {
			clusterName = "test-cluster"
			kubevirtClusterName = "test-kubevirt-cluster"
			kubevirtCluster = testing.NewKubevirtCluster(kubevirtClusterName, kubevirtClusterName)
			kubevirtCluster.Finalizers = []string{infrav1.ClusterFinalizer}
			cluster = testing.NewCluster(kubevirtClusterName, kubevirtCluster)
		})

		AfterEach(func() {})

		It("should throw an error when reconciling unhandled clusters. ", func() {
			objects := []client.Object{
				cluster,
				kubevirtCluster,
			}
			setupClient(objects)
			infraClusterMock.EXPECT().GenerateInfraClusterClient(gomock.Any(), gomock.Any(), gomock.Any()).Return(fakeClient, kubevirtCluster.Namespace, nil)

			_, err := kubevirtClusterReconciler.Reconcile(fakeContext, Request{
				NamespacedName: client.ObjectKey{
					Namespace: kubevirtCluster.Namespace,
					Name:      kubevirtCluster.Name,
				},
			})
			//Load Balancer service is not ready yet.
			Expect(err).Should(HaveOccurred())
		})
	})

	Context("reconcile cluster with finalizer and deletion time stamp", func() {
		BeforeEach(func() {
			clusterName = "test-cluster"
			kubevirtClusterName = "test-kubevirt-cluster"
			kubevirtCluster = testing.NewKubevirtCluster(kubevirtClusterName, kubevirtClusterName)
			kubevirtCluster.Finalizers = []string{infrav1.ClusterFinalizer}
			kubevirtCluster.SetDeletionTimestamp(&metav1.Time{Time: time.Now()})
			cluster = testing.NewCluster(kubevirtClusterName, kubevirtCluster)
		})

		AfterEach(func() {})

		It("should succeed with the kubevirt cluster being deleted.", func() {
			objects := []client.Object{
				cluster,
				kubevirtCluster,
			}
			setupClient(objects)
			infraClusterMock.EXPECT().GenerateInfraClusterClient(gomock.Any(), gomock.Any(), gomock.Any()).Return(fakeClient, kubevirtCluster.Namespace, nil)

			namespacedName := client.ObjectKey{
				Namespace: kubevirtCluster.Namespace,
				Name:      kubevirtCluster.Name,
			}

			result, err := kubevirtClusterReconciler.Reconcile(fakeContext, Request{
				NamespacedName: namespacedName,
			})
			Expect(err).ShouldNot(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero())
			Expect(result.Requeue).To(BeFalse())

			kvc := &infrav1.KubevirtCluster{}
			err = fakeClient.Get(fakeContext, namespacedName, kvc)
			Expect(err).Should(HaveOccurred())
		})
	})
})
