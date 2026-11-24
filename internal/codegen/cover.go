// Why: mk test --cover and the fuzz loop need to know which blocks of a
// package's code ran, read from a live process and mapped back to lines.
//
// What: coverBlock (one counter increment per MIR block), defineCover (a
// function's counters and descriptor row), covered and coverRowType.
//
// How: with Config.Cover every Mk function of a package module (std's are
// not counted) gets one 64-bit counter per MIR block, incremented when the
// block is entered, and one descriptor row that names the counters, the
// function's key, its file and each block's line. The driver reads them.
//
// Counters and rows live in sections of their own: on ELF mk_cnts and
// mk_cover, bounded by __start_ and __stop_ symbols; on COFF .mkcc$M and
// .mkcv$M, which a reader brackets with $B and $Z sentinels. The counters
// are kept while code references them (an inlined increment may outlive
// its function); a row is kept with its counters on ELF (!associated, as
// LLVM's profile data is) and in the counters' own comdat on COFF (an
// associative section, so an inlined function keeps its row in release). So the counters of a running process are one contiguous
// range that can be read live (the fuzz loop's guidance), and the rows map
// each counter to its line. Every counter and row is in
// @llvm.compiler.used, so the optimizer keeps it.
//
// An increment is a relaxed atomic load and store, not a read-modify-write:
// two threads may lose a count between them, but no access races, and the
// cost is a plain load, add and store. The reader, the write at exit and on
// the runner's timeout, and the bounds' sentinels live in the runtime and
// the driver (support.go, std/rt and the test runner), not here.

package codegen

import (
	"mk/internal/llvm"
	"mk/internal/types"
)

// Coverage sections per object format.
const (
	coverCountersELF  = "mk_cnts"
	coverRowsELF      = "mk_cover"
	coverCountersCOFF = ".mkcc$M"
	coverRowsCOFF     = ".mkcv$M"
)

// coverRowSize is one descriptor row's size: { ptr counters, i64 blocks,
// ptr key, i64 key length, ptr file, i64 file length, ptr lines }, the
// lines an [blocks]i32 of 1-based lines (0 for a block with no span).
const coverRowSize = 56

// coverRowType is the LLVM type of one descriptor row.
func coverRowType(m *llvm.Module) llvm.Type {
	i64, ptr := m.I(64), m.Ptr()
	return m.Struct(false, ptr, i64, ptr, i64, ptr, i64, ptr)
}

// covered reports whether fg's function is counted: Config.Cover, and a
// function of a package module, never std's.
func (fg *fnGen) covered() bool {
	g := fg.g
	if !g.cfg.Cover || fg.f.Item == types.NoItem {
		return false
	}
	mod := g.items.Module(g.items.Get(fg.f.Item).Module)
	return mod != nil && !mod.Std
}

var keyCoverCmp = types.ItemKey("std.rt", "CoverCmp")

// coverBlock increments block i's counter, defining the function's
// counters and its row on the first block.
func (fg *fnGen) coverBlock(i int) {
	if !fg.covered() {
		return
	}
	g, m := fg.g, fg.g.m
	i64 := m.I(64)
	if fg.cover.IsZero() {
		fg.cover = g.defineCover(fg)
	}
	p := fg.gep(fg.cover, uint64(i)*8)
	n := fg.cur.AtomicLoad(i64, p, llvm.Monotonic, 8)
	fg.cur.AtomicStore(fg.cur.Bin(llvm.Add, n, m.ConstInt(i64, 1), 0), p, llvm.Monotonic, 8)
}

