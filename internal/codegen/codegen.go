// Why: the back end needs one entry point that turns a build's monomorphic
// MIR into LLVM IR, pack by pack, with every target and profile choice
// stated as data the driver fills in.
//
// What: Config, Env, Func, Unit, Global, EmitPack and its Report,
// EmitSupport and InternalError: the public API of the package.
//
// How: the driver copies mono's instances and packs into a Unit, then calls
// EmitPack once per object pack and EmitSupport once for the support pack.
// Packs may be emitted concurrently: they share only read-only tables, such
// as the exact index built once under idxMu.

// Package codegen turns monomorphic MIR instance bodies into LLVM IR text,
// one module per object pack, and emits the support pack: the memory
// functions, __chkstk, _fltused, mk_switch and mk_task_start, the entry
// stubs, the Linux clone and signal-return trampolines, mk_call_system and
// the bodies of the support intrinsics. It decides nothing about what a
// program means: an invalid instance body is an internal error (E0000).
//
// codegen imports llvm, abi, pgo, mir, types, borrow and source
// (the DAG in internal/archtest). It does not import mono: the driver copies
// mono.Instances and mono.Pack into Unit field for field.
package codegen

import (
	"io"
	"sync"

	"mk/internal/mir"
	"mk/internal/pgo"
	"mk/internal/source"
	"mk/internal/types"
)

// Config is one build's code generation settings. It is part of every pack
// key through the driver (target, profile, CPU, PGO), never through codegen.
type Config struct {
	Target  types.Target
	Profile types.Profile
	// CPU is the "target-cpu" attribute, with its "tune-cpu" (cpuAttrs):
	// "x86-64-v2" by default on x86_64,
	// "generic" on aarch64; never "native". "" emits no attribute.
	CPU string
	// DataLayout is clang's for the target (toolchain probe, once per clang
	// and target); "" lets clang use its default.
	DataLayout string
	// Triple overrides Target.LLVMTriple when not empty (toolchain's
	// normalized triple, so clang sees no -Woverride-module).
	Triple string
	// PGOGen is release --pgo-gen: counters and the ProfileTable.
	PGOGen bool
	// PGO is release --pgo=: entry counts, weights, hot/cold and the
	// ProfileSummary flag; nil otherwise.
	PGO *pgo.Profile
	// RootModule is the module path whose Main is the entry.
	RootModule string
	// Main is the function CallMain calls in place of RootModule's Main:
	// the driver's report wrapper around it, with Main's result and
	// one parameter, the System. The zero key calls Main itself.
	Main types.Key
	// Frames names a function's frame in a panic's backtrace by its item
	// key text, in place of the key's own name: the test runner's
	// `test "name"` for a test's clone. "" leaves the frame out: its row
	// has no site, so the backtrace skips it (Main's report wrapper, the
	// test runner's Main). A key not in it keeps its name.
	Frames map[string]string
	// TestNoOverflowChecks and TestNoBoundsChecks are test-only (never set
	// by mk's commands): they measure what the checks cost, against the
	// budgets "overflow checks at most 5% median" and "bounds checks at
	// most 3% median", by building the same kernels
	// without them. TestNoOverflowChecks lowers + - * and negation of
	// integers to plain instructions (nsw on a signed type, as C's), with no
	// with.overflow call and no trap; TestNoBoundsChecks lowers a TermPanic
	// of PanicBounds (lower's explicit index and slice checks) to
	// unreachable, so LLVM deletes the compare that leads to it. A program
	// built so is not Mk: an out-of-range value is undefined behaviour.
	TestNoOverflowChecks bool
	TestNoBoundsChecks   bool
	// GeneralRegsOnly marks a bin built general-regs-only: every function
	// gets the general-regs-only features but
	// one whose cpu set holds sse2, which gets them with +sse,+sse2 and its
	// closure. The driver sets it per bin (freestanding's default).
	GeneralRegsOnly bool
	// Debug is mk build --debug (dev or release): full debug info, with
	// variables, types and scopes (debug_full.go, dityp.go). False emits
	// line tables only.
	Debug bool
	// Display maps a dependency's canonical id to the root package's local
	// name for it: the error-name
	// table prints a user error by it (ErrorDisplayIn). nil prints keys.
	// The driver gives panic sites' paths the same names through Env.Path.
	Display map[string]string
	// Library is a dynamic or static library build: it has no
	// Main, so std.rt.CallMain is a stub that returns code 0 (the entry
	// that would call it is never run; a library's runtime starts through
	// its exports).
	Library bool
	// LibName is a library build's name: the support pack defines
	// <LibName>_mk_start and <LibName>_mk_stop (exports.go); "" otherwise.
	LibName string
	// FramePointers is --frame-pointers=on (MK_FRAME_POINTERS): every Mk
	// function carries "frame-pointer"="non-leaf", so rbp is the frame
	// chain of every function that calls (fpsize.go).
	FramePointers bool
	// Size is --profile size: every Mk function
	// carries minsize and optsize, beside the -Oz compile (fpsize.go).
	Size bool
	// Cover is mk test --cover: a counter per MIR
	// block of every package function, in a section of its own (cover.go).
	Cover bool
	// TrapForm emits a small function's cold call of a never function with
	// no parameter as a trap the runtime's vectored handler resumes into that
	// call (trapform.go): the driver sets it for x86_64-windows optimized
	// executables.
	TrapForm bool
}

