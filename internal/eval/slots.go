// Why: consts read other consts, possibly in the same module and possibly in
// a cycle; each const must be evaluated once, its failure reported once, and
// a cycle named in the same order every time, whatever order requests come.
//
// What: moduleSlots, slot and outcome, lookup, initializersReady,
// startConst, endSegment, constValue, cycle, abort, answer, and request with
// constRequest and callRequest, the entry of every evaluation.
//
// How: each module's slots sit behind one mutex, held by an evaluation tree.
// A same-module const read is a nested segment; a cycle is rotated to start
// at its least key and stored for every member. A const's own failure goes
// only to its declaration evaluation; every other reader gets poison. An
// outcome from the body stage is reused only while its initializers are
// still ready, and a nested stage stop stops the whole tree with its chain.

package eval

import (
	"sync"

	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

// moduleSlots are the const slots of one module, behind one mutex.
type moduleSlots struct {
	mu sync.Mutex
	m  map[types.ItemID]*slot
}

// slot is one const's or global's outcomes per stage.
type slot struct {
	sig, body *outcome
	reported  bool
}

// outcome is a stored result: a value, the const's own failure (EvalPanic,
// EvalFuel, EvalRefused, EvalCycle), a stage stop (EvalStage, at
// StageSignatures only) or poison.
type outcome struct {
	value   types.ConstID
	err     *types.EvalError
	poison  bool
	steps   uint64
	reads   types.ReadSet
	entered []types.ItemID
}

func (o *outcome) isStage() bool { return o.err != nil && o.err.Kind == types.EvalStage }

func (e *Evaluator) moduleSlots(mod types.ModuleID) *moduleSlots {
	e.slotMu.Lock()
	defer e.slotMu.Unlock()
	ms := e.slots[mod]
	if ms == nil {
		ms = &moduleSlots{m: make(map[types.ItemID]*slot)}
		e.slots[mod] = ms
	}
	return ms
}

// lockSlots takes the request module's slot lock for the rest of the tree.
func (m *Machine) lockSlots() *moduleSlots {
	if m.held == nil {
		ms := m.ev.moduleSlots(m.req.Module)
		ms.mu.Lock()
		m.held = ms
	}
	return m.held
}

func (m *Machine) slotOf(item types.ItemID) *slot {
	ms := m.lockSlots()
	s := ms.m[item]
	if s == nil {
		s = &slot{}
		ms.m[item] = s
	}
	return s
}

// lookup is the usable outcome of item at the request's stage, or nil.
func (m *Machine) lookup(item types.ItemID) *outcome {
	s := m.slotOf(item)
	if m.req.Stage == types.StageSignatures {
		return s.sig
	}
	if s.body != nil {
		return s.body
	}
	if s.sig != nil && !s.sig.isStage() {
		if !m.initializersReady(item) {
			s.body = &outcome{poison: true, steps: s.sig.steps}
			return s.body
		}
		s.body = s.sig
		return s.body
	}
	return nil
}

// initializersReady reports whether every same-module initializer item's
// stage-5 run entered, its own and nested ones, is still BodyReady.
// A slot lists only its own initializer and the consts it read directly; the
// walk follows those slots depth first, each once. It settles what it
// learns, so every walk together costs time linear in the slots, in any
// request order: a slot whose body outcome is already settled is not
// walked again (a value: its initializers were ready; anything else: they
// were not); after a walk that succeeds, every slot it reached reuses its
// sig outcome as lookup would; after one that fails, every slot on the path
// to the initializer that is not ready is poison, as its own lookup would
// make it. Body states are final when declaration evaluations run.
func (m *Machine) initializersReady(item types.ItemID) bool {
	const onPath, finished = 1, 2
	type step struct {
		x    types.ItemID
		next int
	}
	state := map[types.ItemID]uint8{}
	var path []step
	var visited []types.ItemID
	// enter visits x and reports whether it is still ready so far.
	enter := func(x types.ItemID) bool {
		if x != item {
			if s := m.held.m[x]; s != nil && s.body != nil {
				state[x] = finished
				return s.body.value != types.NoConst
			}
		}
		state[x] = onPath
		path = append(path, step{x: x})
		visited = append(visited, x)
		return m.cfg.Program.Body(x, nil).State == BodyReady
	}
	ok := enter(item)
	for ok && len(path) > 0 {
		top := &path[len(path)-1]
		var ent []types.ItemID
		if s := m.held.m[top.x]; s != nil && s.sig != nil {
			ent = s.sig.entered
		}
		if top.next == len(ent) {
			state[top.x] = finished
			path = path[:len(path)-1]
			continue
		}
		y := ent[top.next]
		top.next++
		if state[y] == 0 {
			ok = enter(y)
		}
	}
	settle := func(x types.ItemID, o func(*slot) *outcome) {
		if s := m.held.m[x]; x != item && s != nil && s.sig != nil && !s.sig.isStage() && s.body == nil {
			s.body = o(s)
		}
	}
	if !ok {
		for _, st := range path {
			settle(st.x, func(s *slot) *outcome { return &outcome{poison: true, steps: s.sig.steps} })
		}
		return false
	}
	for _, x := range visited {
		settle(x, func(s *slot) *outcome { return s.sig })
	}
	return true
}

func (m *Machine) storeSlot(item types.ItemID, o *outcome) {
	s := m.slotOf(item)
	if m.req.Stage == types.StageSignatures {
		s.sig = o
	} else {
		s.body = o
	}
}

// constFuel is a const's own fuel, never inherited.
func (m *Machine) constFuel(item types.ItemID) uint64 {
	if d, ok := m.items().Get(item).Decl.(*types.ConstDecl); ok && d.Fuel != 0 {
		return d.Fuel
	}
	if m.req.Item == item && m.req.Fuel != 0 {
		return m.req.Fuel
	}
	return types.DefaultFuel
}

// sameModuleConst reports whether item is a const or global of the request's
// module, whose value comes from a slot.
func (m *Machine) sameModuleConst(item types.ItemID) bool {
	it := m.items().Get(item)
	return item != m.cfg.TargetItem && it.Module == m.req.Module && (it.Kind == types.KindConst || it.Kind == types.KindGlobal)
}

// needSegment runs before a statement that reads const item: when item is a
// same-module const with no usable outcome, it pushes the const's segment
// (or stops on a cycle) and reports true; the statement reruns after the
// segment ends.
func (m *Machine) needSegment(item types.ItemID) bool {
	it := m.items().Get(item)
	if it.Kind == types.KindGlobal || !m.sameModuleConst(item) {
		return false
	}
	if m.lookup(item) != nil {
		return false
	}
	if i, ok := m.inProgress[item]; ok {
		m.cycle(i)
	}
	m.startConst(item, m.frames[len(m.frames)-1].curSpan())
	return true
}

// startConst pushes the segment of const item and its initializer's frame.
func (m *Machine) startConst(item types.ItemID, at source.Span) {
	m.inProgress[item] = len(m.segs)
	m.segs = append(m.segs, segment{item: item, fuel: m.constFuel(item), base: len(m.frames), entered: []types.ItemID{item}})
	m.entering, m.enteringAt = item, at
	body := m.cfg.Program.Body(item, nil)
	m.bodyState(item, &body)
	m.bodyReads(m.items().Get(item).Key, &body)
	cd, ce := m.enter(body.MIR, at)
	if cd.body.ParamCount != 0 {
		source.ICEf(at, "eval: initializer of %s has parameters", m.items().Get(item).Key)
	}
	// The value an initializer builds is the declaration's storage, not
	// recursion: its frames may hold four copies of it, and its fuel pays
	// eight steps per 16 bytes of it (what building, copying and returning
	// it cost), whatever smaller limit and fuel the evaluation has.
	if v := cd.localTI[mir.ReturnLocal].size; v != 0 {
		s := m.seg()
		s.room = satAdd(satAdd(v, v), satAdd(v, v))
		s.fuel = max(s.fuel, v/2)
	}
	m.pushFrame(cd, ce, item, body.OnerrRuns, at, 0)
	m.entering = types.NoItem
}

// endSegment runs when a segment's outermost frame returns: its local 0 is
// serialized; a nested segment's outcome goes to its slot and the reader
// continues. An EvalGen run's local 0 is void, and serializing it
// gives the interned void value: a completed run is Value != NoConst, never
// the NoConst of poison.
func (m *Machine) endSegment() {
	f := &m.frames[len(m.frames)-1]
	val := m.serialize(f.local(0))
	m.popFrame()
	s := m.segs[len(m.segs)-1]
	o := &outcome{value: val, steps: s.steps, reads: s.reads, entered: s.entered}
	if len(m.segs) == 1 {
		m.result = o
		if s.item != types.NoItem {
			m.storeSlot(s.item, o)
		}
		m.segs = m.segs[:0]
		return
	}
	m.storeSlot(s.item, o)
	delete(m.inProgress, s.item)
	m.segs = m.segs[:len(m.segs)-1]
}

// constValue is the value of const item for a reader, recording its read:
// ReadTarget for the Target item, ReadValue otherwise. A const whose
// own evaluation failed, or that is poisoned, gives poison; a stored stage
// stop stops the reader with its chain.
func (m *Machine) constValue(item types.ItemID) types.ConstID {
	it := m.items().Get(item)
	s := m.seg()
	if item == m.cfg.TargetItem && item != types.NoItem {
		for _, f := range []types.TargetField{types.TargetArch, types.TargetOS, types.TargetPointerBytes, types.TargetVectorBits} {
			s.reads.Add(types.Read{Kind: types.ReadTarget, Key: it.Key, Name: f.String(), Hash: m.cfg.Program.TargetHash(f)})
		}
		if d, ok := it.Decl.(*types.ConstDecl); ok && d.HasValue {
			return d.Value
		}
		c := m.cfg.Program.Const(item)
		if c.Poisoned || c.Value == types.NoConst {
			poison()
		}
		return c.Value
	}
	if !m.sameModuleConst(item) {
		return m.lowerConst(item)
	}
	o := m.lookup(item)
	if o == nil {
		source.ICEf(it.Span, "eval: const %s read with no outcome", it.Key)
	}
	switch {
	case o.value != types.NoConst:
		s.reads.Add(types.Read{Kind: types.ReadValue, Key: it.Key, Hash: m.cfg.Program.ConstHash(o.value)})
		s.entered = append(s.entered, item)
		return o.value
	case o.isStage():
		panic(&stop{err: &types.EvalError{Kind: types.EvalStage}, chainItems: o.err.Items, chainSpans: o.err.Trace})
	}
	poison()
	return types.NoConst
}

// lowerConst is a const of a strictly lower module: final (Program.Const).
func (m *Machine) lowerConst(item types.ItemID) types.ConstID {
	c := m.cfg.Program.Const(item)
	if c.Poisoned || c.Value == types.NoConst {
		poison()
	}
	m.seg().reads.Add(types.Read{Kind: types.ReadValue, Key: m.items().Get(item).Key, Hash: m.cfg.Program.ConstHash(c.Value)})
	return c.Value
}

// cycle stops the tree: segments [i, len) form a cycle, rotated to start at
// its least key, stored as EvalCycle for every member at both stages.
func (m *Machine) cycle(i int) {
	k := len(m.segs)
	n := k - i
	items := make([]types.ItemID, n)
	spans := make([]source.Span, n)
	for j := i; j < k; j++ {
		items[j-i] = m.segs[j].item
		last := len(m.frames) - 1
		if j+1 < k {
			last = m.segs[j+1].base - 1
		}
		spans[j-i] = m.frames[last].curSpan()
	}
	least := 0
	for j := range items {
		if m.items().Get(items[j]).Key.Compare(m.items().Get(items[least]).Key) < 0 {
			least = j
		}
	}
	items = append(items[least:], items[:least]...)
	spans = append(spans[least:], spans[:least]...)
	e := &types.EvalError{Kind: types.EvalCycle, Items: items, Trace: spans, At: spans[0]}
	for j := i; j < k; j++ {
		s := m.slotOf(m.segs[j].item)
		o := &outcome{err: e, steps: m.segs[j].steps, reads: m.segs[j].reads, entered: m.segs[j].entered}
		s.sig, s.body = o, o
	}
	panic(&stop{err: e, cycleFrom: i})
}

// abort stores the outcomes a stopped tree leaves: the failing
// segment's own failure, poison above it; a stage stop in every segment with
// its suffix chain; poison everywhere for poison. It returns the request's
// own error, or nil for poison.
func (m *Machine) abort(st *stop) *types.EvalError {
	k := len(m.segs) - 1
	if k < 0 {
		return nil
	}
	if st.poison {
		for j := 0; j <= k; j++ {
			m.storeSeg(j, &outcome{poison: true})
		}
		return nil
	}
	switch st.err.Kind {
	case types.EvalCycle:
		for j := 0; j < st.cycleFrom; j++ {
			m.storeSeg(j, &outcome{poison: true})
		}
		if st.cycleFrom == 0 {
			return st.err
		}
		return nil
	case types.EvalStage:
		// Every segment's suffix chain, in time linear in the frames.
		es := m.completeAll(st)
		for j := 0; j <= k; j++ {
			m.storeSeg(j, &outcome{err: es[j]})
		}
		return es[0]
	}
	e := m.complete(st, k)
	m.storeSeg(k, &outcome{err: e})
	for j := 0; j < k; j++ {
		m.storeSeg(j, &outcome{poison: true})
	}
	if k == 0 {
		return e
	}
	return nil
}

// storeSeg stores segment j's outcome in its const's slot, keeping its
// steps, reads and entered initializers.
func (m *Machine) storeSeg(j int, o *outcome) {
	s := &m.segs[j]
	if s.item == types.NoItem {
		return
	}
	o.steps, o.reads, o.entered = s.steps, s.reads, s.entered
	m.storeSlot(s.item, o)
}

// answer turns a const's stored outcome into the request's result: its own
// failure is Err only to its declaration evaluation, and a
// cycle only to the least-key member's.
func (m *Machine) answer(item types.ItemID, o *outcome) types.EvalResult {
	r := types.EvalResult{Steps: o.steps}
	r.Reads.Merge(&o.reads)
	switch {
	case o.value != types.NoConst:
		r.Value = o.value
	case o.poison:
	case o.isStage():
		r.Err = o.err
	default:
		s := m.slotOf(item)
		decl := m.req.Stage == types.StageBodies
		if decl && !s.reported && (o.err.Kind != types.EvalCycle || o.err.Items[0] == item) {
			s.reported = true
			r.Err = o.err
		}
	}
	return r
}

// request runs the request.
func (m *Machine) request() types.EvalResult {
	req := m.req
	it := m.items().Get(req.Item)
	switch req.What {
	case types.EvalConst:
		if it.Kind != types.KindConst && it.Kind != types.KindGlobal {
			source.ICEf(req.Site, "eval: EvalConst of %s %s", it.Kind, it.Key)
		}
		return m.constRequest(it)
	case types.EvalCall, types.EvalContract, types.EvalGen:
		if it.Kind != types.KindFunc && it.Kind != types.KindLiteral {
			source.ICEf(req.Site, "eval: call request of %s %s", it.Kind, it.Key)
		}
		return m.callRequest(it)
	}
	source.ICEf(req.Site, "eval: request kind %d", req.What)
	return types.EvalResult{}
}

func (m *Machine) constRequest(it *types.Item) types.EvalResult {
	if !m.sameModuleConst(it.ID) {
		// A lower module's const is final; the Target item is read as one.
		m.segs = append(m.segs, segment{fuel: m.constFuel(it.ID)})
		var r types.EvalResult
		if st := m.guard(func() { r.Value = m.constValue(it.ID) }); st != nil && !st.poison {
			source.ICEf(m.req.Site, "eval: reading %s stopped", it.Key)
		}
		r.Reads.Merge(&m.segs[0].reads)
		return r
	}
	if o := m.lookup(it.ID); o != nil {
		return m.answer(it.ID, o)
	}
	st := m.guard(func() { m.startConst(it.ID, m.req.Site) })
	if st == nil {
		st = m.runLoop(0)
	}
	if st != nil {
		m.abort(st)
	}
	return m.answer(it.ID, m.lookup(it.ID))
}

func (m *Machine) callRequest(it *types.Item) types.EvalResult {
	req := m.req
	fd, _ := it.Decl.(*types.FuncDecl)
	if fd == nil {
		source.ICEf(req.Site, "eval: %s has no function declaration", it.Key)
	}
	one := func(e *types.EvalError) types.EvalResult {
		e.Items, e.Trace, e.At = []types.ItemID{it.ID}, []source.Span{req.Site}, req.Site
		return types.EvalResult{Err: e}
	}
	if m.takesIo(&fd.Sig) {
		return one(&types.EvalError{Kind: types.EvalRefused, Refusal: types.RefuseIo})
	}
	// A bodiless callee enters no body, so the stage rule has nothing to
	// stop, as for a call of one inside the interpreter.
	if req.Stage == types.StageSignatures && it.Module == req.Module && !fd.Bodiless {
		return one(&types.EvalError{Kind: types.EvalStage})
	}
	fuel := req.Fuel
	if fuel == 0 {
		fuel = types.DefaultFuel
	}
	m.segs = append(m.segs, segment{fuel: fuel})
	st := m.guard(func() { m.startCall(it, fd) })
	if st == nil {
		st = m.runLoop(0)
	}
	var r types.EvalResult
	if st != nil {
		r.Err = m.abort(st)
		r.Reads.Merge(&m.segs[0].reads)
		r.Steps = m.segs[0].steps
		return r
	}
	r.Value, r.Steps = m.result.value, m.result.steps
	r.Reads.Merge(&m.result.reads)
	return r
}

// startCall builds the outermost frame of a call request and its arguments
// (a contract's predicate parameters point at the const values). A bodiless
// callee runs through a trampoline that calls it, so a request dispatches it
// exactly as a call inside the interpreter does.
func (m *Machine) startCall(it *types.Item, fd *types.FuncDecl) {
	req := m.req
	var body Body
	var cd *compiled
	var ce *centry
	if fd.Bodiless {
		body = Body{MIR: m.trampoline(it, fd), State: BodyReady}
		cd, ce = m.enterCompiled(&compiled{body: body.MIR, types: bodyTypes(m.tab(), body.MIR)}, req.Site)
	} else {
		m.entering, m.enteringAt = it.ID, req.Site
		body = m.cfg.Program.Body(it.ID, req.GenericArgs)
		m.bodyState(it.ID, &body)
		m.bodyReads(it.Key, &body)
		cd, ce = m.enter(body.MIR, req.Site)
	}
	if int(cd.body.ParamCount) != len(req.Args) {
		source.ICEf(req.Site, "eval: %d arguments for %s, which has %d parameters", len(req.Args), it.Key, cd.body.ParamCount)
	}
	f := m.pushFrame(cd, ce, it.ID, body.OnerrRuns, req.Site, 0)
	m.entering = types.NoItem
	for i, a := range req.Args {
		if a == types.NoConst {
			source.ICEf(req.Site, "eval: argument %d of %s is NoConst", i, it.Key)
		}
		dst := f.local(mir.LocalID(i + 1))
		ca := m.constAlloc(a)
		if req.What == types.EvalContract {
			// A predicate parameter is &P to the named const parameter's value,
			// read-only.
			if dst.ti.ty.Kind != types.KRef || m.cfg.Consts.Get(a).Type != dst.ti.ty.Elem {
				source.ICEf(req.Site, "eval: contract argument %d of %s does not match &P", i, it.Key)
			}
			m.store(dst, ptrWord(ca, 0))
			continue
		}
		m.copyValue(dst, loc{a: ca, ti: m.ti(m.cfg.Consts.Get(a).Type)})
	}
}

// trampoline is the body a request of bodiless it runs: its parameters, one
// direct call of it with them into local 0, and a return. The call goes
// through callFn, so the overrides, eval's intrinsics, the host's, the
// runtime-only refusals, RefuseExtern for any other extern and the ICE for an
// unknown intrinsic apply in that order. Its spans are the request's site, so
// a trace names the callee at the site. It is built per request and never
// enters the shared compiled code, which is keyed by body pointer. check folds
// only non-generic calls, so a generic or fallible bodiless request (E0301
// there) is an ICE. m.entering stays unset: a stage stop on a same-module
// parameter type would trace [type], not [callee, type]; unreachable, since
// check sends only lower callees with primitive arguments.
func (m *Machine) trampoline(it *types.Item, fd *types.FuncDecl) *mir.Body {
	req := m.req
	if len(fd.Sig.Generics) > 0 || len(req.GenericArgs) > 0 || fd.Sig.Errors != types.EmptyErrors {
		source.ICEf(req.Site, "eval: request of generic or fallible bodiless %s", it.Key)
	}
	b := mir.NewBody(it.ID, req.Site, fd.Sig.Result)
	args := make([]mir.Arg, len(fd.Sig.Params))
	for i := range fd.Sig.Params {
		p := &fd.Sig.Params[i]
		args[i] = mir.Arg{Operand: mir.Copy(b.Place(b.AddParam(p.Type, p.Name, 0)))}
	}
	call := b.AddBlock(mir.OriginBody, mir.NoDefer)
	done := b.AddBlock(mir.OriginBody, mir.NoDefer)
	b.Blocks[call].Term = mir.Terminator{Kind: mir.TermCall, Callee: mir.Callee{Kind: mir.CallDirect, Fn: mir.FnRef{Item: it.ID}},
		Args: b.AddArgs(args...), Dest: b.Place(mir.ReturnLocal), Target: done, Spawn: mir.NoLocal}
	b.Blocks[done].Term = mir.Terminator{Kind: mir.TermReturn}
	return b
}

// guard runs fn, recovering a stop.
func (m *Machine) guard(fn func()) (st *stop) {
	defer func() {
		if r := recover(); r != nil {
			s, ok := r.(*stop)
			if !ok {
				panic(r)
			}
			st = s
		}
	}()
	fn()
	return nil
}
