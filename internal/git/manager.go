package git

import (
	"context"
	"fmt"
	"os"

	"github.com/functions-dev/func-operator/internal/monitoring"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/client"
	"github.com/go-git/go-git/v6/plumbing/transport"
	"github.com/go-git/go-git/v6/plumbing/transport/http"
	"github.com/go-git/go-git/v6/plumbing/transport/ssh"
	"github.com/go-git/go-git/v6/plumbing/transport/ssh/knownhosts"
	"github.com/prometheus/client_golang/prometheus"
	gossh "golang.org/x/crypto/ssh"
)

const (
	cloneBaseDir = "/git-repos"
)

type Manager interface {
	CloneRepository(ctx context.Context, url, subPath, reference string, auth map[string][]byte) (*Repository, error)
}

func NewManager() (Manager, error) {
	if err := os.MkdirAll(cloneBaseDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create git clone base directory: %w", err)
	}
	return &managerImpl{}, nil
}

type managerImpl struct{}

func (m *managerImpl) CloneRepository(ctx context.Context, repoUrl, subPath, reference string, auth map[string][]byte) (*Repository, error) {
	timer := prometheus.NewTimer(monitoring.GitCloneDuration)
	defer timer.ObserveDuration()

	parsedURL, err := transport.ParseURL(repoUrl)
	if err != nil {
		return nil, fmt.Errorf("failed to parse repository URL: %w", err)
	}

	targetDir, err := os.MkdirTemp(cloneBaseDir, "repo-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temporary directory: %w", err)
	}

	repo, err := git.PlainCloneContext(ctx, targetDir, &git.CloneOptions{
		URL:           repoUrl,
		ReferenceName: plumbing.ReferenceName(reference),
		SingleBranch:  true,
		Depth:         1,
		ClientOptions: m.getClientOptions(parsedURL.Scheme, auth),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to clone repo: %w", err)
	}

	head, err := repo.Head()
	if err != nil {
		return nil, fmt.Errorf("failed to find head: %w", err)
	}

	return &Repository{
		CloneDir: targetDir,
		SubPath:  subPath,
		Commit:   head.Hash().String(),
		Branch:   reference,
	}, nil
}

func (m *managerImpl) getClientOptions(scheme string, authSecret map[string][]byte) []client.Option {
	if scheme == "ssh" {
		return m.getSSHClientOptions(authSecret)
	}
	return m.getHTTPClientOptions(authSecret)
}

func (m *managerImpl) getHTTPClientOptions(authSecret map[string][]byte) []client.Option {
	if len(authSecret) == 0 {
		return nil
	} else if token, ok := authSecret["token"]; ok {
		return []client.Option{
			client.WithHTTPAuth(&http.BasicAuth{
				Username: "empty", // can be anything except an empty string
				Password: string(token),
			}),
		}
	} else if username, ok := authSecret["username"]; ok {
		if password, ok := authSecret["password"]; ok {
			return []client.Option{
				client.WithHTTPAuth(&http.BasicAuth{
					Username: string(username),
					Password: string(password),
				}),
			}
		}
		return nil
	}
	return nil
}

func (m *managerImpl) getSSHClientOptions(authSecret map[string][]byte) []client.Option {
	privateKey, hasKey := authSecret["sshPrivateKey"]
	if !hasKey {
		return []client.Option{
			client.WithSSHAuth(&ssh.PublicKeys{
				User: "git",
				HostKeyCallbackHelper: ssh.HostKeyCallbackHelper{
					HostKeyCallback: gossh.InsecureIgnoreHostKey(),
				},
			}),
		}
	}

	password := string(authSecret["sshPrivateKeyPassword"])

	auth, err := ssh.NewPublicKeys("git", privateKey, password)
	if err != nil {
		return nil
	}

	if knownHostsData, ok := authSecret["known_hosts"]; ok {
		tmpFile, err := os.CreateTemp("", "known_hosts-*")
		if err == nil {
			defer os.Remove(tmpFile.Name())
			if _, err := tmpFile.Write(knownHostsData); err == nil {
				tmpFile.Close()
				db, err := knownhosts.NewDB(tmpFile.Name())
				if err == nil {
					auth.HostKeyCallback = db.HostKeyCallback()
				}
			}
		}
	} else {
		auth.HostKeyCallback = gossh.InsecureIgnoreHostKey()
	}

	return []client.Option{
		client.WithSSHAuth(auth),
	}
}
