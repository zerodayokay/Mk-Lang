// Why: the code every gen line of a module emits must become one parsed
// source file that later stages treat like handwritten code, while errors in
// it still point the user at the gen line that made them.
//
// What: merge (every run's Emits in Index order, printed into one file),
// virtualPath and addFile (the file's path "<module dir>/gen/<target>/gen.mk"),
// parse (E0905 for parse errors and for a gen or import in the output),
// OriginOf, Wrap (re-anchoring diagnostics) and Finish (E0906).
//
// How: the printer records an ItemOrigin per item; Wrap finds the item that
// holds a diagnostic's offset and turns the diagnostic into an E0905 at its
// gen line, except an E0704 on a generated impl, which moves to the gen line
// as itself.

package gen

import (
	"path"
	"strings"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/syntax"
	"mk/internal/types"
)

// Text renders the generated file of a module exactly as File holds it; the
// gen/ view writer, when built, writes this text. Empty when no line
// produced output.
func (o *Output) Text() string { return o.text }

// firstPath is "<module dir>/gen/<target>/gen.mk".
func (c *Context) firstPath(module types.ModuleID) string {
	mod := c.Items.Module(module)
	dir := strings.ReplaceAll(mod.Path, ".", "/")
	if len(mod.Files) > 0 && c.Sources != nil {
		if f := c.Sources.File(mod.Files[0]); f != nil {
			dir = path.Dir(f.Path)
		}
	}
	return path.Join(dir, "gen", c.Target.Name, "gen.mk")
}

// virtualPath is firstPath, or a distinct one when a file is there.
func (c *Context) virtualPath(module types.ModuleID) string {
	mod := c.Items.Module(module)
	dir := strings.ReplaceAll(mod.Path, ".", "/")
	if len(mod.Files) > 0 && c.Sources != nil {
		if f := c.Sources.File(mod.Files[0]); f != nil {
			dir = path.Dir(f.Path)
		}
	}
	p := c.firstPath(module)
	// A second Run of one module in one session (tests; the driver runs
	// stage 4 once per module) gets a distinct path, as SourceMap needs.
	for n := 2; c.Sources != nil; n++ {
		if _, dup := c.Sources.ByPath(p); !dup {
			break
		}
		p = path.Join(dir, "gen", c.Target.Name, "gen~"+itoa(n)+".mk")
	}
	return p
}

// addFile adds the generated text at the module's virtual path; a file
// already there with the same text (the driver's module cache rebuilt it
// for a hit whose interface then failed to decode) is reused, so the
// recomputed module keeps the path its stored entries name.
func (c *Context) addFile(module types.ModuleID, text string) *source.File {
	if c.Sources != nil {
		if f, ok := c.Sources.ByPath(c.firstPath(module)); ok && f.Text == text {
			return f
		}
	}
	return c.Sources.Add(c.virtualPath(module), []byte(text))
}

// merge collects every run's Emits, lines in Index order and each
// run's Emits in ordinal order, printed into one file, parsed.
func (c *Context) merge(out *Output, lines []Line, slots []slot) {
	var items []*printItem
	for i := range lines {
		l, s := &lines[i], &slots[i]
		trace := "gen " + l.Path
		forText := ""
		// A line with emits was bound in full, so its first type or
		// interface argument holds Type handle 1 (bind issues handles 1..
		// in argument order).
		if s.run != nil && len(s.emits) > 0 && s.run.argTypes > 0 {
			forText = " for " + s.run.text(s.run.types[1])
		}
		for k, e := range s.emits {
			for _, it := range splitItems(e.toks) {
				items = append(items, &printItem{toks: it, trace: trace + "#" + itoa(k+1) + forText,
					origin: ItemOrigin{Emitter: e.span, Gen: l.Span, Line: l.Index, Ordinal: k + 1}})
			}
		}
	}
	if len(items) == 0 {
		return
	}
	var p printer
	p.header(c.Items.Module(out.Module).Path, c.Target.Name)
	for _, it := range items {
		p.item(it)
	}
	out.text = p.b.String()
	out.File = c.addFile(out.Module, out.text)
	for i := range p.items {
		p.items[i].Output.File = out.File.ID
	}
	out.Origins, out.Items = p.origins, p.items
	c.parse(out)
}

