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

	"code.gitea.io/sdk/gitea"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	giteaAdminUser = "giteaadmin"
	giteaAdminPass = "giteapass"
)

// RepositoryProvider defines the interface for interacting with Git repository hosting providers
type RepositoryProvider interface {
	// User management
	CreateUser(username, password, email string) error
	DeleteUser(username string) error
	CreateRandomUser() (username, password, email string, err error)

	// Repository management
	CreateRepo(owner, name string, private bool) (string, error)
	DeleteRepo(owner, name string) error
	CreateRandomRepo(owner string, private bool) (name, url string, err error)

	// Authentication
	CreateAccessToken(username, password, tokenName string) (string, error)
}

// GiteaClient wraps the Gitea SDK client and provides helper methods
type GiteaClient struct {
	client    *gitea.Client
	baseURL   string
	adminUser string
	adminPass string
}

// NewGiteaClient discovers Gitea endpoint from ConfigMap and creates client
func NewGiteaClient() (*GiteaClient, error) {
	// Load kubeconfig
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

	cfg, err := kubeConfig.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load kubeconfig: %w", err)
	}

	// Create Kubernetes client
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	// Get gitea-endpoint ConfigMap
	cm, err := clientset.CoreV1().ConfigMaps("kube-public").Get(context.Background(), "gitea-endpoint", metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get gitea-endpoint configmap: %w", err)
	}

	baseURL, ok := cm.Data["http"]
	if !ok {
		return nil, fmt.Errorf("gitea-endpoint configmap missing 'http' key")
	}

	// Create Gitea SDK client
	giteaClient, err := gitea.NewClient(baseURL, gitea.SetBasicAuth(giteaAdminUser, giteaAdminPass))
	if err != nil {
		return nil, fmt.Errorf("failed to create gitea client: %w", err)
	}

	return &GiteaClient{
		client:    giteaClient,
		baseURL:   baseURL,
		adminUser: giteaAdminUser,
		adminPass: giteaAdminPass,
	}, nil
}
