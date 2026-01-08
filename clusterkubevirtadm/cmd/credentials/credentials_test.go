package credentials

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	kubevirtcore "kubevirt.io/api/core"
	cdicore "kubevirt.io/containerized-data-importer-api/pkg/apis/core"

	"sigs.k8s.io/cluster-api-provider-kubevirt/clusterkubevirtadm/common"
)

const (
	namespaceName = "ns-name"
)

var _ = Describe("test credentials common function", func() {
	Context("test ensureNamespace", func() {
		var cmdCtx cmdContext
		BeforeEach(func() {
			cmdCtx = cmdContext{
				Namespace: namespaceName,
			}
		})

		It("should create NS if missing", func() {
			client := fake.NewClientset()
			cmdCtx.Client = client

			Expect(ensureNamespace(context.Background(), cmdCtx, clientOperationCreate)).To(Succeed())

			ns, err := client.CoreV1().Namespaces().Get(context.Background(), namespaceName, metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(ns).ToNot(BeNil())
			Expect(ns.Name).Should(Equal(namespaceName))
		})

		It("should do nothing if applying and the NS already exist", func() {
			client := fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}})
			cmdCtx.Client = client

			Expect(ensureNamespace(context.Background(), cmdCtx, clientOperationApply)).To(Succeed())

			ns, err := client.CoreV1().Namespaces().Get(context.Background(), namespaceName, metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(ns).ToNot(BeNil())
			Expect(ns.Name).Should(Equal(namespaceName))
		})

		It("should return error if creating and the NS already exist", func() {
			client := fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}})
			cmdCtx.Client = client

			err := ensureNamespace(context.Background(), cmdCtx, clientOperationCreate)
			Expect(errors.IsNotFound(err)).ToNot(BeTrue())
		})
	})

	Context("test ensureServiceAccount", func() {
		var cmdCtx cmdContext
		BeforeEach(func() {
			cmdCtx = cmdContext{
				Namespace: namespaceName,
			}
		})
		It("should do add serviceAccount if it's missing", func() {
			client := fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}})
			cmdCtx.Client = client

			Expect(ensureServiceAccount(context.Background(), cmdCtx)).To(Succeed())
		})

		It("should do nothing if the serviceAccount is already exist", func() {
			client := fake.NewClientset(
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}},
				&corev1.ServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Name:      common.ServiceAccountName,
						Namespace: namespaceName,
					},
					Secrets: []corev1.ObjectReference{
						{
							Name: "secretName",
						},
					},
				},
			)
			cmdCtx.Client = client

			Expect(ensureServiceAccount(context.Background(), cmdCtx)).To(Succeed())
		})
	})

	Context("test createOrUpdateRole", func() {
		var cmdCtx cmdContext
		BeforeEach(func() {
			cmdCtx = cmdContext{
				Namespace: namespaceName,
			}
		})

		It("should create Role if missing", func() {
			client := fake.NewClientset(
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}},
			)
			cmdCtx.Client = client

			Expect(createOrUpdateRole(context.Background(), cmdCtx, clientOperationCreate)).To(Succeed())

			roles, err := client.RbacV1().Roles(namespaceName).List(context.Background(), metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())

			Expect(roles).ToNot(BeNil())
			Expect(roles.Items).To(HaveLen(1))

			Expect(roles.Items[0].Name).Should(Equal(roleName))
			Expect(roles.Items[0].Rules).Should(HaveLen(3))
			Expect(roles.Items[0].Rules[0].APIGroups).Should(And(HaveLen(1), ContainElements(kubevirtcore.GroupName)))
			Expect(roles.Items[0].Rules[0].Resources).Should(And(HaveLen(2), ContainElements("virtualmachines", "virtualmachineinstances")))
			Expect(roles.Items[0].Rules[0].Verbs).Should(And(HaveLen(1), ContainElements(rbacv1.VerbAll)))

			Expect(roles.Items[0].Rules[1].APIGroups).Should(And(HaveLen(1), ContainElements(cdicore.GroupName)))
			Expect(roles.Items[0].Rules[1].Resources).Should(And(HaveLen(1), ContainElements("datavolumes")))
			Expect(roles.Items[0].Rules[1].Verbs).Should(And(HaveLen(3), ContainElements("get", "list", "watch")))

			Expect(roles.Items[0].Rules[2].APIGroups).Should(And(HaveLen(1), ContainElements("")))
			Expect(roles.Items[0].Rules[2].Resources).Should(And(HaveLen(2), ContainElements("secrets", "services")))
			Expect(roles.Items[0].Rules[2].Verbs).Should(And(HaveLen(1), ContainElements(rbacv1.VerbAll)))
		})

		It("create should return error if the Role is already exist", func() {
			expectedRole := generateRole(cmdCtx)
			client := fake.NewClientset(
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}},
				expectedRole,
			)
			cmdCtx.Client = client

			Expect(createOrUpdateRole(context.Background(), cmdCtx, clientOperationCreate)).ToNot(Succeed())
		})

		It("should update the role if it is already exist, with different values", func() {
			expectedRole := generateRole(cmdCtx)
			existingRole := generateRole(cmdCtx)

			existingRole.Rules = []rbacv1.PolicyRule{
				{
					APIGroups: []string{"test-group"},
					Resources: []string{"test-resource1", "test-resource2", "test-resource3"},
					Verbs:     []string{"get", "list", "delete"},
				},
			}

			client := fake.NewClientset(
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}},
				existingRole,
			)
			cmdCtx.Client = client

			Expect(createOrUpdateRole(context.Background(), cmdCtx, clientOperationApply)).To(Succeed())

			roles, err := client.RbacV1().Roles(namespaceName).List(context.Background(), metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())

			Expect(roles).ToNot(BeNil())
			Expect(roles.Items).To(HaveLen(1))

			Expect(roles.Items[0].Name).Should(Equal(roleName))
			Expect(roles.Items[0].Rules).Should(Equal(expectedRole.Rules))
		})
	})

	Context("test ensureRoleBinding", func() {
		var cmdCtx cmdContext
		BeforeEach(func() {
			cmdCtx = cmdContext{
				Namespace: namespaceName,
			}
		})

		It("should create RoleBinding if missing", func() {
			client := fake.NewClientset(
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}},
				&rbacv1.Role{
					ObjectMeta: metav1.ObjectMeta{
						Name:      roleName,
						Namespace: namespaceName,
					},
				},
			)
			cmdCtx.Client = client

			Expect(ensureRoleBinding(context.Background(), cmdCtx)).To(Succeed())

			roleBindings, err := client.RbacV1().RoleBindings(namespaceName).List(context.Background(), metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())

			Expect(roleBindings).ToNot(BeNil())
			Expect(roleBindings.Items).To(HaveLen(1))

			Expect(roleBindings.Items[0].Name).Should(Equal(roleName + "-binding"))
		})

		It("should do nothing if the RoleBinding is already exist", func() {
			client := fake.NewClientset(
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}},
				&rbacv1.Role{
					ObjectMeta: metav1.ObjectMeta{
						Name:      roleName,
						Namespace: namespaceName,
					},
				},
				&rbacv1.RoleBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name:      roleName + "-binding",
						Namespace: namespaceName,
					},
				},
			)
			cmdCtx.Client = client

			roleBindings, err := client.RbacV1().RoleBindings(namespaceName).List(context.Background(), metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())

			Expect(roleBindings).ToNot(BeNil())
			Expect(roleBindings.Items).To(HaveLen(1))

			Expect(roleBindings.Items[0].Name).Should(Equal(roleName + "-binding"))
		})
	})
})
