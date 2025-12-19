package git

import (
	"context"
	"fmt"
	neturl "net/url"
	"os"
	"strings"

	"github.com/creydr/func-operator/internal/monitoring"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	cloneBaseDir = "/git-repos"
)

type Manager interface {
	CloneRepository(ctx context.Context, url, reference string) (*Repository, error)
}

func NewManager() Manager {
	return &managerImpl{}
}

type managerImpl struct{}

func (*managerImpl) CloneRepository(ctx context.Context, repoUrl, reference string) (*Repository, error) {
	timer := prometheus.NewTimer(monitoring.GitCloneDuration)
	defer timer.ObserveDuration()

	url, err := neturl.Parse(repoUrl)
	if err != nil {
		return nil, fmt.Errorf("failed to parse repository URL: %w", err)
	}

	pattern := fmt.Sprintf("%s-%s-%s", url.Host, strings.ReplaceAll(strings.TrimSuffix(url.Path, ".git"), "/", "-"), reference)
	targetDir, err := os.MkdirTemp(cloneBaseDir, pattern)
	if err != nil {
		return nil, fmt.Errorf("failed to create temporary directory: %w", err)
	}

	_, err = git.PlainCloneContext(ctx, targetDir, &git.CloneOptions{
		URL:           repoUrl,
		ReferenceName: plumbing.ReferenceName(reference),
		SingleBranch:  true,
		Depth:         1,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to clone repo: %w", err)
	}

	return &Repository{
		CloneDir: targetDir,
	}, nil
}
