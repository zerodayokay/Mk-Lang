// Why: on mk build the instance-limit walk and the native stages are
// independent, so running the walk beside them takes it off a one-function
// edit's critical path.
//
// What: walkTail, pipeline.startWalk and joinWalk.
//
// How: the walk (walk.go) judges E0708 over the package's limit roots; the
// native stages collect again over the bin's roots. Neither reads what the
// other writes: both read the session's semantics (bodies decoded once,
// sync.Once per entry; mono's instance map and the hasher under their own
// mutexes; the type table sharded and locked), and the walk's only write is
// its markers in the store, whose Put is safe beside the object writes.
// So Build starts the walk on its own goroutine once the check is clean,
// and joins it before an executable is placed: a walk that reports
// anything means no executable, and the build's report is the check's with
// the walk's diagnostics, the report a synchronous walk gives (native's own
// diagnostics, which may include E0708 under other roots, are dropped).
// Check, Run and Test keep the walk synchronous.

package driver

import (
	"time"

	"mk/internal/diag"
	"mk/internal/source"
)

// walkTail is a walk started beside the native stages.
type walkTail struct {
	done chan struct{}
	ds   []*diag.Diagnostic
	took time.Duration
}

// startWalk starts the walk over b, the package build, after a clean
// check (p.diags is empty, so the walk's own snapshot of them is nil). The
// walk's prefetch is joined on the same goroutine.
func (p *pipeline) startWalk(b *build) {
	t := &walkTail{done: make(chan struct{})}
	p.tail = t
	w := p.w
	go func() {
		defer close(t.done)
		if ie := source.Guard("instance-limit walk", source.Span{}, func() {
			start := time.Now()
			t.ds = append(t.ds, w.front.joinCompute()...)
			w.front.joinWrites() // the walk keys read the stores' ifKey
			t.ds = append(t.ds, w.walk(b, nil)...)
			t.ds = append(t.ds, w.front.join()...)
			t.took = time.Since(start)
		}); ie != nil {
			t.ds = append(t.ds, diag.FromInternal(ie))
		}
	}()
}

// joinWalk waits for a started walk, once, and adds its diagnostics and
// its timing line to the pipeline. It reports whether the walk reported
// anything (on every later call too).
func (p *pipeline) joinWalk() bool {
	t := p.tail
	if t == nil {
		return p.walkBad
	}
	<-t.done
	p.tail = nil
	if p.w.cfg.Timings != nil {
		p.times = append(p.times, StageTime{Stage: "instance-limit walk", Took: t.took})
	}
	p.diags = append(p.diags, t.ds...)
	p.walkBad = len(t.ds) > 0
	return p.walkBad
}
