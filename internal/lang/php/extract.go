package php

import (
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"

	"gitlab.stripchat.dev/stripcash/kartograf/internal/core/model"
)

// maxSignatureLen caps stored signatures: protects against huge inline
// array constants and garbage produced by parse-error recovery.
const maxSignatureLen = 200

// builtinTypes are PHP type keywords that can appear where a class
// name is expected but never resolve to a symbol.
var builtinTypes = map[string]bool{
	"int": true, "float": true, "string": true, "bool": true,
	"array": true, "void": true, "mixed": true, "never": true,
	"null": true, "false": true, "true": true, "object": true,
	"callable": true, "iterable": true, "self": true, "static": true,
	"parent": true,
}

// scope is the name-resolution context of a namespace block.
type scope struct {
	ns        string
	uses      map[string]string // lowercased alias -> FQN (classes)
	usesFn    map[string]string
	usesConst map[string]string
}

func newScope(ns string) *scope {
	return &scope{
		ns:        ns,
		uses:      map[string]string{},
		usesFn:    map[string]string{},
		usesConst: map[string]string{},
	}
}

// classCtx is the file-local knowledge about the class whose body is
// being walked: enough to resolve $this->, self::, parent:: and calls
// through typed properties.
type classCtx struct {
	fqn       string
	parent    string            // resolved parent FQN, "" if none/unknown
	propTypes map[string]string // property name -> resolved type FQN
}

// extractor walks a parsed PHP tree and fills a FileIndex.
type extractor struct {
	src      []byte
	fi       *model.FileIndex
	skipRefs bool

	refSeen map[model.Ref]bool // dedup, line zeroed in the key
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

func (e *extractor) addRef(r model.Ref) {
	if e.skipRefs && r.Kind != model.EdgeExtends &&
		r.Kind != model.EdgeImplements && r.Kind != model.EdgeUsesTrait {
		return
	}
	if r.To == "" || r.To == r.From {
		return
	}
	key := r
	key.Line = 0
	if e.refSeen[key] {
		return
	}
	e.refSeen[key] = true
	e.fi.Refs = append(e.fi.Refs, r)
}

// resolveClass applies PHP name-resolution rules to a class-like name
// as written in source. Returns "" for names that cannot denote a
// class (builtin type keywords).
func (e *extractor) resolveClass(name string, sc *scope, cls *classCtx) (fqn string, exact bool) {
	if name == "" {
		return "", false
	}
	if strings.HasPrefix(name, "\\") {
		return name[1:], true
	}
	lower := strings.ToLower(name)
	switch lower {
	case "self", "static":
		if cls != nil {
			// static:: is late-bound; treat as the current class,
			// which is correct for navigation purposes.
			return cls.fqn, true
		}
		return "", false
	case "parent":
		if cls != nil && cls.parent != "" {
			return cls.parent, false // may be a grandparent's member
		}
		return "", false
	}
	if builtinTypes[lower] {
		return "", false
	}
	first, rest, cut := strings.Cut(name, "\\")
	if target, ok := sc.uses[strings.ToLower(first)]; ok {
		if cut {
			return target + "\\" + rest, true
		}
		return target, true
	}
	// Unqualified/relative class names resolve to the current
	// namespace (no global fallback for classes in PHP).
	return join(sc.ns, name), true
}

// resolveFunction resolves a called function name. Unqualified names
// inside a namespace are ambiguous (current namespace first, global
// fallback) — those come back exact=false with the global candidate.
func (e *extractor) resolveFunction(name string, sc *scope) (fqn string, exact bool) {
	if strings.HasPrefix(name, "\\") {
		return name[1:], true
	}
	first, rest, cut := strings.Cut(name, "\\")
	if !cut {
		if target, ok := sc.usesFn[strings.ToLower(first)]; ok {
			return target, true
		}
		if sc.ns == "" {
			return name, true
		}
		return name, false // likely a global builtin; ns\name also possible
	}
	if target, ok := sc.uses[strings.ToLower(first)]; ok {
		return target + "\\" + rest, true
	}
	return join(sc.ns, name), true
}

// walkScope processes statements at namespace/file level. Both
// namespace forms are handled: `namespace X;` switches context for the
// following statements, `namespace X { ... }` scopes only its body.
func (e *extractor) walkScope(scopeNode *tree_sitter.Node, sc *scope) {
	for i := uint(0); i < scopeNode.NamedChildCount(); i++ {
		n := scopeNode.NamedChild(i)
		switch n.Kind() {
		case "namespace_definition":
			name := ""
			if nn := n.ChildByFieldName("name"); nn != nil {
				name = e.text(nn)
			}
			if body := n.ChildByFieldName("body"); body != nil {
				e.walkScope(body, newScope(name))
			} else {
				sc = newScope(name)
			}
		case "namespace_use_declaration":
			e.extractUse(n, sc)
		case "class_declaration":
			e.extractClassLike(n, sc, model.KindClass)
		case "interface_declaration":
			e.extractClassLike(n, sc, model.KindInterface)
		case "trait_declaration":
			e.extractClassLike(n, sc, model.KindTrait)
		case "enum_declaration":
			e.extractClassLike(n, sc, model.KindEnum)
		case "function_definition":
			e.extractFunction(n, sc)
		case "const_declaration":
			e.extractConsts(n, sc.ns, "")
		default:
			// Top-level statements (bootstrap code, route files).
			e.walkBody(n, "", sc, nil, nil)
		}
	}
}

// extractUse handles `use A\B;`, `use A\B as C;`, `use function a\b;`,
// and the group form `use A\{B, C as D};`.
func (e *extractor) extractUse(n *tree_sitter.Node, sc *scope) {
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
			e.addUseClause(c, prefix, kind, sc)
		case "namespace_use_group":
			for j := uint(0); j < c.NamedChildCount(); j++ {
				gc := c.NamedChild(j)
				if gc.Kind() == "namespace_use_clause" {
					e.addUseClause(gc, prefix, kind, sc)
				}
			}
		}
	}
}

