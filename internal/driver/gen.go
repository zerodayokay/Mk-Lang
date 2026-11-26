// Why: a gen line runs a generator during a module's check, so the driver
// must hand the generator a view of the module's items, add the generated
// file to the module, and map later diagnostics back to the gen line.
//
// What: stage 4 of the module pipeline (stage4, settleGen, genWrap): the
// note on a name a line that emitted nothing may have declared (noteGen,
// notePatternGen and markGen),
// the error that made a poisoned line emit nothing (poisonRoot, firstDiag),
// the gen.Line translation of resolve's first pass (genLine, genArg), the
// World over check's stage-4 view (genWorld with Decl, Func and Funcs),
// AddGenerated, Finish, and Wrap over every later diagnostic of the module.
//
// How: stage 4 runs between resolve's Finish and Settle, after every module
// the generator's module depends on is Done; generator reads and poison go
// to the module cache through the gen evaluator itself.

package driver

import (
	"cmp"
	"slices"
	"strconv"
	"strings"

	"mk/internal/ast"
	checker "mk/internal/check"
	"mk/internal/diag"
	"mk/internal/gen"
	"mk/internal/iface"
	"mk/internal/resolve"
	"mk/internal/source"
	"mk/internal/types"
)

// genState is a module's stage 4: the context its lines ran with (Finish
// needs it after Settle) and their output.
type genState struct {
	ctx gen.Context
	out *gen.Output
	// sum is the generated file's parse summary, its anchors (GenRef,
	// genref.go), nil with the module cache off.
	sum *iface.ParseSummary
}

// stage4 runs m's gen lines between resolve's Finish and Settle. It does
// nothing for a module with no gen line, or before the semantics exist
// (pipelineAll(false), and the prelude's closure, whose files have no gen
// line). Every module a generator's module depends on is Done.
func (b *build) stage4(m *modNode, rm *resolve.Module) {
	w := b.w
	s := w.sem5
	if s == nil || m.info == nil || len(m.info.Gens) == 0 {
		return
	}
	cin := &checker.ModuleInput{Module: m.id, Info: m.info, BinRoot: m.binRoot, Library: m.libBoundary}
	for _, f := range m.files {
		cin.Files = append(cin.Files, f.ast)
	}
	view := s.chk.GenView(cin)
	lines := make([]gen.Line, 0, len(m.info.Gens))
	for _, g := range m.info.Gens {
		if l, ok := b.genLine(m.info, view, g); ok {
			lines = append(lines, l)
		}
	}
	jobs := w.cfg.Jobs
	if jobs <= 0 {
		jobs = 1
	}
	st := &genState{ctx: gen.Context{
		Types: w.tab, Items: w.items, Consts: w.tab.Consts(), Interner: w.in, Sources: w.sm,
		Eval: s.ev, World: genWorld{v: view, sm: w.sm, items: w.items}, Known: s.knownCopy(), Target: w.cfg.Target, Workers: jobs,
		DataFiles: &program{w: w, s: s},
	}}
	// Run takes the lines in Index order and answers Output.Lines in that
	// order; sorted here too, so line i is lines[i] (Trace.GeneratorRuns).
	slices.SortStableFunc(lines, func(x, y gen.Line) int { return cmp.Compare(x.Index, y.Index) })
	st.out = st.ctx.Run(m.id, lines)
	m.gen = st
	if st.out.AST != nil {
		st.sum = w.front.anchorGen(w, st.out.File, st.out.AST) // GenRef (genref.go)
	}
	for i := range st.out.Lines {
		if i < len(lines) && st.out.Lines[i].Ran {
			m.genRuns = append(m.genRuns, w.items.Get(lines[i].Item).Key)
		}
	}
	m.diags = append(m.diags, st.out.Diags...)
	// gen calls the Evaluator itself, not evalShim: its reads and poison
	// reach the module cache only here.
	for i := range st.out.Lines {
		lr := &st.out.Lines[i]
		if lr.Fail != nil && m.genFail == nil && i < len(lines) {
			m.genFailAt, m.genFail = lines[i].Span, lr.Fail
		}
		if lr.Poisoned {
			m.evalPoison.Store(true)
			if i < len(lines) {
				if m.genRoot == nil {
					if r, in := b.poisonRoot(m, &lines[i]); r != nil {
						m.genRoot, m.genRootIn, m.genRootAt = r, in, lines[i].Span
					}
				}
				m.genPoison = append(m.genPoison, lines[i].Span)
			}
		}
		if lr.Reads.Len() > 0 {
			m.readsMu.Lock()
			m.reads.Merge(&lr.Reads)
			m.readsMu.Unlock()
		}
	}
	if st.out.AST == nil {
		return
	}
	gf := resolve.GeneratedFile{File: st.out.AST}
	for _, it := range st.out.AST.Items {
		if o, ok := st.out.OriginOf(it); ok {
			gf.Origins = append(gf.Origins, resolve.NodeOrigin{Node: it.ID(), Origin: o.GenOrigin()})
		}
	}
	for _, o := range st.out.Origins {
		gf.Names = append(gf.Names, resolve.NameOrigin{Offset: o.Offset, Kind: resolve.NameKind(o.Kind), Module: o.Module, Item: o.Item})
	}
	rm.AddGenerated([]resolve.GeneratedFile{gf})
}

