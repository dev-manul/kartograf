package php

import (
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"

	"gitlab.stripchat.dev/stripcash/kartograf/internal/core/model"
)

// extractor walks a parsed PHP tree and fills a FileIndex.
type extractor struct {
	src []byte
	fi  *model.FileIndex
}

func (e *extractor) text(n *tree_sitter.Node) string {
	return n.Utf8Text(e.src)
}

func nodeRange(n *tree_sitter.Node) model.Range {
	s, en := n.StartPosition(), n.EndPosition()
	return model.Range{
		StartLine: int(s.Row) + 1,
		StartCol:  int(s.Column) + 1,
		EndLine:   int(en.Row) + 1,
		EndCol:    int(en.Column) + 1,
	}
}

// walkScope processes statements at namespace/file level. ns is the
// current namespace ("" for global). Both namespace forms are handled:
// `namespace X;` switches ns for the following statements, while
// `namespace X { ... }` scopes only its body.
func (e *extractor) walkScope(scope *tree_sitter.Node, ns string) {
	for i := uint(0); i < scope.NamedChildCount(); i++ {
		n := scope.NamedChild(i)
		switch n.Kind() {
		case "namespace_definition":
			name := ""
			if nn := n.ChildByFieldName("name"); nn != nil {
				name = e.text(nn)
			}
			if body := n.ChildByFieldName("body"); body != nil {
				e.walkScope(body, name)
			} else {
				ns = name
			}
		case "namespace_use_declaration":
			e.extractUse(n)
		case "class_declaration":
			e.extractClassLike(n, ns, model.KindClass)
		case "interface_declaration":
			e.extractClassLike(n, ns, model.KindInterface)
		case "trait_declaration":
			e.extractClassLike(n, ns, model.KindTrait)
		case "enum_declaration":
			e.extractClassLike(n, ns, model.KindEnum)
		case "function_definition":
			e.extractFunction(n, ns)
		case "const_declaration":
			e.extractConsts(n, ns, "")
		}
	}
}

// extractUse handles `use A\B;`, `use A\B as C;`, `use function a\b;`,
// and the group form `use A\{B, C as D};`.
func (e *extractor) extractUse(n *tree_sitter.Node) {
	kind := "" // "", "function", "const"
	var prefix string
	for i := uint(0); i < n.ChildCount(); i++ {
		c := n.Child(i)
		switch c.Kind() {
		case "function", "const":
			kind = c.Kind()
		case "namespace_name":
			// group form prefix: use A\B\{...}
			prefix = strings.TrimPrefix(e.text(c), "\\")
		case "namespace_use_clause":
			e.addUseClause(c, "", kind)
		case "namespace_use_group":
			for j := uint(0); j < c.NamedChildCount(); j++ {
				gc := c.NamedChild(j)
				if gc.Kind() == "namespace_use_clause" {
					e.addUseClause(gc, prefix, kind)
				}
			}
		}
	}
}

func (e *extractor) addUseClause(clause *tree_sitter.Node, prefix, kind string) {
	var fqn, alias string
	aliasNode := clause.ChildByFieldName("alias")
	if aliasNode != nil {
		alias = e.text(aliasNode)
	}
	for i := uint(0); i < clause.ChildCount(); i++ {
		c := clause.Child(i)
		switch c.Kind() {
		case "function", "const":
			kind = c.Kind()
		case "qualified_name", "namespace_name":
			fqn = strings.TrimPrefix(e.text(c), "\\")
		case "name":
			if fqn == "" && (aliasNode == nil || c.Id() != aliasNode.Id()) {
				fqn = e.text(c)
			}
		}
	}
	if fqn == "" {
		return
	}
	if prefix != "" {
		fqn = prefix + "\\" + fqn
	}
	if alias == "" {
		if idx := strings.LastIndex(fqn, "\\"); idx >= 0 {
			alias = fqn[idx+1:]
		} else {
			alias = fqn
		}
	}
	e.fi.Imports = append(e.fi.Imports, model.Import{Alias: alias, FQN: fqn, Kind: kind})
}

func join(ns, name string) string {
	if ns == "" {
		return name
	}
	return ns + "\\" + name
}