func (e *extractor) addUseClause(clause *tree_sitter.Node, prefix, kind string, sc *scope) {
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
	switch kind {
	case "function":
		sc.usesFn[strings.ToLower(alias)] = fqn
	case "const":
		sc.usesConst[strings.ToLower(alias)] = fqn
	default:
		sc.uses[strings.ToLower(alias)] = fqn
	}
}

// extractClassLike handles class/interface/trait/enum declarations.
func (e *extractor) extractClassLike(n *tree_sitter.Node, sc *scope, kind model.SymbolKind) {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return // anonymous class as a statement — not a named symbol
	}
	name := e.text(nameNode)
	fqn := join(sc.ns, name)
	body := n.ChildByFieldName("body")

	e.addSymbol(model.Symbol{
		Kind:      kind,
		Name:      name,
		FQN:       fqn,
		Range:     nodeRange(n),
		Signature: e.signature(n, body),
		Doc:       e.docComment(n),
	})

	cls := &classCtx{fqn: fqn, propTypes: map[string]string{}}
	e.extractInheritance(n, sc, cls)
	if body != nil {
		e.collectMemberTypes(body, sc, cls)
		e.walkClassBody(body, sc, cls)
	}
}

// extractInheritance resolves and records extends/implements edges.
func (e *extractor) extractInheritance(n *tree_sitter.Node, sc *scope, cls *classCtx) {
	line := nodeRange(n).StartLine
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		switch c.Kind() {
		case "base_clause": // extends (single for classes, list for interfaces)
			for j := uint(0); j < c.NamedChildCount(); j++ {
				fqn, exact := e.resolveClass(e.text(c.NamedChild(j)), sc, nil)
				if fqn == "" {
					continue
				}
				if n.Kind() == "class_declaration" && cls.parent == "" {
					cls.parent = fqn
				}
				e.addRef(model.Ref{From: cls.fqn, Kind: model.EdgeExtends, To: fqn, Resolved: exact, Line: line})
			}
		case "class_interface_clause": // implements
			for j := uint(0); j < c.NamedChildCount(); j++ {
				fqn, exact := e.resolveClass(e.text(c.NamedChild(j)), sc, nil)
				if fqn == "" {
					continue
				}
				e.addRef(model.Ref{From: cls.fqn, Kind: model.EdgeImplements, To: fqn, Resolved: exact, Line: line})
			}
		}
	}
}

