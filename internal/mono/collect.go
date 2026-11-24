// Why: codegen compiles instances, not generic functions, so the build
// needs the closed set of instances its roots reach, found the same way on
// every run.
//
// What: Collect, the worklist over roots (addRoots) and the requests each
// instance body makes (callReq, asmSymReqs, constArgOf), with the E0708
// limits of limit.go.
//
// How: the worklist runs level by level in key order on one goroutine, and
// a node's parent is chosen by (caller key, request order), so the result
// does not depend on GOMAXPROCS.

package mono

import (
	"slices"
	"strings"

	"mk/internal/diag"
	"mk/internal/mir"
	"mk/internal/types"
)

// Collect collects the instances roots reach and builds their bodies.
// Diagnostics are E0708 only; any other failure is an ICE. A root whose
// function has a const value parameter is skipped: it has instances only at
// the constants its callers pass. Deterministic whatever GOMAXPROCS: the
// worklist runs level by level in key order, on one goroutine (mono cannot
// start a guarded goroutine without source.Guard).
func Collect(env Env, roots []Root, opt Options) (*Instances, []*diag.Diagnostic) {
	c := &collector{env: env, opt: opt.withDefaults(), index: map[string]int{}, plainIndex: map[types.ItemID]int{}, lits: newLiterals(env)}
	c.tasksInit()
	c.libcInit() // the system-libc fold (libcfold.go)
	return c.run(roots)
}

// node is one instance of the table while collection runs.
type node struct {
	inst   Instance
	parent int // index of the first requester, -1 for a root
	// site is the call site in the parent's body: the parent's instance
	// body and the span of the requesting statement or terminator.
	siteBody *mir.Body
	siteSpan mir.SpanID
	built    bool
	// root is the index of the root node at the top of the parent chain:
	// the build root this node counts against for the instance limit.
	root int
	// count and stopped are kept on a root node only: how many nodes count
	// against it, and whether it reported its one E0708, after
	// which nothing more of it is added or built.
	count   int
	stopped bool
	// extra is the feature set the instance inherits beyond its own cpu
	// clause and the baseline (cpu.go): its key and name suffix.
	extra []string
}

// request is one function an instance body needs: a call, a fn value, a
// relocation in a const it reads, or a spawned callee.
type request struct {
	item      types.ItemID
	args      []types.GenericArg
	generics  int
	span      mir.SpanID
	addrTaken bool
	spawned   bool
	// call: a direct call (callReq), the one request whose generic instance
	// inherits the caller's feature set.
	call bool
	// ioField is 1 + the IoTable field a const stores the function in, else
	// 0 (deadio.go, tasks.go).
	ioField int
}

type collector struct {
	env        Env
	opt        Options
	nodes      []*node
	index      map[string]int
	plainIndex map[types.ItemID]int // non-literal items with no generic args and no extra CPU features
	diags      []*diag.Diagnostic
	// relocMemo caches the fn relocations reachable from a const.
	relocMemo map[types.ConstID][]relocFn
	// io is the dead-entry and task-use state (deadio.go, tasks.go).
	io ioState
	// cur is the node being built, the caller of a deferred entry.
	cur int
	// tg is the task-use graph (tasks.go).
	tg taskGraph
	// lits are the fn literals kept per content (canon.go).
	lits *literals
}

func (c *collector) run(roots []Root) (*Instances, []*diag.Diagnostic) {
	c.drain(c.addRoots(roots))
	c.settleTasks() // the UsesTasks fact (tasks.go)
	return c.finish(), c.diags
}

