// Why: a fault or panic report names the function a code address is in,
// read from std.rt.FuncTable at run time, but the table must not keep
// functions alive: /OPT:REF and --gc-sections must still drop unreached
// code.
//
// What: the FuncRow kinds, funcRow and funcRowIn (one row per emitted
// function), funcRows (a pack's rows), fnSite (a Mk function's name and
// position) and the support pack's funcTable.
//
// How: one row per emitted function, a Mk function's site the position of
// its name.
//
// A row must exist exactly for the functions the linker keeps: a kept
// function without a row is reported as the function laid out before it,
// and a row that references its function keeps it. So each row is its own
// private constant in a section the linker discards with its function, the
// mechanism LLVM's own profile data uses:
//
//   - ELF: section mk_funcs with !associated !{ptr @f} (SHF_LINK_ORDER), and
//     the rows are found between __start_mk_funcs and __stop_mk_funcs;
//   - COFF: the function in its own comdat (nodeduplicate, the semantics of
//     any definition) and the row in section .mkfn$M in that comdat, an
//     associative section; the support pack's own rows are .mkfn$B and a
//     sentinel is .mkfn$Z, so the linker's grouped-section order puts every
//     pack's rows between them;
//   - Mach-O (aarch64-macos, assembled, not linked): section __DATA,__mk_funcs
//     and ld64's section$start/section$end symbols; nothing is discarded.
//
// Every row is in @llvm.compiler.used, so the optimizer (ThinLTO included)
// keeps it, but the linker does not treat it as used. FuncTable returns the
// slice between the bounds; no row is copied and the support pack names no
// pack, so its bytes do not depend on the packs (outside --pgo-gen).
// Checked with clang 21.1.8 and lld on hand-written IR: a pack defining
// used and unused functions with such rows,
// native and ThinLTO, linked with --gc-sections or /opt:ref, keeps only the
// used function and its row; without them, both.

package codegen