// collectMemberTypes pre-scans a class body for typed properties
// (declared or constructor-promoted) so that method bodies can resolve
// `$this->prop->method()` regardless of declaration order.
func (e *extractor) collectMemberTypes(body *tree_sitter.Node, sc *scope, cls *classCtx) {
	for i := uint(0); i < body.NamedChildCount(); i++ {
		n := body.NamedChild(i)
		switch n.Kind() {
		case "property_declaration":
			typeFQN := e.singleTypeFQN(n.ChildByFieldName("type"), sc, cls)
			if typeFQN == "" {
				continue
			}
			for j := uint(0); j < n.NamedChildCount(); j++ {
				c := n.NamedChild(j)
				if c.Kind() != "property_element" {
					continue
				}
				if name := e.propertyElementName(c); name != "" {
					cls.propTypes[name] = typeFQN
				}
			}
		case "method_declaration":
			nameNode := n.ChildByFieldName("name")
			if nameNode == nil || e.text(nameNode) != "__construct" {
				continue
			}
			params := n.ChildByFieldName("parameters")
			if params == nil {
				continue
			}
			for j := uint(0); j < params.NamedChildCount(); j++ {
				p := params.NamedChild(j)
				if p.Kind() != "property_promotion_parameter" {
					continue
				}
				typeFQN := e.singleTypeFQN(p.ChildByFieldName("type"), sc, cls)
				nameNode := p.ChildByFieldName("name")
				if typeFQN == "" || nameNode == nil {
					continue
				}
				cls.propTypes[strings.TrimPrefix(e.text(nameNode), "$")] = typeFQN
			}
		}
	}
}

// singleTypeFQN resolves a type node to a class FQN when it denotes a
// single (possibly nullable) class type; unions, intersections and
// builtins yield "".
func (e *extractor) singleTypeFQN(typeNode *tree_sitter.Node, sc *scope, cls *classCtx) string {
	n := typeNode
	for n != nil {
		switch n.Kind() {
		case "optional_type": // ?X
			n = n.NamedChild(0)
		case "named_type":
			fqn, _ := e.resolveClass(e.text(n), sc, cls)
			return fqn
		default:
			return ""
		}
	}
	return ""
}

func (e *extractor) propertyElementName(el *tree_sitter.Node) string {
	if vn := el.ChildByFieldName("name"); vn != nil {
		return strings.TrimPrefix(e.text(vn), "$")
	}
	if el.NamedChildCount() > 0 {
		return strings.TrimPrefix(e.text(el.NamedChild(0)), "$")
	}
	return ""
}

