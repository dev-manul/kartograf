package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

func newInstallCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "install <claude|cursor|codex|hook> [root]",
		Short: "Register kartograf as an MCP server for a client",
		Long: `Registers this binary as a stdio MCP server for the given project root
(default: current directory).

  claude — runs "claude mcp add kartograf" (local project scope)
  cursor — writes/merges <root>/.cursor/mcp.json with type=stdio and
           absolute paths (Cursor does not expand ~), and a
           beforeSubmitPrompt hook that injects the branch handoff
  codex  — merges UserPromptSubmit and Stop hooks into <root>/.codex/hooks.json
  hook   — merges UserPromptSubmit and Stop hooks into <root>/.claude/settings.json
           that inject the current branch's working note and surface
           indexed symbols mentioned in each prompt`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			root := "."
			if len(args) == 2 {
				root = args[1]
			}
			absRoot, err := filepath.Abs(root)
			if err != nil {
				return err
			}
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			if exe, err = filepath.EvalSymlinks(exe); err != nil {
				return err
			}
			switch args[0] {
			case "claude":
				return installClaude(exe, absRoot)
			case "cursor":
				return installCursor(exe, absRoot)
			case "codex":
				return installCodex(exe, absRoot)
			case "hook":
				return installHook(exe, absRoot)
			default:
				return fmt.Errorf("unknown client %q (want claude, cursor, codex or hook)", args[0])
			}
		},
	}
	return cmd
}

func installClaude(exe, root string) error {
	claude, err := exec.LookPath("claude")
	if err != nil {
		fmt.Println("claude CLI not found in PATH; register manually:")
		fmt.Printf("  claude mcp add kartograf -- %s serve %s\n", exe, root)
		return nil
	}
	c := exec.Command(claude, "mcp", "add", "kartograf", "--", exe, "serve", root)
	c.Dir = root
	out, err := c.CombinedOutput()
	if err != nil {
		return fmt.Errorf("claude mcp add: %w: %s", err, out)
	}
	fmt.Printf("registered kartograf for %s (local scope); restart the session to pick it up\n", root)
	return nil
}

func installCursor(exe, root string) error {
	path := filepath.Join(root, ".cursor", "mcp.json")
	cfg := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return fmt.Errorf("%s exists but is not valid JSON: %w", path, err)
		}
	}
	servers, _ := cfg["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	servers["kartograf"] = map[string]any{
		"type":    "stdio",
		"command": exe,
		"args":    []string{"serve", root},
	}
	cfg["mcpServers"] = servers

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return err
	}
	if err := installCursorHook(exe, root); err != nil {
		return err
	}
	fmt.Printf("wrote %s; reload the MCP list in Cursor (Settings → MCP)\n", path)
	return nil
}

// installCursorHook merges a beforeSubmitPrompt hook that injects the
// branch handoff on the first prompt of a Cursor chat.
func installCursorHook(exe, root string) error {
	path := filepath.Join(root, ".cursor", "hooks.json")
	cfg := map[string]any{"version": 1}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return fmt.Errorf("%s exists but is not valid JSON: %w", path, err)
		}
	}
	hooks, _ := cfg["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	entries, _ := hooks["beforeSubmitPrompt"].([]any)
	command := fmt.Sprintf("%s hook --root %s --cursor", exe, root)
	for _, e := range entries {
		if strings.Contains(fmt.Sprint(e), " hook --root ") {
			fmt.Printf("a kartograf hook is already configured in %s\n", path)
			return nil
		}
	}
	entries = append(entries, map[string]any{"command": command})
	hooks["beforeSubmitPrompt"] = entries
	cfg["hooks"] = hooks
	if cfg["version"] == nil {
		cfg["version"] = 1
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s; the handoff shows up on the next Cursor chat\n", path)
	return nil
}

// ensureCommandHook appends a command hook for event unless one is
// already present. It reports whether it changed the map.
func ensureCommandHook(hooks map[string]any, event, command string) bool {
	entries, _ := hooks[event].([]any)
	for _, e := range entries {
		if strings.Contains(fmt.Sprint(e), " hook --root ") {
			return false
		}
	}
	hooks[event] = append(entries, map[string]any{
		"hooks": []any{map[string]any{"type": "command", "command": command}},
	})
	return true
}

func installCodex(exe, root string) error {
	path := filepath.Join(root, ".codex", "hooks.json")
	wrote, err := mergeCommandHooks(path, exe, root, "UserPromptSubmit", "Stop")
	if err != nil {
		return err
	}
	if wrote {
		fmt.Printf("wrote %s; in Codex run /hooks to trust it, and enable features.hooks if hooks are off\n", path)
	}
	return nil
}

func installHook(exe, root string) error {
	path := filepath.Join(root, ".claude", "settings.json")
	wrote, err := mergeCommandHooks(path, exe, root, "UserPromptSubmit", "Stop")
	if err != nil {
		return err
	}
	if wrote {
		fmt.Printf("wrote %s; the hook activates on the next session\n", path)
	}
	return nil
}

func mergeCommandHooks(path, exe, root string, events ...string) (bool, error) {
	cfg := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return false, fmt.Errorf("%s exists but is not valid JSON: %w", path, err)
		}
	}
	hooks, _ := cfg["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	command := fmt.Sprintf("%s hook --root %s", exe, root)
	added := false
	for _, event := range events {
		if ensureCommandHook(hooks, event, command) {
			added = true
		}
	}
	if !added {
		fmt.Printf("a kartograf hook is already configured in %s\n", path)
		return false, nil
	}
	cfg["hooks"] = hooks

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return false, err
	}
	return true, nil
}
