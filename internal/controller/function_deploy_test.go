package controller

import (
	functionsdevv1alpha1 "github.com/functions-dev/func-operator/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/client-go/tools/events"
)

var _ = Describe("Function Deploy", func() {
	Context("ensureImagePullSecret", func() {
		var reconciler *FunctionReconciler
		var testNamespace string

		BeforeEach(func() {
			testNamespace = "deploy-test-" + rand.String(6)
			ns := &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}}
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())

			sa := &v1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: testNamespace}}
			Expect(k8sClient.Create(ctx, sa)).To(Succeed())

			reconciler = &FunctionReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: &events.FakeRecorder{},
			}
		})

		It("should add the registry auth secret to the default ServiceAccount's imagePullSecrets", func() {
			function := &functionsdevv1alpha1.Function{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "func-pull-secret",
					Namespace: testNamespace,
				},
				Spec: functionsdevv1alpha1.FunctionSpec{
					Repository: functionsdevv1alpha1.FunctionSpecRepository{
						URL: "https://github.com/foo/bar",
					},
					Registry: functionsdevv1alpha1.FunctionSpecRegistry{
						AuthSecretRef: &v1.LocalObjectReference{
							Name: "my-registry-secret",
						},
					},
				},
			}

			Expect(reconciler.ensureImagePullSecret(ctx, function)).To(Succeed())

			sa := &v1.ServiceAccount{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "default",
				Namespace: testNamespace,
			}, sa)).To(Succeed())

			Expect(sa.ImagePullSecrets).To(ContainElement(v1.LocalObjectReference{
				Name: "my-registry-secret",
			}))
		})

		It("should be idempotent and not duplicate imagePullSecrets", func() {
			function := &functionsdevv1alpha1.Function{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "func-idempotent",
					Namespace: testNamespace,
				},
				Spec: functionsdevv1alpha1.FunctionSpec{
					Repository: functionsdevv1alpha1.FunctionSpecRepository{
						URL: "https://github.com/foo/bar",
					},
					Registry: functionsdevv1alpha1.FunctionSpecRegistry{
						AuthSecretRef: &v1.LocalObjectReference{
							Name: "my-registry-secret",
						},
					},
				},
			}

			Expect(reconciler.ensureImagePullSecret(ctx, function)).To(Succeed())
			Expect(reconciler.ensureImagePullSecret(ctx, function)).To(Succeed())

			sa := &v1.ServiceAccount{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "default",
				Namespace: testNamespace,
			}, sa)).To(Succeed())

			count := 0
			for _, ref := range sa.ImagePullSecrets {
				if ref.Name == "my-registry-secret" {
					count++
				}
			}
			Expect(count).To(Equal(1))
		})

		It("should preserve existing imagePullSecrets on the ServiceAccount", func() {
			sa := &v1.ServiceAccount{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "default",
				Namespace: testNamespace,
			}, sa)).To(Succeed())

			sa.ImagePullSecrets = []v1.LocalObjectReference{
				{Name: "existing-secret"},
			}
			Expect(k8sClient.Update(ctx, sa)).To(Succeed())

			function := &functionsdevv1alpha1.Function{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "func-preserve",
					Namespace: testNamespace,
				},
				Spec: functionsdevv1alpha1.FunctionSpec{
					Repository: functionsdevv1alpha1.FunctionSpecRepository{
						URL: "https://github.com/foo/bar",
					},
					Registry: functionsdevv1alpha1.FunctionSpecRegistry{
						AuthSecretRef: &v1.LocalObjectReference{
							Name: "my-registry-secret",
						},
					},
				},
			}

			Expect(reconciler.ensureImagePullSecret(ctx, function)).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "default",
				Namespace: testNamespace,
			}, sa)).To(Succeed())

			Expect(sa.ImagePullSecrets).To(HaveLen(2))
			Expect(sa.ImagePullSecrets).To(ContainElement(v1.LocalObjectReference{Name: "existing-secret"}))
			Expect(sa.ImagePullSecrets).To(ContainElement(v1.LocalObjectReference{Name: "my-registry-secret"}))
		})
	})
})
