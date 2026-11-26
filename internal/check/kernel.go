// Why: kernels and firmware place data in named sections, align structs to
// pages and read symbols a linker script defines; each of these is low-level
// and allowed only where a package says so.
//
// What: structAlign (align(n) on a struct), sectionClause (the section
// clause on a const, a var and a function), unsafeModuleOnly, and the extern
// var rules: externVar, externVarDecl, externVarRead, externVarPlace and
// externVarWrite.
//
// How: align(n) is a power of two up to 65,536, never with packed, else
// E0706; a malformed section name is E0706. A section clause or an extern
// var outside pkg.mk's unsafe_modules is E1102. An extern var must be plain
// (E0701); reading it or taking its address outside unsafe is E1101, and a
// write is E0404.

package check

import (
	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/types"
)

// maxAlign is align(n)'s largest n.
const maxAlign = 65536

// structAlign is a struct's align(n): n a power of two from 1 to
// 65,536, and never with packed, else E0706 at the clause and 0.
func (m *Module) structAlign(x *tctx, n *ast.StructDecl) uint32 {
	if n.Align == nil || n.Align.Align == nil {
		return 0
	}
	at := n.Align.Span()
	if n.Packed != nil {
		x.errorf(diag.E0706, at, "`align` and `packed` cannot both be written: packed caps the alignment that align raises").
			Text("align", n.Align.Align.Raw).Text("reason", "packed")
		return 0
	}
	v, ok := parseIntLit(n.Align.Align.Raw)
	if !ok || v == 0 || v > maxAlign || v&(v-1) != 0 {
		x.errorf(diag.E0706, at, "align(n) takes a power of two from 1 to 65536").
			Text("align", n.Align.Align.Raw).Text("reason", "power-of-two")
		return 0
	}
	return uint32(v)
}

// sectionClause is a section clause's name: 1 to 64 printable
// ASCII bytes with no space or `"`, else E0706; in a module that pkg.mk's
// unsafe_modules does not list, E1102. "" when absent or wrong.
func (m *Module) sectionClause(x *tctx, lit *ast.BasicLit) string {
	if lit == nil {
		return ""
	}
	raw, ok := litString(lit.Kind, lit.Raw)
	if !ok {
		return ""
	}
	name := string(raw)
	good := len(name) >= 1 && len(name) <= 64
	for i := 0; good && i < len(name); i++ {
		if c := name[i]; c <= ' ' || c > '~' || c == '"' {
			good = false
		}
	}
	if !good {
		x.errorf(diag.E0706, lit.Span(), "a section name is 1 to 64 printable ASCII bytes with no space or quote").
			Text("section", name).Text("reason", "section")
		return ""
	}
	m.unsafeModuleOnly(x, lit.Span(), "a section clause")
	return name
}

// unsafeModuleOnly is E1102 for a section clause or an extern var in a
// module that pkg.mk's unsafe_modules does not list. A design block
// is judged as such a module, which the corpus runner states
// through Module.UnsafePermitted, so check needs no case of its own.
func (m *Module) unsafeModuleOnly(x *tctx, at source.Span, what string) {
	if m.mod == nil || m.mod.UnsafePermitted {
		return
	}
	x.errorf(diag.E1102, at, what+" in module "+m.mod.Path+", which pkg.mk's unsafe_modules does not list").
		Text("module", m.mod.Path).Text("manifest", "unsafe_modules")
}

// externVar declares an extern var: E1102 outside unsafe_modules,
// and its type must be plain, since it is memory a linker script or a .s
// file defines (E0701 with bound Plain), queued to step
// 4 like a const's plain rule.
func (m *Module) externVar(it *types.Item, x *tctx, n *ast.GlobalDecl, d *types.GlobalDecl) {
	m.unsafeModuleOnly(x, n.Name.Span(), "an extern var")
	m.later(it, func() {
		c := m.c
		if d.Type == types.Poison || !c.closed(d.Type) {
			return
		}
		if f := c.Facts(d.Type); f.Kind == types.Plain && !f.Carrier { // no loan rides in linker memory
			return
		}
		at := it.Span
		if n.Type != nil {
			at = n.Type.Span()
		}
		m.Diags = append(m.Diags, diag.New(diag.E0701, at, "an extern var holds plain data: the linker or a .s file defines its bytes").
			Text("bound", "Plain").Text("type", c.cfg.Types.TypeString(d.Type, nil)))
	})
}

// externVarDecl is the GlobalDecl of an extern var, nil for anything else.
func (u *unit) externVarDecl(it types.ItemID) *types.GlobalDecl {
	if it == types.NoItem {
		return nil
	}
	if d, ok := u.c.declOfItem(u.c.cfg.Items.Get(it)).(*types.GlobalDecl); ok && d.Extern {
		return d
	}
	return nil
}

// externVarRead is E1101 for a read of an extern var, or its address,
// outside unsafe: in unsafe, &KernelEnd is its address and KernelEnd reads
// it.
func (u *unit) externVarRead(e ast.Node, it types.ItemID) {
	if u.unsafeDepth > 0 {
		return
	}
	name := u.c.cfg.Interner.Str(u.c.cfg.Items.Get(it).Name)
	u.err(diag.E1101, e.Span(), "reading extern var `"+name+"` or taking its address needs an `unsafe \"reason\" { }` block").
		Text("operation", "extern-var")
}

// externVarPlace reports whether e (parentheses removed) names an extern
// var, and which: by its name, or as another module's member (`m.Var`, so
// `&m.Var` is a pointer and Mk code can write a C-bound extern var of
// another module).
func (u *unit) externVarPlace(e ast.Expr) (types.ItemID, bool) {
	var node ast.Node
	switch x := unparenExpr(e).(type) {
	case *ast.Ident:
		node = x
	case *ast.FieldExpr:
		node = x
	default:
		return types.NoItem, false
	}
	use := u.useOf(node.ID())
	if use.Kind != UseGlobal || u.externVarDecl(use.Item) == nil {
		return types.NoItem, false
	}
	return use.Item, true
}

// externVarWrite is E0404 for `name = x` or `mut name` on an extern var
// the language grants the read and the address only,
// so a write goes through the pointer, `*(&name) = x`, in unsafe.
func (u *unit) externVarWrite(e ast.Expr, how string) {
	it, ok := u.externVarPlace(e)
	if !ok {
		return
	}
	in := u.c.cfg.Interner
	name := in.Str(u.c.cfg.Items.Get(it).Name)
	if field, ok := unparenExpr(e).(*ast.FieldExpr); ok {
		if module, ok := unparenExpr(field.X).(*ast.Ident); ok {
			name = in.Str(module.Name) + "." + name
		}
	}
	u.err(diag.E0404, e.Span(), "extern var `"+name+"` is no Mk var: "+how+" it through its address, `*(&"+name+")`, in unsafe").
		Text("reason", "extern-var")
}
