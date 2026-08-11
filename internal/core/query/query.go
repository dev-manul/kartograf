// Package query is the read side of the index: symbol search, lookups
// and graph traversals used by the MCP tools. All methods are safe for
// concurrent use (SQLite handles reader concurrency).
package query

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gitlab.stripchat.dev/stripcash/kartograf/internal/core/store"
)

type Engine struct {
	s    *store.Store
	root string // project root, for reading source excerpts
}

func New(s *store.Store, root string) *Engine {
	return &Engine{s: s, root: root}
}

// SymbolHit is one search / lookup result row.
type SymbolHit struct {
	FQN       string `json:"fqn"`
	Kind      string `json:"kind"`
	File      string `json:"file"`
	Line      int    `json:"line"`
	EndLine   int    `json:"endLine,omitempty"`
	Signature string `json:"signature,omitempty"`
	Doc       string `json:"doc,omitempty"`
	Vendor    bool   `json:"vendor,omitempty"`
}

const symbolCols = `s.fqn, s.kind, f.path, s.start_line, s.end_line, s.signature, s.doc, f.vendor
	FROM symbols s JOIN files f ON f.id = s.file_id`

func scanHits(rows *sql.Rows) ([]SymbolHit, error) {
	defer rows.Close()
	var out []SymbolHit
	for rows.Next() {
		var h SymbolHit
		var vendor int
		if err := rows.Scan(&h.FQN, &h.Kind, &h.File, &h.Line, &h.EndLine, &h.Signature, &h.Doc, &vendor); err != nil {
			return nil, err
		}
		h.Vendor = vendor == 1
		h.Doc = docSummary(h.Doc)
		out = append(out, h)
	}
	return out, rows.Err()
}

// docSummary reduces a docblock to its first meaningful line.
func docSummary(doc string) string {
	for _, l := range strings.Split(doc, "\n") {
		l = strings.TrimSpace(strings.Trim(strings.TrimSpace(l), "/*"))
		if l == "" || strings.HasPrefix(l, "@") {
			continue
		}
		return l
	}
	return ""
}

// ftsQuery turns free text into an FTS5 prefix query:
// "user repo" -> `"user"* "repo"*` (implicit AND).
func ftsQuery(q string) string {
	fields := strings.FieldsFunc(q, func(r rune) bool {
		// Split on FTS syntax and PHP separators alike.
		return strings.ContainsRune(` "':\()[]{}<>,;`, r)
	})
	var parts []string
	for _, f := range fields {
		parts = append(parts, `"`+f+`"*`)
	}
	return strings.Join(parts, " ")
}

