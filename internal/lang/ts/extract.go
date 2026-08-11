package ts

import (
	"path"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"

	"gitlab.stripchat.dev/stripcash/kartograf/internal/core/model"
)

const maxSignatureLen = 200

// importTarget is where an imported local name points.
type importTarget struct {
	module string // resolved module path or bare specifier
	name   string // original exported name ("" = namespace import)
}

// extractor walks a parsed TS/JS tree and fills a FileIndex.
type extractor struct {
	src      []byte
	fi       *model.FileIndex
	mod      string // this file's module path (FQN prefix)
	fileDir  string
	skipRefs bool
	pkgDirs  map[string]string // package name -> root-relative dir

	imports map[string]importTarget // local name -> target
	locals  map[string]bool         // top-level names declared in this file
	refSeen map[model.Ref]bool
}

func (e *extractor) text(n *tree_sitter.Node) string { return n.Utf8Text(e.src) }

func nodeRange(n *tree_sitter.Node) model.Range {
	s, en := n.StartPosition(), n.EndPosition()
	return model.Range{
		StartLine: int(s.Row) + 1, StartCol: int(s.Column) + 1,
		EndLine: int(en.Row) + 1, EndCol: int(en.Column) + 1,
	}
}

func (e *extractor) addSymbol(s model.Symbol) {
	s.Lang = langID
	s.ID = langID + ":" + s.FQN
	s.File = e.fi.Path
	e.fi.Symbols = append(e.fi.Symbols, s)
}

func (e *extractor) addRef(r model.Ref) {
	if e.skipRefs && r.Kind != model.EdgeExtends && r.Kind != model.EdgeImplements {
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

func (e *extractor) qual(name string) string { return e.mod + "#" + name }

// walkFile runs two passes: first collect imports and top-level
// declared names (JS scoping: an unqualified identifier can be a
// local, an import or a global — only the first two are resolvable),
// then extract symbols and references.
func (e *extractor) walkFile(root *tree_sitter.Node) {
	for i := uint(0); i < root.NamedChildCount(); i++ {
		e.scanTopLevel(root.NamedChild(i))
	}
	for i := uint(0); i < root.NamedChildCount(); i++ {
		e.extractStatement(root.NamedChild(i))
	}
}

// unwrapExport returns the declaration inside an export statement
// (or the node itself).
func (e *extractor) unwrapExport(n *tree_sitter.Node) *tree_sitter.Node {
	if n.Kind() != "export_statement" {
		return n
	}
	if d := n.ChildByFieldName("declaration"); d != nil {
		return d
	}
	if d := n.ChildByFieldName("value"); d != nil {
		return d
	}
	return n
}

func (e *extractor) scanTopLevel(n *tree_sitter.Node) {
	if n.Kind() == "import_statement" {
		e.extractImport(n)
		return
	}
	d := e.unwrapExport(n)
	switch d.Kind() {
	case "class_declaration", "abstract_class_declaration", "interface_declaration",
		"enum_declaration", "type_alias_declaration", "function_declaration",
		"generator_function_declaration":
		if nameNode := d.ChildByFieldName("name"); nameNode != nil {
			e.locals[e.text(nameNode)] = true
		}
	case "lexical_declaration", "variable_declaration":
		for i := uint(0); i < d.NamedChildCount(); i++ {
			v := d.NamedChild(i)
			if v.Kind() != "variable_declarator" {
				continue
			}
			if nameNode := v.ChildByFieldName("name"); nameNode != nil && nameNode.Kind() == "identifier" {
				e.locals[e.text(nameNode)] = true
			}
		}
	}
}

// extractImport handles default, named (with aliases) and namespace
// imports.
func (e *extractor) extractImport(n *tree_sitter.Node) {
	sourceNode := n.ChildByFieldName("source")
	if sourceNode == nil {
		return
	}
	module := e.resolveModule(strings.Trim(e.text(sourceNode), "`'\""))

	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c.Kind() != "import_clause" {
			continue
		}
		for j := uint(0); j < c.NamedChildCount(); j++ {
			cl := c.NamedChild(j)
			switch cl.Kind() {
			case "identifier": // default import
				local := e.text(cl)
				e.imports[local] = importTarget{module: module, name: "default"}
				e.fi.Imports = append(e.fi.Imports, model.Import{Alias: local, FQN: module + "#default"})
			case "namespace_import": // import * as api from '...'
				if cl.NamedChildCount() > 0 {
					local := e.text(cl.NamedChild(0))
					e.imports[local] = importTarget{module: module}
					e.fi.Imports = append(e.fi.Imports, model.Import{Alias: local, FQN: module})
				}
			case "named_imports":
				for k := uint(0); k < cl.NamedChildCount(); k++ {
					spec := cl.NamedChild(k)
					if spec.Kind() != "import_specifier" {
						continue
					}
					nameNode := spec.ChildByFieldName("name")
					if nameNode == nil {
						continue
					}
					name := e.text(nameNode)
					local := name
					if aliasNode := spec.ChildByFieldName("alias"); aliasNode != nil {
						local = e.text(aliasNode)
					}
					e.imports[local] = importTarget{module: module, name: name}
					e.fi.Imports = append(e.fi.Imports, model.Import{Alias: local, FQN: module + "#" + name})
				}
			}
		}
	}
}

// resolveModule normalizes an import specifier: relative specifiers
// become root-relative module paths, known workspace package names map
// to their directories, anything else stays as-is ("react").
func (e *extractor) resolveModule(spec string) string {
	if strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../") {
		return modulePath(path.Clean(path.Join(e.fileDir, spec)))
	}
	best, bestDir := "", ""
	for name, dir := range e.pkgDirs {
		if (spec == name || strings.HasPrefix(spec, name+"/")) && len(name) > len(best) {
			best, bestDir = name, dir
		}
	}
	if best != "" {
		if rest := strings.TrimPrefix(strings.TrimPrefix(spec, best), "/"); rest != "" {
			return modulePath(bestDir + "/" + rest)
		}
		return bestDir
	}
	return spec
}

