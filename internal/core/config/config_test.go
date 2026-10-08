package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnrichURLRequiresCommit(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, FileName), []byte("enrich:\n  url: http://example.com/x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil {
		t.Fatal("expected error")
	}
	if err := os.WriteFile(filepath.Join(root, FileName), []byte("enrich:\n  url: \"https://example.com/{commit}/enrich.phpstan.jsonl\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cfg.Enrich.URL, "{commit}") {
		t.Fatalf("url = %q", cfg.Enrich.URL)
	}
}

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
