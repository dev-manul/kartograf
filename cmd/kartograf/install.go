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
		Use:   "install <claude|cursor|hook> [root]",
		Short: "Register kartograf as an MCP server for a client",
		Long: `Registers this binary as a stdio MCP server for the given project root
(default: current directory).

  claude — runs "claude mcp add kartograf" (local project scope)
  cursor — writes/merges <root>/.cursor/mcp.json with type=stdio and
           absolute paths (Cursor does not expand ~)
  hook   — merges a UserPromptSubmit hook into <root>/.claude/settings.json
           that surfaces indexed symbols mentioned in each prompt and
           nudges the agent to query the graph before grepping`,
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
			case "hook":
				return installHook(exe, absRoot)
			default:
				return fmt.Errorf("unknown client %q (want claude, cursor or hook)", args[0])
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
	fmt.Printf("wrote %s; reload the MCP list in Cursor (Settings → MCP)\n", path)
	return nil
}

func installHook(exe, root string) error {
	path := filepath.Join(root, ".claude", "settings.json")
	cfg := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return fmt.Errorf("%s exists but is not valid JSON: %w", path, err)
		}
	}
	hooks, _ := cfg["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	entries, _ := hooks["UserPromptSubmit"].([]any)
	command := fmt.Sprintf("%s hook --root %s", exe, root)
	for _, e := range entries {
		if strings.Contains(fmt.Sprint(e), " hook --root ") {
			fmt.Printf("a kartograf hook is already configured in %s\n", path)
			return nil
		}
	}
	entries = append(entries, map[string]any{
		"hooks": []any{map[string]any{"type": "command", "command": command}},
	})
	hooks["UserPromptSubmit"] = entries
	cfg["hooks"] = hooks

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
	fmt.Printf("wrote %s; the hook activates on the next session\n", path)
	return nil
}