func (e *extractor) addSymbol(s model.Symbol) {
	s.Lang = langID
	s.ID = langID + ":" + s.FQN
	s.File = e.fi.Path
	e.fi.Symbols = append(e.fi.Symbols, s)
}

// extractClassLike handles class/interface/trait/enum declarations.
func (e *extractor) extractClassLike(n *tree_sitter.Node, ns string, kind model.SymbolKind) {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return // anonymous class as a statement — not a named symbol
	}
	name := e.text(nameNode)
	fqn := join(ns, name)
	body := n.ChildByFieldName("body")

	e.addSymbol(model.Symbol{
		Kind:      kind,
		Name:      name,
		FQN:       fqn,
		Range:     nodeRange(n),
		Signature: e.signature(n, body),
		Doc:       e.docComment(n),
	})
	e.extractInheritance(n, fqn)
	if body != nil {
		e.walkClassBody(body, fqn)
	}
}

// extractInheritance records extends/implements facts with names as
// written in source; resolution to FQNs happens later.
func (e *extractor) extractInheritance(n *tree_sitter.Node, fqn string) {
	isInterface := n.Kind() == "interface_declaration"
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		switch c.Kind() {
		case "base_clause": // extends
			rel := model.EdgeExtends
			for j := uint(0); j < c.NamedChildCount(); j++ {
				e.fi.TypeRels = append(e.fi.TypeRels, model.TypeRel{From: fqn, Rel: rel, To: e.text(c.NamedChild(j))})
			}
			_ = isInterface // interfaces may extend multiple bases; same edge kind
		case "class_interface_clause": // implements
			for j := uint(0); j < c.NamedChildCount(); j++ {
				e.fi.TypeRels = append(e.fi.TypeRels, model.TypeRel{From: fqn, Rel: model.EdgeImplements, To: e.text(c.NamedChild(j))})
			}
		}
	}
}

// walkClassBody extracts members of a class-like declaration.
func (e *extractor) walkClassBody(body *tree_sitter.Node, classFQN string) {
	for i := uint(0); i < body.NamedChildCount(); i++ {
		n := body.NamedChild(i)
		switch n.Kind() {
		case "method_declaration":
			e.extractMethod(n, classFQN)
		case "property_declaration":
			e.extractProperties(n, classFQN)
		case "const_declaration":
			e.extractConsts(n, "", classFQN)
		case "enum_case":
			if nameNode := n.ChildByFieldName("name"); nameNode != nil {
				name := e.text(nameNode)
				e.addSymbol(model.Symbol{
					Kind:      model.KindEnumCase,
					Name:      name,
					FQN:       classFQN + "::" + name,
					Container: classFQN,
					Range:     nodeRange(n),
					Signature: strings.TrimRight(e.signature(n, nil), ";"),
					Doc:       e.docComment(n),
				})
			}
		case "use_declaration": // use SomeTrait;
			for j := uint(0); j < n.NamedChildCount(); j++ {
				c := n.NamedChild(j)
				switch c.Kind() {
				case "name", "qualified_name":
					e.fi.TypeRels = append(e.fi.TypeRels, model.TypeRel{From: classFQN, Rel: model.EdgeUsesTrait, To: e.text(c)})
				}
			}
		}
	}
}

func (e *extractor) extractMethod(n *tree_sitter.Node, classFQN string) {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	name := e.text(nameNode)
	e.addSymbol(model.Symbol{
		Kind:      model.KindMethod,
		Name:      name,
		FQN:       classFQN + "::" + name + "()",
		Container: classFQN,
		Range:     nodeRange(n),
		Signature: e.signature(n, n.ChildByFieldName("body")),
		Doc:       e.docComment(n),
	})
	if name == "__construct" {
		e.extractPromotedProperties(n, classFQN)
	}
}

