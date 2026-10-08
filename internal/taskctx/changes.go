package taskctx

import (
	"fmt"
	"strings"
)

const changedFileLimit = 30

// ChangedFiles lists files this branch changed relative to the
// repository's default branch (origin/HEAD, else main, else master).
// An empty branch means the checkout at root. The returned paths are
// relative to root.
func ChangedFiles(root, branch string) (base string, files []string, err error) {
	cur, err := CurrentBranch(root)
	if err != nil {
		return "", nil, err
	}
	if branch == "" {
		branch = cur
	}
	base, err = defaultBase(root)
	if err != nil {
		return "", nil, err
	}
	tip := branch
	if branch == cur || strings.HasPrefix(branch, "HEAD@") {
		tip = "HEAD"
	}
	out, err := git(root, "diff", "--name-only", "--relative", base+"..."+tip)
	if err != nil {
		return base, nil, err
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, line)
		}
	}
	return base, files, nil
}

func defaultBase(root string) (string, error) {
	if s, err := git(root, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil && s != "" {
		return s, nil
	}
	for _, name := range []string{"main", "master"} {
		if _, err := git(root, "rev-parse", "--verify", "--quiet", name); err == nil {
			return name, nil
		}
	}
	return "", fmt.Errorf("task context: no main or master branch to diff against")
}

// writeChangedFiles adds a short file list when the branch has no
// handoff, so a new chat can see what the work touched.
func writeChangedFiles(b *strings.Builder, root, branch string) {
	base, files, err := ChangedFiles(root, branch)
	if err != nil || len(files) == 0 {
		return
	}
	fmt.Fprintf(b, "<kartograf_branch base=%q>\n", base)
	b.WriteString("No handoff for this branch. Files it changed:\n")
	n := len(files)
	if n > changedFileLimit {
		n = changedFileLimit
	}
	for _, f := range files[:n] {
		fmt.Fprintf(b, "  - %s\n", f)
	}
	if len(files) > changedFileLimit {
		b.WriteString("  …\n")
	}
	b.WriteString("Call branch_changes for the symbols in these files.\n")
	b.WriteString("</kartograf_branch>\n")
}