// coverCmp emits a call to std.rt.CoverCmp(a, b, widthBytes) when live
// coverage is active and std.rt.CoverCmp is compiled in the unit.
func (fg *fnGen) coverCmp(a, b llvm.Value, widthBytes uint8) {
	if !fg.covered() || (widthBytes != 2 && widthBytes != 4 && widthBytes != 8) {
		return
	}
	g, m := fg.g, fg.g.m
	id, ok := g.items.ByKey(keyCoverCmp)
	if !ok {
		return
	}
	fi, ok := g.findFunc(id, nil)
	if !ok {
		return
	}
	ext := func(v llvm.Value) llvm.Value {
		if v.Ty.Bits() < 64 {
			return fg.cur.Cast(llvm.ZExt, v, m.I(64))
		}
		return v
	}
	fn := g.funcDecl(fi)
	ls := g.funcSigOf(fi)
	args := []value{
		{t: types.U64, kind: rScalar, v: ext(a)},
		{t: types.U64, kind: rScalar, v: ext(b)},
		{t: types.U8, kind: rScalar, v: m.ConstInt(m.I(8), uint64(widthBytes))},
	}
	fg.callLowered(fn.Ref(), fn.Type(), ls, args, nil, llvm.Attrs{}.Flag("nounwind"))
}

// defineCover defines fg's counter array and descriptor row in their
// sections, tied to fg's function, and returns the counters.
func (g *packGen) defineCover(fg *fnGen) llvm.Value {
	m := g.m
	i32, i64 := m.I(32), m.I(64)
	nb := len(fg.b.Blocks)
	ct := m.Array(uint64(nb), i64)
	ca := llvm.GlobalAttrs{Linkage: llvm.Private, Align: 8, Type: ct, Init: m.ZeroInit(ct)}
	lines := make([]llvm.Value, nb)
	file := ""
	for bi := range fg.b.Blocks {
		blk := &fg.b.Blocks[bi]
		id := blk.Term.Span
		if len(blk.Stmts) > 0 {
			id = blk.Stmts[0].Span
		}
		sp := fg.spanOf(id)
		var line uint64
		if sp.File.IsValid() {
			if file == "" {
				file = g.env.Path(sp.File)
			}
			line = uint64(g.env.LineCol(sp.StartPos()).Line)
		}
		lines[bi] = m.ConstInt(i32, line)
	}
	if fs := bodySpan(fg.f); fs.File.IsValid() {
		file = g.env.Path(fs.File)
	}
	la := m.ConstArray(i32, lines...)
	key := fg.f.Key
	ra := llvm.GlobalAttrs{Linkage: llvm.Private, Constant: true, Align: 8, Type: coverRowType(m)}
	fn := fg.fn
	// The counters are tied to nothing: code that increments them may be
	// inlined into another function and outlive this one, so they live
	// while referenced. The row lives with them on ELF (!associated to the
	// counters) and in their comdat on COFF, so a row never
	// names a dropped counter array.
	ca.Section = coverCountersELF
	if g.cfg.Target.OS == types.OSWindows {
		ca.Section = coverCountersCOFF
	}
	if g.cfg.Target.OS == types.OSWindows {
		ca.Comdat = fn.Name() + ".cov"
		ca.Linkage = llvm.Internal // a comdat key may not be private
		m.Comdat(ca.Comdat)
	}
	cnt := m.GlobalRef(m.AddGlobal(fn.Name()+".cov", ca))
	switch g.cfg.Target.OS {
	case types.OSWindows:
		// The row and its lines go in the counters' own comdat, an
		// associative section of theirs, as ELF's !associated: they live
		// while code (inlined or not) references the counters, so a
		// release build keeps the rows of functions it inlines (once
		// tied to the function's comdat, they went with it).
		ra.Section, ra.Comdat = coverRowsCOFF, ca.Comdat
	default: // ELF (Linux, freestanding, Nutshell)
		ra.Section, ra.Associated = coverRowsELF, cnt
	}
	la2 := llvm.GlobalAttrs{Linkage: llvm.Private, Constant: true, Align: 4, Type: la.Ty, Init: la, Comdat: ca.Comdat}
	lg := m.GlobalRef(m.AddGlobal(fn.Name()+".covl", la2))
	ra.Init = m.ConstStruct(false, cnt, m.ConstInt(i64, uint64(nb)), g.bytesGlobal([]byte(key)), m.ConstInt(i64, uint64(len(key))),
		g.bytesGlobal([]byte(file)), m.ConstInt(i64, uint64(len(file))), lg)
	row := m.GlobalRef(m.AddGlobal(fn.Name()+".covr", ra))
	m.Used(cnt, row)
	return cnt
}
