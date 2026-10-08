// Package taskctx stores a working note per git branch so an agent can
// resume a task after a new chat or a branch switch.
//
// Notes live in <git-common-dir>/kartograf/context/. Checkout does not
// touch that directory, and the files sit outside the working tree, so
// they stay put across branch switches and are not committed. The code
// index database is a rebuildable cache and is not used here.
package taskctx

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// MaxBody is the maximum stored note size in bytes. A note is a handoff
// for a later chat (what was done, why, what is left), written once
// when work pauses. 4 KiB is enough for that in Russian, where each
// letter is two bytes, and small enough to inject once per session.
const MaxBody = 4 << 10

// now is replaced in tests so list order does not depend on the clock.
var now = time.Now

// Note is the full working note for one branch.
type Note struct {
	Branch    string
	UpdatedAt time.Time
	Body      string
}

// Summary is one row of a branch listing.
type Summary struct {
	Branch    string
	UpdatedAt time.Time
	Preview   string
}

// CurrentBranch reports the branch checked out at root.
// A detached HEAD is reported as HEAD@<12-char sha> so unrelated
// detached states do not share one note.
func CurrentBranch(root string) (string, error) {
	name, err := git(root, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	if name != "HEAD" {
		return name, nil
	}
	sha, err := git(root, "rev-parse", "--short=12", "HEAD")
	if err != nil {
		return "", err
	}
	return "HEAD@" + sha, nil
}

// Get returns the note for branch. An empty branch means the branch
// checked out at root. A missing note is an empty body and a nil error.
func Get(root, branch string) (Note, error) {
	branch, err := resolveBranch(root, branch)
	if err != nil {
		return Note{}, err
	}
	path, err := notePath(root, branch)
	if err != nil {
		return Note{}, err
	}
	note, err := readNote(path)
	if os.IsNotExist(err) {
		return Note{Branch: branch}, nil
	}
	if err != nil {
		return Note{}, err
	}
	if note.Branch != branch {
		return Note{}, collision(path, note.Branch)
	}
	return note, nil
}

// Put replaces the note for branch. An empty branch means the branch
// checked out at root. A blank body deletes the note. The previous
// text is not merged: callers pass the full note they want kept.
func Put(root, branch, body string) (Note, error) {
	branch, err := resolveBranch(root, branch)
	if err != nil {
		return Note{}, err
	}
	path, err := notePath(root, branch)
	if err != nil {
		return Note{}, err
	}
	existing, err := readNote(path)
	if err != nil && !os.IsNotExist(err) {
		return Note{}, err
	}
	if err == nil && existing.Branch != branch {
		return Note{}, collision(path, existing.Branch)
	}
	if strings.TrimSpace(body) == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return Note{}, err
		}
		return Note{Branch: branch}, nil
	}
	if len(body) > MaxBody {
		return Note{}, fmt.Errorf("task context: body is %d bytes, limit is %d", len(body), MaxBody)
	}
	note := Note{
		Branch:    branch,
		UpdatedAt: now().UTC().Truncate(time.Second),
		Body:      body,
	}
	if err := writeAtomic(path, format(note)); err != nil {
		return Note{}, err
	}
	return note, nil
}

// List returns notes stored for this repository, newest first.
func List(root string) ([]Summary, error) {
	dir, err := contextDir(root)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Summary
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".md") || strings.HasPrefix(name, ".") {
			continue
		}
		note, err := readNote(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("task context: %s: %w", name, err)
		}
		out = append(out, Summary{
			Branch:    note.Branch,
			UpdatedAt: note.UpdatedAt,
			Preview:   preview(note.Body),
		})
	}
	slices.SortFunc(out, func(a, b Summary) int {
		if c := b.UpdatedAt.Compare(a.UpdatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.Branch, b.Branch)
	})
	return out, nil
}

// HookText is the prompt-hook block for the current branch. sessionID
// scopes the injection to one chat: the handoff is shown on the first
// prompt of that session and not again, because a repeated block would
// stay in the transcript and be paid for on every later turn. An empty
// sessionID injects nothing. Other branches' handoffs are listed by
// name so follow-up work on a new branch can find last week's note.
// The result is empty when there is nothing saved or git is
// unavailable — a hook must stay silent rather than fail the prompt.
func HookText(root, sessionID string) string {
	if sessionID == "" || alreadySeen(root, sessionID) {
		return ""
	}
	// Mark before returning so a session that starts with no note does
	// not receive the note again after the agent writes it: that write
	// is already in the transcript as the tool result.
	remember(root, sessionID)
	branch, err := CurrentBranch(root)
	if err != nil {
		return ""
	}
	var b strings.Builder
	if note, err := Get(root, branch); err == nil && strings.TrimSpace(note.Body) != "" {
		writeNote(&b, note)
	}
	// A follow-up a week later often lands on another branch. List the
	// other handoffs so the new chat can open the one that matches.
	if others, err := List(root); err == nil {
		n := 0
		for _, s := range others {
			if s.Branch == branch {
				continue
			}
			if n == 0 {
				b.WriteString("<kartograf_tasks>\n")
			}
			fmt.Fprintf(&b, "%s (%s): %s\n", s.Branch, s.UpdatedAt.UTC().Format("2006-01-02"), s.Preview)
			n++
			if n == 5 {
				break
			}
		}
		if n > 0 {
			b.WriteString("</kartograf_tasks>\n")
		}
	}
	return b.String()
}