// walkClassBody extracts members of a class-like declaration.
func (e *extractor) walkClassBody(body *tree_sitter.Node, sc *scope, cls *classCtx) {
	for i := uint(0); i < body.NamedChildCount(); i++ {
		n := body.NamedChild(i)
		switch n.Kind() {
		case "method_declaration":
			e.extractMethod(n, sc, cls)
		case "property_declaration":
			e.extractProperties(n, sc, cls)
		case "const_declaration":
			e.extractConsts(n, "", cls.fqn)
		case "enum_case":
			if nameNode := n.ChildByFieldName("name"); nameNode != nil {
				name := e.text(nameNode)
				e.addSymbol(model.Symbol{
					Kind:      model.KindEnumCase,
					Name:      name,
					FQN:       cls.fqn + "::" + name,
					Container: cls.fqn,
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
					fqn, exact := e.resolveClass(e.text(c), sc, cls)
					if fqn != "" {
						e.addRef(model.Ref{From: cls.fqn, Kind: model.EdgeUsesTrait, To: fqn, Resolved: exact, Line: nodeRange(n).StartLine})
					}
				}
			}
		}
	}
}

func (e *extractor) extractMethod(n *tree_sitter.Node, sc *scope, cls *classCtx) {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	name := e.text(nameNode)
	fqn := cls.fqn + "::" + name + "()"
	e.addSymbol(model.Symbol{
		Kind:      model.KindMethod,
		Name:      name,
		FQN:       fqn,
		Container: cls.fqn,
		Range:     nodeRange(n),
		Signature: e.signature(n, n.ChildByFieldName("body")),
		Doc:       e.docComment(n),
	})
	if name == "__construct" {
		e.extractPromotedProperties(n, cls)
	}
	vars := e.signatureTypes(n, fqn, sc, cls)
	if body := n.ChildByFieldName("body"); body != nil {
		e.walkBody(body, fqn, sc, cls, vars)
	}
}

func (e *extractor) extractFunction(n *tree_sitter.Node, sc *scope) {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	name := e.text(nameNode)
	fqn := join(sc.ns, name) + "()"
	e.addSymbol(model.Symbol{
		Kind:      model.KindFunction,
		Name:      name,
		FQN:       fqn,
		Range:     nodeRange(n),
		Signature: e.signature(n, n.ChildByFieldName("body")),
		Doc:       e.docComment(n),
	})
	vars := e.signatureTypes(n, fqn, sc, nil)
	if body := n.ChildByFieldName("body"); body != nil {
		e.walkBody(body, fqn, sc, nil, vars)
	}
}

// signatureTypes emits references_type edges for parameter/return type
// hints and returns a map of typed parameter variables ($name -> FQN)
// for resolving method calls on them in the body.
func (e *extractor) signatureTypes(n *tree_sitter.Node, from string, sc *scope, cls *classCtx) map[string]string {
	vars := map[string]string{}
	if params := n.ChildByFieldName("parameters"); params != nil {
		for i := uint(0); i < params.NamedChildCount(); i++ {
			p := params.NamedChild(i)
			typeNode := p.ChildByFieldName("type")
			e.emitTypeRefs(typeNode, from, sc, cls)
			if fqn := e.singleTypeFQN(typeNode, sc, cls); fqn != "" {
				if nameNode := p.ChildByFieldName("name"); nameNode != nil {
					vars[e.text(nameNode)] = fqn // key includes "$"
				}
			}
		}
	}
	e.emitTypeRefs(n.ChildByFieldName("return_type"), from, sc, cls)
	return vars
}

// emitTypeRefs records references_type edges for every class name
// inside a (possibly union/intersection/nullable) type node.
func (e *extractor) emitTypeRefs(typeNode *tree_sitter.Node, from string, sc *scope, cls *classCtx) {
	if typeNode == nil || e.skipRefs {
		return
	}
	var walk func(n *tree_sitter.Node)
	walk = func(n *tree_sitter.Node) {
		if n.Kind() == "named_type" {
			if fqn, exact := e.resolveClass(e.text(n), sc, cls); fqn != "" {
				e.addRef(model.Ref{From: from, Kind: model.EdgeReferencesType, To: fqn, Resolved: exact, Line: nodeRange(n).StartLine})
			}
			return
		}
		for i := uint(0); i < n.NamedChildCount(); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(typeNode)
}

// walkBody recursively collects references from executable code.
// from is the enclosing symbol FQN, vars maps typed parameter
// variables to class FQNs ($x -> App\Foo).
func (e *extractor) walkBody(n *tree_sitter.Node, from string, sc *scope, cls *classCtx, vars map[string]string) {
	if e.skipRefs {
		return
	}
	line := func(node *tree_sitter.Node) int { return nodeRange(node).StartLine }

	switch n.Kind() {
	case "object_creation_expression":
		for i := uint(0); i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			if c.Kind() == "name" || c.Kind() == "qualified_name" {
				if fqn, exact := e.resolveClass(e.text(c), sc, cls); fqn != "" {
					e.addRef(model.Ref{From: from, Kind: model.EdgeInstantiates, To: fqn, Resolved: exact, Line: line(n)})
				}
				break
			}
		}

	case "scoped_call_expression": // X::m(), self::m(), parent::m()
		scopeN, nameN := n.ChildByFieldName("scope"), n.ChildByFieldName("name")
		if scopeN != nil && nameN != nil && nameN.Kind() == "name" {
			if fqn, exact := e.resolveClass(e.text(scopeN), sc, cls); fqn != "" {
				e.addRef(model.Ref{From: from, Kind: model.EdgeCalls, To: fqn + "::" + e.text(nameN) + "()", Resolved: exact, Line: line(n)})
			}
		}

	case "class_constant_access_expression": // X::CONST, X::class
		if n.NamedChildCount() >= 2 {
			scopeN, nameN := n.NamedChild(0), n.NamedChild(1)
			if fqn, exact := e.resolveClass(e.text(scopeN), sc, cls); fqn != "" {
				if e.text(nameN) == "class" {
					e.addRef(model.Ref{From: from, Kind: model.EdgeReferencesType, To: fqn, Resolved: exact, Line: line(n)})
				} else {
					e.addRef(model.Ref{From: from, Kind: model.EdgeReferences, To: fqn + "::" + e.text(nameN), Resolved: exact, Line: line(n)})
				}
			}
		}

	case "scoped_property_access_expression": // X::$prop
		scopeN, nameN := n.ChildByFieldName("scope"), n.ChildByFieldName("name")
		if scopeN != nil && nameN != nil {
			if fqn, exact := e.resolveClass(e.text(scopeN), sc, cls); fqn != "" {
				e.addRef(model.Ref{From: from, Kind: model.EdgeReferences, To: fqn + "::" + e.text(nameN), Resolved: exact, Line: line(n)})
			}
		}

	case "member_call_expression": // $expr->m()
		if nameN := n.ChildByFieldName("name"); nameN != nil && nameN.Kind() == "name" {
			if recv := e.receiverType(n.ChildByFieldName("object"), cls, vars); recv != "" {
				e.addRef(model.Ref{From: from, Kind: model.EdgeCalls, To: recv + "::" + e.text(nameN) + "()", Resolved: false, Line: line(n)})
			}
		}

	case "function_call_expression":
		if fn := n.ChildByFieldName("function"); fn != nil {
			if fn.Kind() == "name" || fn.Kind() == "qualified_name" {
				if fqn, exact := e.resolveFunction(e.text(fn), sc); fqn != "" {
					e.addRef(model.Ref{From: from, Kind: model.EdgeCalls, To: fqn + "()", Resolved: exact, Line: line(n)})
				}
			}
		}

	case "binary_expression": // $x instanceof Foo
		if op := n.ChildByFieldName("operator"); op != nil && op.Kind() == "instanceof" {
			if right := n.ChildByFieldName("right"); right != nil &&
				(right.Kind() == "name" || right.Kind() == "qualified_name") {
				if fqn, exact := e.resolveClass(e.text(right), sc, cls); fqn != "" {
					e.addRef(model.Ref{From: from, Kind: model.EdgeReferencesType, To: fqn, Resolved: exact, Line: line(n)})
				}
			}
		}

	case "catch_clause":
		if typeN := n.ChildByFieldName("type"); typeN != nil {
			e.emitTypeRefs(typeN, from, sc, cls)
		}

	case "attribute":
		for i := uint(0); i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			if c.Kind() == "name" || c.Kind() == "qualified_name" {
				if fqn, exact := e.resolveClass(e.text(c), sc, cls); fqn != "" {
					e.addRef(model.Ref{From: from, Kind: model.EdgeReferencesType, To: fqn, Resolved: exact, Line: line(n)})
				}
				break
			}
		}
	}

	for i := uint(0); i < n.NamedChildCount(); i++ {
		e.walkBody(n.NamedChild(i), from, sc, cls, vars)
	}
}

