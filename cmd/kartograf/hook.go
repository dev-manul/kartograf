package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dev-manul/kartograf/internal/core/query"
	"github.com/dev-manul/kartograf/internal/core/store"
)

// hook is a Claude Code UserPromptSubmit hook: it reads the hook JSON
// from stdin, looks up identifier-looking words from the prompt in the
// kartograf index and, on a match, prints a small context block that
// nudges the agent to query the code graph instead of grepping.
//
// Contract: whatever this prints to stdout is injected into the
// conversation as context. It must be fast and silent when it has
// nothing to say, and must always exit 0 — a hook failure must never
// block the user's prompt.
func newHookCmd() *cobra.Command {
	var root string
	cmd := &cobra.Command{
		Use:    "hook",
		Short:  "Claude Code UserPromptSubmit hook: surface indexed symbols mentioned in the prompt",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			runHook(root) // best-effort by design
			return nil
		},
	}
	cmd.Flags().StringVar(&root, "root", ".", "project root whose index to query")
	return cmd
}

func runHook(root string) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return
	}
	dbPath, err := store.DefaultPath(absRoot)
	if err != nil {
		return
	}
	if _, err := os.Stat(dbPath); err != nil {
		return // no index yet — never create one from a hook
	}

	var in struct {
		Prompt string `json:"prompt"`
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if err != nil || json.Unmarshal(raw, &in) != nil || in.Prompt == "" {
		return
	}
	names := identifierCandidates(in.Prompt)
	if len(names) == 0 {
		return
	}

	s, err := store.Open(dbPath, absRoot)
	if err != nil {
		return
	}
	defer s.Close()
	hits, err := query.New(s, absRoot).SymbolsByNames(names, 5)
	if err != nil || len(hits) == 0 {
		return
	}

	fmt.Println(`<kartograf_context note="the kartograf index has symbols matching this prompt — query the code graph before grepping files.">`)
	fmt.Println("Matching indexed symbols:")
	for _, h := range hits {
		fmt.Printf("  - %s (%s — %s:%d)\n", h.FQN, h.Kind, h.File, h.Line)
	}
	fmt.Println("Call the kartograf `explore` tool once with the relevant name for source, callers and hierarchy; use get_callers/find_references for specific slices.")
	fmt.Println(`</kartograf_context>`)
}

var identRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]{2,}`)

// identifierCandidates extracts words that plausibly name code symbols:
// CamelCase, snake_case, or the tail of qualified names (Foo::bar,
// api.Client, mod#Button). Plain lowercase prose words are skipped.
func identifierCandidates(prompt string) []string {
	seen := map[string]bool{}
	var out []string
	for _, tok := range identRe.FindAllString(prompt, 60) {
		hasUpper := strings.ToLower(tok) != tok
		interior := strings.ContainsAny(tok[1:], "ABCDEFGHIJKLMNOPQRSTUVWXYZ") || strings.Contains(tok, "_")
		if !hasUpper && !interior {
			continue
		}
		if len(tok) < 3 || seen[tok] {
			continue
		}
		seen[tok] = true
		out = append(out, tok)
	}
	return out
}