// resolveName resolves an unqualified identifier used as a value/type:
// imports first, then top-level locals; unknown names (globals like
// Promise, console) are not resolvable and yield "".
func (e *extractor) resolveName(name string) (fqn string, exact bool) {
	if t, ok := e.imports[name]; ok {
		target := t.name
		if target == "" || target == "default" {
			target = name
		}
		// Cross-module: the target module may be a barrel (index
		// re-exports) or a bare package, so this is a best guess.
		return t.module + "#" + target, false
	}
	if e.locals[name] {
		return e.qual(name), true
	}
	return "", false
}

func (e *extractor) extractStatement(n *tree_sitter.Node) {
	d := e.unwrapExport(n)
	switch d.Kind() {
	case "class_declaration", "abstract_class_declaration":
		e.extractClass(d)
	case "interface_declaration":
		e.extractInterface(d)
	case "enum_declaration":
		e.extractEnum(d)
	case "type_alias_declaration":
		e.extractTypeAlias(d)
	case "function_declaration", "generator_function_declaration":
		e.extractFunction(d)
	case "lexical_declaration", "variable_declaration":
		e.extractVariables(d)
	case "import_statement":
		// handled in pass one
	default:
		// Top-level executable statements.
		e.walkBody(d, "", nil, nil)
	}
}

func (e *extractor) nameOf(d *tree_sitter.Node) string {
	if nameNode := d.ChildByFieldName("name"); nameNode != nil {
		return e.text(nameNode)
	}
	return "default" // export default class {...}
}

type classCtx struct {
	fqn       string
	propTypes map[string]string // property name -> type FQN
}

func (e *extractor) extractClass(d *tree_sitter.Node) {
	name := e.nameOf(d)
	fqn := e.qual(name)
	body := d.ChildByFieldName("body")
	e.addSymbol(model.Symbol{
		Kind:      model.KindClass,
		Name:      name,
		FQN:       fqn,
		Range:     nodeRange(d),
		Signature: e.signature(d, body),
		Doc:       e.docComment(d),
	})
	e.extractHeritage(d, fqn)
	if body == nil {
		return
	}
	cls := &classCtx{fqn: fqn, propTypes: map[string]string{}}
	e.collectMemberTypes(body, cls)
	for i := uint(0); i < body.NamedChildCount(); i++ {
		e.extractClassMember(body.NamedChild(i), cls)
	}
}

