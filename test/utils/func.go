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

package utils

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/functions-dev/func-operator/internal/funccli"
	ginkgo "github.com/onsi/ginkgo/v2"
)

// funcBin returns the func binary to invoke. It honors the FUNC_BIN environment
// variable (an absolute path or a name on PATH), defaulting to "func" on PATH.
// This lets manual runs point at a local build and CI point at a binary extracted
// from the operator image.
func funcBin() string {
	if b := os.Getenv("FUNC_BIN"); b != "" {
		return b
	}
	return "func"
}

// RunFunc executes the func CLI with the current/latest version
func RunFunc(command string, args ...string) (string, error) {
	allArgs := append([]string{command}, args...)
	cmd := exec.Command(funcBin(), allArgs...)
	return Run(cmd)
}

// RunFuncWithVersion executes the func CLI with a specific version
// It downloads and caches the version if not already present
func RunFuncWithVersion(version string, command string, args ...string) (string, error) {
	funcBinary, err := ensureFuncVersion(version)
	if err != nil {
		return "", err
	}

	allArgs := append([]string{command}, args...)
	cmd := exec.Command(funcBinary, allArgs...)
	return Run(cmd)
}

// funcDeployArgs assembles the `func deploy` flags from the resolved options.
func funcDeployArgs(functionDir string, opts *FuncDeployOptions) []string {
	args := []string{"--path", functionDir}

	// --image and --registry are mutually exclusive in func. With a pre-built
	// image (digest) func skips build/push, so the registry flags don't apply.
	if opts.Image != "" {
		// Deploy a pre-built image as-is: no build, no push.
		args = append(args, "--image", opts.Image, "--build=false", "--push=false")
	} else {
		args = append(args,
			"--registry", opts.Registry,
			fmt.Sprintf("--registry-insecure=%t", opts.RegistryInsecure),
		)
	}

	if opts.Namespace != "" {
		args = append(args, "--namespace", opts.Namespace)
	}

	// A pre-built image with a digest disables build/push, so the builder is
	// irrelevant in that case.
	if opts.Builder != "" && opts.Image == "" {
		args = append(args, "--builder", opts.Builder)
	}

	if opts.Deployer != "" && opts.Deployer != "knative" {
		args = append(args, "--deployer", opts.Deployer)
	}

	return args
}

// RunFuncDeploy runs func deploy with retry logic for transient network errors
func RunFuncDeploy(functionDir string, optFns ...FuncDeployOption) (string, error) {
	opts := &FuncDeployOptions{
		// defaults
		Registry:         Registry(),
		RegistryInsecure: IsRegistryInsecure(),
		Builder:          os.Getenv("DEFAULT_BUILDER"),
		Deployer:         os.Getenv("DEFAULT_DEPLOYER"),
	}

	for _, optFn := range optFns {
		optFn(opts)
	}

	args := funcDeployArgs(functionDir, opts)

	// When using the pack builder, create a per-deploy PACK_HOME to prevent
	// parallel builds from corrupting the shared ~/.pack/volume-keys.toml.
	if opts.Builder == "pack" {
		packHome, err := os.MkdirTemp("", "pack-home-*")
		if err != nil {
			return "", fmt.Errorf("failed to create PACK_HOME: %w", err)
		}
		defer os.RemoveAll(packHome)

		if opts.EnvVars == nil {
			opts.EnvVars = make(map[string]string)
		}
		opts.EnvVars["PACK_HOME"] = packHome
	}

	var output string
	var err error

	maxAttempts := 5
	retryDelay := 10 * time.Second

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			_, _ = fmt.Fprintf(ginkgo.GinkgoWriter,
				"func deploy attempt %d/%d failed: %v (retrying in %s)\n",
				attempt, maxAttempts, err, retryDelay)
			time.Sleep(retryDelay)
			retryDelay *= 2
		}

		var funcBinary string
		if opts.CliVersion != "" {
			funcBinary, err = ensureFuncVersion(opts.CliVersion)
			if err != nil {
				return "", err
			}
		} else {
			funcBinary = funcBin()
		}

		cmd := exec.Command(funcBinary, append([]string{"deploy"}, args...)...)
		for k, v := range opts.EnvVars {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
		}
		output, err = Run(cmd)

		if err == nil {
			return output, nil
		}
	}

	return output, err
}

