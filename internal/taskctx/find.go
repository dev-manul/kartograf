package taskctx

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/dev-manul/kartograf/internal/core/config"
)

// TaskMatch is one branch whose name matches a task id.
type TaskMatch struct {
	Repo    string // root-relative repository directory; empty at the serve root
	Branch  string
	HasNote bool
	Preview string
}

// FindTask searches git repositories under root for branches that
// match id. Each repository uses its own .kartograf.yml task.branch
// template when set; otherwise a branch matches when its name contains
// the id.
func FindTask(root, id string) ([]TaskMatch, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("task context: empty task id")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	repos, err := gitRepos(root)
	if err != nil {
		return nil, err
	}
	var out []TaskMatch
	for _, repo := range repos {
		cfg, err := config.Load(repo)
		if err != nil {
			return nil, err
		}
		branches, err := localBranches(repo)
		if err != nil {
			continue // not a usable checkout
		}
		rel, err := filepath.Rel(root, repo)
		if err != nil {
			return nil, err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			rel = ""
		}
		for _, branch := range branches {
			if !branchMatches(cfg.Task.Branch, branch, id) {
				continue
			}
			note, err := Get(repo, branch)
			if err != nil {
				return nil, err
			}
			out = append(out, TaskMatch{
				Repo:    rel,
				Branch:  branch,
				HasNote: strings.TrimSpace(note.Body) != "",
				Preview: preview(note.Body),
			})
			if len(out) == 20 {
				return out, nil
			}
		}
	}
	return out, nil
}

func branchMatches(template, branch, id string) bool {
	if template == "" {
		return strings.Contains(strings.ToLower(branch), strings.ToLower(id))
	}
	return strings.Contains(branch, strings.ReplaceAll(template, "{id}", id))
}

func localBranches(repo string) ([]string, error) {
	out, err := git(repo, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return nil, err
	}
	var branches []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			branches = append(branches, line)
		}
	}
	return branches, nil
}

// gitRepos returns repositories under root, including root when it is
// one. Vendor and dot-directories are skipped; a directory named .git
// marks its parent as a repository.
func gitRepos(root string) ([]string, error) {
	var repos []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return err
			}
			return filepath.SkipDir
		}
		if !d.IsDir() {
			return nil
		}
		name := d.Name()
		if name == ".git" {
			repos = append(repos, filepath.Dir(path))
			return filepath.SkipDir
		}
		if path != root && (strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules") {
			return filepath.SkipDir
		}
		return nil
	})
	return repos, err
}

// Repo resolves a root-relative repository directory returned by
// FindTask. An empty rel is the serve root. The result must be a git
// checkout inside root.
func Repo(root, rel string) (string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	rel = strings.Trim(filepath.ToSlash(strings.TrimSpace(rel)), "/")
	if rel == "" || rel == "." {
		return root, nil
	}
	cleaned := filepath.Clean(filepath.FromSlash(rel))
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) || filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("task context: repo %q escapes the project", rel)
	}
	abs := filepath.Join(root, cleaned)
	info, err := os.Stat(filepath.Join(abs, ".git"))
	if err != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
		return "", fmt.Errorf("task context: %s is not a git repository", rel)
	}
	return abs, nil
}