// extractHeritage records extends/implements edges from class_heritage
// (classes) or extends_type_clause (interfaces).
func (e *extractor) extractHeritage(d *tree_sitter.Node, fqn string) {
	line := nodeRange(d).StartLine
	var walk func(n *tree_sitter.Node, kind model.EdgeKind)
	walk = func(n *tree_sitter.Node, kind model.EdgeKind) {
		switch n.Kind() {
		case "extends_clause", "extends_type_clause":
			kind = model.EdgeExtends
		case "implements_clause":
			kind = model.EdgeImplements
		case "identifier", "type_identifier":
			if to, exact := e.resolveName(e.text(n)); to != "" {
				e.addRef(model.Ref{From: fqn, Kind: kind, To: to, Resolved: exact, Line: line})
			}
			return
		case "member_expression", "nested_type_identifier":
			// api.Base — resolve through the namespace import.
			if to, exact := e.resolveQualified(n); to != "" {
				e.addRef(model.Ref{From: fqn, Kind: kind, To: to, Resolved: exact, Line: line})
			}
			return
		case "class_body", "interface_body", "object_type":
			return
		}
		for i := uint(0); i < n.NamedChildCount(); i++ {
			walk(n.NamedChild(i), kind)
		}
	}
	for i := uint(0); i < d.NamedChildCount(); i++ {
		c := d.NamedChild(i)
		if c.Kind() == "class_heritage" || c.Kind() == "extends_type_clause" {
			walk(c, model.EdgeExtends)
		}
	}
}

// collectMemberTypes pre-scans typed class fields (including
// constructor parameter properties) for this.prop.method() resolution.
func (e *extractor) collectMemberTypes(body *tree_sitter.Node, cls *classCtx) {
	for i := uint(0); i < body.NamedChildCount(); i++ {
		m := body.NamedChild(i)
		switch m.Kind() {
		case "public_field_definition", "field_definition":
			nameNode := m.ChildByFieldName("name")
			if nameNode == nil {
				continue
			}
			if t := e.singleTypeFQN(m.ChildByFieldName("type")); t != "" {
				cls.propTypes[e.text(nameNode)] = t
			}
		case "method_definition":
			if nameNode := m.ChildByFieldName("name"); nameNode == nil || e.text(nameNode) != "constructor" {
				continue
			}
			params := m.ChildByFieldName("parameters")
			if params == nil {
				continue
			}
			for j := uint(0); j < params.NamedChildCount(); j++ {
				p := params.NamedChild(j)
				if !hasAccessibilityModifier(p) {
					continue
				}
				pat := p.ChildByFieldName("pattern")
				if pat == nil || pat.Kind() != "identifier" {
					continue
				}
				if t := e.singleTypeFQN(p.ChildByFieldName("type")); t != "" {
					cls.propTypes[e.text(pat)] = t
				}
			}
		}
	}
}

func hasAccessibilityModifier(p *tree_sitter.Node) bool {
	for i := uint(0); i < p.NamedChildCount(); i++ {
		k := p.NamedChild(i).Kind()
		if k == "accessibility_modifier" || k == "override_modifier" || k == "readonly" {
			return true
		}
	}
	return false
}

