package query

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/dev-manul/kartograf/internal/core/store"
)

const phpstanMissingNotice = "No PHPStan graph is imported. These edges are only the calls written in the source; calls through an interface or a container are missing."

const phpstanUnstampedNotice = "PHPStan graph has no commit stamp, so it may not match this checkout."

// GraphNotice warns when a PHP symbol's call graph cannot see container
// and interface calls, or when the imported graph was built at another
// commit. Other languages return an empty string.
func (e *Engine) GraphNotice(fqn string) string {
	if !strings.Contains(fqn, `\`) {
		return ""
	}
	var present int
	err := e.s.DB().QueryRow(`SELECT 1 FROM ext_edges WHERE source = 'phpstan' LIMIT 1`).Scan(&present)
	if err != nil {
		return phpstanMissingNotice
	}
	return e.phpstanFreshness()
}

func (e *Engine) phpstanFreshness() string {
	rows, err := e.s.DB().Query(`SELECT DISTINCT origin FROM ext_edges WHERE source = 'phpstan'`)
	if err != nil {
		return ""
	}
	defer rows.Close()
	var stamps []string
	for rows.Next() {
		var origin string
		if err := rows.Scan(&origin); err != nil {
			return ""
		}
		stamp, err := e.s.Meta(store.EnrichCommitKey(origin))
		if err != nil {
			return ""
		}
		stamps = append(stamps, stamp)
	}
	if err := rows.Err(); err != nil {
		return ""
	}
	head := headCommit(e.root)
	if head == "" {
		return ""
	}
	stamped := false
	for _, stamp := range stamps {
		if stamp == "" {
			continue
		}
		stamped = true
		if stamp != head {
			return fmt.Sprintf("PHPStan graph was built at %s, this checkout is %s. Files changed since then are only covered by the source text.", shortSHA(stamp), shortSHA(head))
		}
	}
	if !stamped {
		return phpstanUnstampedNotice
	}
	return ""
}

func headCommit(root string) string {
	out, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