// triple is the LLVM triple printed into every module of the build.
func (c *Config) triple() string {
	if c.Triple != "" {
		return c.Triple
	}
	return c.Target.LLVMTriple
}

// Env is what codegen reads. The driver implements it over check's shape pass
// and the source map. Every method is safe for concurrent use and total on
// the concrete types an instance body names.
type Env interface {
	Table() *types.Table
	Items() *types.Items
	Consts() *types.ConstTable
	// Layout of a concrete type (no KParam, KSelf, KInfer, KPoison).
	Layout(types.TypeID) *types.Layout
	// Facts of a concrete type; codegen reads Freeze.
	Facts(types.TypeID) types.Facts
	// Path is a file's package-relative path, '/' separated.
	Path(source.FileID) string
	// LineCol is the 1-based line and column of a position.
	LineCol(source.Pos) source.LineCol
}

// Func is one function to emit: a mono.Instance, copied by the driver.
type Func struct {
	// Symbol is the instance's symbol, printed through llvm.Quote.
	Symbol string
	// Key is the instance's key text, for diagnostics and the PGO record.
	Key  string
	Item types.ItemID
	// Args are the instance's arguments as mono keyed it (the
	// item's generic arguments, then one CAValue per const value parameter).
	Args []types.GenericArg
	// Body is the instance body: GenericCount 0, no TermTypeTest,
	// TermOnerrGuard, CallInterface or loop FieldRef. nil for an extern or
	// intrinsic, which is never in a pack.
	Body *mir.Body
	// Spawned: the target of some s.go. The thunk is emitted by each
	// caller's pack (spawn.go), so Spawned changes nothing codegen prints;
	// an s.go site is a reference to the callee for Local.
	Spawned bool
	// AddrTaken: a fn value, export extern or callback; never internal.
	AddrTaken bool
	// Local: every reference to the symbol (a call, a fn reference, a
	// const relocation, an s.go thunk, which the caller's pack emits) is in
	// the function's own pack, so it is internal in every profile.
	// The driver computes it from mono's call graph and the partition; the
	// zero value, hidden or dso_local external, is always correct.
	Local bool
	// Pack is the index in Unit.Packs of the pack that defines it.
	Pack int
}