func (e *extractor) extractClassMember(m *tree_sitter.Node, cls *classCtx) {
	switch m.Kind() {
	case "method_definition", "abstract_method_signature", "method_signature":
		nameNode := m.ChildByFieldName("name")
		if nameNode == nil {
			return
		}
		name := e.text(nameNode)
		fqn := cls.fqn + "." + name + "()"
		e.addSymbol(model.Symbol{
			Kind:      model.KindMethod,
			Name:      name,
			FQN:       fqn,
			Container: cls.fqn,
			Range:     nodeRange(m),
			Signature: e.signature(m, m.ChildByFieldName("body")),
			Doc:       e.docComment(m),
		})
		vars := e.signatureTypes(m, fqn)
		if name == "constructor" {
			e.extractParamProperties(m, cls)
		}
		if body := m.ChildByFieldName("body"); body != nil {
			e.walkBody(body, fqn, cls, vars)
		}
	case "public_field_definition", "field_definition":
		nameNode := m.ChildByFieldName("name")
		if nameNode == nil {
			return
		}
		name := e.text(nameNode)
		value := m.ChildByFieldName("value")
		// Arrow-function fields are methods in practice
		// (handleClick = () => {...}).
		if value != nil && (value.Kind() == "arrow_function" || value.Kind() == "function_expression" || value.Kind() == "function") {
			fqn := cls.fqn + "." + name + "()"
			e.addSymbol(model.Symbol{
				Kind:      model.KindMethod,
				Name:      name,
				FQN:       fqn,
				Container: cls.fqn,
				Range:     nodeRange(m),
				Signature: e.signature(m, value.ChildByFieldName("body")),
				Doc:       e.docComment(m),
			})
			vars := e.signatureTypes(value, fqn)
			if body := value.ChildByFieldName("body"); body != nil {
				e.walkBody(body, fqn, cls, vars)
			}
			return
		}
		e.emitTypeRefs(m.ChildByFieldName("type"), cls.fqn)
		e.addSymbol(model.Symbol{
			Kind:      model.KindProperty,
			Name:      name,
			FQN:       cls.fqn + "." + name,
			Container: cls.fqn,
			Range:     nodeRange(m),
			Signature: e.signature(m, nil),
			Doc:       e.docComment(m),
		})
		if value != nil {
			e.walkBody(value, cls.fqn, cls, nil)
		}
	case "property_signature": // interface member
		nameNode := m.ChildByFieldName("name")
		if nameNode == nil {
			return
		}
		name := e.text(nameNode)
		e.emitTypeRefs(m.ChildByFieldName("type"), cls.fqn)
		e.addSymbol(model.Symbol{
			Kind:      model.KindProperty,
			Name:      name,
			FQN:       cls.fqn + "." + name,
			Container: cls.fqn,
			Range:     nodeRange(m),
			Signature: e.signature(m, nil),
			Doc:       e.docComment(m),
		})
	}
}

// extractParamProperties turns constructor parameter properties
// (`constructor(private readonly api: Client)`) into property symbols.
func (e *extractor) extractParamProperties(ctor *tree_sitter.Node, cls *classCtx) {
	params := ctor.ChildByFieldName("parameters")
	if params == nil {
		return
	}
	for i := uint(0); i < params.NamedChildCount(); i++ {
		p := params.NamedChild(i)
		if !hasAccessibilityModifier(p) {
			continue
		}
		pat := p.ChildByFieldName("pattern")
		if pat == nil || pat.Kind() != "identifier" {
			continue
		}
		name := e.text(pat)
		e.addSymbol(model.Symbol{
			Kind:      model.KindProperty,
			Name:      name,
			FQN:       cls.fqn + "." + name,
			Container: cls.fqn,
			Range:     nodeRange(p),
			Signature: strings.TrimRight(e.signature(p, nil), ","),
		})
	}
}

func (e *extractor) extractInterface(d *tree_sitter.Node) {
	name := e.nameOf(d)
	fqn := e.qual(name)
	body := d.ChildByFieldName("body")
	e.addSymbol(model.Symbol{
		Kind:      model.KindInterface,
		Name:      name,
		FQN:       fqn,
		Range:     nodeRange(d),
		Signature: e.signature(d, body),
		Doc:       e.docComment(d),
	})
	e.extractHeritage(d, fqn)
	if body == nil {
		return
	}
	cls := &classCtx{fqn: fqn, propTypes: map[string]string{}}
	for i := uint(0); i < body.NamedChildCount(); i++ {
		e.extractClassMember(body.NamedChild(i), cls)
	}
}