// settleGen wraps Settle's diagnostics of the generated file and adds
// Finish's E0906s. Called after Settle for every module.
func (b *build) settleGen(m *modNode, rm *resolve.Module, gs []*diag.Diagnostic) []*diag.Diagnostic {
	if m.gen == nil {
		return gs
	}
	out := make([]*diag.Diagnostic, 0, len(gs))
	for _, d := range gs {
		m.noteGen(d)
		out = append(out, gen.Wrap(d, m.gen.out))
	}
	return append(out, m.gen.ctx.Finish(m.gen.out, func(n source.Symbol) types.ItemID {
		if id, ok := rm.Lookup(n); ok {
			return id
		}
		return types.NoItem
	})...)
}

// genWrap passes stage 5 to 7 diagnostics of m through gen.Wrap: E0905 for
// one inside the generated file, a generated impl's E0704 re-anchored at its
// gen line.
func (m *modNode) genWrap(ds []*diag.Diagnostic) []*diag.Diagnostic {
	for _, d := range ds {
		m.noteGen(d)
	}
	if m.gen == nil || m.gen.out.File == nil {
		return ds
	}
	out := make([]*diag.Diagnostic, len(ds))
	for i, d := range ds {
		out[i] = gen.Wrap(d, m.gen.out)
	}
	return out
}

// noteGen marks an unknown-name E0201 of m that a gen line may cause: the
// name may be one the line's output would have declared, and the output is
// empty. The line is m's, or dep's for a member miss `dep.X`
// (resolve.MissedIn). A line that failed with its own error makes the E0201
// its consequence, so the report holds that error whatever its phase
// (diag.Order). A line whose run ended in poison because a module it
// depends on failed to check makes the E0201 a consequence of the error
// poisonRoot names (genRoot, which stageDiags reports); with no such
// module (a type of m the stage-4 view could not see, which may have no
// diagnostic of its own) a line of m is only cited. A label, type
// parameter or parameter is local to its function, so no gen line declares
// one.
func (m *modNode) noteGen(d *diag.Diagnostic) {
	if d.Code != diag.E0201 {
		return
	}
	if _, ok := d.FactValue("gen_line"); ok {
		return
	}
	if e, ok := d.FactValue("expected"); ok && (e.Str == "label" || e.Str == "type parameter" || e.Str == "parameter") {
		return
	}
	owner, at := m, "this module"
	if p := resolve.MissedIn(d); p != "" {
		owner = nil
		for _, dep := range m.deps {
			if dep.path == p {
				owner, at = dep, "`"+dep.path+"`"
				break
			}
		}
		if owner == nil || (owner.genFail == nil && owner.genRoot == nil) {
			return
		}
	}
	owner.markGen(d, at)
}

// notePatternGen marks, as noteGen marks an E0201, a pattern binding's
// E0207 that Settle reported (resolve.PatternMiss): its name was found
// nowhere while m's gen lines were unsettled, so it may be a const a line's
// output would have declared. Finish's E0207s name a name found in the
// module scope, which no gen line declares, and get no mark.
func (m *modNode) notePatternGen(d *diag.Diagnostic) {
	if !resolve.PatternMiss(d) {
		return
	}
	if _, ok := d.FactValue("gen_line"); ok {
		return
	}
	m.markGen(d, "this module")
}

