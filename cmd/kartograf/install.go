package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"
)

func newInstallCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "install <claude|cursor> [root]",
		Short: "Register kartograf as an MCP server for a client",
		Long: `Registers this binary as a stdio MCP server for the given project root
(default: current directory).

  claude — runs "claude mcp add kartograf" (local project scope)
  cursor — writes/merges <root>/.cursor/mcp.json with type=stdio and
           absolute paths (Cursor does not expand ~)`,
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
			default:
				return fmt.Errorf("unknown client %q (want claude or cursor)", args[0])
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
