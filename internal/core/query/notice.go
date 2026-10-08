package query

import "strings"

const phpstanMissingNotice = "No PHPStan graph is imported. These edges are only the calls written in the source; calls through an interface or a container are missing."

// GraphNotice warns when a PHP symbol's call graph cannot see container
// and interface calls. Other languages return an empty string.
func (e *Engine) GraphNotice(fqn string) string {
	if !strings.Contains(fqn, `\`) {
		return ""
	}
	var present int
	err := e.s.DB().QueryRow(`SELECT 1 FROM ext_edges WHERE source = 'phpstan' LIMIT 1`).Scan(&present)
	if err == nil {
		return ""
	}
	return phpstanMissingNotice
}
