// Why: a module hit's importers need its interface, not its bodies, so
// decoding every function result of a hit up front delays every module
// above it. Most of std.core's lookup went to decoding its function
// results.
//
// What: lazy body entries for the function results a module hit names
// (lazyBody, bodyEntry, lazyEntry), their background prefetch (prefetch,
// prefetchWalked), and the frontCache's background tasks and their join.
//
// How: each result is decoded on first use, or by the prefetch of the
// instance-limit walk's modules, never on the module's own worker; the
// bytes are dropped once decoded. Background tasks have their own bound, so
// they never take a worker slot, and join waits for all of them before the
// stage that started them ends.

package driver

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"

	"mk/internal/diag"
	"mk/internal/eval"
	"mk/internal/iface"
	"mk/internal/source"
	"mk/internal/types"
)

// lazyBody decodes one function result into its bodyEntry, once.
type lazyBody struct {
	once   sync.Once
	done   atomic.Bool
	decode func()
	// err is a decode failure: a payload that passed its frame and does not
	// decode is a compiler bug, an ICE where the body is used and an E0000
	// of the build at the join (frontCache.join).
	err error
}

// force fills e from its function result if it is lazy. Safe for
// concurrent use; every caller returns after the one decode. A failed
// decode is an ICE of the caller.
func (e *bodyEntry) force() {
	if e == nil || e.lazy == nil {
		return
	}
	e.lazy.once.Do(e.lazy.decode)
	if e.lazy.err != nil {
		source.ICEf(source.Span{}, "%v", e.lazy.err)
	}
}

// forced is e after force.
func (e *bodyEntry) forced() *bodyEntry {
	e.force()
	return e
}

// lazyEntry is the body entry of the function result b of item, a result
// of a module hit of path. It decodes into fc.env, like the eager decode
// it replaces; the bytes are dropped once decoded.
func (fc *frontCache) lazyEntry(path string, item types.Key, b []byte) *bodyEntry {
	e := &bodyEntry{}
	lz := &lazyBody{}
	lz.decode = func() {
		defer func() {
			b = nil
			lz.done.Store(true)
		}()
		var r *iface.FuncResult
		var err error
		if ie := source.Guard("decode "+item.String(), source.Span{}, func() { r, err = iface.DecodeFuncResult(fc.env, b) }); ie != nil {
			err = fmt.Errorf("%s", ie.What)
		}
		if err == nil && r.Item != item {
			err = fmt.Errorf("it names %s", r.Item)
		}
		var fl *bodyEntry
		if err == nil {
			fl, err = decodeFlow(r.Borrow)
		}
		if err != nil {
			lz.err = fmt.Errorf("driver: function result %s of a module hit of %s: %v", item, path, err)
			e.state = eval.BodyPoisoned
			return
		}
		e.mir, e.state, e.onerr, e.flowed, e.bodyHash, e.hasBodyHash = r.Body, fl.state, fl.onerr, fl.flowed, fl.bodyHash, fl.hasBodyHash
	}
	e.lazy = lz
	return e
}

// prefetchChunk is how many results one prefetch task decodes.
const prefetchChunk = 32

// prefetch decodes es in the background, at most runtime.NumCPU() tasks at
// a time (its own bound, not Config.Jobs: a decode takes no worker slot, so
// Jobs 1 cannot deadlock on it, it only runs beside the stage). join waits
// for every task, so none outlives the stage that started it.
func (fc *frontCache) prefetch(es []*bodyEntry) {
	fc.preOnce.Do(func() { fc.preSem = make(chan struct{}, runtime.NumCPU()) })
	for len(es) > 0 {
		n := min(prefetchChunk, len(es))
		chunk := es[:n]
		es = es[n:]
		fc.preWG.Add(1)
		go func() {
			defer fc.preWG.Done()
			if ie := source.Guard("prefetch", source.Span{}, func() {
				fc.preSem <- struct{}{}
				defer func() { <-fc.preSem }()
				for _, e := range chunk {
					e.lazy.once.Do(e.lazy.decode)
					if err := e.lazy.err; err != nil {
						fc.noteBackground(source.Guard("load", source.Span{}, func() { source.ICEf(source.Span{}, "%v", err) }))
					}
				}
			}); ie != nil {
				fc.noteBackground(ie)
			}
		}()
	}
}

// prefetchWalked prefetches the function results of the module hits among
// mods, the modules whose roots the instance-limit walk lists: it reads
// every body of those modules, one at a time, so they are decoded in
// parallel first. A hit's results are not prefetched at lookup: an edit
// build walks only the edited module and its importers (walkcache.go), and
// decoding every hit's results then doubled the heap and slowed the check
// beside it. fc may be nil.
func (fc *frontCache) prefetchWalked(mods []*modNode) {
	if fc == nil {
		return
	}
	for _, m := range mods {
		if m.hit != nil {
			fc.prefetch(m.hit.lazy)
		}
	}
}

// background runs f beside the stage, under source.Guard; join waits for
// it. A fault is an E0000 of the build.
func (fc *frontCache) background(where string, f func()) {
	fc.preWG.Add(1)
	go func() {
		defer fc.preWG.Done()
		if ie := source.Guard(where, source.Span{}, f); ie != nil {
			fc.noteBackground(ie)
		}
	}()
}