func (e *extractor) extractEnum(d *tree_sitter.Node) {
	name := e.nameOf(d)
	fqn := e.qual(name)
	e.addSymbol(model.Symbol{
		Kind:      model.KindEnum,
		Name:      name,
		FQN:       fqn,
		Range:     nodeRange(d),
		Signature: "enum " + name,
		Doc:       e.docComment(d),
	})
	body := d.ChildByFieldName("body")
	if body == nil {
		return
	}
	for i := uint(0); i < body.NamedChildCount(); i++ {
		m := body.NamedChild(i)
		var nameNode *tree_sitter.Node
		switch m.Kind() {
		case "enum_assignment":
			nameNode = m.ChildByFieldName("name")
		case "property_identifier":
			nameNode = m
		}
		if nameNode == nil {
			continue
		}
		caseName := e.text(nameNode)
		e.addSymbol(model.Symbol{
			Kind:      model.KindEnumCase,
			Name:      caseName,
			FQN:       fqn + "." + caseName,
			Container: fqn,
			Range:     nodeRange(m),
			Signature: e.signature(m, nil),
		})
	}
}

func (e *extractor) extractTypeAlias(d *tree_sitter.Node) {
	name := e.nameOf(d)
	fqn := e.qual(name)
	e.addSymbol(model.Symbol{
		Kind:      model.KindTypeAlias,
		Name:      name,
		FQN:       fqn,
		Range:     nodeRange(d),
		Signature: e.signature(d, nil),
		Doc:       e.docComment(d),
	})
	e.emitTypeRefs(d.ChildByFieldName("value"), fqn)
}

func (e *extractor) extractFunction(d *tree_sitter.Node) {
	name := e.nameOf(d)
	fqn := e.qual(name) + "()"
	e.addSymbol(model.Symbol{
		Kind:      model.KindFunction,
		Name:      name,
		FQN:       fqn,
		Range:     nodeRange(d),
		Signature: e.signature(d, d.ChildByFieldName("body")),
		Doc:       e.docComment(d),
	})
	vars := e.signatureTypes(d, fqn)
	if body := d.ChildByFieldName("body"); body != nil {
		e.walkBody(body, fqn, nil, vars)
	}
}

// extractVariables handles top-level const/let/var. A declarator whose
// value is a function/arrow becomes a function symbol (the dominant
// style for React components and hooks).
func (e *extractor) extractVariables(d *tree_sitter.Node) {
	isConst := strings.HasPrefix(e.text(d), "const")
	doc := e.docComment(d)
	for i := uint(0); i < d.NamedChildCount(); i++ {
		v := d.NamedChild(i)
		if v.Kind() != "variable_declarator" {
			continue
		}
		nameNode := v.ChildByFieldName("name")
		if nameNode == nil || nameNode.Kind() != "identifier" {
			continue
		}
		name := e.text(nameNode)
		value := v.ChildByFieldName("value")

		if value != nil && (value.Kind() == "arrow_function" || value.Kind() == "function_expression" || value.Kind() == "function") {
			fqn := e.qual(name) + "()"
			e.addSymbol(model.Symbol{
				Kind:      model.KindFunction,
				Name:      name,
				FQN:       fqn,
				Range:     nodeRange(v),
				Signature: e.signature(v, value.ChildByFieldName("body")),
				Doc:       doc,
			})
			vars := e.signatureTypes(value, fqn)
			if body := value.ChildByFieldName("body"); body != nil {
				e.walkBody(body, fqn, nil, vars)
			}
			continue
		}

		kind := model.KindProperty
		if isConst {
			kind = model.KindConstant
		}
		fqn := e.qual(name)
		e.emitTypeRefs(v.ChildByFieldName("type"), fqn)
		e.addSymbol(model.Symbol{
			Kind:      kind,
			Name:      name,
			FQN:       fqn,
			Range:     nodeRange(v),
			Signature: e.signature(v, nil),
			Doc:       doc,
		})
		if value != nil {
			e.walkBody(value, fqn, nil, nil)
		}
	}
}

