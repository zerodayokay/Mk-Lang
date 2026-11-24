// Why: a `Ctx` struct passed by reference carries capabilities, and state
// reached through one of its fields is a node of the parameter like any
// other; a store into that state is E0604 `*c into c`, a clause that then
// ties every later use of the state to the `Ctx`. The rewrite the codebase
// guide gives is to pass the state beside the `Ctx`, and E0604 has to say so.
//
// What: ctxStateAlt, ctxStateField and ctxStateClause.
//
// How: when the store lands in the deep node of a parameter, not the
// receiver, of type `&S` or `mut S`, where S is a struct holding at least
// one capability (an allocator reference, `Io`, or a shared reference to
// memory that takes no store) beside exactly one reference to state, that
// field is the state: the E0604 names it, says to pass it as its own
// parameter, and gives the clause that then declares the store, which
// depends on where the stored loan comes from.

package borrow

import (
	"strings"

	"mk/internal/diag"
	"mk/internal/types"
)

// ctxStateAlt completes E0604 d for a store of loan l into root t, an
// abstract node of parameter p, when the store reaches state through the one
// state field of a `Ctx`-shaped p (ctxStateField): the fact `field` names
// it, and an alternative says to pass it beside p with the clause that then
// checks (ctxStateClause).
func (c *checker) ctxStateAlt(d *diag.Diagnostic, t rootID, l loanID, p types.ParamIndex) {
	s := c.env.Sig
	if s == nil || p == types.NoParam || int(p) > len(s.Params) || int(p) >= len(c.params) || (c.params[p].n2 != t && !c.isParamN2Field(p, t)) {
		return
	}
	if int(p) == 1 && s.Receiver != types.NoReceiver {
		return // a method's receiver is the value it works on, no Ctx
	}
	idx, name, ft, ok := c.ctxStateField(s.Params[p-1].Type)
	if !ok {
		return
	}
	pn := sigParam(c, s, p)
	clause, ok := c.ctxStateClause(d, l, p, idx, name, ft, s)
	if !ok {
		return
	}
	d.Text("field", pn+"."+name)
	d.Alt("the store lands in what `" + pn + "." + name + "` refers to, state inside a `Ctx`: pass it beside the `Ctx` as `" +
		name + ": " + c.tab.TypeString(ft, s.Generics) + "` (doc/codebase.md section 3)" + clause)
}

// ctxStateClause is the end of ctxStateAlt's text: the clause the rewrite
// needs for the stored loan l. A loan read through the state field (the
// field's value, or what it refers to) becomes the new parameter's, `st: T
// into st`; any other loan keeps its source, the written clause with the
// field in place of p.
func (c *checker) ctxStateClause(d *diag.Diagnostic, l loanID, p types.ParamIndex, idx uint32, name string, ft types.TypeID, s *types.Signature) (string, bool) {
	lo := &c.loans[l]
	if lo.param == p && lo.tag != nil && lo.tag[0] == idx {
		return " and write `" + name + ": " + c.tab.TypeString(ft, s.Generics) + " into " + name + "`", true
	}
	clause := storeFactText(d, "clause")
	suffix := " into " + sigParam(c, s, p)
	if clause == "" || !strings.HasSuffix(clause, suffix) {
		return "", false
	}
	return " and write `" + strings.TrimSuffix(clause, suffix) + " into " + name + "`", true
}

// ctxStateField is the state field of the struct behind a parameter of type
// pt (`&S` or `mut S`): S holds at least one capability field (an allocator
// reference, `Io`, or a shared reference to memory that takes no store) and
// exactly one reference, or optional reference, to state (a `mut`
// reference, or a shared one to memory that takes stores). ok is false for
// any other shape. It returns the field's index, name and type.
func (c *checker) ctxStateField(pt types.TypeID) (idx uint32, name string, ft types.TypeID, ok bool) {
	ty := c.tab.Get(pt)
	if ty.Kind != types.KRef && ty.Kind != types.KMut {
		return 0, "", types.NoType, false
	}
	st := c.tab.Get(ty.Elem)
	if st.Kind != types.KNamed || st.Item == types.NoItem || c.tab.Items() == nil {
		return 0, "", types.NoType, false
	}
	decl, isStruct := c.tab.Items().Get(st.Item).Decl.(*types.StructDecl)
	fts := c.w.inlineFields(ty.Elem)
	if !isStruct || len(fts) != len(decl.Fields) {
		return 0, "", types.NoType, false
	}
	found, caps := -1, 0
	for i, f := range fts {
		if c.tab.TypeString(f, nil) == "Io" {
			caps++
			continue
		}
		r := c.tab.Get(f)
		if r.Kind == types.KOptional {
			r = c.tab.Get(r.Elem)
		}
		switch {
		case r.Kind != types.KRef && r.Kind != types.KMut:
		case c.w.factsOf(r.Elem).Allocator:
			caps++
		case r.Kind == types.KRef && !c.w.writableByType(r.Elem) && !c.w.takesStores(r.Elem):
			caps++ // a configuration read by everyone
		case found >= 0:
			return 0, "", types.NoType, false
		default:
			found = i
		}
	}
	if found < 0 || caps == 0 {
		return 0, "", types.NoType, false
	}
	return uint32(found), c.symbol(decl.Fields[found].Name), fts[found], true
}