// markGen makes d a consequence of the error that made a gen line of m
// emit nothing, and cites the line; at names m in the note.
func (m *modNode) markGen(d *diag.Diagnostic, at string) {
	switch {
	case m.genFail != nil:
		d.ConsequenceOf(m.genFail.Code, m.genFail.At)
		d.Loc("gen_line", m.genFailAt).Text("gen", "a gen line of "+at+" failed and emitted nothing: the name may be one it would have declared")
	case m.genRoot != nil:
		d.ConsequenceOf(m.genRoot.Code, m.genRoot.At)
		d.Loc("gen_line", m.genRootAt).Text("gen", "a gen line of "+at+" emitted nothing: `"+m.genRootIn+"`, which it depends on, failed to check")
	case len(m.genPoison) > 0:
		d.Loc("gen_line", m.genPoison[0]).Text("gen", "a gen line of this module emitted nothing: it read a type that failed to check, or one it cannot see before checking")
	}
}

// poisonRoot finds why a poisoned line emitted nothing. It walks from the
// modules the line reads (the generator's, the fuel const's, each
// argument's; m itself, not yet checked, is not walked) through their
// imports in path order, three times. The first walk stops at a module with
// an error before types: that module and every module above it are never
// checked, so the generator or a function argument there has no
// declaration, and the report is of that error's phase. Only when no module
// has one does the second stop at a module that failed to check, with an
// error before stage 7: it taints every module above it, so every body a
// run enters there is poison. Only when no module has one either does the
// third stop at a module with a borrow error (stage 7), which rejects only
// its own body and leaves the module clean; the walk cannot tell which
// rejected body the run entered. The root is that module's error of that
// kind the report prints first (firstDiag). It runs only for a poisoned
// line, after every module it reaches is done. nil when no module has one:
// the line read a type of its own module it cannot see before checking.
func (b *build) poisonRoot(m *modNode, l *gen.Line) (*diag.Diagnostic, string) {
	w, s := b.w, b.w.sem5
	var from []*modNode
	read := func(id types.ItemID) {
		if id != types.NoItem {
			from = append(from, s.module(w.items.Get(id).Module))
		}
	}
	read(l.Generator)
	read(l.FuelItem)
	for _, a := range l.Args {
		id := a.Item // ArgInterface, ArgFunction
		if a.Kind == gen.ArgType {
			// The named type, or the one under []T, &T, ?T and the
			// like, which a generator reaches through Type.Elem.
			id = types.NoItem
			for t := a.Type; t != types.NoType; {
				tt := w.tab.Get(t)
				if tt.Kind == types.KNamed {
					id = tt.Item
					break
				}
				t = tt.Elem
			}
		}
		read(id)
	}
	for walkNo := range 3 {
		seen := map[*modNode]bool{m: true}
		var walk func(x *modNode) (*diag.Diagnostic, string)
		walk = func(x *modNode) (*diag.Diagnostic, string) {
			if x == nil || seen[x] {
				return nil, ""
			}
			seen[x] = true
			ds := [][]*diag.Diagnostic{x.diags, x.cdiags}
			switch walkNo {
			case 0:
				ds = [][]*diag.Diagnostic{x.diags} // stage 3's
			case 2:
				ds = [][]*diag.Diagnostic{x.fdiags}
			}
			if d := b.firstDiag(x, ds, walkNo == 0); d != nil {
				return d, x.path
			}
			for _, d := range x.deps {
				if r, p := walk(d); r != nil {
					return r, p
				}
			}
			return nil, ""
		}
		for _, x := range from {
			if r, p := walk(x); r != nil {
				return r, p
			}
		}
	}
	return nil, ""
}