import (
	"strings"

	"mk/internal/llvm"
	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

// FuncRow kinds.
const (
	rowMk     = 0 // a Mk function; site is its name's position
	rowChkstk = 1 // __chkstk or mk_chkstk_65536: the caller's return address is at [sp + 16]
	rowOther  = 2 // another support routine, thunk or adapter; no site
	// rowMem is a memory function C code calls too (memcpy, memmove,
	// memset, memcmp, bcmp): no site, and the Windows fault walk
	// passes it on to its caller.
	rowMem = 3
)

// memRow reports whether support function name has a rowMem row.
func memRow(name string) bool {
	switch name {
	case symMemcpy, symMemmove, symMemset, symMemcmp, symBcmp:
		return true
	}
	return false
}

// rowSize is FuncRow's size: { ptr, ptr, i32 } padded to 8 on every target.
// Every row is one global of this size at alignment 8, so the linker puts
// them back to back with no padding.
const rowSize = 24

// Row sections and bounds per object format.
const (
	rowSectionELF   = "mk_funcs"
	rowStartELF     = "__start_mk_funcs"
	rowStopELF      = "__stop_mk_funcs"
	rowSectionCOFF  = ".mkfn$M"
	rowSupportCOFF  = ".mkfn$B"
	rowEndCOFF      = ".mkfn$Z"
	rowSectionMachO = "__DATA,__mk_funcs"
	rowStartMachO   = "\x01section$start$__DATA$__mk_funcs"
	rowStopMachO    = "\x01section$end$__DATA$__mk_funcs"
)

// rowType is the LLVM type of one FuncRow.
func rowType(m *llvm.Module) llvm.Type { return m.Struct(false, m.Ptr(), m.Ptr(), m.I(32)) }

// funcRow defines fn's row: a private constant in the row section that the
// linker discards together with fn (see the file comment).
func (g *packGen) funcRow(fn *llvm.Function, site llvm.Value, kind uint64) {
	funcRowIn(g.m, g.cfg.Target, fn, site, kind)
}

// funcRowIn is funcRow in module m for target t; the support pack uses it
// for routines no program needs to keep (support_ti.go).
func funcRowIn(m *llvm.Module, t types.Target, fn *llvm.Function, site llvm.Value, kind uint64) {
	a := llvm.GlobalAttrs{Linkage: llvm.Private, Constant: true, Align: 8, Type: rowType(m),
		Init: m.ConstStruct(false, m.FuncRef(fn), site, m.ConstInt(m.I(32), kind))}
	switch t.OS {
	case types.OSWindows:
		m.Comdat(fn.Name())
		fn.SetComdat(fn.Name())
		a.Section, a.Comdat = rowSectionCOFF, fn.Name()
	case types.OSLinux, types.OSFreestanding, types.OSNutshell: // ELF
		a.Section, a.Associated = rowSectionELF, m.FuncRef(fn)
	default:
		a.Section = rowSectionMachO
	}
	m.CompilerUsed(m.GlobalRef(m.AddGlobal("mk.fr."+fn.Name(), a)))
}

// funcRows defines the pack's rows: its members in key order, each
// with the site of its item's name (named as Config.Frames says, or with
// no site when it says ""), then its thunks and adapters in creation
// order with no site. Called after every body is emitted.
func (g *packGen) funcRows() {
	m := g.m
	for _, fi := range g.u.Packs[g.p] {
		f := &g.u.Funcs[fi]
		site := m.Null()
		if it := g.items.Get(f.Item); it != nil && it.Span.File.IsValid() {
			// The name without its module path, which the file shows
			// (hello's size budget): "Down", "str:Len".
			name := it.Key.String()
			if mod := g.items.Module(it.Module); mod != nil {
				name = strings.TrimPrefix(name, mod.Path+".")
				if mod.Std {
					// std's frames print their file only: every program
					// carries std's rows, and hello's 64 KiB budget has no
					// room for their names. The empty name is also how
					// std.rt's backtrace tells std's frames (StdIn): every
					// other Mk function's row has a name.
					name = ""
				}
			}
			frame, named := g.cfg.Frames[it.Key.String()]
			if named {
				name = frame
			}
			if !named || frame != "" {
				site = g.fnSite(it.Span, name, "mk.pos."+f.Symbol+".fn")
			}
		}
		g.funcRow(g.defs[fi], site, rowMk)
		if body, ok := g.tailDefs[fi]; ok {
			// A split member's body (tailbody.go): a backtrace names
			// both frames.
			g.funcRow(body, site, rowMk)
		}
		if body, ok := g.pnoneDefs[fi]; ok {
			// A preserve_nonecc tail-table body (pnone.go): a backtrace
			// names both frames.
			g.funcRow(body, site, rowMk)
		}
	}
	for _, fn := range g.extraFns {
		g.funcRow(fn, m.Null(), rowOther)
	}
}

// fnSite is a Mk function's row site: a Site (file, line, column of its
// name), then the length (i32, at 24) and bytes (at 28) of the function's name as its item key spells it
// ("main.Down", "std.core.str:Len"), std.rt's FnSite, which the panic
// report's backtrace reads. It is its own
// constant, never shared with a check's site, so the name always follows
// it; with -fdata-sections it is dropped with the
// row when the linker drops the function.
func (g *packGen) fnSite(sp source.Span, name, sym string) llvm.Value {
	m := g.m
	path := g.env.Path(sp.File)
	lc := g.env.LineCol(source.Pos{File: sp.File, Off: sp.Start})
	init := m.ConstStruct(false, g.bytesGlobal([]byte(path)), m.ConstInt(m.I(64), uint64(len(path))),
		m.ConstInt(m.I(32), uint64(lc.Line)), m.ConstInt(m.I(32), uint64(lc.Col)),
		m.ConstInt(m.I(32), uint64(len(name))), m.Bytes([]byte(name)))
	return m.GlobalRef(m.AddGlobal(sym, llvm.GlobalAttrs{Linkage: llvm.Private,
		Constant: true, UnnamedAddr: true, Align: 8, Type: init.Ty, Init: init}))
}

// funcTable is the support pack's std.rt.FuncTable(): []FuncRow: the rows
// between the row section's bounds. Its own rows (the support routines in
// module assembly and IR, its intrinsic bodies included; __chkstk and the
// aligned probe kind 1, the memory functions kind 3, the others kind 2)
// are one array in that section, which FuncTable
// references, so the linker keeps it: on COFF it is the first contribution
// (.mkfn$B) and the slice starts there; on ELF it is anywhere between the
// bounds. It is emitted after every other support function, so its rows
// name them all.
func (s *support) funcTable(g *packGen, sig *types.Signature) {
	m := g.m
	i64 := m.I(64)
	fg := s.defineMk(g, sig, "mk.std.rt.FuncTable")
	defer fg.done()
	rowT := rowType(m)
	var rows []llvm.Value
	for _, n := range s.asmNames {
		// The IR name: LLVM adds Mach-O's '_' itself (asmName is the assembler's).
		f := m.Declare(n, llvm.FuncSig{Linkage: llvm.External, Ret: m.Void()})
		kind := uint64(rowOther)
		if n == symChkstk || n == symChkstkAligned {
			kind = rowChkstk // the aligned probe pushes as __chkstk does
		}
		rows = append(rows, m.ConstStruct(false, m.FuncRef(f), m.Null(), m.ConstInt(m.I(32), kind)))
	}
	for _, f := range s.defs {
		kind := uint64(rowOther)
		if memRow(f.Name()) {
			kind = rowMem
		}
		rows = append(rows, m.ConstStruct(false, m.FuncRef(f), m.Null(), m.ConstInt(m.I(32), kind)))
	}
	own := m.ConstArray(rowT, rows...)
	oa := llvm.GlobalAttrs{Linkage: llvm.Private, Constant: true, Align: 8, Type: own.Ty, Init: own}
	var start, stop llvm.Value
	bound := func(name string) llvm.Value {
		vis := llvm.Hidden
		if s.t.OS == types.OSWindows {
			vis = llvm.Default
		}
		return m.GlobalRef(m.AddGlobal(name, llvm.GlobalAttrs{Linkage: llvm.External, Visibility: vis, DSOLocal: true,
			Align: 8, Type: i64}))
	}
	switch s.t.OS {
	case types.OSWindows:
		oa.Section = rowSupportCOFF
		end := m.AddGlobal("mk.support.funcend", llvm.GlobalAttrs{Linkage: llvm.Private, Constant: true, Align: 8,
			Type: i64, Init: m.ConstInt(i64, 0), Section: rowEndCOFF})
		start = m.GlobalRef(m.AddGlobal("mk.support.funcs", oa))
		stop = m.GlobalRef(end)
	case types.OSLinux, types.OSFreestanding, types.OSNutshell: // ELF
		oa.Section = rowSectionELF
		m.Used(m.GlobalRef(m.AddGlobal("mk.support.funcs", oa)))
		start, stop = bound(rowStartELF), bound(rowStopELF)
	default:
		oa.Section = rowSectionMachO
		m.Used(m.GlobalRef(m.AddGlobal("mk.support.funcs", oa)))
		start, stop = bound(rowStartMachO), bound(rowStopMachO)
	}
	a := fg.cur.Cast(llvm.PtrToInt, start, i64)
	b := fg.cur.Cast(llvm.PtrToInt, stop, i64)
	n := fg.cur.Bin(llvm.UDiv, fg.cur.Bin(llvm.Sub, b, a, 0), m.ConstInt(i64, rowSize), 0)
	out := fg.locals[mir.ReturnLocal]
	fg.cur.Store(start, out.v, 8)
	fg.cur.Store(n, fg.gep(out.v, 8), 8)
	fg.ret()
}