func writeNote(b *strings.Builder, note Note) {
	body, truncated := truncate(note.Body, MaxBody)
	fmt.Fprintf(b, "<kartograf_task branch=%q>\n", note.Branch)
	b.WriteString(body)
	if !strings.HasSuffix(body, "\n") {
		b.WriteByte('\n')
	}
	if truncated {
		b.WriteString("…\n")
	}
	b.WriteString("</kartograf_task>\n")
}

func alreadySeen(root, sessionID string) bool {
	path, err := seenPath(root, sessionID)
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

func remember(root, sessionID string) {
	path, err := seenPath(root, sessionID)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(path, nil, 0o644)
}

func seenPath(root, sessionID string) (string, error) {
	dir, err := contextDir(root)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, ".seen", sessionFile(sessionID)), nil
}

func sessionFile(sessionID string) string {
	if safeSession(sessionID) {
		return sessionID
	}
	sum := sha256.Sum256([]byte(sessionID))
	return hex.EncodeToString(sum[:8])
}

func safeSession(s string) bool {
	if s == "" || s == "." || s == ".." || len(s) > 80 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

func resolveBranch(root, branch string) (string, error) {
	if branch != "" {
		return branch, nil
	}
	return CurrentBranch(root)
}

func collision(path, held string) error {
	return fmt.Errorf("task context: %s already holds branch %q", filepath.Base(path), held)
}

func notePath(root, branch string) (string, error) {
	name, err := fileName(branch)
	if err != nil {
		return "", err
	}
	dir, err := contextDir(root)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

func contextDir(root string) (string, error) {
	dir, err := git(root, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(root, dir)
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "kartograf", "context"), nil
}

// fileName maps a branch onto a single path segment. Slashes become
// "--"; the file's branch header is what distinguishes feature/pay
// from a branch literally named feature--pay.
func fileName(branch string) (string, error) {
	if branch == "" || len(branch) > 200 || strings.ContainsAny(branch, "\x00\n\r") {
		return "", fmt.Errorf("task context: invalid branch name %q", branch)
	}
	name := strings.ReplaceAll(branch, "/", "--")
	if name == "." || name == ".." || strings.Contains(name, "..") || strings.ContainsAny(name, `/\`) || filepath.Base(name) != name {
		return "", fmt.Errorf("task context: invalid branch name %q", branch)
	}
	return name + ".md", nil
}

func format(note Note) string {
	return fmt.Sprintf("branch: %s\nupdated: %s\n\n%s", note.Branch, note.UpdatedAt.UTC().Format(time.RFC3339), note.Body)
}

func readNote(path string) (Note, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Note{}, err
	}
	text := string(data)
	line, rest, ok := strings.Cut(text, "\n")
	if !ok || !strings.HasPrefix(line, "branch: ") {
		return Note{}, fmt.Errorf("task context: %s: missing branch header", filepath.Base(path))
	}
	branch := strings.TrimPrefix(line, "branch: ")
	line, rest, ok = strings.Cut(rest, "\n")
	if !ok || !strings.HasPrefix(line, "updated: ") {
		return Note{}, fmt.Errorf("task context: %s: missing updated header", filepath.Base(path))
	}
	updated, err := time.Parse(time.RFC3339, strings.TrimPrefix(line, "updated: "))
	if err != nil {
		return Note{}, fmt.Errorf("task context: %s: %w", filepath.Base(path), err)
	}
	if !strings.HasPrefix(rest, "\n") {
		return Note{}, fmt.Errorf("task context: %s: missing header separator", filepath.Base(path))
	}
	return Note{Branch: branch, UpdatedAt: updated, Body: rest[1:]}, nil
}

func writeAtomic(path, content string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".note-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	done := false
	defer func() {
		if !done {
			os.Remove(tmp)
		}
	}()
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	done = true
	return nil
}

func preview(body string) string {
	line, _, _ := strings.Cut(body, "\n")
	line = strings.TrimSpace(line)
	const max = 120
	if utf8.RuneCountInString(line) <= max {
		return line
	}
	runes := []rune(line)
	return string(runes[:max]) + "…"
}

func truncate(s string, maxBytes int) (string, bool) {
	if len(s) <= maxBytes {
		return s, false
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}

func git(root string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}