// firstDiag is the diagnostic of ds, x's own, that the report prints first:
// by path, line and column, code, message, then end, as diag.Order sorts,
// and wrapped as checkDiags reports it, so a Cause naming it resolves. The
// lists' order differs between a cold check and a cache hit; this order
// does not. With early, only an error before types counts, as namesClean
// judges one. nil when ds holds none.
func (b *build) firstDiag(x *modNode, ds [][]*diag.Diagnostic, early bool) *diag.Diagnostic {
	var best *diag.Diagnostic
	var bs, be source.Position
	for _, l := range ds {
		for _, d := range l {
			if early && (d.Phase == diag.PhaseInternal || d.Phase >= diag.PhaseTypes) {
				continue
			}
			if x.gen != nil {
				d = gen.Wrap(d, x.gen.out)
			}
			var s, e source.Position
			if d.At.File != source.NoFile {
				s, e = b.w.sm.SpanPositions(d.At)
			}
			if best == nil || cmp.Or(strings.Compare(s.Path, bs.Path), cmp.Compare(s.Line, bs.Line), cmp.Compare(s.Col, bs.Col),
				cmp.Compare(d.Code, best.Code), strings.Compare(d.Message, best.Message),
				cmp.Compare(e.Line, be.Line), cmp.Compare(e.Col, be.Col)) < 0 {
				best, bs, be = d, s, e
			}
		}
	}
	return best
}

// genLine translates one of resolve's gen lines (first pass, gen/ empty).
func (b *build) genLine(info *resolve.Info, view *checker.GenView, g resolve.GenLine) (gen.Line, bool) {
	w := b.w
	if g.File < 0 || g.File >= len(info.Files) {
		return gen.Line{}, false
	}
	fi := info.Files[g.File]
	var node *ast.GenItem
	for _, it := range fi.File.Items {
		if gi, ok := it.(*ast.GenItem); ok && gi.ID() == g.Node {
			node = gi
			break
		}
	}
	if node == nil {
		source.ICEf(source.Span{File: fi.FileID}, "driver: gen line %v has no GenItem node", g.Item)
	}
	l := gen.Line{Item: g.Item, Node: node, Index: genIndex(w.items.Get(g.Item).Key), Span: node.Span(), Generator: g.Generator}
	if node.Generator != nil {
		parts := make([]string, 0, len(node.Generator.Names))
		for _, id := range node.Generator.Names {
			parts = append(parts, w.in.Str(id.Name))
		}
		l.Path = strings.Join(parts, ".")
	}
	for _, a := range node.Args {
		l.Args = append(l.Args, b.genArg(fi, g.File, view, a))
	}
	switch f := node.Fuel.(type) {
	case nil:
	case *ast.BasicLit:
		l.FuelLit = f
	default:
		if r := refOf(fi, f); r.Kind == resolve.RefItem && w.items.Get(types.ItemID(r.A)).Kind == types.KindConst {
			l.FuelItem = types.ItemID(r.A)
		} else {
			l.FuelOther = true
		}
	}
	if node.Fuel != nil {
		l.FuelSpan = node.Fuel.Span()
	}
	return l, true
}

// genArg classifies one argument of a gen line.
func (b *build) genArg(fi *resolve.FileInfo, file int, view *checker.GenView, a ast.TypeOrExpr) gen.Arg {
	w := b.w
	sp := a.Span()
	switch x := a.(type) {
	case *ast.BasicLit:
		return gen.Arg{Kind: gen.ArgLiteral, Lit: x, Span: sp}
	case *ast.UnaryExpr:
		if lit, ok := x.X.(*ast.BasicLit); ok && x.Op == ast.TokMinus {
			return gen.Arg{Kind: gen.ArgLiteral, Lit: lit, Neg: true, Span: sp}
		}
	case *ast.Ident:
		// true and false are universe names, not BasicLits; gen folds a
		// BasicLit whose Raw is "true" or "false" (run.go fold).
		if r := refOf(fi, x); r.Kind == resolve.RefUniverse {
			if s := w.in.Str(x.Name); s == "true" || s == "false" {
				return gen.Arg{Kind: gen.ArgLiteral, Lit: &ast.BasicLit{Base: x.Base, Raw: s}, Span: sp}
			}
		}
	}
	if r := refOf(fi, a); r.Kind == resolve.RefItem {
		switch w.items.Get(types.ItemID(r.A)).Kind {
		case types.KindInterface:
			// GenView.TypeOf gives NoType for an interface.
			return gen.Arg{Kind: gen.ArgInterface, Item: types.ItemID(r.A), Span: sp}
		case types.KindFunc:
			// A function, bound as a Method when it is a free
			// non-generic one.
			return gen.Arg{Kind: gen.ArgFunction, Item: types.ItemID(r.A), Span: sp}
		}
	}
	t, name := view.TypeOf(file, a)
	switch {
	case t != types.NoType:
		return gen.Arg{Kind: gen.ArgType, Type: t, Span: sp}
	case name != 0:
		return gen.Arg{Kind: gen.ArgDeferred, Name: name, Span: sp}
	}
	return gen.Arg{Kind: gen.ArgOther, Span: sp}
}