// parse parses the output: parse diagnostics and a gen or import item become
// E0905 through Wrap, and the bad items leave Output.AST.
func (c *Context) parse(out *Output) {
	f, ds := syntax.ParseFile(out.File, c.Interner, syntax.Options{})
	var bad []source.Span
	for _, d := range ds {
		out.Diags = append(out.Diags, Wrap(d, out))
		bad = append(bad, d.At)
	}
	kept := f.Items[:0:0]
	for _, it := range f.Items {
		sp := it.Span()
		drop := false
		switch it.(type) {
		case *ast.BadItem:
			drop = true
		case *ast.GenItem, *ast.Import:
			inner := diag.New(diag.E0101, sp, "a generator's output holds no `gen` and no `import`; its names are pinned")
			out.Diags = append(out.Diags, Wrap(inner, out))
			drop = true
		}
		for _, b := range bad {
			if b.Start >= sp.Start && b.Start <= sp.End {
				drop = true
			}
		}
		if !drop {
			kept = append(kept, it)
		}
	}
	f.Items = kept
	out.AST = f
}

// OriginOf is the ItemOrigin of a top-level item of Output.AST, matched by
// span (Output.AST.Items drops the items that did not parse, so it is not
// parallel to Items). The driver builds resolve's per-node origins
// with it. False for a node that is not an item of this output.
func (o *Output) OriginOf(it ast.Item) (ItemOrigin, bool) {
	if it == nil || o.File == nil {
		return ItemOrigin{}, false
	}
	sp := it.Span()
	for _, x := range o.Items {
		if x.Output == sp {
			return x, true
		}
	}
	return ItemOrigin{}, false
}

// GenOrigin is the item's types.Item.Origin.
func (x ItemOrigin) GenOrigin() types.GenOrigin {
	return types.GenOrigin{Output: x.Output, Emitter: x.Emitter, Gen: x.Gen}
}

// originAt is the ItemOrigin of the generated item holding off: the item
// whose span holds it, else the last item before it, else the first.
func (o *Output) originAt(off uint32) *ItemOrigin {
	var best *ItemOrigin
	for i := range o.Items {
		it := &o.Items[i]
		if it.Output.Start > off {
			break
		}
		best = it
	}
	if best == nil && len(o.Items) > 0 {
		best = &o.Items[0]
	}
	return best
}

// Wrap returns the E0905 for a diagnostic whose At lies in out.File, and d
// itself otherwise; an E0704 on a generated impl is re-anchored at its gen
// line instead. Either keeps d's Cause. The driver calls it on every
// diagnostic before ordering.
func Wrap(d *diag.Diagnostic, out *Output) *diag.Diagnostic {
	if d == nil || out == nil || out.File == nil || d.At.File != out.File.ID || d.Code == diag.E0905 {
		return d
	}
	o := out.originAt(d.At.Start)
	if o == nil {
		return d
	}
	if d.Code == diag.E0704 {
		cp := *d
		cp.At = o.Gen
		return &cp
	}
	w := diag.New(diag.E0905, o.Gen, "generated code is invalid: "+d.Message).In(d.Phase).
		Loc("output", d.At).Loc("emitter", o.Emitter).Loc("gen", o.Gen).
		Fact("inner", diag.ObjectFact(diag.Field{Key: "code", Value: diag.CodeFact(d.Code)},
			diag.Field{Key: "name", Value: diag.TextFact(d.Code.Name())},
			diag.Field{Key: "message", Value: diag.TextFact(d.Message)}))
	w.Alternatives = append(w.Alternatives, "fix the generator, or the gen line's arguments")
	w.Cause = d.Cause
	return w
}

// Finish is called after resolve's rerun over the generated items: for each
// deferred argument and each tentative E0906 it emits E0906 when the name now
// resolves to an item of this module's generated output, and nothing
// otherwise (resolve reports E0201 itself). resolved maps a deferred name of
// the module to the item it now names, NoItem when none.
func (c *Context) Finish(out *Output, resolved func(name source.Symbol) types.ItemID) []*diag.Diagnostic {
	var ds []*diag.Diagnostic
	for _, p := range out.pending {
		it := resolved(p.name)
		if it == types.NoItem {
			continue
		}
		if item := c.Items.Get(it); item.Origin != nil && item.Module == out.Module {
			ds = append(ds, p.d)
		}
	}
	out.pending = nil
	return ds
}
