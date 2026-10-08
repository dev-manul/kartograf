package query

// Patterns are text, not a running app. A route registered only inside
// PHP (a container, a loop, a variable) will not appear.
const (
	routePattern   = `(?i)(Route::(get|post|put|patch|delete|any|match)\s*\(|#\[Route\s*\(|app\.(get|post|put|patch|delete)\s*\(|router\.(get|post|put|delete)\s*\(|http\.HandleFunc\s*\(|\.HandleFunc\s*\(|\.(GET|POST|PUT|PATCH|DELETE)\s*\(\s*")`
	commandPattern = `(?i)(extends\s+Command\b|cobra\.Command\s*\{|Use:\s*"|Console\\Command)`
	sqlPattern     = `(?i)\b(select\s+.+\s+from|insert\s+into|update\s+\S+\s+set|delete\s+from)\b`
)

// ProjectMap is a short list of routes, commands and SQL strings written
// in source. It does not execute PHP.
type ProjectMap struct {
	Routes   []CodeMatch `json:"routes"`
	Commands []CodeMatch `json:"commands"`
	SQL      []CodeMatch `json:"sql"`
}

// ProjectMap scans non-vendor files for route, command and SQL lines.
// limit caps each list. The result is a text scan.
func (e *Engine) ProjectMap(limit int) (ProjectMap, error) {
	if limit <= 0 || limit > 50 {
		limit = 30
	}
	var out ProjectMap
	var err error
	if out.Routes, _, err = e.SearchCode(routePattern, true, "", limit); err != nil {
		return ProjectMap{}, err
	}
	if out.Commands, _, err = e.SearchCode(commandPattern, true, "", limit); err != nil {
		return ProjectMap{}, err
	}
	if out.SQL, _, err = e.SearchCode(sqlPattern, true, "", limit); err != nil {
		return ProjectMap{}, err
	}
	if out.Routes == nil {
		out.Routes = []CodeMatch{}
	}
	if out.Commands == nil {
		out.Commands = []CodeMatch{}
	}
	if out.SQL == nil {
		out.SQL = []CodeMatch{}
	}
	return out, nil
}
