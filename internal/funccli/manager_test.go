package funccli

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/go-logr/logr"
)

// newFakeCLIManager returns a manager whose func binary writes its arguments,
// one per line, to the returned file.
func newFakeCLIManager(t *testing.T) (*managerImpl, string) {
	t.Helper()

	dir := t.TempDir()
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$(dirname \"$0\")/args\"\n"
	if err := os.WriteFile(filepath.Join(dir, binaryName), []byte(script), 0755); err != nil {
		t.Fatalf("failed to write fake func binary: %v", err)
	}

	m, err := NewManager(logr.Discard(), dir, 0, true)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	return m, filepath.Join(dir, "args")
}

func TestDeploy_Args(t *testing.T) {
	tests := []struct {
		name string
		opts DeployOptions
		want []string
	}{
		{
			name: "no options",
			want: []string{"deploy", "--remote", "--namespace", "ns", "--management-disabled"},
		},
		{
			name: "registry auth file and image pull secret",
			opts: DeployOptions{RegistryAuthFile: "/auth.json", ImagePullSecret: "pull-secret"},
			want: []string{"deploy", "--remote", "--namespace", "ns", "--management-disabled",
				"--registry-authfile", "/auth.json", "--image-pull-secret", "pull-secret"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, argsFile := newFakeCLIManager(t)

			if err := m.Deploy(context.Background(), t.TempDir(), "ns", tt.opts); err != nil {
				t.Fatalf("Deploy() error = %v", err)
			}

			out, err := os.ReadFile(argsFile)
			if err != nil {
				t.Fatalf("failed to read func arguments: %v", err)
			}

			got := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
			if !slices.Equal(got, tt.want) {
				t.Errorf("func called with %q, want %q", got, tt.want)
			}
		})
	}
}
