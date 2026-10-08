package taskctx

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// filePathRe finds root-relative paths mentioned in a handoff, such as
// internal/taskctx/taskctx.go. Bare words without a dot are skipped.
var filePathRe = regexp.MustCompile(`[A-Za-z0-9_][A-Za-z0-9_./-]*\.[A-Za-z0-9]+`)

// StaleFiles returns paths named in the note whose latest commit is
// newer than the note. A later chat can see that the handoff is
// talking about code that has since moved.
func StaleFiles(root string, note Note) []string {
	if note.UpdatedAt.IsZero() || strings.TrimSpace(note.Body) == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, path := range filePathRe.FindAllString(note.Body, 40) {
		if strings.Contains(path, "..") || seen[path] {
			continue
		}
		seen[path] = true
		committed, err := fileCommitTime(root, path)
		if err != nil || !committed.After(note.UpdatedAt) {
			continue
		}
		out = append(out, path)
	}
	return out
}

func fileCommitTime(root, path string) (time.Time, error) {
	out, err := git(root, "log", "-1", "--format=%cI", "--", path)
	if err != nil || out == "" {
		return time.Time{}, err
	}
	ts, err := time.Parse(time.RFC3339, out)
	if err != nil {
		return time.Time{}, fmt.Errorf("task context: %s: %w", path, err)
	}
	return ts, nil
}
