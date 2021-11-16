package ssh_test

import (
	gocontext "context"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha4"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/context"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/ssh"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubevirtv1 "kubevirt.io/client-go/api/v1"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/testing"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1alpha4"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)
var (
	clusterName         = "test-cluster"
	kubevirtClusterName = "test-kubevirt-cluster"
	kubevirtCluster     = testing.NewKubevirtCluster(clusterName, kubevirtClusterName)
	cluster             = testing.NewCluster(clusterName, kubevirtCluster)

	clusterContext = &context.ClusterContext{
		Logger:          ctrl.LoggerFrom(gocontext.TODO()).WithName("test"),
		Context:         gocontext.TODO(),
		Cluster:         cluster,
		KubevirtCluster: kubevirtCluster,
	}
	clusterNodeSshKeys	ssh.ClusterNodeSshKeys
)
var _ = Describe("ClusterNodeSshKeys", func() {
	var _ = Describe("test without ssh keys set", func() {
		var (
			fakeClient client.Client
		)
		Context("Generate new keys", func() {
			BeforeEach(func() {
				objects := []client.Object{
					cluster,
					kubevirtCluster,
				}
				fakeClient = fake.NewClientBuilder().WithScheme(setupScheme()).WithObjects(objects...).Build()
				clusterNodeSshKeys = ssh.ClusterNodeSshKeys{
					Client: fakeClient,
					ClusterContext: clusterContext,
				}
			})
			It("test generate new keys", func() {
				err := clusterNodeSshKeys.GenerateNewKeys()
				Expect(err).NotTo(HaveOccurred())
			})
			It("test persist keys", func() {
				resp, errors := clusterNodeSshKeys.PersistKeysToSecret()
				Expect(resp).To(BeNil())
				Expect(errors).To(HaveOccurred())
			})
			It("test get keys", func() {
				err, _ :=clusterNodeSshKeys.GetKeysDataSecret()
				Expect(err).To(BeNil())
			})
			It("test is persisted false", func() {
				result := clusterNodeSshKeys.IsPersistedToSecret()
				Expect(result).To(BeFalse())
			})
			It("test fetch persisted keys from secret", func() {
				err := clusterNodeSshKeys.FetchPersistedKeysFromSecret()
				Expect(err).To(HaveOccurred())
			})
		})
	})
	var _ = Describe("test with ssh keys set", func() {
		var (
			fakeClient client.Client
		)
		Context("when SSHKey is set", func() {
			BeforeEach(func() {
				objects := []client.Object{
					cluster,
					kubevirtCluster,
				}
				fakeClient = fake.NewClientBuilder().WithScheme(setupScheme()).WithObjects(objects...).Build()
				clusterNodeSshKeys = ssh.ClusterNodeSshKeys{
					Client: fakeClient,
					ClusterContext: clusterContext,
				}
			})
			It("test persist keys", func() {
				err := clusterNodeSshKeys.GenerateNewKeys()
				Expect(err).NotTo(HaveOccurred())
				sshKeysDataSecret, err := clusterNodeSshKeys.PersistKeysToSecret()
				Expect(err).NotTo(HaveOccurred())
				Expect(sshKeysDataSecret.Name).To(Equal("test-kubevirt-cluster-ssh-keys"))
				clusterContext.KubevirtCluster.Spec.SshKeys = infrav1.SSHKeys{
					ConfigRef: &corev1.ObjectReference{
						APIVersion: sshKeysDataSecret.APIVersion,
						Kind:       sshKeysDataSecret.Kind,
						Name:       sshKeysDataSecret.Name,
						Namespace:  sshKeysDataSecret.Namespace,
						UID:        sshKeysDataSecret.UID,
					},
					DataSecretName: &sshKeysDataSecret.Name,
				}
				secret, _ :=clusterNodeSshKeys.GetKeysDataSecret()
				Expect(secret).NotTo(BeNil())
				result := clusterNodeSshKeys.IsPersistedToSecret()
				Expect(result).To(BeTrue())
				_ = clusterNodeSshKeys.FetchPersistedKeysFromSecret()
			})
		})
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