// signatureTypes emits references_type edges for parameter/return
// types and returns typed parameter names for receiver resolution.
func (e *extractor) signatureTypes(fn *tree_sitter.Node, from string) map[string]string {
	vars := map[string]string{}
	if params := fn.ChildByFieldName("parameters"); params != nil {
		for i := uint(0); i < params.NamedChildCount(); i++ {
			p := params.NamedChild(i)
			typeNode := p.ChildByFieldName("type")
			e.emitTypeRefs(typeNode, from)
			if t := e.singleTypeFQN(typeNode); t != "" {
				if pat := p.ChildByFieldName("pattern"); pat != nil && pat.Kind() == "identifier" {
					vars[e.text(pat)] = t
				}
			}
		}
	}
	e.emitTypeRefs(fn.ChildByFieldName("return_type"), from)
	return vars
}

// singleTypeFQN resolves a type annotation to a single named type.
func (e *extractor) singleTypeFQN(typeNode *tree_sitter.Node) string {
	n := typeNode
	for n != nil {
		switch n.Kind() {
		case "type_annotation":
			n = n.NamedChild(0)
		case "generic_type":
			n = n.ChildByFieldName("name")
		case "type_identifier":
			fqn, _ := e.resolveName(e.text(n))
			return fqn
		default:
			return ""
		}
	}
	return ""
}