// extractPromotedProperties turns constructor-promoted parameters
// (`__construct(private readonly Cache $cache)`) into property symbols.
func (e *extractor) extractPromotedProperties(method *tree_sitter.Node, classFQN string) {
	params := method.ChildByFieldName("parameters")
	if params == nil {
		return
	}
	for i := uint(0); i < params.NamedChildCount(); i++ {
		p := params.NamedChild(i)
		if p.Kind() != "property_promotion_parameter" {
			continue
		}
		nameNode := p.ChildByFieldName("name")
		if nameNode == nil {
			continue
		}
		name := strings.TrimPrefix(e.text(nameNode), "$")
		e.addSymbol(model.Symbol{
			Kind:      model.KindProperty,
			Name:      name,
			FQN:       classFQN + "::$" + name,
			Container: classFQN,
			Range:     nodeRange(p),
			Signature: strings.TrimRight(e.signature(p, nil), ","),
		})
	}
}

func (e *extractor) extractFunction(n *tree_sitter.Node, ns string) {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	name := e.text(nameNode)
	e.addSymbol(model.Symbol{
		Kind:      model.KindFunction,
		Name:      name,
		FQN:       join(ns, name) + "()",
		Range:     nodeRange(n),
		Signature: e.signature(n, n.ChildByFieldName("body")),
		Doc:       e.docComment(n),
	})
}

// extractProperties handles `private Foo $a, $b;` — one symbol per
// property_element. Exactly one of ns/classFQN context applies: PHP
// properties exist only inside classes.
func (e *extractor) extractProperties(n *tree_sitter.Node, classFQN string) {
	sig := strings.TrimRight(e.signature(n, nil), ";")
	doc := e.docComment(n)
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c.Kind() != "property_element" {
			continue
		}
		varName := ""
		if vn := c.ChildByFieldName("name"); vn != nil {
			varName = e.text(vn)
		} else if c.NamedChildCount() > 0 {
			varName = e.text(c.NamedChild(0))
		}
		if varName == "" {
			continue
		}
		name := strings.TrimPrefix(varName, "$")
		e.addSymbol(model.Symbol{
			Kind:      model.KindProperty,
			Name:      name,
			FQN:       classFQN + "::$" + name,
			Container: classFQN,
			Range:     nodeRange(c),
			Signature: sig,
			Doc:       doc,
		})
	}
}

// extractConsts handles both top-level and class constants; exactly one
// of ns/classFQN is meaningful.
func (e *extractor) extractConsts(n *tree_sitter.Node, ns, classFQN string) {
	sig := strings.TrimRight(e.signature(n, nil), ";")
	doc := e.docComment(n)
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c.Kind() != "const_element" {
			continue
		}
		if c.NamedChildCount() == 0 {
			continue
		}
		name := e.text(c.NamedChild(0))
		fqn := join(ns, name)
		container := ""
		if classFQN != "" {
			fqn = classFQN + "::" + name
			container = classFQN
		}
		e.addSymbol(model.Symbol{
			Kind:      model.KindConstant,
			Name:      name,
			FQN:       fqn,
			Container: container,
			Range:     nodeRange(c),
			Signature: sig,
			Doc:       doc,
		})
	}
}

// maxSignatureLen caps stored signatures: protects against huge inline
// array constants and garbage produced by parse-error recovery.
const maxSignatureLen = 200

// signature returns the declaration text up to (not including) its body,
// with whitespace runs collapsed: `public function bar(int $x): string`.
func (e *extractor) signature(n *tree_sitter.Node, body *tree_sitter.Node) string {
	start, end := n.ByteRange()
	if body != nil {
		end, _ = body.ByteRange()
	}
	if end > uint(len(e.src)) || start >= end {
		return ""
	}
	sig := strings.Join(strings.Fields(string(e.src[start:end])), " ")
	if len(sig) > maxSignatureLen {
		sig = sig[:maxSignatureLen] + "…"
	}
	return sig
}

// docComment returns the docblock immediately preceding the declaration,
// skipping over attribute lists (#[...] sit between docblock and node in
// source but are separate siblings in the tree).
func (e *extractor) docComment(n *tree_sitter.Node) string {
	prev := n.PrevNamedSibling()
	if prev == nil {
		return ""
	}
	if prev.Kind() == "comment" {
		t := e.text(prev)
		if strings.HasPrefix(t, "/**") {
			return t
		}
	}
	return ""
}
