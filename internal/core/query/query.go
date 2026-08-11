// Package query is the read side of the index: symbol search, lookups
// and graph traversals used by the MCP tools. All methods are safe for
// concurrent use (SQLite handles reader concurrency).
package query

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dev-manul/kartograf/internal/core/store"
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
// kind optionally narrows to one symbol kind, name to an exact symbol
// name. total is the number of matches regardless of limit/offset.
func (e *Engine) SearchSymbols(q, kind, name string, limit, offset int) (hits []SymbolHit, total int, err error) {
	match := ftsQuery(q)
	if match == "" {
		return nil, 0, fmt.Errorf("empty query")
	}
	filter := ""
	filterArgs := []any{match}
	if kind != "" {
		filter += " AND s.kind = ?"
		filterArgs = append(filterArgs, kind)
	}
	if name != "" {
		filter += " AND s.name = ?"
		filterArgs = append(filterArgs, name)
	}

	if err := e.s.DB().QueryRow(`SELECT COUNT(*) FROM symbols s
		JOIN symbols_fts ON symbols_fts.rowid = s.rowid
		WHERE symbols_fts MATCH ?`+filter, filterArgs...).Scan(&total); err != nil {
		return nil, 0, err
	}

	// Ranking: project code first, then tests, then vendor; FTS rank
	// within each group.
	args := append(filterArgs, limit, offset)
	rows, err := e.s.DB().Query(`SELECT `+symbolCols+`
		JOIN symbols_fts ON symbols_fts.rowid = s.rowid
		WHERE symbols_fts MATCH ?`+filter+`
		ORDER BY f.vendor ASC,
			(f.path LIKE 'tests/%' OR f.path LIKE '%/tests/%' OR f.path LIKE '%/Tests/%') ASC,
			rank LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, 0, err
	}
	hits, err = scanHits(rows)
	return hits, total, err
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
	// with the requested path — avoids a full LIKE scan. The separator
	// before the suffix is language-specific: PHP "\", Go "." / "/",
	// TS "#" / ".". A function lookup also tries the "()"-suffixed
	// form ("Button" finds "mod#Button()").
	esc := escapeLike(fqn)
	patterns := []string{"%\\" + esc, "%." + esc, "%/" + esc, "%#" + esc}
	if !strings.HasSuffix(fqn, "()") {
		fnEsc := esc + "()"
		patterns = append(patterns, "%\\"+fnEsc, "%."+fnEsc, "%/"+fnEsc, "%#"+fnEsc)
	}
	where := make([]string, len(patterns))
	args := []any{shortName(fqn)}
	for i, p := range patterns {
		where[i] = "s.fqn LIKE ? ESCAPE '!'"
		args = append(args, p)
	}
	rows, err = e.s.DB().Query(`SELECT `+symbolCols+`
		WHERE s.name = ? AND (`+strings.Join(where, " OR ")+`)
		ORDER BY f.vendor ASC LIMIT 20`, args...)
	if err != nil {
		return nil, err
	}
	return scanHits(rows)
}

// shortName extracts the bare symbol name from an FQN-ish string:
// "A\B::bar()" -> "bar", "a/b.C.D()" -> "D", "mod#Button" -> "Button".
func shortName(fqn string) string {
	s := strings.TrimSuffix(fqn, "()")
	if i := strings.LastIndexAny(s, "\\./:#"); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimPrefix(s, "$")
}

func escapeLike(s string) string {
	r := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_")
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
	Resolved bool   `json:"resolved"`         // false = heuristic match
	Source   string `json:"source,omitempty"` // "ast" | "phpstan" | "go-types" | ...
}

func scanEdges(rows *sql.Rows) ([]EdgeHit, error) {
	defer rows.Close()
	var out []EdgeHit
	for rows.Next() {
		var h EdgeHit
		var resolved int
		if err := rows.Scan(&h.From, &h.Kind, &h.To, &h.File, &h.Line, &resolved, &h.Source); err != nil {
			return nil, err
		}
		h.Resolved = resolved == 1
		out = append(out, h)
	}
	return out, rows.Err()
}

// all_edges is the union view of AST edges and enrichment (PHPStan /
// go-types) edges; enrichment rows are resolved=1 by definition.
const edgeCols = `e.from_fqn, e.kind, e.to_fqn, e.file, e.line, e.resolved, e.source
	FROM all_edges e`

// EdgeFilter narrows call-graph results by file location.
type EdgeFilter struct {
	PathPrefix   string // root-relative path prefix, e.g. "api/src/"
	ExcludeTests bool
}

func (f EdgeFilter) sql() (cond string, args []any) {
	if f.PathPrefix != "" {
		cond += " AND e.file LIKE ? ESCAPE '!'"
		args = append(args, escapeLike(f.PathPrefix)+"%")
	}
	if f.ExcludeTests {
		cond += ` AND NOT (e.file LIKE 'tests/%' OR e.file LIKE '%/tests/%'
			OR e.file LIKE '%/Tests/%' OR e.file LIKE '%/__tests__/%'
			OR e.file LIKE '%_test.go' OR e.file LIKE '%.test.ts' OR e.file LIKE '%.test.tsx')`
	}
	return cond, args
}

// dedupeEdges collapses duplicates between the AST layer and
// enrichment layers. Rows are pre-sorted resolved-first, so the exact
// edge wins over its heuristic twin.
func dedupeEdges(hits []EdgeHit) []EdgeHit {
	type key struct {
		from, kind, to, file string
		line                 int
	}
	seen := map[key]bool{}
	out := hits[:0]
	for _, h := range hits {
		k := key{h.From, h.Kind, h.To, h.File, h.Line}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, h)
	}
	return out
}

// References returns all edges pointing at the symbol (any kind).
func (e *Engine) References(fqn string, limit int, f EdgeFilter) ([]EdgeHit, error) {
	cond, condArgs := f.sql()
	args := append([]any{fqn}, condArgs...)
	args = append(args, limit)
	rows, err := e.s.DB().Query(`SELECT `+edgeCols+`
		WHERE e.to_fqn = ?`+cond+` ORDER BY e.resolved DESC, e.file, e.line LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	hits, err := scanEdges(rows)
	if err != nil {
		return nil, err
	}
	return dedupeEdges(hits), nil
}

// Callers returns call edges into a callable. For methods, the class
// hierarchy is taken into account: calls recorded against an ancestor
// or descendant receiver with the same method name are included
// (marked unresolved), since the static receiver type in the source is
// often a supertype of the class that defines the method.
func (e *Engine) Callers(fqn string, limit int, f EdgeFilter) ([]EdgeHit, error) {
	cond, condArgs := f.sql()
	class, member, isMethod := methodSplit(fqn)
	if !isMethod {
		args := append([]any{fqn}, condArgs...)
		args = append(args, limit)
		rows, err := e.s.DB().Query(`SELECT `+edgeCols+`
			WHERE e.to_fqn = ? AND e.kind IN ('calls', 'instantiates')`+cond+`
			ORDER BY e.resolved DESC, e.file, e.line LIMIT ?`, args...)
		if err != nil {
			return nil, err
		}
		hits, err := scanEdges(rows)
		if err != nil {
			return nil, err
		}
		return dedupeEdges(hits), nil
	}
	// family = the class itself + its ancestors + its descendants.
	// Deliberately NOT descendants-of-ancestors: siblings sharing a
	// base class don't receive each other's calls.
	//
	// Two steps on purpose: the closure itself is fast (indexed
	// joins), but an `IN (SELECT ...)` over the all_edges UNION view
	// defeats predicate pushdown and scans every edge (~250ms on a
	// monolith). A literal IN list keeps the index in play (~ms).
	family, err := e.family(class)
	if err != nil {
		return nil, err
	}
	sep := memberSep(fqn)
	candidates := make([]string, 0, len(family))
	for _, f := range family {
		candidates = append(candidates, f+sep+member)
	}

	var hits []EdgeHit
	for start := 0; start < len(candidates); start += 400 {
		chunk := candidates[start:min(start+400, len(candidates))]
		placeholders := strings.Repeat("?, ", len(chunk)-1) + "?"
		args := make([]any, 0, len(chunk)+len(condArgs))
		for _, c := range chunk {
			args = append(args, c)
		}
		args = append(args, condArgs...)
		rows, err := e.s.DB().Query(`SELECT `+edgeCols+`
			WHERE e.kind = 'calls' AND e.to_fqn IN (`+placeholders+`)`+cond, args...)
		if err != nil {
			return nil, err
		}
		part, err := scanEdges(rows)
		if err != nil {
			return nil, err
		}
		hits = append(hits, part...)
	}

	// Hierarchy-widened matches are heuristic by construction.
	for i := range hits {
		if hits[i].To != fqn {
			hits[i].Resolved = false
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		hi, hj := hits[i], hits[j]
		if (hi.To == fqn) != (hj.To == fqn) {
			return hi.To == fqn
		}
		if hi.Resolved != hj.Resolved {
			return hi.Resolved
		}
		if hi.File != hj.File {
			return hi.File < hj.File
		}
		return hi.Line < hj.Line
	})
	hits = dedupeEdges(hits)
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// hierarchyStep expands one BFS frontier in the class hierarchy.
// direction "up" follows from->to (ancestors), "down" to->from
// (descendants). Queries hit edges and ext_edges directly — a
// recursive CTE over the all_edges UNION view cannot use their
// indexes and degrades to full scans per iteration.
func (e *Engine) hierarchyStep(frontier []string, direction string) (map[string]string, error) {
	srcCol, dstCol := "from_fqn", "to_fqn"
	if direction == "down" {
		srcCol, dstCol = "to_fqn", "from_fqn"
	}
	out := map[string]string{} // discovered fqn -> edge kind
	for start := 0; start < len(frontier); start += 400 {
		chunk := frontier[start:min(start+400, len(frontier))]
		placeholders := strings.Repeat("?, ", len(chunk)-1) + "?"
		args := make([]any, 0, len(chunk)*2)
		for _, c := range chunk {
			args = append(args, c)
		}
		for _, c := range chunk {
			args = append(args, c)
		}
		rows, err := e.s.DB().Query(`
			SELECT `+dstCol+`, kind FROM edges
				WHERE kind IN ('extends', 'implements', 'uses_trait') AND `+srcCol+` IN (`+placeholders+`)
			UNION
			SELECT `+dstCol+`, kind FROM ext_edges
				WHERE kind IN ('extends', 'implements', 'uses_trait') AND `+srcCol+` IN (`+placeholders+`)`,
			args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var fqn, kind string
			if err := rows.Scan(&fqn, &kind); err != nil {
				rows.Close()
				return nil, err
			}
			out[fqn] = kind
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}

// closure BFS-expands from a class in one direction until fixpoint,
// returning discovered fqn -> linking edge kind (start excluded).
func (e *Engine) closure(class, direction string, cap int) (map[string]string, error) {
	seen := map[string]string{}
	frontier := []string{class}
	for len(frontier) > 0 && len(seen) < cap {
		found, err := e.hierarchyStep(frontier, direction)
		if err != nil {
			return nil, err
		}
		frontier = frontier[:0]
		for fqn, kind := range found {
			if fqn == class {
				continue
			}
			if _, ok := seen[fqn]; !ok {
				seen[fqn] = kind
				frontier = append(frontier, fqn)
			}
		}
	}
	return seen, nil
}

// family returns the transitive hierarchy neighborhood of a class:
// itself, its ancestors and its descendants.
func (e *Engine) family(class string) ([]string, error) {
	up, err := e.closure(class, "up", 200)
	if err != nil {
		return nil, err
	}
	down, err := e.closure(class, "down", 2000)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(up)+len(down)+1)
	out = append(out, class)
	for fqn := range up {
		out = append(out, fqn)
	}
	for fqn := range down {
		if _, ok := up[fqn]; !ok {
			out = append(out, fqn)
		}
	}
	return out, nil
}

// methodSplit splits a member FQN into its container and member parts:
// PHP "A\B::bar()" -> ("A\B", "bar()"), Go "pkg.T.M()" -> ("pkg.T",
// "M()"). Not-a-member FQNs (bare classes) return ok=false.
func methodSplit(fqn string) (class, member string, ok bool) {
	if c, m, found := strings.Cut(fqn, "::"); found {
		return c, m, true
	}
	if !strings.HasSuffix(fqn, "()") {
		return "", "", false
	}
	if i := strings.LastIndex(fqn, "."); i >= 0 {
		return fqn[:i], fqn[i+1:], true
	}
	return "", "", false
}

// memberSep is the container/member separator used in the FQN dialect.
func memberSep(fqn string) string {
	if strings.Contains(fqn, "::") {
		return "::"
	}
	return "."
}

// Callees returns outgoing calls/instantiations of a symbol.
func (e *Engine) Callees(fqn string, limit int, f EdgeFilter) ([]EdgeHit, error) {
	cond, condArgs := f.sql()
	args := append([]any{fqn}, condArgs...)
	args = append(args, limit)
	rows, err := e.s.DB().Query(`SELECT `+edgeCols+`
		WHERE e.from_fqn = ? AND e.kind IN ('calls', 'instantiates')`+cond+`
		ORDER BY e.resolved DESC, e.line LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	hits, err := scanEdges(rows)
	if err != nil {
		return nil, err
	}
	return dedupeEdges(hits), nil
}

// Relative is one hierarchy neighbor.
type Relative struct {
	FQN  string `json:"fqn"`
	Kind string `json:"kind"` // edge kind linking it to the queried class
}

// Hierarchy returns supertypes and subtypes of a class-like symbol,
// each transitively closed.
func (e *Engine) Hierarchy(fqn string) (ancestors, descendants []Relative, err error) {
	toRelatives := func(m map[string]string) []Relative {
		out := make([]Relative, 0, len(m))
		for f, kind := range m {
			out = append(out, Relative{FQN: f, Kind: kind})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].FQN < out[j].FQN })
		return out
	}
	up, err := e.closure(fqn, "up", 200)
	if err != nil {
		return nil, nil, err
	}
	down, err := e.closure(fqn, "down", 2000)
	if err != nil {
		return nil, nil, err
	}
	return toRelatives(up), toRelatives(down), nil
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
