package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTaskBranchRequiresID(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, FileName), []byte("task:\n  branch: feature/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil {
		t.Fatal("expected error")
	}
	if err := os.WriteFile(filepath.Join(root, FileName), []byte("task:\n  branch: \"feature/{id}-\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Task.Branch != "feature/{id}-" {
		t.Fatalf("branch = %q", cfg.Task.Branch)
	}
}
