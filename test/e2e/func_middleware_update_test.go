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
	"os/exec"
	"strconv"
	"time"

	functionsdevv1alpha1 "github.com/functions-dev/func-operator/api/v1alpha1"
	"github.com/functions-dev/func-operator/test/utils"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var _ = Describe("Middleware Update", func() {

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	Context("with a function deployed using old func CLI", func() {
		var repoURL string
		var repoDir string
		var functionName, functionNamespace string

		BeforeEach(func() {
			var err error

			// Create repository provider resources with automatic cleanup
			username, password, _, cleanup, err := repoProvider.CreateRandomUser()
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(cleanup)

			_, repoURL, cleanup, err = repoProvider.CreateRandomRepo(username, false)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(cleanup)

			// Initialize repository with function code
			repoDir, err = utils.InitializeRepoWithFunction(repoURL, username, password, "go")
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(os.RemoveAll, repoDir)

			functionNamespace, err = utils.GetTestNamespace()
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(cleanupNamespaces, functionNamespace)

			// Deploy function using OLD func CLI version
			out, err := utils.RunFuncWithVersion("v1.20.0", "deploy",
				"--namespace", functionNamespace,
				"--path", repoDir,
				"--registry", registry,
				"--registry-insecure", strconv.FormatBool(registryInsecure))
			Expect(err).NotTo(HaveOccurred())
			_, _ = fmt.Fprint(GinkgoWriter, out)

			// Cleanup func deployment
			DeferCleanup(func() {
				_, _ = utils.RunFunc("delete", "--path", repoDir, "--namespace", functionNamespace)
			})

			// Commit func.yaml changes
			err = utils.CommitAndPush(repoDir, "Update func.yaml after deploy", "func.yaml")
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			specReport := CurrentSpecReport()
			if specReport.Failed() {
				if functionName != "" {
					cmd := exec.Command("kubectl", "get", "function", functionName, "-n", functionNamespace, "-o", "yaml")
					function, err := utils.Run(cmd)
					if err == nil {
						_, _ = fmt.Fprintf(GinkgoWriter, "Function:\n %s", function)
					} else {
						_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get function: %s", err)
					}
				}

				By("Fetching controller manager pod logs")
				cmd := exec.Command("kubectl", "logs", "-l", "control-plane=controller-manager", "-n", namespace)
				controllerLogs, err := utils.Run(cmd)
				if err == nil {
					_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n %s", controllerLogs)
				} else {
					_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Controller logs: %s", err)
				}
			}

			// Cleanup function resource
			if functionName != "" {
				cmd := exec.Command("kubectl", "delete", "function", functionName, "-n", functionNamespace, "--ignore-not-found")
				_, err := utils.Run(cmd)
				Expect(err).NotTo(HaveOccurred())
			}
		})

		It("should update the middleware and mark the function as ready", func() {
			// Create a Function resource
			function := &functionsdevv1alpha1.Function{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "my-function-",
					Namespace:    functionNamespace,
				},
				Spec: functionsdevv1alpha1.FunctionSpec{
					Source: functionsdevv1alpha1.FunctionSpecSource{
						RepositoryURL: repoURL,
					},
					Registry: functionsdevv1alpha1.FunctionSpecRegistry{
						Path:     registry,
						Insecure: registryInsecure,
					},
				},
			}

			err := k8sClient.Create(ctx, function)
			Expect(err).NotTo(HaveOccurred())

			functionName = function.Name

			funcBecomeReady := func(g Gomega) {
				fn := &functionsdevv1alpha1.Function{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: function.Name, Namespace: function.Namespace}, fn)
				g.Expect(err).NotTo(HaveOccurred())

				for _, cond := range fn.Status.Conditions {
					if cond.Type == functionsdevv1alpha1.TypeReady {
						g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
						return
					}
				}
				g.Expect(false).To(BeTrue(), "Ready condition not found")
			}

			// Middleware update could take a bit longer therefore give more time
			Eventually(funcBecomeReady, 6*time.Minute).Should(Succeed())
		})
	})
})
