// Why: moves and loans are decided by one forward dataflow over the body's
// blocks; this file is its driver and holds the checker's whole per-body
// state.
//
// What: checker (every table and cache of one check), newChecker and run,
// order (reverse postorder), solve (the fixpoint), propagate, replay,
// joinPartly, transferBlock and stmtCtx.
//
// How: run does setup, liveness, the forward fixpoint with transfers only,
// then one replay of every reachable block from its final entry state with
// every check on, then the body-level checks and ordering. Joins at block
// entries turn T into M where two or more edges meet; onerr guards are
// decided in both passes.

package borrow

import (
	"mk/internal/diag"
	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

// checker is the state of one Check: everything per body, discarded when
// Check returns.
type checker struct {
	body *mir.Body
	env  *Env
	tab  *types.Table
	w    *walker
	sets *setStore

	paths     *pathTable
	roots     []rootInfo
	loans     []loan
	params    []paramInfo
	siteLoans map[siteKey]loanID
	slots     map[siteKey]slotInfo
	statics   map[types.ItemID]staticNode
	// global is the global-memory node (globalNode), root noRoot until
	// setup makes it.
	global staticNode
	tags   map[tagKey]loanID

	rpo     []mir.BlockID
	rpoNum  []int32
	preds   []uint32
	entries []*state
	live    *liveness

	// report is set during the replay: checks run and diagnostics are
	// collected.
	report bool
	diags  []*diag.Diagnostic
	// reported holds the move sites some E0401 already named.
	reported setID
	// partly holds the roots already given a join E0402.
	partly map[rootID]bool
	// boundaryFail holds the spans of _0 statements whose boundary check
	// failed (such a check reports alone).
	boundaryFail []mir.SpanID
	// staticStore marks the E0604s of a static of global memory stored into
	// a parameter's node (storeAbstract): no clause fixes one, so finish
	// lets it win over a clause E0604 at the same span and drops the E0605
	// that follows from it there.
	staticStore map[*diag.Diagnostic]bool
	// lastDiscr maps, within the current block, a discriminant's local to
	// the place it reads (for variant narrowing).
	lastDiscr map[mir.LocalID]mir.Place
	// argSlots are the argument slots (slots.go).
	argSlots map[mir.LocalID]argSlot
	// tail is set while call transfers a TermTailCall (tailcall.go): its
	// arguments may hold no loan on the frame.
	tail bool

	// Caches and scratch of the loan pass.
	byRoot map[rootID][]loanID
	// holders is the body-wide holders index: holders[x] lists
	// every root that held a loan rooted at x in some state.
	holders [][]rootID
	// holderPos is each root's position in holders[x], keyed x<<32|r.
	holderPos map[uint64]int32
	// curSpan is the span of the statement or terminator being transferred;
	// storedAt records, during the replay, the statement at which a loan
	// first entered a root's held set (key root<<32 | loan): E0505 points
	// there.
	curSpan  mir.SpanID
	storedAt map[uint64]mir.SpanID
	// via is, during the replay, how the last writable walk reached each
	// root it found on a later hop: the root that held the loan, and the
	// loan. E0604 and E0505 name it (DF-lox-06, DF-mgrep-02).
	via map[rootID]viaHop
	// capField is, for a body with a hidden environment parameter, the
	// environment field index through which the last writable walk or direct
	// write reached a hidden parameter's abstract root, or -1 when unknown or
	// more than one field.
	capField map[rootID]int
	// laterAlloc are the allocator roots the last writable walk reached on
	// a later hop: the call rule stores into them through
	// allocSelf.
	laterAlloc map[rootID]bool
	// allocMem maps each root the last writable walk reached only through
	// an allocator's own memory hop to that allocator (memHop): the
	// call rule stores into it through memSelf.
	allocMem map[rootID]rootID
	// memParams is set when some parameter is a memNode (setup): only then
	// does the writable walk track allocMem.
	memParams bool
	// csrConst is set while the self-reach rule's inferred stores walk and
	// store (storeArg): an abstract node then reads as {p.inner} and records
	// nothing. The stores it infers stand for a write the callee may make; the
	// callee's own body is judged for that write, through what its nodes
	// record.
	csrConst bool
	// callStored is, per root, what the current call's steps 3 and 4 have
	// stored into it (noteCallStored); a later fixpoint round skips a loan
	// of the set it stores only where the call put it (skipHop).
	callStored map[rootID]setID
	// shIn is the shallow marks of the set the current store adds
	// (marks.go): the loans of it that a call's steps 3 and 4 store from a value
	// of a shallow type. It is empty outside storeArg, so every other store
	// is deep.
	shIn setID
	// absHolders indexes, per abstract node, the roots that held an
	// abstract loan targeting it (indexAbsHolder); absTargets caches
	// absTargeting while len(loans) is absTargetsAt.
	absHolders    map[rootID][]rootID
	absHolderSeen map[uint64]struct{}
	absTargets    map[rootID]setID
	absTargetsAt  int
	// allocStored is, per concrete allocator root, every loan stored into
	// its contents in the body, grow-only; allocGrew marks a
	// growth since solve's last round.
	allocStored map[rootID]setID
	allocGrew   bool
	// taskFlows are the tasks' clauses `*q into p`, one per site and
	// clause (taskFlowAt), grow-only for the body (taskflow.go);
	// taskFlowGrew marks a growth since solve's last round. flowVia is the
	// flow being applied, which E0505 and E0604 name; flowDepth is set
	// while the flows are applied; flowSeen is, per root, the loans the
	// report pass judged there through a flow; taskGrew is set when an
	// applied flow grows a root, for the call rule's fixpoint.
	taskFlows    []taskFlow
	taskFlowAt   map[siteKey]int
	taskFlowGrew bool
	taskGrew     bool
	flowVia      *taskFlow
	flowDepth    int
	// flowCall is the block of the call whose steps 3 and 4 run, plus
	// one, else 0.
	flowCall mir.BlockID
	flowSeen map[rootID]setID
	// flowAt indexes, per root, the flows whose region holds it
	// (flowAtSeen: root<<32 | flow); walkRead, when set, collects the
	// roots the writable walk reads.
	flowAt     map[rootID][]int
	flowAtSeen map[uint64]bool
	walkRead   map[rootID]bool
	// allocGen counts the growths of allocStored, which the cached walks
	// of task flows depend on.
	allocGen int
	// loanHolders is the same index per loan: loanHolders[l] lists every
	// root that held l in some state, in holders[root(l)]'s order, so the
	// seeds of liveHolder are those of a scan of holders[root(l)]
	// without visiting the roots that held only other loans on root(l).
	loanHolders    [][]rootID
	loanHolderSeen map[uint64]struct{}
	// fullScan makes access visit every candidate, unfiltered, and keep
	// the live one with the least loan ID, the reference of
	// TestAccessWinnerEqualsFullScan for the lazy walk that stops at the
	// first. Nothing else sets it.
	fullScan     bool
	kills        map[pathID]setID
	rooted       map[rootID]setID
	cands        map[candKey][]loanID
	scratchRoots []rootID
	// onMin and pruneMax are onFilterMin and onPruneMax; tests lower them
	// to exercise the filter and the superset path on small bodies.
	onMin, pruneMax int
	// noOnFilter turns the on sets off: put does not maintain them and
	// access visits every cached candidate (test only: the reference of
	// TestOnFilterEqualsUnfiltered, and the A/B of the filter's cost).
	noOnFilter bool
	targets    map[rootID]setID
	rootMark   []uint32
	epoch      uint32
	loopSets   [][]bool
	endsForm   map[siteKey]rootID
	// liveVia is liveHolder's scratch: per split holder, the loans it was
	// reached through.
	liveVia map[rootID]setID
	// bindStart is where each user local is first bound; release caches
	// each type item's consuming methods (fixes.go).
	bindStart map[rootID]uint32
	release   map[types.ItemID][]consumer
	// whole checks the body with no field sets (call.go remnantReach):
	// every root keeps one set. remnant is set when the first run meets a
	// call that may reach the remnant; anySplit when some root may keep
	// field sets at all.
	whole, remnant, anySplit bool

	// Report results collected during the replay.
	guards    []OnerrRun
	e0411     map[uint32]source.Span
	e0411Root map[uint32]rootID

	res *Result
}

func newChecker(body *mir.Body, env *Env) *checker {
	res := &Result{}
	c := &checker{
		body:           body,
		env:            env,
		tab:            env.Types,
		sets:           newSetStore(),
		paths:          newPathTable(),
		siteLoans:      make(map[siteKey]loanID),
		slots:          make(map[siteKey]slotInfo),
		statics:        make(map[types.ItemID]staticNode),
		global:         staticNode{root: noRoot, loan: noLoan},
		tags:           make(map[tagKey]loanID),
		partly:         make(map[rootID]bool),
		lastDiscr:      make(map[mir.LocalID]mir.Place),
		kills:          make(map[pathID]setID),
		holderPos:      make(map[uint64]int32),
		loanHolderSeen: make(map[uint64]struct{}),
		rooted:         make(map[rootID]setID),
		targets:        make(map[rootID]setID),
		cands:          make(map[candKey][]loanID),
		endsForm:       make(map[siteKey]rootID),
		liveVia:        make(map[rootID]setID),
		bindStart:      make(map[rootID]uint32),
		release:        make(map[types.ItemID][]consumer),
		e0411:          make(map[uint32]source.Span),
		e0411Root:      make(map[uint32]rootID),
		res:            res,
		onMin:          onFilterMin,
		pruneMax:       onPruneMax,
	}
	c.w = newWalker(env, &res.Reads)
	return c
}

func (c *checker) run() *Result {
	c.setup()
	c.order()
	c.live = c.computeLiveness()
	c.findArgSlots()
	c.solve()
	c.report = true
	c.replay()
	c.unhandledUnions()
	c.finish()
	return c.res
}

// order computes a reverse postorder of the blocks reachable from the entry,
// and the CFG predecessor counts.
func (c *checker) order() {
	n := len(c.body.Blocks)
	c.preds = make([]uint32, n)
	c.rpoNum = make([]int32, n)
	for i := range c.rpoNum {
		c.rpoNum[i] = -1
	}
	if n == 0 {
		return
	}
	seen := make([]bool, n)
	post := make([]mir.BlockID, 0, n)
	// Iterative DFS with an explicit stack of (block, next successor).
	type frame struct {
		b    mir.BlockID
		succ []mir.BlockID
		i    int
	}
	var stack []frame
	push := func(b mir.BlockID) {
		seen[b] = true
		stack = append(stack, frame{b: b, succ: c.body.Successors(b, nil)})
	}
	push(mir.EntryBlock)
	for len(stack) > 0 {
		f := &stack[len(stack)-1]
		if f.i < len(f.succ) {
			s := f.succ[f.i]
			f.i++
			if int(s) < n && !seen[s] {
				push(s)
			}
			continue
		}
		post = append(post, f.b)
		stack = stack[:len(stack)-1]
	}
	c.rpo = make([]mir.BlockID, len(post))
	for i, b := range post {
		c.rpo[len(post)-1-i] = b
	}
	var buf []mir.BlockID
	for i, b := range c.rpo {
		c.rpoNum[b] = int32(i)
		buf = c.body.Successors(b, buf[:0])
		for _, s := range buf {
			c.preds[s]++
		}
	}
}

// out is one out-edge of a block transfer.
type out struct {
	to mir.BlockID
	st *state
}

// solve is the forward fixpoint: transfers only, in reverse
// postorder rounds until no entry changes. It terminates because every
// transfer is monotone over a finite universe.
func (c *checker) solve() {
	c.entries = make([]*state, len(c.body.Blocks))
	if len(c.rpo) == 0 {
		return
	}
	c.entries[mir.EntryBlock], _ = joinInto(c.sets, nil, c.entryState())
	dirty := make([]bool, len(c.body.Blocks))
	dirty[mir.EntryBlock] = true
	for changed := true; changed; {
		changed = false
		for _, b := range c.rpo {
			if !dirty[b] {
				continue
			}
			dirty[b] = false
			for _, o := range c.transferBlock(b, c.entries[b]) {
				if c.propagate(o) {
					dirty[o.to] = true
					changed = true
				}
			}
		}
		// The writable walk reads allocStored, and every store reads
		// taskFlows: when either grew, every block reached so far runs
		// again.
		if c.allocGrew || c.taskFlowGrew {
			c.allocGrew, c.taskFlowGrew = false, false
			for _, b := range c.rpo {
				if c.entries[b] != nil {
					dirty[b] = true
				}
			}
			changed = true
		}
	}
}

// propagate joins an out-state into its successor's entry: at a block
// with two or more predecessors T becomes M in the incoming state first.
func (c *checker) propagate(o out) bool {
	in := o.st
	if c.preds[o.to] >= 2 {
		in.takenToMoved()
	}
	ne, changed := joinInto(c.sets, c.entries[o.to], in)
	if changed {
		c.entries[o.to] = ne
	}
	return changed
}

// replay is the report pass: every reachable block once, in reverse postorder,
// from its final entry state, with every check on. Out-states are dropped.
func (c *checker) replay() {
	for _, b := range c.rpo {
		e := c.entries[b]
		if e == nil {
			continue
		}
		c.joinPartly(b, e)
		c.transferBlock(b, e)
	}
}

// joinPartly reports E0402 at a block entry for a linear local whose init
// has more than one element (it is initialized on some paths only), once
// per root.
func (c *checker) joinPartly(b mir.BlockID, st *state) {
	if c.preds[b] < 2 {
		return
	}
	for r := range c.body.Locals {
		ri := &c.roots[r]
		if !ri.linear || c.partly[rootID(r)] {
			continue
		}
		in := initOf(st.get(rootID(r)))
		if in&(in-1) == 0 {
			continue // one element
		}
		if ri.kind == rkTemp && in == initU|initM {
			// A call's destination temporary moved on its success edge
			// and never assigned on its error edge (a value `else`, a
			// `match` on a call): it holds no value on either, so nothing
			// is left unconsumed. A user local keeps the rule ({U, M} is
			// E0402).
			continue
		}
		c.partly[rootID(r)] = true
		d := c.errAt(diag.E0402, c.body.Locals[r].Span, "%s is not consumed on every path", c.rootName(rootID(r))).
			Text("place", c.rootName(rootID(r))).Loc("path", c.body.Span(c.blockSpan(b)))
		c.releaseFacts(d, rootID(r), ri.typ)
	}
}

// transferBlock runs one block's statements and terminator from entry state
// in, returning the out-state of each successor edge.
func (c *checker) transferBlock(b mir.BlockID, in *state) []out {
	st := in.clone()
	blk := &c.body.Blocks[b]
	clear(c.lastDiscr)
	for i := range blk.Stmts {
		ctx := stmtCtx{b: b, i: i, span: blk.Stmts[i].Span}
		c.curSpan = ctx.span
		c.statement(st, &ctx, &blk.Stmts[i])
	}
	ctx := stmtCtx{b: b, i: len(blk.Stmts), span: blk.Term.Span, term: true}
	c.curSpan = ctx.span
	return c.terminator(st, &ctx, &blk.Term)
}

// stmtCtx is where a transfer runs: the block, the statement index (the
// terminator is index len(Stmts)) and its span.
type stmtCtx struct {
	b    mir.BlockID
	i    int
	span mir.SpanID
	term bool
	// slots are the argument slots formed so far at this call.
	slots setID
	// exempt is the ends exemption for the access being checked.
	exempt func(loanID) bool
	// bad409 is set when an operand of the statement reported E0409 (in
	// both passes, so the transfer is the same): the error hygiene of
	// statement.
	bad409 bool
}

// blockSpan is the span of block b's first statement, or its terminator's.
func (c *checker) blockSpan(b mir.BlockID) mir.SpanID {
	blk := &c.body.Blocks[b]
	if len(blk.Stmts) > 0 {
		return blk.Stmts[0].Span
	}
	return blk.Term.Span
}

// site is the statement's move-site number: dense over the body, in block
// then statement order.
func (c *checker) site(ctx *stmtCtx) uint32 { return c.live.base[ctx.b] + uint32(ctx.i) }

// origin is the block's cleanup origin.
func (c *checker) origin(ctx *stmtCtx) mir.BlockOrigin { return c.body.Blocks[ctx.b].Origin }
