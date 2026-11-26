// Why: a slice parameter's data pointer gets noalias only when the ABI
// passes it as an LLVM ptr parameter. Where the ABI passes the slice as a
// pointer to a copy of its header (Win64 C ABI, System V byval) or as an
// LLVM array (AArch64 [2 x i64]), LLVM cannot attach noalias to the data
// pointer, and without alias facts it keeps runtime alias tests in hot
// loops.
//
// What: scoped alias metadata (!alias.scope, !noalias and
// llvm.experimental.noalias.scope.decl) for slice parameters whose data
// pointer does not travel as an LLVM ptr parameter: aliasCandidate,
// headerHeld and the per-access tagging.
//
// How: one distinct domain per function and one scope per candidate
// parameter, declared in the entry block; each element load or store through
// a parameter carries its own scope and !noalias of the scopes it is
// disjoint from.
//
// A Direct x86_64 slice parameter whose parts travel separately (System V in
// registers, and Win64 under abi.LowerInternal) passes its data pointer as
// an LLVM ptr parameter, so it gets noalias (a mut []T, a str, a []T of a
// Freeze T). Where the ABI passes the slice another way, the data pointer is
// loaded from memory or from a local copy of an aggregate, which no
// parameter attribute reaches: the Win64 C ABI Indirect slice (a pointer to
// the caller's 16-byte copy), the AArch64 Direct [2 x i64] array
// (aarch64-linux and aarch64-macos), and a System V byval slice when integer
// registers are exhausted. For those parameters this file states the same
// fact as LLVM's inliner states a noalias argument: one distinct domain per
// function, one distinct scope per such parameter, an
// llvm.experimental.noalias.scope.decl for each in the entry block, and on
// each scalar load or store of an element through the parameter,
// !alias.scope of its own scope and !noalias of the scopes it is disjoint
// from. This removes LLVM's runtime alias tests in hot loops such as matmul
// and axpy kernels.
//
// The obligation, at each tagged access. An access tagged !alias.scope S_p
// addresses an element of parameter p's slice: its address is p's data
// pointer, loaded from p's own unchanged header, plus an index, and then
// only field offsets, variant selections and array-element offsets
// (aliasAddr). An access tagged
// !noalias S_q is such an access of a parameter p != q, with p or q a mut
// []T. Both slices are arguments of this call, so the caller's loans on
// both last for the whole call; an exclusive loan excludes every other
// access to its elements while it lasts, so no element of p's slice is an
// element of q's, and the two accesses never touch one byte. Two shared
// slices are never paired: their elements may be one memory, read through
// both, and a []T of a non-Freeze T is never tagged at all.
//
// The rule this relies on. A slice whose elements overlap another
// argument's, built from raw pointers by unsafe code or passed by C calling
// the function as a callback, breaks the exclusive loan as a judged
// reference would. The language reference says so: a reference that a raw
// pointer or C made obeys the loan rules a judged one would, so two
// references to a common byte in one call, one of them mut, are undefined
// behaviour however they were made. (The narrower rule, that a raw write
// under a live loan is undefined, would not be enough.) System V's noalias
// on the same parameter, and noalias on a mut T parameter on every target,
// rest on the same rule. The scopes stay per call only
// because LLVM clones them for each inlined call and each unrolled copy,
// and LoopAccessAnalysis drops a scope declared inside the loop: checked
// with clang 21.1.8 only. tests/programs/aliaschain
// prints other sums in release when the declarations are missing.
//
// p's header is unchanged when p is not var (only a var parameter
// is assigned or borrowed mut; checkImmutParams is the backstop for the
// Indirect copy), the body has no inline assembly, and it never borrows p's
// whole value, which unsafe code could write through (headerHeld). Each
// access the rule does not prove is left untagged, which LLVM reads as
// "may alias".
// Nothing here depends on another pack or on a call site, so the pack's
// IR stays a function of its own instances, which its cache key relies on.

package codegen

import (
	"strconv"

	"mk/internal/abi"
	"mk/internal/llvm"
	"mk/internal/mir"
	"mk/internal/types"
)

// aliasParam is one parameter that aliasScopes tags.
type aliasParam struct {
	local   mir.LocalID
	mut     bool    // a mut []T
	scope   llvm.MD // the parameter's scope
	self    llvm.MD // !{scope}
	noalias llvm.MD // !{the scopes it is disjoint from}, zero when none
}

// aliasCandidate reports whether Mk parameter i of the function is a slice
// whose data pointer the ABI does not pass as an LLVM ptr parameter
// (Win64 C ABI Indirect, AArch64 Direct [2 x i64] array, or System V byval),
// and of a kind that is noalias; mut reports a mut []T.
func (fg *fnGen) aliasCandidate(i int) (ok, mut bool) {
	g, sig := fg.g, fg.sig
	l := fg.b.Locals[i+1]
	if l.Var {
		return false, false
	}
	sl := sig.s.Params[i]
	if sl.Kind == abi.Ignore || (sl.Kind == abi.Direct && !sig.array[i]) {
		return false, false
	}
	ty := g.tt.Get(l.Type)
	switch ty.Kind {
	case types.KMutSlice:
		return true, true
	case types.KStr:
		return true, false
	case types.KSlice:
		return g.env.Facts(ty.Elem).Freeze, false
	}
	return false, false
}