// addRoots adds roots as nodes and returns the new ones, sorted.
// The scope, spawn and system-stack entries wait for the UsesTasks fact while
// std declares UsesTasks (tasks.go lazyRoot).
func (c *collector) addRoots(roots []Root) []int {
	var level []int
	for _, r := range roots {
		if c.lazyRoot(r) {
			c.tg.lazy = append(c.tg.lazy, r)
			continue
		}
		if b := c.env.Body(r.Item); b != nil && b.GenericCount > 0 {
			ice("root %s is generic", c.env.Items().Get(r.Item).Key)
		}
		if c.hasConstParam(r.Item) {
			// A function with a const value parameter has no instance without
			// its const arguments: its body forwards or calls a
			// parameter no constant binds. It is reached only through the
			// callers that pass them, so as a root it adds nothing (the
			// driver's limit roots list every non-generic function).
			continue
		}
		k := keyOf(c.env, r.Item, nil)
		addr := r.Kind == RootExport || r.Kind == RootAddressTaken
		if i, ok := c.index[k.Text]; ok {
			c.nodes[i].inst.AddrTaken = c.nodes[i].inst.AddrTaken || addr
			if r.Kind != RootRuntime {
				c.tg.userRoots = append(c.tg.userRoots, i)
			}
			continue
		}
		i := len(c.nodes)
		c.add(&node{parent: -1, root: i, count: 1, inst: Instance{Key: k, Item: r.Item, AddrTaken: addr, CPU: c.ownCPU(r.Item)}})
		if r.Kind != RootRuntime {
			c.tg.userRoots = append(c.tg.userRoots, i)
		}
		level = append(level, i)
	}
	c.sortLevel(level)
	return level
}

// pend is one request with its caller node.
type pend struct {
	caller int
	req    request
}

// drain runs the worklist from level to its end (steps 2 to 5): each level's
// requests in body order, then the IoTable entries its reads released
// (deadio.go), merged in that order.
func (c *collector) drain(level []int) {
	for len(level) > 0 {
		var reqs []pend
		for _, i := range level {
			if c.nodes[c.nodes[i].root].stopped {
				continue // its root reported: that root's collection stopped after that level
			}
			for _, r := range c.build(i) {
				reqs = append(reqs, pend{i, r})
			}
			reqs = append(reqs, c.io.activated...)
			c.io.activated = c.io.activated[:0]
		}
		var next []int
		for _, p := range reqs {
			if j := c.merge(p.caller, p.req); j >= 0 {
				next = append(next, j)
			}
		}
		c.sortLevel(next)
		level = next
	}
}

// sortLevel orders a level by key text.
func (c *collector) sortLevel(level []int) {
	slices.SortFunc(level, func(a, b int) int {
		return strings.Compare(c.nodes[a].inst.Key.Text, c.nodes[b].inst.Key.Text)
	})
}

func (c *collector) add(n *node) int {
	c.nodes = append(c.nodes, n)
	i := len(c.nodes) - 1
	c.index[n.inst.Key.Text] = i
	if len(n.inst.Args) == 0 && len(n.extra) == 0 && c.env.Items().Get(n.inst.Item).Kind != types.KindLiteral {
		c.plainIndex[n.inst.Item] = i
	}
	return i
}

// merge records one request of caller: an existing key only gains
// the request's flags; a new key becomes a node of the next level, unless a
// limit stops it. It returns the new node, or -1.
func (c *collector) merge(caller int, r request) int {
	set, extra := c.instanceCPU(c.nodes[caller].inst.CPU, r) // the caller's cpu features
	if len(r.args) == 0 && len(extra) == 0 {
		if i, ok := c.plainIndex[r.item]; ok {
			n := c.nodes[i]
			c.edge(caller, i, r.ioField-1)
			n.inst.AddrTaken = n.inst.AddrTaken || r.addrTaken
			n.inst.Spawned = n.inst.Spawned || r.spawned
			return -1
		}
	}
	k := c.keyCPU(keyOf(c.env, r.item, r.args), r.item, r.args, extra)
	if i, ok := c.index[k.Text]; ok {
		n := c.nodes[i]
		c.edge(caller, i, r.ioField-1)
		n.inst.AddrTaken = n.inst.AddrTaken || r.addrTaken
		n.inst.Spawned = n.inst.Spawned || r.spawned
		return -1
	}
	p := c.nodes[caller]
	root := c.nodes[p.root]
	if root.stopped {
		return -1
	}
	// A literal named through a const or global value, which mono cannot
	// rewrite, still gets the kept literal's item.
	r.item = c.lits.item(r.item, r.args)
	depth := p.inst.Depth
	if len(r.args) > 0 {
		depth++ // a generic instance deepens its chain; a plain function does not
	}
	if int(depth) > c.opt.MaxDepth {
		c.limit(caller, r, "depth", c.opt.MaxDepth)
		return -1
	}
	if root.count+1 > c.opt.MaxCount {
		c.limit(caller, r, "count", c.opt.MaxCount)
		return -1
	}
	root.count++
	n := &node{parent: caller, root: p.root, siteBody: p.inst.Body, siteSpan: r.span, extra: extra, inst: Instance{
		Key: k, Item: r.item, Args: r.args, Generics: r.generics, Depth: depth,
		AddrTaken: r.addrTaken, Spawned: r.spawned, CPU: set}}
	j := c.add(n)
	c.edge(caller, j, r.ioField-1)
	return j
}

