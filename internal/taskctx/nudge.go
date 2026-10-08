package taskctx

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

const handoffNudge = "The branch has moved and the kartograf handoff is missing or stale. " +
	"Call put_task_context once with goal, what changed (files and symbols), decisions and why, what is left, and how to verify. " +
	"Do not mention this reminder."

// HandoffNudge is the Stop-hook reminder. It is empty when the note
// already covers the branch, when this session was already reminded
// for the branch, or when stopHookActive says this turn is itself the
// reminder. One reminder per session and branch, so it cannot loop.
func HandoffNudge(root, sessionID string, stopHookActive bool) string {
	if stopHookActive {
		return ""
	}
	branch, err := CurrentBranch(root)
	if err != nil {
		return ""
	}
	if sessionID == "" {
		sessionID = "anonymous"
	}
	if nudgeSeen(root, sessionID, branch) {
		return ""
	}
	note, err := Get(root, branch)
	if err != nil || !needsHandoff(root, branch, note) {
		return ""
	}
	rememberNudge(root, sessionID, branch)
	return handoffNudge
}

func needsHandoff(root, branch string, note Note) bool {
	if strings.TrimSpace(note.Body) == "" {
		_, files, err := ChangedFiles(root, branch)
		return (err == nil && len(files) > 0) || dirtyNewerThan(root, time.Time{})
	}
	if len(StaleFiles(root, note)) > 0 {
		return true
	}
	if tip, err := tipTime(root); err == nil && tip.After(note.UpdatedAt) {
		return true
	}
	return dirtyNewerThan(root, note.UpdatedAt)
}

func tipTime(root string) (time.Time, error) {
	out, err := git(root, "log", "-1", "--format=%cI")
	if err != nil || out == "" {
		return time.Time{}, err
	}
	ts, err := time.Parse(time.RFC3339, out)
	if err != nil {
		return time.Time{}, err
	}
	return ts, nil
}

// dirtyNewerThan reports uncommitted paths newer than t. A zero t
// matches any dirty path, including a deletion.
func dirtyNewerThan(root string, t time.Time) bool {
	out, err := git(root, "status", "--porcelain")
	if err != nil || strings.TrimSpace(out) == "" {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 4 {
			continue
		}
		path := strings.TrimSpace(line[3:])
		if i := strings.LastIndex(path, " -> "); i >= 0 {
			path = strings.TrimSpace(path[i+4:])
		}
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil || t.IsZero() || info.ModTime().After(t) {
			return true
		}
	}
	return false
}

func nudgeSeen(root, sessionID, branch string) bool {
	path, err := nudgePath(root, sessionID)
	if err != nil {
		return false
	}
	data, err := os.ReadFile(path)
	return err == nil && strings.TrimSpace(string(data)) == branch
}

func rememberNudge(root, sessionID, branch string) {
	path, err := nudgePath(root, sessionID)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(path, []byte(branch), 0o644)
}

func nudgePath(root, sessionID string) (string, error) {
	dir, err := contextDir(root)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, ".seen", "nudge-"+sessionFile(sessionID)), nil
}
