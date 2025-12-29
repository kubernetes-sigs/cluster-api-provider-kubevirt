package kubeconfig

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"sigs.k8s.io/cluster-api-provider-kubevirt/clusterkubevirtadm/common"
)

const (
	namespaceName = "ns-name"
)

var _ = Describe("test kubeconfig function", func() {
	Context("test getToken", func() {
		var cmdCtx cmdContext
		BeforeEach(func() {
			cmdCtx = cmdContext{
				Namespace: namespaceName,
			}
		})

		It("should return the token if exists", func() {
			sa := &corev1.ServiceAccount{
				ObjectMeta: metav1.ObjectMeta{
					Name:      namespaceName,
					Namespace: namespaceName,
				},
				Secrets: []corev1.ObjectReference{
					{
						Name: namespaceName + "-token",
					},
				},
			}
			client := fake.NewClientset(
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}},
				sa,
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      namespaceName + "-token",
						Namespace: namespaceName,
					},
					Data: map[string][]byte{
						"token": []byte("testing getToken"),
					},
				},
			)
			cmdCtx.Client = client
			token, err := getToken(context.Background(), cmdCtx, sa)
			Expect(err).ToNot(HaveOccurred())
			Expect(token).ToNot(BeEmpty())
			Expect(string(token)).Should(Equal("testing getToken"))
		})

		It("should return error if the token in no exist", func() {
			sa := &corev1.ServiceAccount{
				ObjectMeta: metav1.ObjectMeta{
					Name:      namespaceName,
					Namespace: namespaceName,
				},
				Secrets: []corev1.ObjectReference{
					{
						Name: namespaceName + "-token",
					},
				},
			}
			client := fake.NewClientset(
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}},
				sa,
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      namespaceName + "-token",
						Namespace: namespaceName,
					},
					Data: map[string][]byte{
						"not-token": []byte("testing getToken"),
					},
				},
			)
			cmdCtx.Client = client
			token, err := getToken(context.Background(), cmdCtx, sa)
			Expect(err).To(HaveOccurred())
			Expect(token).To(BeNil())
		})

		It("should return error if the secret is not there", func() {
			sa := &corev1.ServiceAccount{
				ObjectMeta: metav1.ObjectMeta{
					Name:      namespaceName,
					Namespace: namespaceName,
				},
				Secrets: []corev1.ObjectReference{
					{
						Name: namespaceName + "-token",
					},
				},
			}
			client := fake.NewClientset(
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}},
				sa,
			)
			cmdCtx.Client = client
			token, err := getToken(context.Background(), cmdCtx, sa)
			Expect(err).To(HaveOccurred())
			Expect(token).To(BeNil())
		})
	})

	Context("test GetServiceAccount", func() {
		var cmdCtx cmdContext
		BeforeEach(func() {
			cmdCtx = cmdContext{
				Namespace: namespaceName,
			}
		})

		It("Should wait until the there are secrets in the SA", func() {
			sa := &corev1.ServiceAccount{
				ObjectMeta: metav1.ObjectMeta{
					Name:      common.ServiceAccountName,
					Namespace: namespaceName,
				},
			}

			client := fake.NewClientset(sa)
			cmdCtx.Client = client

			doneUpdatingSa := make(chan struct{})
			// mimic K8s operation of adding secrets to the serviceAccount
			go func() {
				for {
					sa, err := client.CoreV1().ServiceAccounts(namespaceName).Get(context.Background(), common.ServiceAccountName, metav1.GetOptions{})
					if err != nil {
						time.Sleep(time.Millisecond * 10)
						continue
					}
					sa.Secrets = []corev1.ObjectReference{{Name: "secretName"}}
					_, err = client.CoreV1().ServiceAccounts(namespaceName).Update(context.Background(), sa, metav1.UpdateOptions{})
					if err != nil {
						GinkgoWriter.Println(err)
					} else {
						close(doneUpdatingSa)
						break
					}
				}
			}()

			found, err := getServiceAccount(context.Background(), cmdCtx)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).ShouldNot(BeNil())
			Expect(found.Secrets).ToNot(BeEmpty())

			Eventually(doneUpdatingSa).Should(BeClosed())
		})

		It("should fail after 10 seconds if the secret was not created", func() {
			sa := &corev1.ServiceAccount{
				ObjectMeta: metav1.ObjectMeta{
					Name:      common.ServiceAccountName,
					Namespace: namespaceName,
				},
			}

			client := fake.NewClientset(sa)
			cmdCtx.Client = client

			timestampBeforeGet := time.Now()
			_, err := getServiceAccount(context.Background(), cmdCtx)
			Expect(err).To(HaveOccurred())
			Expect(time.Since(timestampBeforeGet)).Should(BeNumerically(">=", time.Second*10))
		})

		It("Should fail if the SA is not exist", func() {
			client := fake.NewClientset()
			cmdCtx.Client = client

			_, err := getServiceAccount(context.Background(), cmdCtx)
			Expect(errors.IsNotFound(err)).To(BeTrue())
		})
	})
})