type FuncDeployOptions struct {
	Registry         string
	RegistryInsecure bool
	Namespace        string
	Builder          string
	Deployer         string
	CliVersion       string
	EnvVars          map[string]string
	// Image, when set to a ref including a digest, makes func deploy the given
	// pre-built image and skip the build/push phases. Used to seed a function
	// with a specific (e.g. stale-middleware) image.
	Image string
}

type FuncDeployOption func(*FuncDeployOptions)

func WithNamespace(namespace string) FuncDeployOption {
	return func(opts *FuncDeployOptions) {
		opts.Namespace = namespace
	}
}

func WithBuilder(builder string) FuncDeployOption {
	return func(o *FuncDeployOptions) {
		o.Builder = builder
	}
}

func WithDeployer(deployer string) FuncDeployOption {
	return func(o *FuncDeployOptions) {
		o.Deployer = deployer
	}
}

// WithImage deploys a pre-built image (ref should include a digest so func skips
// build/push), rather than building from source.
func WithImage(image string) FuncDeployOption {
	return func(o *FuncDeployOptions) {
		o.Image = image
	}
}

func WithDeployCliVersion(version string) FuncDeployOption {
	return func(opts *FuncDeployOptions) {
		opts.CliVersion = version
	}
}

func WithEnvVars(envVars map[string]string) FuncDeployOption {
	return func(opts *FuncDeployOptions) {
		opts.EnvVars = envVars
	}
}

// ensureFuncVersion ensures the specified func version is available and returns its path.
// Uses file locking to prevent parallel Ginkgo processes from racing on the download.
func ensureFuncVersion(version string) (string, error) {
	projectDir, err := GetProjectDir()
	if err != nil {
		return "", fmt.Errorf("failed to get project directory: %w", err)
	}

	versionDir := filepath.Join(projectDir, "bin", "func-cli", version)
	funcBinary := filepath.Join(versionDir, "func")

	// Fast path: binary already exists, no lock needed
	if _, err := os.Stat(funcBinary); err == nil {
		return funcBinary, nil
	}

	// Ensure the directory exists before creating the lock file
	if err := os.MkdirAll(versionDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create version directory: %w", err)
	}

	// Acquire an exclusive file lock so only one Ginkgo process downloads at a time.
	// Ginkgo's -p flag runs specs in separate OS processes, so sync.Mutex doesn't work.
	lockFile, err := os.OpenFile(filepath.Join(versionDir, ".lock"), os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return "", fmt.Errorf("failed to create lock file: %w", err)
	}
	defer lockFile.Close()

	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return "", fmt.Errorf("failed to acquire file lock: %w", err)
	}
	defer syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN) //nolint:errcheck

	// Re-check after acquiring the lock - another process may have finished the download
	if _, err := os.Stat(funcBinary); err == nil {
		return funcBinary, nil
	}

	// Download the version
	if err := downloadFuncVersion(version, funcBinary); err != nil {
		return "", err
	}

	return funcBinary, nil
}

// downloadFuncVersion downloads the specified func version from GitHub releases.
// It writes to a temporary file first and atomically renames it to avoid exposing
// a partially-written binary to other processes.
func downloadFuncVersion(version, funcBinary string) error {
	asset := funccli.AssetName()
	base := "https://github.com/knative/func/releases/download/knative-" + version
	binaryURL := base + "/" + asset
	checksumURL := base + "/checksums.txt"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if err := funccli.DownloadAndInstall(ctx, binaryURL, checksumURL, funcBinary, nil); err != nil {
		return fmt.Errorf("failed to download func %s: %w", version, err)
	}

	return nil
}
