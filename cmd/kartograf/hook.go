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
	"github.com/dev-manul/kartograf/internal/taskctx"
)

// hook is a prompt hook for Claude Code and Cursor. On the first
// prompt of a session it surfaces the handoff note for the current
// git branch, then looks up identifier-looking words from the prompt
// in the kartograf index and, on a match, adds a small block that
// nudges the agent to query the code graph instead of grepping.
//
// Claude Code reads plain text from stdout. Cursor's beforeSubmitPrompt
// hook reads a JSON object; pass --cursor or send a Cursor hook event
// and the same text goes out as additional_context.
//
// Contract: output is injected into the conversation as context. It
// must be fast and silent when it has nothing to say, and must always
// exit 0 — a hook failure must never block the user's prompt.
func newHookCmd() *cobra.Command {
	var root string
	var cursor bool
	cmd := &cobra.Command{
		Use:    "hook",
		Short:  "Prompt hook: surface the branch handoff and indexed symbols mentioned in the prompt",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			runHook(root, cursor) // best-effort by design
			return nil
		},
	}
	cmd.Flags().StringVar(&root, "root", ".", "project root whose index to query")
	cmd.Flags().BoolVar(&cursor, "cursor", false, "emit Cursor hook JSON (additional_context) instead of plain text")
	return cmd
}

type hookInput struct {
	Prompt         string `json:"prompt"`
	SessionID      string `json:"session_id"`
	ConversationID string `json:"conversation_id"`
	HookEvent      string `json:"hook_event_name"`
}

func runHook(root string, cursor bool) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return
	}
	var in hookInput
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if err == nil {
		_ = json.Unmarshal(raw, &in)
	}
	if in.HookEvent == "beforeSubmitPrompt" || in.HookEvent == "sessionStart" {
		cursor = true
	}
	session := in.SessionID
	if session == "" {
		session = in.ConversationID
	}
	var body strings.Builder
	defer func() {
		fmt.Print(formatHookOutput(cursor, body.String()))
	}()

	if text := taskctx.HookText(absRoot, session); text != "" {
		body.WriteString(text)
	}
	if in.Prompt == "" {
		return
	}

	dbPath, err := store.DefaultPath(absRoot)
	if err != nil {
		return
	}
	if _, err := os.Stat(dbPath); err != nil {
		return // no index yet — never create one from a hook
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

	fmt.Fprintf(&body, "<kartograf_context note=%q>\n", "the kartograf index has symbols matching this prompt — query the code graph before grepping files.")
	body.WriteString("Matching indexed symbols:\n")
	for _, h := range hits {
		fmt.Fprintf(&body, "  - %s (%s — %s:%d)\n", h.FQN, h.Kind, h.File, h.Line)
	}
	body.WriteString("Call the kartograf `explore` tool once with the relevant name for source, callers and hierarchy; use get_callers/find_references for specific slices.\n")
	body.WriteString("</kartograf_context>\n")
}

// formatHookOutput wraps text for the client that invoked the hook.
// Cursor wants JSON; Claude Code wants the text itself. Empty Claude
// output stays empty so a quiet hook adds nothing.
func formatHookOutput(cursor bool, text string) string {
	if !cursor {
		return text
	}
	payload := map[string]any{"continue": true}
	if text != "" {
		payload["additional_context"] = text
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "{\"continue\":true}\n"
	}
	return string(data) + "\n"
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
