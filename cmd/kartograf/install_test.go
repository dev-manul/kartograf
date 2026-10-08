package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMergeCodexHooks(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".codex", "hooks.json")
	wrote, err := mergeCommandHooks(path, "/bin/kartograf", root, "UserPromptSubmit", "Stop")
	if err != nil || !wrote {
		t.Fatalf("wrote=%v err=%v", wrote, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "UserPromptSubmit") || !strings.Contains(text, "Stop") || !strings.Contains(text, "hook --root "+root) {
		t.Fatalf("hooks.json:\n%s", text)
	}
	wrote, err = mergeCommandHooks(path, "/bin/kartograf", root, "UserPromptSubmit", "Stop")
	if err != nil || wrote {
		t.Fatalf("second wrote=%v err=%v", wrote, err)
	}
}