// build makes node i's instance body and returns its requests in body
// order.
func (c *collector) build(i int) []request {
	n := c.nodes[i]
	n.built = true
	in := &n.inst
	it := c.env.Items().Get(in.Item)
	in.Owner = it.Module
	in.Name = nameOf(c.env, in.Item, in.Args)
	if len(n.extra) > 0 {
		in.Name += cpuSuffix(n.extra)
	}
	in.Symbol = symbolOf(c.env, in.Item, in.Key, in.Args)
	c.noteFolded(i, c.env.Body(in.Item))
	var probs []fmtProblem
	in.Body, probs = instantiate(c.env, in.Item, in.Args, c.opt, c.lits)
	for _, p := range probs {
		c.formatProblem(i, p)
	}
	if in.Body == nil {
		return nil
	}
	c.asmRegisters(i) // E1104
	c.generalRegs(i)  // E1108
	in.Generics = VisibleGenerics(c.env.Body(in.Item))
	for bi := range in.Body.Blocks {
		in.Ops += uint32(len(in.Body.Blocks[bi].Stmts)) + 1
	}
	if !c.opt.SkipContent {
		in.Content = [32]byte(c.env.Hasher().InstanceContentHash(c.env.World(), in.Body))
	}
	c.cur = i
	c.ioReads(in.Body)
	return c.scan(in.Body)
}

// finish keeps the built nodes, sorted by key text.
func (c *collector) finish() *Instances {
	out := &Instances{env: c.env, usesTasks: c.opt.tasks.item == types.NoItem || c.opt.tasks.value}
	for _, n := range c.nodes {
		if n.built {
			out.List = append(out.List, n.inst)
		}
	}
	slices.SortFunc(out.List, func(a, b Instance) int { return strings.Compare(a.Key.Text, b.Key.Text) })
	out.index = make(map[string]int, len(out.List))
	for i := range out.List {
		out.index[out.List[i].Key.Text] = i
	}
	return out
}

// scan lists the requests of an instance body: every direct
// callee with its const value arguments (spawned callees marked), every
// RvFnRef (address taken), and every fn relocation reachable from a const
// the body reads (OpConst, RvConstRef, RvGlobalRef), address taken.
func (c *collector) scan(b *mir.Body) []request {
	var out []request
	consts := func(o *mir.Operand, span mir.SpanID) {
		if o.Kind == mir.OpConst {
			out = c.relocReqs(out, o.Const, span)
		}
	}
	for bi := range b.Blocks {
		blk := &b.Blocks[bi]
		for si := range blk.Stmts {
			st := &blk.Stmts[si]
			rv := &st.Rvalue
			if st.Kind == mir.StAsm {
				out = c.asmSymReqs(out, &b.Asms[st.Asm], st.Span)
				continue
			}
			if st.Kind != mir.StAssign {
				continue
			}
			consts(&rv.A, st.Span)
			consts(&rv.B, st.Span)
			if rv.Kind == mir.RvAggregate {
				for oi := rv.Ops.Start; oi < rv.Ops.End(); oi++ {
					consts(&b.Operands[oi], st.Span)
				}
			}
			switch rv.Kind {
			case mir.RvFnRef:
				out = append(out, c.fnRefReq(b, rv.Fn, st.Span))
			case mir.RvConstRef, mir.RvGlobalRef:
				out = c.itemValueReqs(out, rv.Item, st.Span)
			}
		}
		t := &blk.Term
		consts(&t.On, t.Span)
		consts(&t.Msg, t.Span)
		if !isCall(t) {
			continue
		}
		consts(&t.Callee.Value, t.Span)
		for ai := t.Args.Start; ai < t.Args.End(); ai++ {
			consts(&b.Args[ai].Operand, t.Span)
		}
		switch t.Callee.Kind {
		case mir.CallDirect:
			r := c.callReq(b, t)
			r.spawned = t.Spawn != mir.NoLocal
			out = append(out, r)
		case mir.CallInterface:
			ice("interface call survived instantiation")
		}
	}
	return out
}

