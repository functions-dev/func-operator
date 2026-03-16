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
	"strings"

	"github.com/functions-dev/func-operator/test/utils"
	"k8s.io/apimachinery/pkg/util/rand"
)

// buildAuthURL embeds credentials into git URL for authenticated operations
func buildAuthURL(repoURL, username, password string) string {
	return strings.Replace(repoURL, "http://",
		fmt.Sprintf("http://%s:%s@", username, password), 1)
}

// InitializeRepoWithFunction clones an empty Gitea repo, initializes a function, and pushes it
func InitializeRepoWithFunction(repoURL, username, password, language string) (repoDir string, err error) {
	repoDir = fmt.Sprintf("%s/func-test-%s", os.TempDir(), rand.String(10))

	// Build authenticated URL
	authURL := buildAuthURL(repoURL, username, password)

	// Clone empty repo
	cmd := exec.Command("git", "clone", authURL, repoDir)
	if _, err = utils.Run(cmd); err != nil {
		return "", fmt.Errorf("failed to clone repo: %w", err)
	}

	// Initialize function
	cmd = exec.Command("func", "init", "-l", language)
	cmd.Dir = repoDir
	if _, err = utils.Run(cmd); err != nil {
		return "", fmt.Errorf("failed to init function: %w", err)
	}

	// Commit and push
	if err = exec.Command("git", "-C", repoDir, "add", ".").Run(); err != nil {
		return "", fmt.Errorf("failed to git add: %w", err)
	}
	if err = exec.Command("git", "-C", repoDir, "commit", "-m", "Initial function").Run(); err != nil {
		return "", fmt.Errorf("failed to git commit: %w", err)
	}
	if err = exec.Command("git", "-C", repoDir, "push").Run(); err != nil {
		return "", fmt.Errorf("failed to push initial commit: %w", err)
	}

	return repoDir, nil
}

// CommitAndPush commits and pushes specified files with a custom message
// Requires at least one file to be specified
func CommitAndPush(repoDir string, msg string, file string, otherFiles ...string) error {
	// Add first file
	if err := exec.Command("git", "-C", repoDir, "add", file).Run(); err != nil {
		return fmt.Errorf("failed to git add %s: %w", file, err)
	}

	// Add other files if provided
	for _, f := range otherFiles {
		if err := exec.Command("git", "-C", repoDir, "add", f).Run(); err != nil {
			return fmt.Errorf("failed to git add %s: %w", f, err)
		}
	}

	// Commit
	if err := exec.Command("git", "-C", repoDir, "commit", "-m", msg).Run(); err != nil {
		return fmt.Errorf("failed to git commit: %w", err)
	}

	// Push
	if err := exec.Command("git", "-C", repoDir, "push").Run(); err != nil {
		return fmt.Errorf("failed to push: %w", err)
	}

	return nil
}