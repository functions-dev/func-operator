package git

import (
	"os"
	"path"
)

type Repository struct {
	CloneDir  string
	SubPath   string
	Commit    string
	Branch    string
	tempFiles []string
}

func (r *Repository) AddTempFile(path string) {
	if path != "" {
		r.tempFiles = append(r.tempFiles, path)
	}
}

func (r *Repository) Path() string {
	return path.Join(r.CloneDir, r.SubPath)
}

func (r *Repository) Cleanup() error {
	for _, f := range r.tempFiles {
		os.Remove(f)
	}
	return os.RemoveAll(r.CloneDir)
}
