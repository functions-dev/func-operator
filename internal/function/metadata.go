package function

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
	fn "knative.dev/func/pkg/functions"
)

func Metadata(repoPath string) (fn.Function, error) {
	funcYaml, err := os.ReadFile(fmt.Sprintf("%s/%s", repoPath, "func.yaml"))
	if err != nil {
		return fn.Function{}, fmt.Errorf("failed to read func.yaml: %w", err)
	}

	metadata := fn.Function{}
	if err = yaml.Unmarshal(funcYaml, &metadata); err != nil {
		return fn.Function{}, fmt.Errorf("failed to parse func.yaml: %w", err)
	}

	return metadata, nil
}
