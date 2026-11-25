// Why: a borrow error is cheaper to fix when it carries the edit that fixes
// it; mk fix and the editor apply these edits mechanically.
//
// What: addVarFix (E0404 and E0508 "add var", never for a receiver),
// bindLocalFix (E0506 "bind a local"), and releaseFacts and consumers
// (E0402's `release` and `target` facts).
//
// How: each fix is a zero-width or small edit computed from spans the body
// already holds (noteBinding records binding sites); the consumer scan
// records its reads so a cached result follows the type's methods.

package borrow

import (
	"slices"
	"strconv"
	"strings"

	"mk/internal/diag"
	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

// noteBinding records where a user local is first bound (the StAssign or
// call that takes it from {U} to {I}), for the "add var" fix.
func (c *checker) noteBinding(r rootID, sp mir.SpanID) {
	if c.roots[r].kind != rkUser {
		return
	}
	if _, ok := c.bindStart[r]; !ok {
		c.bindStart[r] = c.body.Span(sp).Start
	}
}

// addVarFix attaches E0404's and E0508's "add var" fix: one zero-width edit
// inserting `var ` at the declaration. Only for a parameter that is not the
// receiver, and a user local whose binding statement starts at its
// declaration (not a pattern binding). A by-value self gets the alternative
// instead, since `var self` does not parse.
func (c *checker) addVarFix(d *diag.Diagnostic, r rootID) {
	ri := &c.roots[r]
	decl := c.body.Span(c.body.Locals[ri.local].Span)
	if ri.kind == rkParam && c.env.Sig != nil && c.env.Sig.Receiver != types.NoReceiver && ri.param == 1 {
		if c.env.Sig.Receiver == types.ByValue {
			d.Alt("move it into a local first: `var s := self`")
		}
		return
	}
	if ri.kind == rkUser {
		if bs, ok := c.bindStart[r]; !ok || bs != decl.Start {
			return
		}
	}
	if ri.kind != rkUser && ri.kind != rkParam {
		return
	}
	at := decl.Start
	if ri.kind == rkUser {
		var ok bool
		if at, ok = c.destructureStart(decl); !ok {
			return
		}
	}
	d.Fix(diag.Fix{Class: diag.Proven, Edits: []diag.Edit{{Span: source.Span{File: decl.File, Start: at, End: at},
		Text: "var "}}, Why: "declare it var so it may be assigned again or borrowed mut"})
}

// destructureStart is where `var ` goes for a user local declared at decl:
// decl itself for `x := e`, or the start of the statement for a name bound
// by a destructuring binding, `Wrap(n, ..) := w`, `(a, b) := r` or
// `[a, b] := xs`, which `var` marks as a whole (an edit inside the
// pattern, `Wrap(var n, ..)`, does not parse). ok is false for any other
// binding (a match arm's pattern), which gets no fix.
func (c *checker) destructureStart(decl source.Span) (uint32, bool) {
	if c.env.File == nil {
		return decl.Start, true
	}
	f := c.env.File(decl.File)
	if f == nil || decl.Start > f.Len() {
		return decl.Start, true
	}
	line := int(f.LineCol(decl.Start).Line)
	start := f.LineStart(line)
	text := f.Line(line)
	indent := len(text) - len(strings.TrimLeft(text, " \t"))
	at := start + uint32(indent)
	if at >= decl.Start {
		return decl.Start, true
	}
	lead := text[indent : decl.Start-start]
	rest := text[decl.Start-start:]
	if strings.HasPrefix(lead, "var ") || !strings.Contains(rest, ":=") {
		return 0, false
	}
	if lead[0] != '(' && lead[0] != '[' && (lead[0] < 'A' || lead[0] > 'Z') {
		return 0, false
	}
	return at, true
}

// bindLocalFix attaches E0506's "bind a local" fix: insert `<indent><name>
// := <text>` on a line before the statement that holds the temporary, and
// replace the temporary's expression by the name. Omitted without
// Env.File, and for a temporary of a loop or a switch scrutinee, where no
// line before it is inside the construct.
func (c *checker) bindLocalFix(d *diag.Diagnostic, site mir.SpanID, t rootID) {
	if c.env.File == nil || c.roots[t].kind != rkTemp || c.scrutinee(c.roots[t].local) {
		return
	}
	lc := &c.body.Locals[c.roots[t].local]
	if lc.Loop != mir.NoLoop {
		return
	}
	ts := c.body.Span(lc.Span)
	f := c.env.File(ts.File)
	if f == nil || ts.End > f.Len() {
		return
	}
	stmt := c.body.Span(site)
	line := int(f.LineCol(stmt.Start).Line)
	start := f.LineStart(line)
	text := f.Line(line)
	indent := text[:len(text)-len(strings.TrimLeft(text, " \t"))]
	name := c.freshName()
	d.Fix(diag.Fix{Class: diag.Proven, Edits: []diag.Edit{
		{Span: source.Span{File: ts.File, Start: start, End: start}, Text: indent + name + " := " + f.Slice(ts) + "\n"},
		{Span: ts, Text: name},
	}, Why: "a named local lives to the end of its block"})
}

// scrutinee reports whether local l is read by a switch: the temporary of
// an if or match condition.
func (c *checker) scrutinee(l mir.LocalID) bool {
	for i := range c.body.Blocks {
		t := &c.body.Blocks[i].Term
		if t.Kind == mir.TermSwitch && (t.On.Kind == mir.OpCopy || t.On.Kind == mir.OpMove) && t.On.Place.Local == l {
			return true
		}
	}
	return false
}

// freshName is tmp, else tmp1, tmp2, ...: the first not used by a user local.
func (c *checker) freshName() string {
	used := map[string]bool{}
	for _, l := range c.body.Locals {
		if l.Kind == mir.LocalUser {
			used[c.symbol(l.Name)] = true
		}
	}
	if !used["tmp"] {
		return "tmp"
	}
	for i := 1; ; i++ {
		if n := "tmp" + strconv.Itoa(i); !used[n] {
			return n
		}
	}
}

// releaseFacts adds E0402's `release` (the type's consuming methods, by
// name) and, for a named local or parameter whose type has exactly one
// consuming method taking only self and returning void, `target`: the line
// `defer <x>.<Release>()`.
func (c *checker) releaseFacts(d *diag.Diagnostic, r rootID, ty types.TypeID) {
	ms := c.consumers(ty)
	if len(ms) == 0 {
		return
	}
	names := make([]string, len(ms))
	for i, m := range ms {
		names[i] = m.name
	}
	d.Fact("release", diag.TextList(names...))
	if r == noRoot || len(ms) != 1 || !ms[0].bare {
		return
	}
	if k := c.roots[r].kind; k == rkUser || k == rkParam {
		d.Text("target", "defer "+c.rootName(r)+"."+ms[0].name+"()")
	}
}

type consumer struct {
	name string
	bare bool // only self, void result, cannot fail
}

// consumers lists the methods of ty's item that take self by value, sorted by
// name. E0402's `release` and `target` facts are built from this list, so the
// scan is recorded in Result.Reads: ReadExports of the type's member namespace
// (a method added, removed or renamed) and ReadSignature of every method of
// the type it looked at, by-value or not (a receiver, parameter or result
// change; the namespace hash does not see receivers).
func (c *checker) consumers(ty types.TypeID) []consumer {
	if c.tab == nil || ty == types.NoType {
		return nil
	}
	t := c.tab.Get(ty)
	if t.Kind != types.KNamed {
		return nil
	}
	if ms, ok := c.release[t.Item]; ok {
		return ms
	}
	items := c.tab.Items()
	// The key of a type's member namespace is the type's item key and ':'
	// (iface.TypeNamespaceKey, check's nsKey), which borrow cannot import;
	// a bare item key would name a module.
	c.res.Reads.Add(types.Read{Kind: types.ReadExports, Key: types.KeyFromString(items.Get(t.Item).Key.String() + ":")})
	var out []consumer
	// A type's methods live in its own module, whose item list is
	// final once that module is through resolve. Scanning the whole table
	// would read items another module's resolve is still writing (a data
	// race).
	var ids []types.ItemID
	if m := items.Module(items.Get(t.Item).Module); m != nil {
		ids = m.Items
	}
	for _, id := range ids {
		it := items.Get(id)
		if it.Owner != t.Item || it.Kind != types.KindFunc {
			continue
		}
		c.readSig(id)
		fd, ok := it.Decl.(*types.FuncDecl)
		if !ok || fd.Sig.Receiver != types.ByValue {
			continue
		}
		out = append(out, consumer{name: c.symbol(it.Name),
			bare: len(fd.Sig.Params) == 1 && fd.Sig.Result == types.Void && fd.Sig.Errors == types.EmptyErrors})
	}
	slices.SortFunc(out, func(a, b consumer) int { return strings.Compare(a.name, b.name) })
	c.release[t.Item] = out
	return out
}