// asmSymReqs roots every sym operand's item: a function is requested address
// taken, so it is never internal and never dropped (the template names its
// symbol); a var's value is requested as an RvGlobalRef's is.
func (c *collector) asmSymReqs(out []request, a *mir.Asm, span mir.SpanID) []request {
	for i := range a.Operands {
		op := &a.Operands[i]
		if op.Kind != mir.AsmSym || op.Sym == types.NoItem {
			continue
		}
		switch c.env.Items().Get(op.Sym).Decl.(type) {
		case *types.FuncDecl:
			out = append(out, request{item: op.Sym, span: span, addrTaken: true})
		case *types.GlobalDecl:
			out = c.itemValueReqs(out, op.Sym, span)
		}
	}
	return out
}

// fnRefReq is the request of a function used as a value: address taken. A
// function with const value parameters cannot be a value.
func (c *collector) fnRefReq(b *mir.Body, f mir.FnRef, span mir.SpanID) request {
	args := c.canonArgs(b.GenericArgs[f.Args.Start:f.Args.End()])
	if sig := c.sigOf(f.Item); sig != nil {
		for _, p := range sig.Params {
			if p.Const {
				ice("%s has const parameters and is used as a value", c.env.Items().Get(f.Item).Key)
			}
		}
	}
	return request{item: f.Item, args: args, generics: len(args), span: span, addrTaken: true}
}

// callReq is the request of a direct call: the callee's generic arguments,
// then the constant passed for each const value parameter.
func (c *collector) callReq(b *mir.Body, t *mir.Terminator) request {
	f := t.Callee.Fn
	args := c.canonArgs(b.GenericArgs[f.Args.Start:f.Args.End()])
	r := request{item: f.Item, generics: len(args), span: t.Span, call: true}
	sig := c.sigOf(f.Item)
	if sig != nil {
		cargs := b.Args[t.Args.Start:t.Args.End()]
		for pi, p := range sig.Params {
			if !p.Const {
				continue
			}
			if pi >= len(cargs) {
				ice("call of %s: no argument for const parameter %d", c.env.Items().Get(f.Item).Key, pi+1)
			}
			args = append(args, types.ConstGenericArg(types.ConstArg{Kind: types.CAValue,
				Value: c.constArgOf(cargs[pi].Operand)}))
		}
	}
	r.args = args
	return r
}

// constArgOf is the const value an argument for a const value parameter
// carries: an OpConst, or an OpImm interned as a const. The instance body
// has already made every other form check accepts a constant operand
// (canon.go constArgs; lower's const fn argument naming the caller's
// generics included), and codegen reads only these two forms, so
// anything else is an ICE.
func (c *collector) constArgOf(o mir.Operand) types.ConstID {
	switch o.Kind {
	case mir.OpConst:
		return o.Const
	case mir.OpImm:
		return c.immConst(o.Type, o.Bits)
	}
	ice("const parameter argument is not a constant (operand kind %d)", o.Kind)
	return types.NoConst
}