// writes runs f, a store's file writes: inline, or with deferWrites on
// its own goroutine under writeWG, which joinWrites and join wait for. A
// fault is an E0000 of the build, as background's.
func (fc *frontCache) writes(where string, f func()) {
	if !fc.deferWrites {
		f()
		return
	}
	fc.writeWG.Add(1)
	go func() {
		defer fc.writeWG.Done()
		if ie := source.Guard(where, source.Span{}, f); ie != nil {
			fc.noteBackground(ie)
		}
	}()
}

// joinWrites waits for the deferred store writes; their faults stay for
// join. fc may be nil.
func (fc *frontCache) joinWrites() {
	if fc != nil {
		fc.writeWG.Wait()
	}
}

// joinCompute is join without the deferred store writes: it waits for the
// prefetches and the stores' encodings, and returns the faults so far.
func (fc *frontCache) joinCompute() []*diag.Diagnostic {
	if fc == nil {
		return nil
	}
	fc.preWG.Wait()
	return fc.takeDiags()
}

// takeDiags returns the background faults recorded so far, once.
func (fc *frontCache) takeDiags() []*diag.Diagnostic {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	ds := fc.bgDiags
	fc.bgDiags = nil
	return ds
}

// noteBackground records a background task's fault, if any.
func (fc *frontCache) noteBackground(ie *source.InternalError) {
	if ie == nil {
		return
	}
	fc.mu.Lock()
	fc.bgDiags = append(fc.bgDiags, diag.FromInternal(ie))
	fc.mu.Unlock()
}

// join waits for every background task (prefetches, stores and their
// writes) and returns their faults, once. The report sorts them.
func (fc *frontCache) join() []*diag.Diagnostic {
	if fc == nil {
		return nil
	}
	fc.preWG.Wait()
	fc.writeWG.Wait()
	return fc.takeDiags()
}

// prefetchBinBodies decodes unforced lazy cached bodies of non-std modules
// reachable from j's root modules in parallel across w.parallel workers before
// mono.Collect walks them sequentially.
func (w *world) prefetchBinBodies(j nativeJob) {
	if w.front == nil || len(j.pkg) == 0 {
		return
	}
	seen := make(map[*modNode]bool, len(j.pkg))
	var stack []*modNode
	push := func(m *modNode) {
		if m != nil && !m.std && !seen[m] {
			seen[m] = true
			stack = append(stack, m)
		}
	}
	for _, m := range j.pkg {
		if j.allFuncs || m.path == j.mainMod {
			push(m)
			continue
		}
		if m.mod == nil {
			continue
		}
		for _, id := range m.mod.Items {
			if fd, ok := w.items.Get(id).Decl.(*types.FuncDecl); ok && (fd.Sig.Extern && w.items.Get(id).Exported || fd.Section != "") {
				push(m)
				break
			}
		}
	}
	var todo []*bodyEntry
	for len(stack) > 0 {
		m := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if m.hit != nil {
			for _, e := range m.hit.lazy {
				if e != nil && e.lazy != nil && !e.lazy.done.Load() {
					todo = append(todo, e)
				}
			}
		}
		for _, d := range m.deps {
			push(d)
		}
	}
	if len(todo) == 0 {
		return
	}
	chunks := (len(todo) + prefetchChunk - 1) / prefetchChunk
	if chunks <= 1 {
		for _, e := range todo {
			e.lazy.once.Do(e.lazy.decode)
		}
		return
	}
	w.parallel(chunks, func(int) string { return "prefetch bodies" }, func(c int) {
		lo := c * prefetchChunk
		hi := min(lo+prefetchChunk, len(todo))
		for _, e := range todo[lo:hi] {
			e.lazy.once.Do(e.lazy.decode)
		}
	})
}

// canCacheBodyHash reports whether e's lowered MIR body hash depends solely on
// e.mir's own fields, item keys, and immutable types/consts (no generics,
// fn literals, or relocated consts), so it can be cached in FuncResult's flow
// payload and reused by InstanceContentHash on warm builds.
func canCacheBodyHash(w *world, it *types.Item, e *bodyEntry) bool {
	if e == nil || e.state != eval.BodyReady || e.mir == nil || e.mir.GenericCount != 0 || it.Kind != types.KindFunc {
		return false
	}
	if fd, ok := it.Decl.(*types.FuncDecl); !ok || len(fd.Sig.Generics) > 0 {
		return false
	}
	b := e.mir
	items := w.items
	consts := w.tab.Consts()
	hasReloc := func(c types.ConstID) bool {
		return c != types.NoConst && len(consts.Get(c).Relocs) > 0
	}
	isLit := func(id types.ItemID) bool {
		return id != types.NoItem && items.Get(id).Kind == types.KindLiteral
	}
	for i := range b.GenericArgs {
		a := &b.GenericArgs[i]
		if a.IsConst && a.Const.Kind == types.CAValue && hasReloc(a.Const.Value) {
			return false
		}
	}
	for i := range b.Operands {
		if hasReloc(b.Operands[i].Const) {
			return false
		}
	}
	for i := range b.Args {
		if hasReloc(b.Args[i].Operand.Const) {
			return false
		}
	}
	for i := range b.Blocks {
		bl := &b.Blocks[i]
		for j := range bl.Stmts {
			rv := &bl.Stmts[j].Rvalue
			if hasReloc(rv.A.Const) || hasReloc(rv.B.Const) || isLit(rv.Fn.Item) {
				return false
			}
		}
		t := &bl.Term
		if hasReloc(t.On.Const) || hasReloc(t.Msg.Const) || hasReloc(t.Callee.Value.Const) || isLit(t.Callee.Fn.Item) {
			return false
		}
	}
	return true
}
