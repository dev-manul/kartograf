package taskctx

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func initRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init", "-b", "main")
	runGit(t, root, "config", "user.email", "kartograf@example.com")
	runGit(t, root, "config", "user.name", "kartograf")
	runGit(t, root, "commit", "--allow-empty", "-m", "init")
	return root
}

func runGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestBranchSwitchKeepsBothNotes(t *testing.T) {
	root := initRepo(t)
	runGit(t, root, "checkout", "-b", "feature/pay")
	if _, err := Put(root, "", "pay work\nnext: wire the handler"); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "checkout", "-b", "feature/other")
	if _, err := Put(root, "", "other work"); err != nil {
		t.Fatal(err)
	}

	runGit(t, root, "checkout", "feature/pay")
	note, err := Get(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if note.Branch != "feature/pay" || note.Body != "pay work\nnext: wire the handler" {
		t.Fatalf("note = %+v", note)
	}
	if note.UpdatedAt.IsZero() {
		t.Fatal("missing updated time")
	}

	status := runGit(t, root, "status", "--porcelain")
	if status != "" {
		t.Fatalf("working tree dirty:\n%s", status)
	}
	common := runGit(t, root, "rev-parse", "--git-common-dir")
	if !filepath.IsAbs(common) {
		common = filepath.Join(root, common)
	}
	if _, err := os.Stat(filepath.Join(common, "kartograf", "context", "feature--pay.md")); err != nil {
		t.Fatal(err)
	}
}

func TestWorktreeSharesNote(t *testing.T) {
	root := initRepo(t)
	runGit(t, root, "checkout", "-b", "feature/pay")
	if _, err := Put(root, "", "shared"); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "checkout", "main")
	wt := filepath.Join(t.TempDir(), "wt")
	runGit(t, root, "worktree", "add", wt, "feature/pay")

	note, err := Get(wt, "")
	if err != nil {
		t.Fatal(err)
	}
	if note.Branch != "feature/pay" || note.Body != "shared" {
		t.Fatalf("note = %+v", note)
	}
}

func TestDetachedHEAD(t *testing.T) {
	root := initRepo(t)
	sha := runGit(t, root, "rev-parse", "--short=12", "HEAD")
	runGit(t, root, "checkout", "--detach")

	branch, err := CurrentBranch(root)
	if err != nil {
		t.Fatal(err)
	}
	if branch != "HEAD@"+sha {
		t.Fatalf("branch = %q", branch)
	}
	if _, err := Put(root, "", "detached note"); err != nil {
		t.Fatal(err)
	}
	note, err := Get(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if note.Branch != branch || note.Body != "detached note" {
		t.Fatalf("note = %+v", note)
	}
}

func TestMissingNoteAndDelete(t *testing.T) {
	root := initRepo(t)
	note, err := Get(root, "feature/missing")
	if err != nil {
		t.Fatal(err)
	}
	if note.Branch != "feature/missing" || note.Body != "" || !note.UpdatedAt.IsZero() {
		t.Fatalf("note = %+v", note)
	}

	if _, err := Put(root, "feature/missing", "temp"); err != nil {
		t.Fatal(err)
	}
	if _, err := Put(root, "feature/missing", "  \n"); err != nil {
		t.Fatal(err)
	}
	note, err = Get(root, "feature/missing")
	if err != nil || note.Body != "" {
		t.Fatalf("after delete: %+v %v", note, err)
	}
	list, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("list = %+v", list)
	}
}

func TestNameCollision(t *testing.T) {
	root := initRepo(t)
	if _, err := Put(root, "feature/pay", "slash"); err != nil {
		t.Fatal(err)
	}
	if _, err := Put(root, "feature--pay", "flat"); err == nil {
		t.Fatal("expected collision")
	}
	note, err := Get(root, "feature/pay")
	if err != nil || note.Body != "slash" {
		t.Fatalf("note = %+v %v", note, err)
	}
	if _, err := Put(root, "feature--pay", ""); err == nil {
		t.Fatal("empty body must not delete the other branch's note")
	}
}

func TestBodyLimit(t *testing.T) {
	root := initRepo(t)
	if _, err := Put(root, "main", strings.Repeat("a", MaxBody)); err != nil {
		t.Fatal(err)
	}
	if _, err := Put(root, "main", strings.Repeat("a", MaxBody+1)); err == nil {
		t.Fatal("expected size error")
	}
}

func TestInvalidBranch(t *testing.T) {
	root := initRepo(t)
	for _, name := range []string{"../x", "feature/../../main", "a\nb"} {
		if _, err := Put(root, name, "nope"); err == nil {
			t.Fatalf("branch %q accepted", name)
		}
	}
}

func TestNotAGitRepo(t *testing.T) {
	dir := t.TempDir()
	if _, err := CurrentBranch(dir); err == nil {
		t.Fatal("expected error")
	}
	if _, err := List(dir); err == nil {
		t.Fatal("expected error")
	}
	if HookText(dir, "sess") != "" {
		t.Fatal("hook text on a non-repo")
	}
}

func TestListOrdersByUpdatedAt(t *testing.T) {
	root := initRepo(t)
	orig := now
	t.Cleanup(func() { now = orig })

	now = func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }
	if _, err := Put(root, "old", "older work"); err != nil {
		t.Fatal(err)
	}
	now = func() time.Time { return time.Date(2026, 1, 3, 3, 4, 5, 0, time.UTC) }
	if _, err := Put(root, "new", "newer work\nmore"); err != nil {
		t.Fatal(err)
	}

	list, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Branch != "new" || list[1].Branch != "old" {
		t.Fatalf("list = %+v", list)
	}
	if list[0].Preview != "newer work" {
		t.Fatalf("preview = %q", list[0].Preview)
	}
}

func TestHookTextOncePerSession(t *testing.T) {
	root := initRepo(t)
	if HookText(root, "") != "" {
		t.Fatal("empty session id must stay quiet")
	}
	if HookText(root, "sess-a") != "" {
		t.Fatal("expected silence with no note")
	}
	if _, err := Put(root, "", "goal: pay\nstatus: halfway\nnext: handler\n"); err != nil {
		t.Fatal(err)
	}
	// sess-a already ran, so the note the agent writes stays in the
	// tool result and is not injected again.
	if HookText(root, "sess-a") != "" {
		t.Fatal("session was injected twice")
	}
	text := HookText(root, "sess-b")
	if !strings.Contains(text, "goal: pay") || !strings.Contains(text, "</kartograf_task>") {
		t.Fatalf("hook text:\n%s", text)
	}
	if HookText(root, "sess-b") != "" {
		t.Fatal("same session injected twice")
	}
}