// Unit is the build's functions and their packs. Packs[p] lists the indices
// in Funcs of pack p's members, in key order. A function in no pack (an
// extern, an intrinsic) has Pack -1 and a nil Body.
type Unit struct {
	Funcs []Func
	Packs [][]int
	// Lookup finds the Funcs index of the instance of item with args, as
	// mono keys it (its canonical arguments). The driver implements it
	// over mono's table; nil means an exact match of (Item, Args) over Funcs,
	// which cannot see mono's canonicalization of fn-literal const arguments.
	Lookup func(item types.ItemID, args []types.GenericArg) (int, bool)
	// Globals assigns every var global the build references to the pack
	// that defines it (the first pack of its module, the driver's choice);
	// every other pack declares it.
	Globals []Global

	// idx is the exact (Item, Args) index over Funcs used when Lookup is
	// nil, built once per Unit and shared by every EmitPack (packs
	// share read-only tables). idxN is len(Funcs) when it was
	// built: Funcs must not change once emission starts, except that
	// appending rebuilds the index (tests extend a unit between emissions).
	idxMu sync.Mutex
	idx   *symIndex
	idxN  int
	// tkeys is the program's std.dyn.erased.Key table (typekey.go), built
	// under idxMu's guard on the first Key call.
	tkeys *typeKeys
	// sole counts each item's direct call sites (solecall.go), built under
	// idxMu's guard on first use.
	sole *soleCalls
	// small is the unit's small functions (smallfn.go), built under
	// idxMu's guard on first use; smallN is len(Funcs) when it was made.
	small  *smallSet
	smallN int
	// traps is whether some function traps a call (trapform.go unitTraps),
	// memoized under idxMu; trapsN is len(Funcs)+1 once it is set.
	traps  bool
	trapsN int
	// tails is the items on a `become` edge (tailbody.go tailItems),
	// memoized under idxMu; tailsN is len(Funcs)+1 once it is set.
	tails  map[types.ItemID]bool
	tailsN int
	// pnone is the preserve_nonecc tail-dispatch tables and handlers
	// (pnone.go pnoneSetOf), memoized under idxMu keyed by pnoneKey.
	pnone    *pnoneSet
	pnoneKey pnoneKey
	// vals is the Funcs some pack takes the address of (sig.go
	// valueFns), memoized under idxMu; valsN is len(Funcs)+1 once set.
	vals  map[int]bool
	valsN int
}

// index is u's shared exact index: built on the first call,
// then reused by every pack.
func (u *Unit) index() *symIndex {
	u.idxMu.Lock()
	defer u.idxMu.Unlock()
	if u.idx == nil || u.idxN != len(u.Funcs) {
		u.idx, u.idxN = unitIndex(u), len(u.Funcs)
	}
	return u.idx
}

// Global is one var global and the index of the pack that defines it.
type Global struct {
	Item types.ItemID
	Pack int
	// Local: only pack Pack references it, so it is internal in release
	// (dev keeps it hidden or dso_local external).
	Local bool
	// Define marks an export extern var that this unit defines:
	// pack Pack emits its C symbol zero-initialized, external with default
	// visibility and kept in @llvm.used; every other pack declares it, as
	// it declares any extern var. Never set for a plain extern var or a C
	// import's binding, which C or a linker script defines.
	Define bool
}

// Report lists what --timings shows: codegen lists the functions whose PGO
// record was stale (key matched, checksum or length not).
type Report struct {
	Stale []string
}

// EmitPack writes pack p's .ll. Every function is emitted under source.Guard;
// an internal error poisons only this pack and is returned as an
// *InternalError, whose IE is the recovered *source.InternalError (the
// driver reports it with diag.FromInternal). Same inputs, same bytes.
func EmitPack(u *Unit, p int, env Env, cfg Config, w io.Writer) (Report, error) {
	return emitPack(u, p, env, &cfg, w)
}

// EmitSupport writes the support pack of the build: native, compiled at
// -O2 in every profile. It depends on the unit only through the
// error-name table, the entry and the PGO table, and on env only for the
// bodies of the support intrinsics; env may be nil, in which case those
// bodies are left out (the target-support routines need no types).
func EmitSupport(u *Unit, env Env, cfg Config, w io.Writer) error {
	return emitSupport(u, env, &cfg, w)
}

// InternalError is an ICE raised while emitting a pack: an invalid instance
// body, an unknown intrinsic, a construct that must not reach codegen. The
// driver reports IE through diag.FromInternal as E0000.
type InternalError struct {
	IE *source.InternalError
}

func (e *InternalError) Error() string {
	return "codegen: internal error in " + e.IE.Where + ": " + e.IE.What
}

// guard runs f under source.Guard and returns its ICE as an error.
func guard(where string, span source.Span, f func()) error {
	if ie := source.Guard(where, span, f); ie != nil {
		return &InternalError{IE: ie}
	}
	return nil
}