// receiverType infers the class of a method-call receiver from
// file-local knowledge: $this, typed parameters, typed properties.
func (e *extractor) receiverType(obj *tree_sitter.Node, cls *classCtx, vars map[string]string) string {
	if obj == nil {
		return ""
	}
	switch obj.Kind() {
	case "variable_name":
		name := e.text(obj)
		if name == "$this" {
			if cls != nil {
				return cls.fqn
			}
			return ""
		}
		return vars[name]
	case "member_access_expression": // $this->prop
		inner, nameN := obj.ChildByFieldName("object"), obj.ChildByFieldName("name")
		if cls != nil && inner != nil && nameN != nil &&
			inner.Kind() == "variable_name" && e.text(inner) == "$this" {
			return cls.propTypes[e.text(nameN)]
		}
	}
	return ""
}

// extractPromotedProperties turns constructor-promoted parameters
// (`__construct(private readonly Cache $cache)`) into property symbols.
func (e *extractor) extractPromotedProperties(method *tree_sitter.Node, cls *classCtx) {
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
			FQN:       cls.fqn + "::$" + name,
			Container: cls.fqn,
			Range:     nodeRange(p),
			Signature: strings.TrimRight(e.signature(p, nil), ","),
		})
	}
}

// extractProperties handles `private Foo $a, $b;` — one symbol per
// property_element.
func (e *extractor) extractProperties(n *tree_sitter.Node, sc *scope, cls *classCtx) {
	sig := strings.TrimRight(e.signature(n, nil), ";")
	doc := e.docComment(n)
	e.emitTypeRefs(n.ChildByFieldName("type"), cls.fqn, sc, cls)
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c.Kind() != "property_element" {
			continue
		}
		name := e.propertyElementName(c)
		if name == "" {
			continue
		}
		e.addSymbol(model.Symbol{
			Kind:      model.KindProperty,
			Name:      name,
			FQN:       cls.fqn + "::$" + name,
			Container: cls.fqn,
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
