package enrich

import (
	"fmt"
	"go/ast"
	"go/types"
	"io/fs"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"

	"gitlab.stripchat.dev/stripcash/kartograf/internal/core/store"
)

// RunGo type-checks every Go module under root with go/packages and
// returns precision edges the AST layer cannot see: method calls
// resolved to their declaring type (incl. interface method calls and
// calls through fields typed in other files), plus structural
// implements edges between project types and project interfaces.
func RunGo(root string, logf func(format string, args ...any)) ([]store.ExtEdge, error) {
	moduleDirs, err := findGoModules(root)
	if err != nil {
		return nil, err
	}
	if len(moduleDirs) == 0 {
		return nil, fmt.Errorf("no go.mod found under %s", root)
	}

	var edges []store.ExtEdge
	seen := map[store.ExtEdge]bool{}
	add := func(e store.ExtEdge) {
		if e.From == "" || e.To == "" || e.From == e.To {
			return
		}
		key := e
		key.Line = 0
		if seen[key] {
			return
		}
		seen[key] = true
		edges = append(edges, e)
	}

	for _, dir := range moduleDirs {
		logf("enrich(go): type-checking %s", dir)
		cfg := &packages.Config{
			Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
				packages.NeedTypes | packages.NeedTypesInfo | packages.NeedDeps |
				packages.NeedModule,
			Dir:   dir,
			Tests: false,
		}
		pkgs, err := packages.Load(cfg, "./...")
		if err != nil {
			return nil, fmt.Errorf("%s: %w", dir, err)
		}
		nerr := 0
		for _, p := range pkgs {
			nerr += len(p.Errors)
		}
		if nerr > 0 {
			logf("enrich(go): %s: %d load/type errors (best effort)", dir, nerr)
		}
		collectGoCalls(root, pkgs, add)
		collectGoImplements(root, pkgs, add)
	}
	return edges, nil
}

func findGoModules(root string) ([]string, error) {
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == "go.mod" {
			dirs = append(dirs, filepath.Dir(path))
		}
		return nil
	})
	return dirs, err
}

// goFuncFQN renders a *types.Func in kartograf's Go FQN dialect:
// pkg.Func() / pkg.Type.Method() (pointer receivers and generic
// instantiations are reduced to the base named type).
func goFuncFQN(fn *types.Func) string {
	sig, ok := fn.Type().(*types.Signature)
	if !ok {
		return ""
	}
	if recv := sig.Recv(); recv != nil {
		named := baseNamed(recv.Type())
		if named == nil {
			return ""
		}
		obj := named.Obj()
		if obj.Pkg() == nil {
			return "" // universe scope (error.Error)
		}
		return obj.Pkg().Path() + "." + obj.Name() + "." + fn.Name() + "()"
	}
	if fn.Pkg() == nil {
		return ""
	}
	return fn.Pkg().Path() + "." + fn.Name() + "()"
}

func baseNamed(t types.Type) *types.Named {
	for {
		switch v := t.(type) {
		case *types.Pointer:
			t = v.Elem()
		case *types.Named:
			return v
		case *types.Alias:
			t = types.Unalias(t)
		default:
			return nil
		}
	}
}

func namedFQN(n *types.Named) string {
	obj := n.Obj()
	if obj.Pkg() == nil {
		return ""
	}
	return obj.Pkg().Path() + "." + obj.Name()
}

// collectGoCalls walks function bodies and records calls resolved via
// the type checker. Closures are attributed to their enclosing
// declaration.
func collectGoCalls(root string, pkgs []*packages.Package, add func(store.ExtEdge)) {
	for _, p := range pkgs {
		if p.TypesInfo == nil {
			continue
		}
		for _, file := range p.Syntax {
			pos := p.Fset.Position(file.Pos())
			rel := relPath(root, pos.Filename)
			if rel == "" {
				continue // generated cache files etc.
			}
			for _, decl := range file.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				from := ""
				if def, ok := p.TypesInfo.Defs[fd.Name].(*types.Func); ok {
					from = goFuncFQN(def)
				}
				if from == "" {
					continue
				}
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					if callee := calleeFunc(p.TypesInfo, call); callee != nil {
						if to := goFuncFQN(callee); to != "" {
							add(store.ExtEdge{
								From: from, Kind: "calls", To: to,
								File: rel, Line: p.Fset.Position(call.Pos()).Line,
							})
						}
					}
					return true
				})
			}
		}
	}
}

// calleeFunc resolves the called function object, or nil for func
// values, conversions and builtins.
func calleeFunc(info *types.Info, call *ast.CallExpr) *types.Func {
	switch fun := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		if f, ok := info.Uses[fun].(*types.Func); ok {
			return f
		}
	case *ast.SelectorExpr:
		if sel, ok := info.Selections[fun]; ok && sel.Kind() == types.MethodVal {
			if f, ok := sel.Obj().(*types.Func); ok {
				return f
			}
			return nil
		}
		// Package-qualified call: pkg.Func()
		if f, ok := info.Uses[fun.Sel].(*types.Func); ok {
			return f
		}
	}
	return nil
}

// collectGoImplements emits structural implements edges between
// project-local concrete types and project-local interfaces.
func collectGoImplements(root string, pkgs []*packages.Package, add func(store.ExtEdge)) {
	type namedType struct {
		named *types.Named
		file  string
		line  int
	}
	var concretes []namedType
	var ifaces []*types.Named

	for _, p := range pkgs {
		if p.Types == nil || !isProjectPkg(root, p) {
			continue
		}
		scope := p.Types.Scope()
		for _, name := range scope.Names() {
			tn, ok := scope.Lookup(name).(*types.TypeName)
			if !ok || tn.IsAlias() {
				continue
			}
			named, ok := tn.Type().(*types.Named)
			if !ok {
				continue
			}
			if iface, ok := named.Underlying().(*types.Interface); ok {
				if !iface.Empty() {
					ifaces = append(ifaces, named)
				}
				continue
			}
			pos := p.Fset.Position(tn.Pos())
			concretes = append(concretes, namedType{named, relPath(root, pos.Filename), pos.Line})
		}
	}

	for _, c := range concretes {
		for _, i := range ifaces {
			iface := i.Underlying().(*types.Interface)
			if types.Implements(c.named, iface) || types.Implements(types.NewPointer(c.named), iface) {
				add(store.ExtEdge{
					From: namedFQN(c.named), Kind: "implements", To: namedFQN(i),
					File: c.file, Line: c.line,
				})
			}
		}
	}
}

func isProjectPkg(root string, p *packages.Package) bool {
	if p.Module == nil || p.Module.Dir == "" {
		return false
	}
	rel, err := filepath.Rel(root, p.Module.Dir)
	return err == nil && !strings.HasPrefix(rel, "..")
}

func relPath(root, abs string) string {
	rel, err := filepath.Rel(root, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	return filepath.ToSlash(rel)
}