// emitTypeRefs records references_type edges for every named type in
// a type expression.
func (e *extractor) emitTypeRefs(typeNode *tree_sitter.Node, from string) {
	if typeNode == nil || e.skipRefs {
		return
	}
	var walk func(n *tree_sitter.Node)
	walk = func(n *tree_sitter.Node) {
		if n.Kind() == "type_identifier" {
			if fqn, exact := e.resolveName(e.text(n)); fqn != "" {
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

// resolveQualified resolves ns.Name where ns is a namespace import.
func (e *extractor) resolveQualified(n *tree_sitter.Node) (fqn string, exact bool) {
	var objNode, propNode *tree_sitter.Node
	switch n.Kind() {
	case "member_expression":
		objNode, propNode = n.ChildByFieldName("object"), n.ChildByFieldName("property")
	case "nested_type_identifier":
		objNode, propNode = n.ChildByFieldName("module"), n.ChildByFieldName("name")
	default:
		return "", false
	}
	if objNode == nil || propNode == nil || objNode.Kind() != "identifier" {
		return "", false
	}
	if t, ok := e.imports[e.text(objNode)]; ok && t.name == "" {
		return t.module + "#" + e.text(propNode), false
	}
	return "", false
}

// walkBody collects references from executable code.
func (e *extractor) walkBody(n *tree_sitter.Node, from string, cls *classCtx, vars map[string]string) {
	if e.skipRefs {
		return
	}
	line := func(node *tree_sitter.Node) int { return nodeRange(node).StartLine }

	switch n.Kind() {
	case "call_expression":
		if fn := n.ChildByFieldName("function"); fn != nil {
			e.extractCall(fn, from, cls, vars, line(n))
		}

	case "new_expression":
		if ctor := n.ChildByFieldName("constructor"); ctor != nil {
			var to string
			var exact bool
			switch ctor.Kind() {
			case "identifier":
				to, exact = e.resolveName(e.text(ctor))
			case "member_expression":
				to, exact = e.resolveQualified(ctor)
			}
			if to != "" {
				e.addRef(model.Ref{From: from, Kind: model.EdgeInstantiates, To: to, Resolved: exact, Line: line(n)})
			}
		}

	case "jsx_opening_element", "jsx_self_closing_element":
		// Rendering a component is a call of the component function,
		// so the edge is calls with a "()" target — it joins with
		// function-component FQNs ("mod#Button()").
		if nameNode := n.ChildByFieldName("name"); nameNode != nil {
			var to string
			switch nameNode.Kind() {
			case "identifier", "jsx_identifier":
				name := e.text(nameNode)
				if name != "" && name[0] >= 'A' && name[0] <= 'Z' { // components only, not <div>
					to, _ = e.resolveName(name)
				}
			case "member_expression":
				to, _ = e.resolveQualified(nameNode)
			}
			if to != "" {
				e.addRef(model.Ref{From: from, Kind: model.EdgeCalls, To: to + "()", Resolved: false, Line: line(n)})
			}
		}

	case "type_annotation", "as_expression", "satisfies_expression":
		e.emitTypeRefs(n, from)

	case "arrow_function", "function_expression":
		// Nested closures keep the enclosing symbol as `from`; typed
		// closure params add to the receiver map.
		inner := vars
		if extra := e.signatureTypes(n, from); len(extra) > 0 {
			inner = map[string]string{}
			for k, v := range vars {
				inner[k] = v
			}
			for k, v := range extra {
				inner[k] = v
			}
		}
		if body := n.ChildByFieldName("body"); body != nil {
			e.walkBody(body, from, cls, inner)
		}
		return
	}

	for i := uint(0); i < n.NamedChildCount(); i++ {
		e.walkBody(n.NamedChild(i), from, cls, vars)
	}
}

// extractCall records a calls edge for f(), ns.f(), this.m(),
// param.m() and this.prop.m().
func (e *extractor) extractCall(fn *tree_sitter.Node, from string, cls *classCtx, vars map[string]string, line int) {
	switch fn.Kind() {
	case "identifier":
		if to, exact := e.resolveName(e.text(fn)); to != "" {
			e.addRef(model.Ref{From: from, Kind: model.EdgeCalls, To: to + "()", Resolved: exact, Line: line})
		}
	case "member_expression":
		obj := fn.ChildByFieldName("object")
		prop := fn.ChildByFieldName("property")
		if obj == nil || prop == nil {
			return
		}
		method := e.text(prop)
		switch obj.Kind() {
		case "identifier":
			id := e.text(obj)
			if t, ok := e.imports[id]; ok && t.name == "" { // namespace import
				e.addRef(model.Ref{From: from, Kind: model.EdgeCalls, To: t.module + "#" + method + "()", Resolved: false, Line: line})
				return
			}
			if t, ok := vars[id]; ok { // typed parameter
				e.addRef(model.Ref{From: from, Kind: model.EdgeCalls, To: t + "." + method + "()", Resolved: false, Line: line})
			}
		case "this":
			if cls != nil {
				e.addRef(model.Ref{From: from, Kind: model.EdgeCalls, To: cls.fqn + "." + method + "()", Resolved: false, Line: line})
			}
		case "member_expression": // this.prop.m()
			innerObj := obj.ChildByFieldName("object")
			innerProp := obj.ChildByFieldName("property")
			if cls != nil && innerObj != nil && innerProp != nil && innerObj.Kind() == "this" {
				if t, ok := cls.propTypes[e.text(innerProp)]; ok {
					e.addRef(model.Ref{From: from, Kind: model.EdgeCalls, To: t + "." + method + "()", Resolved: false, Line: line})
				}
			}
		}
	}
}

// signature returns the declaration text up to (not including) its
// body, whitespace-collapsed and capped.
func (e *extractor) signature(n *tree_sitter.Node, body *tree_sitter.Node) string {
	start, end := n.ByteRange()
	if body != nil {
		end, _ = body.ByteRange()
	}
	if end > uint(len(e.src)) || start >= end {
		return ""
	}
	sig := strings.Join(strings.Fields(string(e.src[start:end])), " ")
	sig = strings.TrimSuffix(strings.TrimSpace(sig), "=>")
	sig = strings.TrimSuffix(strings.TrimSpace(sig), "=")
	sig = strings.TrimSpace(sig)
	if len(sig) > maxSignatureLen {
		sig = sig[:maxSignatureLen] + "…"
	}
	return sig
}

// docComment returns the JSDoc block or contiguous // run directly
// above the declaration (looking through the export wrapper).
func (e *extractor) docComment(n *tree_sitter.Node) string {
	if p := n.Parent(); p != nil && p.Kind() == "export_statement" {
		n = p
	}
	var parts []string
	expect := int(n.StartPosition().Row)
	for prev := n.PrevNamedSibling(); prev != nil && prev.Kind() == "comment"; prev = prev.PrevNamedSibling() {
		if int(prev.EndPosition().Row) < expect-1 {
			break
		}
		expect = int(prev.StartPosition().Row)
		parts = append([]string{e.text(prev)}, parts...)
	}
	return strings.Join(parts, "\n")
}
