// Why: one evaluation request needs its own mutable state (frames, fuel,
// reads, the trace) apart from the Evaluator's shared compiled code, and a
// const's outcome must not depend on how deep the chain that reached it was.
//
// What: frame, segment, centry and Machine, with newMachine, charge, ti,
// enter, pushFrame, popFrame, the trace builders (chain, complete,
// completeAll, traceTail, capJoin) and runLoop, the interpreter loop.
//
// How: frames live on a Go slice, never the host stack; a const read inside
// an evaluation is a nested segment on the same slice, with its own fuel,
// steps, frame bytes and read set. Charges are taken before a statement
// runs, and the stage rule is applied to every body and layout an
// evaluation enters. A trace keeps at most 64 entries.

package eval

import (
	"encoding/binary"

	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

// frame is one running call. Its locals live in allocation base, whose data
// record is d (memory.data; fixed while the frame lives).
type frame struct {
	code *compiled
	// ce is the segment's entry of code: its call-site records.
	ce    *centry
	onerr []bool
	item  types.ItemID
	base  uint32
	d     uint32
	block mir.BlockID
	stmt  int
	// callSpan is the span of the call that pushed it (the request's site
	// for a segment's outermost frame).
	callSpan source.Span
	// arena marks the frame of Config.EvalArena building the segment's
	// arena: its result is kept. hostArena marks one a host's Arena()
	// pushed: its return goes to no caller's destination.
	arena, hostArena bool
	// detached marks a frame the tagged mode's host pushed (a task's call,
	// a host-run std function, taghost.go): its return goes to no caller's
	// destination, only to sink when hasSink is set.
	detached, hasSink bool
	sink              loc
}

// segment is one const evaluation in progress, or the request itself (item
// NoItem for a request that is not of a const): its own fuel, steps and
// frame bytes, so a const's outcome never depends on how deep the chain that
// reached it was.
type segment struct {
	item       types.ItemID
	fuel       uint64
	steps      uint64
	frameBytes uint64
	// room is the frame bytes a const's or global's initializer may hold
	// for its own value (four times its size; startConst), 0 elsewhere.
	room uint64
	// base is the index of the segment's outermost frame.
	base  int
	reads types.ReadSet
	// entered are this segment's own initializer and the same-module consts
	// it read; with their slots' lists they give sigEntered (slots.go).
	entered []types.ItemID
	// tis are the types this segment has applied the stage rule to and
	// recorded the layout reads of, and bodies the compiled bodies it has
	// entered, with their call-site records. They are the segment's own, not
	// the tree's, so a const's reads never depend on which segment of a tree
	// first touched a type or a body, and the call-site records of an
	// evaluation scale with the bodies it enters.
	tis    map[types.TypeID]*tinfo
	bodies map[*compiled]*centry
	// arena is the pointer word of the segment's EvalArena, 0 before its
	// first use, and arenaVal the &Arena value Arena gave hosts: one arena
	// per segment, so a const's steps never depend on whether a nested const
	// built one first. makingArena: a host's Arena() is running
	// Config.EvalArena's body.
	arena       uint64
	arenaVal    Value
	makingArena bool
}

// centry is one compiled body as entered by one segment: the resolved direct
// calls of its blocks' terminators, made on the first call.
type centry struct {
	sites []callSite
}

// Machine is one running evaluation. Hosts read and write it through the
// exported methods (host.go); everything else is the interpreter's.
type Machine struct {
	ev   *Evaluator
	cfg  *Config
	req  *types.EvalRequest
	host Host

	mem    memory
	frames []frame
	segs   []segment
	// tailing is set while a TermTailCall's call is made (tailcall.go): the
	// callee's frame replaces the caller's instead of going on top of it.
	tailing bool

	consts  map[types.ConstID]uint32
	fns     map[string]uint32
	globals map[types.ItemID]uint32
	// held is the module slot table whose lock this tree holds.
	held *moduleSlots
	// scratch is the evaluation's buffer for building aggregates.
	scratch uint32
	// keyBuf is a reusable byte buffer.
	keyBuf []byte
	// entering is the item whose body is being prepared and has no frame
	// yet, enteringAt the span it is entered from (a trace entry).
	entering   types.ItemID
	enteringAt source.Span
	// result is segment 0's outcome once its outermost frame returned.
	result *outcome
	// inProgress maps a const whose segment is running to its index.
	inProgress map[types.ItemID]int
	// preBlock is the block of frame preFrame charged once on entry and
	// still running, nil otherwise; npre counts such entries.
	preBlock *cblock
	preFrame int
	npre     uint64
	// tg is the tagged mode's state, nil in plain mode; every tagged hook on a
	// general path is one test of it.
	tg *tagState
}

func (e *Evaluator) newMachine(req *types.EvalRequest, h Host) *Machine {
	m := &Machine{ev: e, cfg: &e.cfg, req: req, host: h}
	m.mem = newMemory(&e.pool)
	m.mem.materialize = m.materializeConst
	m.consts = make(map[types.ConstID]uint32)
	m.fns = make(map[string]uint32)
	m.globals = make(map[types.ItemID]uint32)
	m.inProgress = make(map[types.ItemID]int)
	return m
}

// finish releases the evaluation's memory and the slot lock it holds.
func (m *Machine) finish() {
	m.ev.preEntries.Add(m.npre)
	m.mem.release()
	if m.held != nil {
		m.held.mu.Unlock()
		m.held = nil
	}
}

func (m *Machine) items() *types.Items { return m.cfg.Items }
func (m *Machine) tab() *types.Table   { return m.cfg.Types }

// seg is the running segment.
func (m *Machine) seg() *segment { return &m.segs[len(m.segs)-1] }

// charge takes n steps from the running segment, or stops with EvalFuel at
// the current statement, which does not run.
func (m *Machine) charge(n uint64) {
	s := m.seg()
	if n > s.fuel-s.steps {
		panic(&stop{err: &types.EvalError{Kind: types.EvalFuel}})
	}
	s.steps += n
}

// ti returns the info of t after applying the stage rule to its layout and
// recording the layout reads; first use per segment.
func (m *Machine) ti(t types.TypeID) *tinfo {
	if ti := m.seg().tis[t]; ti != nil {
		return ti
	}
	m.noteType(t, source.Span{})
	return m.seg().tis[t]
}

// noteType applies the stage rule to t's layout and records ReadLayout for
// each named type a by-value walk reaches; at is the span to stop at when the
// current statement is not the place (a frame being built).
func (m *Machine) noteType(t types.TypeID, at source.Span) {
	if m.seg().tis[t] != nil {
		return
	}
	ents := m.ev.byValue(t)
	if m.req.Stage == types.StageSignatures {
		for _, en := range ents {
			if m.items().Get(en.item).Module == m.req.Module {
				panic(&stop{err: &types.EvalError{Kind: types.EvalStage}, entered: en.item, enteredAt: at})
			}
		}
	}
	for _, en := range ents {
		if en.t == types.NoType {
			// An error of E!T or an error set: its payload sizes the union
			// and places the fields a SelCase reads, so the walk reads its
			// layout, keyed as iface keys it (Name "", errors are not
			// generic).
			m.seg().reads.Add(types.Read{Kind: types.ReadLayout, Key: m.items().Get(en.item).Key, Hash: m.errorLayoutHash(en.item)})
			continue
		}
		nt := m.ev.tinfo(en.t)
		m.seg().reads.Add(types.Read{Kind: types.ReadLayout, Key: m.items().Get(en.item).Key,
			Name: m.cfg.Program.LayoutName(m.tab().Get(en.t).Args), Hash: nt.hash})
	}
	s := m.seg()
	if s.tis == nil {
		s.tis = make(map[types.TypeID]*tinfo)
	}
	s.tis[t] = m.ev.tinfo(t)
}

// errorLayoutHash is the hash an error's ReadLayout records: its payload
// field count and each field type's layout hash (Program.Layout), so a
// payload field added, removed or retyped changes it. An error is never a
// type of its own, so Program has no layout to ask for.
// internal/driver/frontcache.go answers an error's layout read from its
// payload fields the same way; the two must agree.
func (m *Machine) errorLayoutHash(item types.ItemID) source.Hash {
	it := m.items().Get(item)
	d, ok := it.Decl.(*types.ErrorDecl)
	if !ok {
		source.ICEf(it.Span, "eval: error %s has no declaration", it.Key)
	}
	b := binary.AppendUvarint(nil, uint64(len(d.Fields)))
	for _, f := range d.Fields {
		h := m.ev.tinfo(f.Type).hash
		b = append(b, h[:]...)
	}
	return source.HashBytes("mk.eval.errorlayout.v1", b)
}

// enter prepares a body for a frame: the first entry per segment applies the
// stage rule to every layout it reads and records them, cached or not.
func (m *Machine) enter(b *mir.Body, at source.Span) (*compiled, *centry) {
	return m.enterCompiled(m.ev.compiledFor(b, m.tg != nil), at)
}

// enterCompiled is enter for a compiled body already in hand (a request's
// trampoline, which is not shared).
func (m *Machine) enterCompiled(cd *compiled, at source.Span) (*compiled, *centry) {
	if ce := m.seg().bodies[cd]; ce != nil {
		return cd, ce
	}
	for _, t := range cd.types {
		m.noteType(t, at)
	}
	m.ev.ready(cd)
	ce := &centry{}
	s := m.seg()
	if s.bodies == nil {
		s.bodies = make(map[*compiled]*centry)
	}
	s.bodies[cd] = ce
	return cd, ce
}

// pushFrame builds a frame for cd, charged as an allocation of its size
// on top of extra steps already due (the call terminator's static
// cost, given back when the frame does not fit), and checked against the
// stack limit.
func (m *Machine) pushFrame(cd *compiled, ce *centry, item types.ItemID, onerr []bool, callSpan source.Span, refund uint64) *frame {
	s := m.seg()
	// A frame too large for the limit, or for any allocation, is the stack
	// limit panic, a user program's diagnostic; frameSize is
	// capped far below wrapping (compile.go), so the sum cannot wrap.
	if lim := m.stackLimit(s); cd.frameSize > maxAlloc || cd.frameSize > lim-s.frameBytes {
		panicWith(types.PanicStackLimit, "")
	}
	if n := c(cd.frameSize); n > s.fuel-s.steps {
		s.steps -= refund
		panic(&stop{err: &types.EvalError{Kind: types.EvalFuel}})
	}
	s.steps += c(cd.frameSize)
	s.frameBytes += cd.frameSize
	base := m.mem.newAlloc(allocFrame, cd.frameSize)
	m.frames = append(m.frames, frame{code: cd, ce: ce, onerr: onerr, item: item, base: base, d: m.mem.recs[base].d, callSpan: callSpan})
	f := &m.frames[len(m.frames)-1]
	if m.tg != nil {
		m.tgPushFrame(f)
	}
	return f
}

// popFrame frees the top frame.
func (m *Machine) popFrame() {
	f := &m.frames[len(m.frames)-1]
	if m.tg != nil {
		m.tgPopFrame(f)
	}
	m.seg().frameBytes -= f.code.frameSize
	m.mem.free(f.base)
	m.frames = m.frames[:len(m.frames)-1]
}

// curSpan is the span of the statement or terminator frame f is at.
func (f *frame) curSpan() source.Span {
	b := f.code.body
	bl := &b.Blocks[f.block]
	if f.stmt < len(bl.Stmts) {
		return b.Span(bl.Stmts[f.stmt].Span)
	}
	return b.Span(bl.Term.Span)
}

// chain is the trace of frames [from, len): items and the span each frame is
// at, outermost first.
func (m *Machine) chain(from int) ([]types.ItemID, []source.Span) {
	var items []types.ItemID
	var spans []source.Span
	for i := from; i < len(m.frames); i++ {
		items = append(items, m.frames[i].item)
		spans = append(spans, m.frames[i].curSpan())
	}
	return items, spans
}

// complete fills an error's trace, At and Steps for segment j: the frames
// from the segment's outermost one, then the item being entered when no
// frame of it exists yet, a stored chain or the entered item of a stage stop;
// capped at 64 entries.
func (m *Machine) complete(st *stop, j int) *types.EvalError {
	s := &m.segs[j]
	fi, fs := m.chain(s.base)
	last := m.req.Site
	if len(fs) > 0 {
		last = fs[len(fs)-1]
	}
	ti, ts, at := m.traceTail(st, last)
	return m.traceError(st, j, fi, fs, ti, ts, at)
}

// completeAll is complete for every segment of a stopped tree, in time
// linear in the frames: the frames' entries are built once and segment j's
// trace is their suffix from its base, joined to the shared tail and capped
// without building the whole list. Only the last segment can have no
// frame of its own yet (one being entered); its tail falls back to the
// request's site as complete's does.
func (m *Machine) completeAll(st *stop) []*types.EvalError {
	fi, fs := m.chain(0)
	var withFrames struct {
		items []types.ItemID
		spans []source.Span
		at    source.Span
	}
	if len(fs) > 0 {
		withFrames.items, withFrames.spans, withFrames.at = m.traceTail(st, fs[len(fs)-1])
	}
	out := make([]*types.EvalError, len(m.segs))
	for j := range m.segs {
		base := m.segs[j].base
		if base < len(fi) {
			out[j] = m.traceError(st, j, fi[base:], fs[base:], withFrames.items, withFrames.spans, withFrames.at)
			continue
		}
		ti, ts, at := m.traceTail(st, m.req.Site)
		out[j] = m.traceError(st, j, nil, nil, ti, ts, at)
	}
	return out
}

// traceTail is the part of a trace after a segment's frames: a stored
// chain, or the item being entered when no frame of it exists yet, then the
// entered item of a stage stop. last is the span of the frames' last entry
// (the request's site without one); it returns the tail and the trace's
// last span, the error's At.
func (m *Machine) traceTail(st *stop, last source.Span) ([]types.ItemID, []source.Span, source.Span) {
	var items []types.ItemID
	var spans []source.Span
	add := func(it types.ItemID, sp source.Span) {
		items = append(items, it)
		spans = append(spans, sp)
		last = sp
	}
	switch {
	case st.chainItems != nil:
		items = append(items, st.chainItems...)
		spans = append(spans, st.chainSpans...)
		if len(spans) > 0 {
			last = spans[len(spans)-1]
		}
	case m.entering != types.NoItem && st.err.Kind == types.EvalStage:
		at := m.enteringAt
		if !at.IsValid() {
			at = last
		}
		add(m.entering, at)
	}
	if st.entered != types.NoItem {
		at := st.enteredAt
		if !at.IsValid() {
			at = last
		}
		add(st.entered, at)
	}
	return items, spans, last
}

// traceError is segment j's error over the trace fi/fs then ti/ts.
func (m *Machine) traceError(st *stop, j int, fi []types.ItemID, fs []source.Span, ti []types.ItemID, ts []source.Span, at source.Span) *types.EvalError {
	src := st.err
	e := &types.EvalError{Kind: src.Kind, Panic: src.Panic, Message: src.Message, Refusal: src.Refusal, File: src.File, Path: src.Path, Steps: m.segs[j].steps, At: at}
	e.Items, e.Trace = capJoin(fi, fs, ti, ts)
	return e
}

// capJoin is the trace a then b, in fresh slices: all of it up to 64
// entries, else the first 32 and last 32 with a zero span and NoItem
// between them. It costs time in the entries it keeps, not in the
// trace's length.
func capJoin(a []types.ItemID, as []source.Span, b []types.ItemID, bs []source.Span) ([]types.ItemID, []source.Span) {
	n := len(a) + len(b)
	item := func(i int) (types.ItemID, source.Span) {
		if i < len(a) {
			return a[i], as[i]
		}
		return b[i-len(a)], bs[i-len(a)]
	}
	keep := n
	if n > 64 {
		keep = 65
	}
	if keep == 0 {
		return nil, nil
	}
	ni := make([]types.ItemID, 0, keep)
	ns := make([]source.Span, 0, keep)
	put := func(lo, hi int) {
		for i := lo; i < hi; i++ {
			it, sp := item(i)
			ni = append(ni, it)
			ns = append(ns, sp)
		}
	}
	if n <= 64 {
		put(0, n)
		return ni, ns
	}
	put(0, 32)
	ni = append(ni, types.NoItem)
	ns = append(ns, source.Span{})
	put(n-32, n)
	return ni, ns
}

// runLoop runs until the frame stack is back to floor frames (0: the
// request's outermost frame returned; more: the frame a host's Arena()
// pushed returned) or the evaluation stops. It returns the stop, nil
// on completion.
func (m *Machine) runLoop(floor int) (st *stop) {
	defer func() {
		if r := recover(); r != nil {
			s, ok := r.(*stop)
			if !ok {
				panic(r)
			}
			m.giveBack()
			st = s
		}
	}()
	for len(m.frames) > floor {
		fi := len(m.frames) - 1
		f := &m.frames[fi]
		cb := &f.code.blocks[f.block]
		// Block entry: charge the whole block once when it fits. A
		// stop inside it gives back what did not start (giveBack).
		if f.stmt == 0 && cb.pre {
			if s := m.seg(); cb.total <= s.fuel-s.steps {
				s.steps += cb.total
				m.preBlock, m.preFrame = cb, fi
				m.npre++
				m.runBlock(f, cb)
				m.preBlock = nil
				continue
			}
		}
		if k := f.stmt; k < len(cb.ins) {
			in := &cb.ins[k]
			if in.kind != iSlow {
				m.charge(cb.cost[k])
				m.exec(f, in)
				f.stmt++
				continue
			}
			stmt := &f.code.body.Blocks[f.block].Stmts[k]
			if stmt.Kind == mir.StAssign && (stmt.Rvalue.Kind == mir.RvConstRef || stmt.Rvalue.Kind == mir.RvGlobalRef) {
				if m.needSegment(stmt.Rvalue.Item) {
					continue // a nested const segment was pushed; rerun after it
				}
			}
			m.charge(cb.cost[k])
			m.stmt(f, stmt)
			// f may be stale only if the statement pushed a frame, which
			// statements never do.
			f.stmt++
			continue
		}
		m.charge(cb.cost[len(cb.ins)])
		m.runTerm(f, cb)
	}
	return nil
}
