/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package e2e

import (
	"fmt"
	"os"
	"time"

	functionsdevv1alpha1 "github.com/functions-dev/func-operator/api/v1alpha1"
	"github.com/functions-dev/func-operator/test/utils"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// STALE_FUNCTION_IMAGE must point at a pre-built, publicly pullable function image
// whose "middleware-version" label is older than the operator's current version.
// The operator reads that label off the deployed image, finds it stale, and
// rebuilds the function from source. The spec is skipped when it is unset.
//
// Why a pre-built image (and not a build step): the middleware version is baked
// by the func image, which defaults to a floating "latest" tag, so there's
// no way to produce a *stale* build on the fly. The image is built once as
// `FROM <current function image>` + `LABEL middleware-version=<old>` and stored in
// a durable public repo. The image ref is intentionally not hardcoded here so the
// same test works across upstream/downstream with each CI supplying its own image.
func staleFunctionImage() string {
	return os.Getenv("STALE_FUNCTION_IMAGE")
}

var _ = Describe("Middleware Rebuild", func() {

	Context("with a function deployed from a stale-middleware image", func() {
		var repoURL string
		var repoDir string
		var functionName, functionNamespace string

		BeforeEach(func() {
			// Opt-in: requires a pre-built stale-middleware  image.
			if staleFunctionImage() == "" {
				Skip("set STALE_FUNCTION_IMAGE to a function image whose middleware-version label is stale")
			}

			username, password, _, cleanup, err := repoProvider.CreateRandomUser()
			Expect(err).NotTo(HaveOccurred())
			utils.DeferCleanupOnSuccess(cleanup)

			_, repoURL, cleanup, err = repoProvider.CreateRandomRepo(username, false)
			Expect(err).NotTo(HaveOccurred())
			utils.DeferCleanupOnSuccess(cleanup)

			repoDir, err = utils.InitializeRepoWithFunction(repoURL, username, password, "go")
			Expect(err).NotTo(HaveOccurred())
			utils.DeferCleanupOnSuccess(os.RemoveAll, repoDir)

			functionNamespace, err = utils.GetTestNamespace()
			Expect(err).NotTo(HaveOccurred())
			utils.DeferCleanupOnSuccess(cleanupNamespaces, functionNamespace)

			// Seed the deployment with the pre-built stale image (func skips build
			// because the ref carries a digest), so the operator sees outdated
			// middleware and must rebuild from the repo.
			out, err := utils.RunFuncDeploy(repoDir,
				utils.WithNamespace(functionNamespace),
				utils.WithImage(staleFunctionImage()))
			Expect(err).NotTo(HaveOccurred())
			_, _ = fmt.Fprint(GinkgoWriter, out)

			utils.DeferCleanupOnSuccess(func() {
				_, _ = utils.RunFunc("delete", "--path", repoDir, "--namespace", functionNamespace)
			})

			err = utils.CommitAndPush(repoDir, "Update func.yaml after deploy", "func.yaml")
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			logFailedTestDetails(functionName, functionNamespace)
		})

		It("should rebuild the function and mark middleware up to date", func() {
			function := &functionsdevv1alpha1.Function{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "my-function-",
					Namespace:    functionNamespace,
				},
				Spec: functionsdevv1alpha1.FunctionSpec{
					Repository: functionsdevv1alpha1.FunctionSpecRepository{
						URL: repoURL,
					},
				},
			}

			err := k8sClient.Create(ctx, function)
			Expect(err).NotTo(HaveOccurred())
			utils.DeferCleanupOnSuccess(func() {
				_, _ = utils.RunCmd("kubectl", "delete", "function", function.Name, "--namespace", function.Namespace)
			})
			functionName = function.Name

			// The operator detects the stale middleware label, rebuilds from the
			// repo, and the function becomes ready and up to date.
			Eventually(functionBecomesReady(functionName, functionNamespace)).Should(Succeed())
			Eventually(functionMiddlewareUpToDate(functionName, functionNamespace), 2*time.Minute).Should(Succeed())
		})
	})
})