// SearchSymbols runs a full-text search over names, FQNs and docs.
// kind optionally narrows to one symbol kind. Project symbols rank
// before vendor ones.
func (e *Engine) SearchSymbols(q, kind string, limit int) ([]SymbolHit, error) {
	match := ftsQuery(q)
	if match == "" {
		return nil, fmt.Errorf("empty query")
	}
	args := []any{match}
	kindFilter := ""
	if kind != "" {
		kindFilter = " AND s.kind = ?"
		args = append(args, kind)
	}
	args = append(args, limit)
	// Ranking: project code first, then tests, then vendor; FTS rank
	// within each group.
	rows, err := e.s.DB().Query(`SELECT `+symbolCols+`
		JOIN symbols_fts ON symbols_fts.rowid = s.rowid
		WHERE symbols_fts MATCH ?`+kindFilter+`
		ORDER BY f.vendor ASC,
			(f.path LIKE 'tests/%' OR f.path LIKE '%/tests/%' OR f.path LIKE '%/Tests/%') ASC,
			rank LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	return scanHits(rows)
}

// GetSymbol returns all declarations matching an FQN. When the given
// name has no exact match, it is retried as a suffix (short names like
// "UserService" or "UserService::run()" work).
func (e *Engine) GetSymbol(fqn string) ([]SymbolHit, error) {
	rows, err := e.s.DB().Query(`SELECT `+symbolCols+` WHERE s.fqn = ? LIMIT 20`, fqn)
	if err != nil {
		return nil, err
	}
	hits, err := scanHits(rows)
	if err != nil || len(hits) > 0 {
		return hits, err
	}
	// Suffix fallback ("UserService", "UserService::run()"): find by
	// the short symbol name via its index, then narrow to FQNs ending
	// with the requested path — avoids a full LIKE scan.
	rows, err = e.s.DB().Query(`SELECT `+symbolCols+`
		WHERE s.name = ? AND s.fqn LIKE ? ESCAPE '#'
		ORDER BY f.vendor ASC LIMIT 20`,
		shortName(fqn), "%\\"+escapeLike(fqn))
	if err != nil {
		return nil, err
	}
	return scanHits(rows)
}

// shortName extracts the bare symbol name from an FQN-ish string:
// "A\B::bar()" -> "bar", "A\B::$x" -> "x", "A\B" -> "B".
func shortName(fqn string) string {
	s := strings.TrimSuffix(fqn, "()")
	if i := strings.LastIndex(s, "::"); i >= 0 {
		s = s[i+2:]
	} else if i := strings.LastIndex(s, "\\"); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimPrefix(s, "$")
}

func escapeLike(s string) string {
	r := strings.NewReplacer("#", "##", "%", "#%", "_", "#_")
	return r.Replace(s)
}

// Members lists symbols contained in a class-like symbol.
func (e *Engine) Members(containerFQN string) ([]SymbolHit, error) {
	rows, err := e.s.DB().Query(`SELECT `+symbolCols+`
		WHERE s.container = ? ORDER BY s.start_line LIMIT 500`, containerFQN)
	if err != nil {
		return nil, err
	}
	return scanHits(rows)
}

// Source reads the declaration's source text (capped at maxLines).
func (e *Engine) Source(h SymbolHit, maxLines int) (string, error) {
	data, err := os.ReadFile(filepath.Join(e.root, filepath.FromSlash(h.File)))
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(data), "\n")
	start, end := h.Line-1, h.EndLine
	if start < 0 || start >= len(lines) {
		return "", fmt.Errorf("stale index: %s out of range in %s", h.FQN, h.File)
	}
	if end > len(lines) {
		end = len(lines)
	}
	truncated := false
	if end-start > maxLines {
		end = start + maxLines
		truncated = true
	}
	src := strings.Join(lines[start:end], "\n")
	if truncated {
		src += "\n// … truncated …"
	}
	return src, nil
}

// EdgeHit is one reference/call-graph result row.
type EdgeHit struct {
	From     string `json:"from"` // "" = file-level code
	Kind     string `json:"kind"`
	To       string `json:"to"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Resolved bool   `json:"resolved"` // false = heuristic match
}

func scanEdges(rows *sql.Rows) ([]EdgeHit, error) {
	defer rows.Close()
	var out []EdgeHit
	for rows.Next() {
		var h EdgeHit
		var resolved int
		if err := rows.Scan(&h.From, &h.Kind, &h.To, &h.File, &h.Line, &resolved); err != nil {
			return nil, err
		}
		h.Resolved = resolved == 1
		out = append(out, h)
	}
	return out, rows.Err()
}

const edgeCols = `e.from_fqn, e.kind, e.to_fqn, f.path, e.line, e.resolved
	FROM edges e JOIN files f ON f.id = e.file_id`

// References returns all edges pointing at the symbol (any kind).
func (e *Engine) References(fqn string, limit int) ([]EdgeHit, error) {
	rows, err := e.s.DB().Query(`SELECT `+edgeCols+`
		WHERE e.to_fqn = ? ORDER BY e.resolved DESC, f.path, e.line LIMIT ?`, fqn, limit)
	if err != nil {
		return nil, err
	}
	return scanEdges(rows)
}

// Callers returns call edges into a callable. For methods, the class
// hierarchy is taken into account: calls recorded against an ancestor
// or descendant receiver with the same method name are included
// (marked unresolved), since the static receiver type in the source is
// often a supertype of the class that defines the method.
func (e *Engine) Callers(fqn string, limit int) ([]EdgeHit, error) {
	class, member, isMethod := strings.Cut(fqn, "::")
	if !isMethod {
		rows, err := e.s.DB().Query(`SELECT `+edgeCols+`
			WHERE e.to_fqn = ? AND e.kind IN ('calls', 'instantiates')
			ORDER BY e.resolved DESC, f.path, e.line LIMIT ?`, fqn, limit)
		if err != nil {
			return nil, err
		}
		return scanEdges(rows)
	}
	// family = the class itself + its ancestors + its descendants.
	// Deliberately NOT descendants-of-ancestors: siblings sharing a
	// base class don't receive each other's calls.
	rows, err := e.s.DB().Query(`
		WITH RECURSIVE up(fqn) AS (
			SELECT ?
			UNION
			SELECT e2.to_fqn FROM edges e2 JOIN up ON e2.from_fqn = up.fqn
				AND e2.kind IN ('extends', 'implements', 'uses_trait')
		), down(fqn) AS (
			SELECT ?
			UNION
			SELECT e3.from_fqn FROM edges e3 JOIN down ON e3.to_fqn = down.fqn
				AND e3.kind IN ('extends', 'implements', 'uses_trait')
		), family(fqn) AS (
			SELECT fqn FROM up UNION SELECT fqn FROM down
		)
		SELECT `+edgeCols+`
		WHERE e.kind = 'calls' AND e.to_fqn IN (SELECT fqn || '::' || ? FROM family)
		ORDER BY (e.to_fqn = ?) DESC, e.resolved DESC, f.path, e.line LIMIT ?`,
		class, class, member, fqn, limit)
	if err != nil {
		return nil, err
	}
	hits, err := scanEdges(rows)
	if err != nil {
		return nil, err
	}
	// Hierarchy-widened matches are heuristic by construction.
	for i := range hits {
		if hits[i].To != fqn {
			hits[i].Resolved = false
		}
	}
	return hits, nil
}

// Callees returns outgoing calls/instantiations of a symbol.
func (e *Engine) Callees(fqn string, limit int) ([]EdgeHit, error) {
	rows, err := e.s.DB().Query(`SELECT `+edgeCols+`
		WHERE e.from_fqn = ? AND e.kind IN ('calls', 'instantiates')
		ORDER BY e.line LIMIT ?`, fqn, limit)
	if err != nil {
		return nil, err
	}
	return scanEdges(rows)
}

// Relative is one hierarchy neighbor.
type Relative struct {
	FQN  string `json:"fqn"`
	Kind string `json:"kind"` // edge kind linking it to the queried class
}

// Hierarchy returns supertypes and subtypes of a class-like symbol,
// each transitively closed.
func (e *Engine) Hierarchy(fqn string) (ancestors, descendants []Relative, err error) {
	collect := func(query string) ([]Relative, error) {
		rows, err := e.s.DB().Query(query, fqn)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []Relative
		for rows.Next() {
			var r Relative
			if err := rows.Scan(&r.FQN, &r.Kind); err != nil {
				return nil, err
			}
			out = append(out, r)
		}
		return out, rows.Err()
	}
	ancestors, err = collect(`
		WITH RECURSIVE up(fqn, kind) AS (
			SELECT ?, ''
			UNION
			SELECT e.to_fqn, e.kind FROM edges e JOIN up ON e.from_fqn = up.fqn
				AND e.kind IN ('extends', 'implements', 'uses_trait')
		)
		SELECT fqn, kind FROM up WHERE kind != '' LIMIT 200`)
	if err != nil {
		return nil, nil, err
	}
	descendants, err = collect(`
		WITH RECURSIVE down(fqn, kind) AS (
			SELECT ?, ''
			UNION
			SELECT e.from_fqn, e.kind FROM edges e JOIN down ON e.to_fqn = down.fqn
				AND e.kind IN ('extends', 'implements', 'uses_trait')
		)
		SELECT fqn, kind FROM down WHERE kind != '' LIMIT 500`)
	return ancestors, descendants, err
}

// FileOutline lists all symbols declared in a file.
func (e *Engine) FileOutline(path string) ([]SymbolHit, error) {
	rows, err := e.s.DB().Query(`SELECT `+symbolCols+`
		WHERE f.path = ? ORDER BY s.start_line LIMIT 1000`, path)
	if err != nil {
		return nil, err
	}
	return scanHits(rows)
}
