package controller

import (
	functionsdevv1alpha1 "github.com/functions-dev/func-operator/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/client-go/tools/events"
)

var _ = Describe("Function RBAC", func() {
	Context("ensureDeployFunctionRoleBinding", func() {
		var reconciler *FunctionReconciler
		var testNamespace string

		BeforeEach(func() {
			testNamespace = "rbac-test-" + rand.String(6)
			ns := &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}}
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())

			reconciler = &FunctionReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: &events.FakeRecorder{},
			}
		})

		It("should add owner references for multiple Functions without overwriting", func() {
			functionA := &functionsdevv1alpha1.Function{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "function-a",
					Namespace: testNamespace,
				},
				Spec: functionsdevv1alpha1.FunctionSpec{
					Repository: functionsdevv1alpha1.FunctionSpecRepository{
						URL: "https://github.com/foo/bar",
					},
				},
			}
			Expect(k8sClient.Create(ctx, functionA)).To(Succeed())

			functionB := &functionsdevv1alpha1.Function{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "function-b",
					Namespace: testNamespace,
				},
				Spec: functionsdevv1alpha1.FunctionSpec{
					Repository: functionsdevv1alpha1.FunctionSpecRepository{
						URL: "https://github.com/foo/baz",
					},
				},
			}
			Expect(k8sClient.Create(ctx, functionB)).To(Succeed())

			By("Reconciling the RoleBinding for Function A")
			Expect(reconciler.ensureDeployFunctionRoleBinding(ctx, functionA)).To(Succeed())

			By("Verifying RoleBinding was created with Function A as owner")
			rb := &rbacv1.RoleBinding{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      deployFunctionRoleBindingName,
				Namespace: testNamespace,
			}, rb)).To(Succeed())
			Expect(rb.OwnerReferences).To(HaveLen(1))
			Expect(rb.OwnerReferences[0].Name).To(Equal("function-a"))

			By("Reconciling the RoleBinding for Function B")
			Expect(reconciler.ensureDeployFunctionRoleBinding(ctx, functionB)).To(Succeed())

			By("Verifying RoleBinding now has both Functions as owners")
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      deployFunctionRoleBindingName,
				Namespace: testNamespace,
			}, rb)).To(Succeed())
			Expect(rb.OwnerReferences).To(HaveLen(2))

			ownerNames := []string{rb.OwnerReferences[0].Name, rb.OwnerReferences[1].Name}
			Expect(ownerNames).To(ConsistOf("function-a", "function-b"))

			By("Verifying no owner is marked as controller")
			for _, ref := range rb.OwnerReferences {
				if ref.Controller != nil {
					Expect(*ref.Controller).To(BeFalse())
				}
			}

			By("Reconciling Function A again should be idempotent")
			Expect(reconciler.ensureDeployFunctionRoleBinding(ctx, functionA)).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      deployFunctionRoleBindingName,
				Namespace: testNamespace,
			}, rb)).To(Succeed())
			Expect(rb.OwnerReferences).To(HaveLen(2))
		})

		It("should set the expected Subjects and RoleRef", func() {
			function := &functionsdevv1alpha1.Function{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "function-rbac-check",
					Namespace: testNamespace,
				},
				Spec: functionsdevv1alpha1.FunctionSpec{
					Repository: functionsdevv1alpha1.FunctionSpecRepository{
						URL: "https://github.com/foo/bar",
					},
				},
			}
			Expect(k8sClient.Create(ctx, function)).To(Succeed())

			Expect(reconciler.ensureDeployFunctionRoleBinding(ctx, function)).To(Succeed())

			rb := &rbacv1.RoleBinding{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      deployFunctionRoleBindingName,
				Namespace: testNamespace,
			}, rb)).To(Succeed())

			Expect(rb.Subjects).To(HaveLen(1))
			Expect(rb.Subjects[0].Kind).To(Equal("ServiceAccount"))
			Expect(rb.Subjects[0].Name).To(Equal("default"))

			Expect(rb.RoleRef.APIGroup).To(Equal("rbac.authorization.k8s.io"))
			Expect(rb.RoleRef.Kind).To(Equal("Role"))
			Expect(rb.RoleRef.Name).To(Equal(deployFunctionRoleName))
		})
	})

	Context("ensureDeployFunctionRole", func() {
		var reconciler *FunctionReconciler
		var testNamespace string

		// func's keda deployer reads ScaledObjects on every keda deploy
		kedaRule := rbacv1.PolicyRule{
			APIGroups: []string{"keda.sh"},
			Resources: []string{"scaledobjects", "triggerauthentications"},
			Verbs:     []string{"create", "delete", "get", "list", "update"},
		}

		BeforeEach(func() {
			testNamespace = "rbac-role-test-" + rand.String(6)
			ns := &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}}
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())

			reconciler = &FunctionReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: &events.FakeRecorder{},
			}
		})

		It("should grant the keda deployer access to ScaledObjects and TriggerAuthentications", func() {
			Expect(reconciler.ensureDeployFunctionRole(ctx, testNamespace)).To(Succeed())

			role := &rbacv1.Role{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      deployFunctionRoleName,
				Namespace: testNamespace,
			}, role)).To(Succeed())
			Expect(role.Rules).To(ContainElement(kedaRule))
		})

		It("should add the keda.sh rule to an existing Role without it", func() {
			existing := &rbacv1.Role{
				ObjectMeta: metav1.ObjectMeta{
					Name:      deployFunctionRoleName,
					Namespace: testNamespace,
				},
				Rules: []rbacv1.PolicyRule{{
					APIGroups: []string{"http.keda.sh"},
					Resources: []string{"httpscaledobjects"},
					Verbs:     []string{"create", "delete", "get", "list", "patch", "update", "watch"},
				}},
			}
			Expect(k8sClient.Create(ctx, existing)).To(Succeed())

			Expect(reconciler.ensureDeployFunctionRole(ctx, testNamespace)).To(Succeed())

			role := &rbacv1.Role{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      deployFunctionRoleName,
				Namespace: testNamespace,
			}, role)).To(Succeed())
			Expect(role.Rules).To(ContainElement(kedaRule))
		})
	})
})
