package query

import (
	"database/sql"
	"strings"
)

// aliasDepth is how many barrel hops a lookup will follow.
// index -> pkg -> real is two; three covers one more wrapper.
const aliasDepth = 3

type reexportAlias struct {
	fqn  string
	star bool
}

// aliasesOf returns FQNs that re-export fqn, including through
// `export *`, and whether every hop was a named reexport.
// Keys match the shape of fqn: a function `mod#name()` yields
// `barrel#name()`, a method `mod#Type.method()` yields
// `barrel#Type.method()`. Languages without a `#` module separator
// have no barrel edges and get an empty map.
func (e *Engine) aliasesOf(fqn string) (map[string]bool, error) {
	if !strings.Contains(fqn, "#") {
		return nil, nil
	}
	base, restore := exportBase(fqn)
	type item struct {
		fqn   string
		exact bool
	}
	seen := map[string]bool{base: true}
	frontier := []item{{fqn: base, exact: true}}
	found := map[string]bool{}
	for depth := 0; depth < aliasDepth && len(frontier) > 0; depth++ {
		next := []item{}
		for _, cur := range frontier {
			als, err := e.directAliases(cur.fqn)
			if err != nil {
				return nil, err
			}
			for _, a := range als {
				if seen[a.fqn] {
					continue
				}
				seen[a.fqn] = true
				exact := cur.exact && !a.star
				restored := restore(a.fqn)
				if restored != fqn {
					if prev, ok := found[restored]; !ok || (exact && !prev) {
						found[restored] = exact
					}
				}
				next = append(next, item{fqn: a.fqn, exact: exact})
			}
		}
		frontier = next
	}
	if len(found) == 0 {
		return nil, nil
	}
	return found, nil
}

// directAliases lists names that reexport fqn in one hop.
// A star reexport of the module (`barrel#*` -> module) becomes
// `barrel#Name` and is marked star.
func (e *Engine) directAliases(fqn string) ([]reexportAlias, error) {
	module, name, hasName := strings.Cut(fqn, "#")
	args := []any{fqn}
	if hasName && name != "" && name != "*" {
		args = append(args, module)
	}
	placeholders := strings.Repeat("?,", len(args))
	placeholders = placeholders[:len(placeholders)-1]
	rows, err := e.s.DB().Query(
		`SELECT from_fqn, to_fqn FROM edges WHERE kind = 'reexports' AND to_fqn IN (`+placeholders+`)`,
		args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []reexportAlias
	for rows.Next() {
		var from, to string
		if err := rows.Scan(&from, &to); err != nil {
			return nil, err
		}
		if to == fqn && !strings.HasSuffix(from, "#*") {
			out = append(out, reexportAlias{fqn: from})
			continue
		}
		if hasName && to == module && strings.HasSuffix(from, "#*") {
			out = append(out, reexportAlias{fqn: strings.TrimSuffix(from, "*") + name, star: true})
		}
	}
	return out, rows.Err()
}

// canonical follows reexport edges from fqn to the symbol that defines
// it. exact is false when a hop was `export *`.
func (e *Engine) canonical(fqn string) (string, bool, error) {
	if !strings.Contains(fqn, "#") {
		return fqn, false, nil
	}
	base, restore := exportBase(fqn)
	cur := base
	exact := true
	moved := false
	seen := map[string]bool{}
	for range aliasDepth {
		if seen[cur] {
			break
		}
		seen[cur] = true
		next, star, ok, err := e.reexportTarget(cur)
		if err != nil || !ok {
			return restoreIf(moved, cur, fqn, restore), exact, err
		}
		if star {
			exact = false
		}
		cur = next
		moved = true
	}
	return restoreIf(moved, cur, fqn, restore), exact, nil
}

func restoreIf(moved bool, cur, orig string, restore func(string) string) string {
	if !moved || cur == "" {
		return orig
	}
	return restore(cur)
}

func (e *Engine) reexportTarget(fqn string) (next string, star, ok bool, err error) {
	var to string
	err = e.s.DB().QueryRow(
		`SELECT to_fqn FROM edges WHERE from_fqn = ? AND kind = 'reexports' AND instr(to_fqn, '#') > 0 LIMIT 1`,
		fqn).Scan(&to)
	if err == nil {
		return to, false, true, nil
	}
	if err != sql.ErrNoRows {
		return "", false, false, err
	}
	module, name, hasName := strings.Cut(fqn, "#")
	if !hasName || name == "" || name == "*" {
		return "", false, false, nil
	}
	err = e.s.DB().QueryRow(
		`SELECT to_fqn FROM edges WHERE from_fqn = ? AND kind = 'reexports' LIMIT 1`,
		module+"#*").Scan(&to)
	if err == sql.ErrNoRows {
		return "", false, false, nil
	}
	if err != nil {
		return "", false, false, err
	}
	if strings.Contains(to, "#") {
		return to, true, true, nil
	}
	return to + "#" + name, true, true, nil
}

// exportBase strips a call or method suffix so reexport edges, which
// are stored as module#Name, can be matched. restore puts it back.
func exportBase(fqn string) (base string, restore func(string) string) {
	keep := func(s string) string { return s }
	if !strings.HasSuffix(fqn, "()") {
		return fqn, keep
	}
	if class, member, ok := methodSplit(fqn); ok {
		return class, func(s string) string {
			if strings.HasSuffix(s, "()") {
				return s
			}
			return s + "." + member
		}
	}
	return strings.TrimSuffix(fqn, "()"), func(s string) string {
		if strings.HasSuffix(s, "()") {
			return s
		}
		return s + "()"
	}
}

// aliasTargets is fqn plus every barrel name that re-exports it.
func (e *Engine) aliasTargets(fqn string) ([]string, map[string]bool, error) {
	aliases, err := e.aliasesOf(fqn)
	if err != nil {
		return nil, nil, err
	}
	targets := make([]string, 0, 1+len(aliases))
	targets = append(targets, fqn)
	for a := range aliases {
		targets = append(targets, a)
	}
	return targets, aliases, nil
}

// applyAlias rewrites an edge target that was recorded against a
// barrel name back to the symbol the caller asked about.
func applyAlias(h *EdgeHit, queried string, aliases map[string]bool) {
	if h.To == queried || aliases == nil {
		return
	}
	exact, ok := aliases[h.To]
	if !ok {
		return
	}
	h.To = queried
	h.Resolved = exact
}