// immConst interns a scalar immediate as a const value of its type's size.
func (c *collector) immConst(t types.TypeID, bits uint64) types.ConstID {
	ty := c.env.Table().Get(t)
	var n int
	switch ty.Kind {
	case types.KInt, types.KFloat:
		n = int(ty.Bits) / 8
	case types.KIntPtr:
		n = 8
	case types.KBool:
		n = 1
	case types.KChar:
		n = 4
	default:
		ice("immediate of type %s", c.env.Table().TypeString(t, nil))
	}
	bs := make([]byte, n)
	types.StoreUint(bs, n, bits)
	return c.env.Consts().Intern(types.ConstValue{Type: t, Align: uint32(n), Bytes: bs})
}

// canonArgs folds SizeOf[T]() of a closed T in request arguments, so a key
// never depends on how a size was spelled.
func (c *collector) canonArgs(as []types.GenericArg) []types.GenericArg {
	if len(as) == 0 {
		return nil
	}
	s := substituter{tab: c.env.Table(), fold: func(t types.TypeID) types.ConstArg {
		l := c.env.Layout(t)
		if l == nil {
			ice("SizeOf[%s]: no layout", c.env.Table().TypeString(t, nil))
		}
		return types.ConstArg{Kind: types.CAValue, Value: intConst(c.env.Consts(), int64(l.Shape.Size))}
	}}
	return s.genericArgs(as)
}

func (c *collector) sigOf(item types.ItemID) *types.Signature {
	if fd, ok := c.env.Items().Get(item).Decl.(*types.FuncDecl); ok {
		return &fd.Sig
	}
	return nil
}

// hasConstParam reports whether item's signature has a const value
// parameter (such a function is no fn value, and an instance of it is keyed
// by the constants).
func (c *collector) hasConstParam(item types.ItemID) bool {
	if sig := c.sigOf(item); sig != nil {
		for _, p := range sig.Params {
			if p.Const {
				return true
			}
		}
	}
	return false
}

// itemValueReqs requests the functions a const's or global's value names.
func (c *collector) itemValueReqs(out []request, item types.ItemID, span mir.SpanID) []request {
	switch d := c.env.Items().Get(item).Decl.(type) {
	case *types.ConstDecl:
		if d.HasValue {
			out = c.relocReqs(out, d.Value, span)
		}
	case *types.GlobalDecl:
		if d.HasValue {
			out = c.relocReqs(out, d.Value, span)
		}
	}
	return out
}

// relocReqs requests every fn relocation reachable from const v, through
// its data relocations, address taken (a function stored as data). A
// function stored in field F of a const IoTable is requested only once some
// built body reads IoTable field F (deadio.go): until then it
// waits in ioPending under the node being built.
func (c *collector) relocReqs(out []request, v types.ConstID, span mir.SpanID) []request {
	for _, f := range c.relocFns(v) {
		args := c.canonArgs(f.fn.Args)
		r := request{item: f.fn.Item, args: args, generics: len(args), span: span, addrTaken: true, ioField: f.field + 1}
		if f.field >= 0 && c.deferEntry(f.field, r) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// relocFn is one fn relocation of a const: field is the IoTable field the
// function is stored in when the relocation is a field of a const IoTable,
// else -1.
type relocFn struct {
	fn    types.FnRef
	field int
}

func (c *collector) relocFns(v types.ConstID) []relocFn {
	cv := c.env.Consts().Get(v)
	if len(cv.Relocs) == 0 {
		return nil
	}
	if fs, ok := c.relocMemo[v]; ok {
		return fs
	}
	if c.relocMemo == nil {
		c.relocMemo = map[types.ConstID][]relocFn{}
	}
	c.relocMemo[v] = nil // a cycle of data relocations ends here
	var fs []relocFn
	for _, r := range cv.Relocs {
		switch r.Kind {
		case types.RelocFn:
			fs = append(fs, relocFn{fn: r.Fn, field: c.ioFieldAt(cv.Type, r.Offset)})
		case types.RelocData:
			fs = append(fs, c.relocFns(r.Data)...)
		}
	}
	c.relocMemo[v] = fs
	return fs
}