// aliasScopes declares the scopes of the function's tagged parameters in
// the entry block and records them in fg.alias; it emits nothing unless at
// least two parameters qualify and one of them is a mut []T.
func (fg *fnGen) aliasScopes() {
	g, b := fg.g, fg.b
	f := fg.f
	if f == nil || f.Body == nil || g.isExtern(f) || g.nakedFn(f) || len(b.Asms) > 0 {
		return
	}
	held := fg.headerHeld()
	var ps []aliasParam
	anyMut := false
	for i := 0; i < int(b.ParamCount); i++ {
		if ok, mut := fg.aliasCandidate(i); ok && !held[mir.LocalID(i+1)] {
			ps = append(ps, aliasParam{local: mir.LocalID(i + 1), mut: mut})
			anyMut = anyMut || mut
		}
	}
	if len(ps) < 2 || !anyMut {
		return
	}
	md := g.m.Metadata()
	name := fg.fn.Name()
	dom := md.AliasDomain(name)
	for k := range ps {
		ps[k].scope = md.AliasScope(dom, name+": param "+strconv.Itoa(int(ps[k].local)-1))
		ps[k].self = md.Tuple(llvm.MDRef(ps[k].scope))
	}
	for k := range ps {
		var ops []llvm.MDOp
		for j := range ps {
			if j != k && (ps[k].mut || ps[j].mut) {
				ops = append(ops, llvm.MDRef(ps[j].scope))
			}
		}
		if len(ops) > 0 {
			ps[k].noalias = md.Tuple(ops...)
		}
		fg.entry.NoAliasScopeDecl(ps[k].self)
	}
	fg.alias = ps
}

// aliasIndex is the aliasParam number + 1 of local l, 0 when l is not
// tagged.
func (fg *fnGen) aliasIndex(l mir.LocalID) int32 {
	for k := range fg.alias {
		if fg.alias[k].local == l {
			return int32(k + 1)
		}
	}
	return 0
}

// aliasMD is the attachments of a scalar access at a: none for an address
// aliasAddr did not mark.
func (fg *fnGen) aliasMD(a addr) []llvm.Attach {
	if a.scope == 0 {
		return nil
	}
	p := &fg.alias[a.scope-1]
	out := []llvm.Attach{llvm.AliasScope(p.self)}
	if !p.noalias.IsZero() {
		out = append(out, llvm.NoAlias(p.noalias))
	}
	return out
}

// aliasAddr is elem, the address indexAddr computed from base of type t at
// projection k of a place rooted at local root, with its scope: an index
// into root's own header (k == 0) is an element of that parameter's slice;
// an index into an array keeps the array's scope, since the element is
// inside it; an index into any other slice loads another data pointer,
// which nothing here proves anything of.
func (fg *fnGen) aliasAddr(elem, base addr, t types.TypeID, root mir.LocalID, k int) addr {
	switch {
	case !fg.g.tt.Get(t).IsSliceLike():
		elem.scope = base.scope
	case k == 0 && fg.alias != nil:
		elem.scope = fg.aliasIndex(root)
	default:
		elem.scope = 0
	}
	return elem
}

// headerHeld is the parameters whose own header the body reaches other
// than by reading it: a borrow of the whole slice value (&s, which unsafe
// code could turn into a raw pointer and write through once the loan
// ends), an assignment to it, or a call result or error stored into it.
// Lower never assigns a non-var parameter, so the last two are a
// backstop; the scan keeps an untested path from becoming a wrong fact.
func (fg *fnGen) headerHeld() map[mir.LocalID]bool {
	b := fg.b
	var out map[mir.LocalID]bool
	mark := func(p mir.Place) {
		if p.Local >= 1 && int(p.Local) <= int(b.ParamCount) && p.Proj.Len == 0 {
			if out == nil {
				out = map[mir.LocalID]bool{}
			}
			out[p.Local] = true
		}
	}
	for bi := range b.Blocks {
		blk := &b.Blocks[bi]
		for si := range blk.Stmts {
			s := &blk.Stmts[si]
			if s.Kind != mir.StAssign {
				continue
			}
			mark(s.Place)
			if s.Rvalue.Kind == mir.RvRef {
				mark(s.Rvalue.Place)
			}
		}
		if t := &blk.Term; isCall(t) {
			mark(t.Dest)
			if t.HasErr {
				mark(mir.LocalPlace(t.Err.Error))
			}
		}
	}
	return out
}