// refOf is resolve's Ref for a name argument: an Ident, a module-qualified
// FieldExpr, or a Path or PathType without arguments.
func refOf(fi *resolve.FileInfo, n ast.Node) resolve.Ref {
	switch x := n.(type) {
	case *ast.Ident, *ast.FieldExpr, *ast.Path:
	case *ast.PathType:
		if x.Args != nil || x.Path == nil {
			return resolve.Ref{}
		}
		n = x.Path
	default:
		return resolve.Ref{}
	}
	id := int(n.ID())
	if id <= 0 || id >= len(fi.Refs) {
		return resolve.Ref{}
	}
	return fi.Refs[id]
}

// genIndex is n of the gen line's key, types.GenKey(module, n).
func genIndex(k types.Key) int {
	s := k.String()
	i := strings.LastIndex(s, "#gen(")
	if i < 0 || !strings.HasSuffix(s, ")") {
		source.ICEf(source.Span{}, "driver: %q is not a gen key", s)
	}
	n, err := strconv.Atoi(s[i+len("#gen(") : len(s)-1])
	if err != nil {
		source.ICEf(source.Span{}, "driver: %q is not a gen key", s)
	}
	return n
}

// genWorld is gen.World over check's stage-4 view. The two sets of view
// types have the same fields (internal/check/stage4.go).
type genWorld struct {
	v     *checker.GenView
	sm    *source.SourceMap // Funcs' order by file path
	items *types.Items
}

func (g genWorld) Decl(item types.ItemID, from types.ModuleID) (*gen.DeclView, bool) {
	d, ok := g.v.Decl(item, from)
	if !ok {
		return nil, false
	}
	out := &gen.DeclView{Item: d.Item, Kind: d.Kind, Exported: d.Exported, Generics: d.Generics, Fields: genFields(d.Fields)}
	for _, vr := range d.Variants {
		out.Variants = append(out.Variants, gen.VariantView{Name: vr.Name, Fields: genFields(vr.Fields), Span: vr.Span})
	}
	for _, mt := range d.Methods {
		out.Methods = append(out.Methods, genMethod(mt))
	}
	return out, true
}

// genMethod is a stage-4 method or function view as gen's.
func genMethod(mt checker.GenMethod) gen.MethodView {
	v := gen.MethodView{Sig: mt.Sig, Span: mt.Span}
	for _, f := range mt.Flows {
		v.Flows = append(v.Flows, gen.FlowView{Kind: f.Kind, Param: f.Param, Path: f.Path, Field: f.Field})
	}
	return v
}

// Func implements gen.FuncWorld.
func (g genWorld) Func(item types.ItemID) (*gen.MethodView, bool) {
	mt, ok := g.v.Func(item)
	if !ok {
		return nil, false
	}
	v := genMethod(*mt)
	return &v, true
}

// Funcs implements gen.FuncWorld.
// Declaration order is the declaring file's path, then the offset in it,
// so the order never depends on the order the files were loaded in.
func (g genWorld) Funcs(item types.ItemID, from types.ModuleID) ([]types.ItemID, bool) {
	ids, ok := g.v.Funcs(item, from)
	if !ok || g.sm == nil {
		return ids, ok
	}
	path := func(id types.ItemID) string {
		if f := g.sm.File(g.items.Get(id).Span.File); f != nil {
			return f.Path
		}
		return ""
	}
	slices.SortStableFunc(ids, func(a, b types.ItemID) int {
		return cmp.Or(strings.Compare(path(a), path(b)), cmp.Compare(g.items.Get(a).Span.Start, g.items.Get(b).Span.Start))
	})
	return ids, true
}

// Hash is zero until iface hashes declaration views (World.Hash).
func (genWorld) Hash(types.ItemID, types.ReadKind, string) source.Hash { return source.Hash{} }

func genFields(fs []checker.GenField) []gen.FieldView {
	if fs == nil {
		return nil
	}
	out := make([]gen.FieldView, len(fs))
	for i, f := range fs {
		out[i] = gen.FieldView{Name: f.Name, Type: f.Type, Unresolved: f.Unresolved, Exported: f.Exported, Span: f.Span}
	}
	return out
}
