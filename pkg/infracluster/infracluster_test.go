package infracluster_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	. "sigs.k8s.io/cluster-api-provider-kubevirt/pkg/infracluster"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/testing"
)

var (
	fakeClient         client.Client
	infraClusterSecret *corev1.Secret
	ownerNamespace     = "Mordor"
	infraSecretName    = "external-infra-kubeconfig"
	kubeconfig         = `apiVersion: v1
clusters:
- cluster:
    insecure-skip-tls-verify: false
    server: https://gondor.com
  name: gondor
contexts:
- context:
    cluster: gondor
    namespace: minastirith
    user: aragorn
  name: gondor
current-context: gondor
kind: Config
preferences: {}
users:
- name: aragorn
`
)

var _ = Describe("InfraCluster", func() {

	It("should return the management client and namespace when the infrastructure secret reference is nil", func(ctx context.Context) {
		fakeClient = fake.NewClientBuilder().WithScheme(testing.SetupScheme()).Build()

		infraCluster := New(fakeClient, fakeClient, "")
		infraClient, infraNamespace, err := infraCluster.GenerateInfraClusterClient(nil, ownerNamespace, ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(infraClient).To(BeIdenticalTo(fakeClient))
		Expect(infraNamespace).To(Equal(ownerNamespace))
	})

	It("should reject a cross-namespace infraClusterSecretRef", func(ctx context.Context) {
		fakeClient := fake.NewClientBuilder().WithScheme(testing.SetupScheme()).Build()

		infraClusterSecretRef := &corev1.ObjectReference{
			APIVersion: "v1",
			Kind:       "Secret",
			Name:       infraSecretName,
			Namespace:  "other-namespace",
		}
		infraCluster := New(fakeClient, nil, "controller-ns")

		_, _, err := infraCluster.GenerateInfraClusterClient(infraClusterSecretRef, ownerNamespace, ctx)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("infraClusterSecretRef.namespace must match"))
		Expect(err.Error()).To(ContainSubstring(ownerNamespace))
		Expect(err.Error()).To(ContainSubstring("other-namespace"))
	})

	It("should allow infraClusterSecretRef pointing to the controller namespace", func(ctx context.Context) {
		controllerNS := "capk-system"
		infraClusterSecret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      infraSecretName,
				Namespace: controllerNS,
			},
			Data: map[string][]byte{
				"kubeconfig": []byte(kubeconfig),
			},
		}
		fakeClient = fake.NewClientBuilder().WithScheme(testing.SetupScheme()).WithObjects(infraClusterSecret).Build()

		infraClusterSecretRef := &corev1.ObjectReference{
			APIVersion: "v1",
			Kind:       "Secret",
			Name:       infraSecretName,
			Namespace:  controllerNS,
		}

		fakeInfraClient := fake.NewClientBuilder().Build()
		infraCluster := NewWithFactory(fakeClient, nil,
			func(config *rest.Config, options client.Options) (client.Client, error) {
				return fakeInfraClient, nil
			}, controllerNS,
		)
		infraClient, _, err := infraCluster.GenerateInfraClusterClient(infraClusterSecretRef, ownerNamespace, ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(infraClient).To(BeIdenticalTo(fakeInfraClient))
	})

	It("should allow infraClusterSecretRef with same namespace as owner", func(ctx context.Context) {
		infraClusterSecret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      infraSecretName,
				Namespace: ownerNamespace,
			},
			Data: map[string][]byte{
				"kubeconfig": []byte(kubeconfig),
			},
		}
		fakeClient = fake.NewClientBuilder().WithScheme(testing.SetupScheme()).WithObjects(infraClusterSecret).Build()

		infraClusterSecretRef := &corev1.ObjectReference{
			APIVersion: "v1",
			Kind:       "Secret",
			Name:       infraSecretName,
			Namespace:  ownerNamespace,
		}

		fakeInfraClient := fake.NewClientBuilder().Build()
		infraCluster := NewWithFactory(fakeClient, nil,
			func(config *rest.Config, options client.Options) (client.Client, error) {
				return fakeInfraClient, nil
			}, "",
		)
		infraClient, _, err := infraCluster.GenerateInfraClusterClient(infraClusterSecretRef, ownerNamespace, ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(infraClient).To(BeIdenticalTo(fakeInfraClient))
	})

	It("should failed when the referenced infrastructure secret cannot be found", func(ctx context.Context) {
		fakeClient := fake.NewClientBuilder().WithScheme(testing.SetupScheme()).Build()

		infraClusterSecretRef := &corev1.ObjectReference{
			APIVersion: "v1",
			Kind:       "Secret",
			Name:       infraSecretName,
		}
		infraCluster := New(fakeClient, nil, "")

		_, _, err := infraCluster.GenerateInfraClusterClient(infraClusterSecretRef, ownerNamespace, ctx)
		Expect(errors.IsNotFound(err)).To(BeTrue())
	})

	It("should fail when the referenced infrastructure secret doesn't have a kubeconfig data in it", func(ctx context.Context) {
		infraClusterSecret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      infraSecretName,
				Namespace: ownerNamespace,
			},
			Data: map[string][]byte{},
		}
		fakeClient = fake.NewClientBuilder().WithScheme(testing.SetupScheme()).WithObjects(infraClusterSecret).Build()

		infraClusterSecretRef := &corev1.ObjectReference{
			APIVersion: "v1",
			Kind:       "Secret",
			Name:       infraSecretName,
		}

		infraCluster := New(fakeClient, nil, "")
		_, _, err := infraCluster.GenerateInfraClusterClient(infraClusterSecretRef, ownerNamespace, ctx)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(Equal("failed to retrieve infra kubeconfig from secret: 'kubeconfig' key is missing"))
	})

	It("should fail when the referenced infrastructure secret kubeconfig data is invalid", func(ctx context.Context) {
		infraClusterSecret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      infraSecretName,
				Namespace: ownerNamespace,
			},
			Data: map[string][]byte{
				"kubeconfig": []byte("hello world"),
			},
		}
		fakeClient = fake.NewClientBuilder().WithScheme(testing.SetupScheme()).WithObjects(infraClusterSecret).Build()

		infraClusterSecretRef := &corev1.ObjectReference{
			APIVersion: "v1",
			Kind:       "Secret",
			Name:       infraSecretName,
		}

		infraCluster := New(fakeClient, nil, "")
		_, _, err := infraCluster.GenerateInfraClusterClient(infraClusterSecretRef, ownerNamespace, ctx)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("failed to create K8s-API client config"))
	})

	It("should return the infra-client and the namespace defined in the secret, when set", func(ctx context.Context) {

		infraClusterSecret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      infraSecretName,
				Namespace: ownerNamespace,
			},
			Data: map[string][]byte{
				"kubeconfig": []byte(kubeconfig),
				"namespace":  []byte("Shire"),
			},
		}
		fakeClient = fake.NewClientBuilder().WithScheme(testing.SetupScheme()).WithObjects(infraClusterSecret).Build()

		infraClusterSecretRef := &corev1.ObjectReference{
			APIVersion: "v1",
			Kind:       "Secret",
			Name:       infraSecretName,
		}

		fakeInfraClient := fake.NewClientBuilder().Build()
		infraCluster := NewWithFactory(fakeClient, nil,
			func(config *rest.Config, options client.Options) (client.Client, error) {
				return fakeInfraClient, nil
			}, "",
		)
		infraClient, namespace, err := infraCluster.GenerateInfraClusterClient(infraClusterSecretRef, ownerNamespace, ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(infraClient).To(BeIdenticalTo(fakeInfraClient))
		Expect(namespace).To(Equal("Shire"))
	})

	It("should return the infra-client and kubeconfig namespace when the secret doesn't specified one", func(ctx context.Context) {
		infraClusterSecret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      infraSecretName,
				Namespace: ownerNamespace,
			},
			Data: map[string][]byte{
				"kubeconfig": []byte(kubeconfig),
			},
		}
		fakeClient = fake.NewClientBuilder().WithScheme(testing.SetupScheme()).WithObjects(infraClusterSecret).Build()

		infraClusterSecretRef := &corev1.ObjectReference{
			APIVersion: "v1",
			Kind:       "Secret",
			Name:       infraSecretName,
		}

		fakeInfraClient := fake.NewClientBuilder().Build()
		infraCluster := NewWithFactory(fakeClient, nil,
			func(config *rest.Config, options client.Options) (client.Client, error) {
				return fakeInfraClient, nil
			}, "",
		)
		infraClient, namespace, err := infraCluster.GenerateInfraClusterClient(infraClusterSecretRef, ownerNamespace, ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(infraClient).To(BeIdenticalTo(fakeInfraClient))
		Expect(namespace).To(Equal("minastirith"))
	})

	Context("RestConfigHardening", func() {
		It("should drop the AuthProvider from the configurations", func() {
			customKubeconfig := []byte(`apiVersion: v1
clusters:
- cluster:
    insecure-skip-tls-verify: false
    server: https://gondor.com
  name: gondor
contexts:
- context:
    cluster: gondor
    namespace: minastirith
    user: aragorn
  name: gondor
current-context: gondor
kind: Config
preferences: {}
users:
- name: aragorn
  user:
    auth-provider:
      name: fake
`)

			By("sanity: check that the AuthProvider is in the configurations")
			clientConfig, err := clientcmd.NewClientConfigFromBytes(customKubeconfig)
			Expect(err).NotTo(HaveOccurred())

			restConfig, err := clientConfig.ClientConfig()
			Expect(err).NotTo(HaveOccurred())
			Expect(restConfig).NotTo(BeNil())
			Expect(restConfig.AuthProvider).NotTo(BeNil())

			By("now check it was gone")
			RestConfigHardening(restConfig)
			Expect(restConfig.AuthProvider).To(BeNil())
		})

		It("should drop the ExecProvider from the configurations", func() {
			customKubeconfig := []byte(`apiVersion: v1
clusters:
- cluster:
    insecure-skip-tls-verify: false
    server: https://gondor.com
  name: gondor
contexts:
- context:
    cluster: gondor
    namespace: minastirith
    user: aragorn
  name: gondor
current-context: gondor
kind: Config
preferences: {}
users:
- name: aragorn
  user:
    exec:
      apiVersion: v1
      command: 'ls -la /'
      interactiveMode: IfAvailable
`)
			By("sanity: check that the ExecProvider is in the configurations")
			clientConfig, err := clientcmd.NewClientConfigFromBytes(customKubeconfig)
			Expect(err).NotTo(HaveOccurred())

			restConfig, err := clientConfig.ClientConfig()
			Expect(err).NotTo(HaveOccurred())
			Expect(restConfig).NotTo(BeNil())
			Expect(restConfig.ExecProvider).NotTo(BeNil())
			Expect(restConfig.ExecProvider.Command).To(Equal("ls -la /"))

			By("now check it was gone")
			RestConfigHardening(restConfig)
			Expect(restConfig.ExecProvider).To(BeNil())
		})
	})
})
