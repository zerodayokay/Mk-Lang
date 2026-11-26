// Why: codegen emits monomorphic functions only, so generic MIR must be
// turned into instance bodies with stable names before any IR exists.
//
// What: the public API of mono: Options, Collect's result Instances with
// Instance, Key, Find and the object packs (Pack), and the RootKind of each
// root.
//
// How: Instances is built once per build by Collect and then read
// concurrently by codegen's packs; keys and hashes are independent of
// interning order, so the same program gives the same instances, symbols
// and packs on every run.

// Package mono collects the build's instances from its roots, substitutes
// generic MIR into monomorphic instance bodies, names each instance by its
// nominal identity (iface.Hasher.InstanceName) and reports E0708 when
// instantiation runs away. It folds nothing and emits no IR.
//
// mono imports only mir, types, iface and diag (internal/archtest's DAG
// row). It names no source type: hashes are held as [32]byte, which converts
// to and from source.Hash, and an internal error is a panic with an error
// value that source.Guard recovers (see ice).
package mono

import (
	"mk/internal/iface"
	"mk/internal/mir"
	"mk/internal/types"
)

// Env is what mono reads; the driver implements it over check, lower, borrow,
// iface and the cache. Every method is safe for concurrent use.
type Env interface {
	Table() *types.Table
	Items() *types.Items
	Consts() *types.ConstTable
	// Body is the MIR of a function, method, fn literal, test, contract or
	// global initializer, generic or not; nil for a bodiless item. Bodies of
	// imported generics come from the body store (iface).
	Body(types.ItemID) *mir.Body
	// Layout of a concrete type (computed on demand by check's shape pass).
	Layout(types.TypeID) *types.Layout
	// ImplMethod resolves method m of interface iface on concrete self to the
	// implementing function and its generic arguments (check's impl table).
	ImplMethod(iface types.ItemID, m uint32, self types.TypeID) (types.ItemID, []types.GenericArg, bool)
	// OnerrRuns is borrow's resolution of the OnerrGuard ending block b of
	// item's generic body (borrow.Facts): true runs the onerr body.
	OnerrRuns(item types.ItemID, b mir.BlockID) bool
	// RuntimeEntry finds a std function codegen calls by stable key.
	RuntimeEntry(key types.Key) (types.ItemID, bool)
	// Hasher and World give mono the incremental cache's InstanceName and
	// InstanceContentHash (Key.Hash and Instance.Content); both are safe for
	// concurrent use and independent of interning order.
	Hasher() *iface.Hasher
	World() iface.World
}

// RootKind says why a function is a root of collection.
type RootKind uint8

const (
	// RootMain is the build root's Main (and the entry).
	RootMain RootKind = iota
	// RootExport is an export extern.
	RootExport
	// RootTest is a test block, in test builds only (plus the runner).
	RootTest
	// RootRuntime is a std function codegen calls by key.
	RootRuntime
	// RootAddressTaken is a function whose address the build takes outside
	// any instance body (a callback the driver knows of).
	RootAddressTaken
)

// Root is one root: a non-generic function, test or export extern. A generic
// item is never a root (an ICE).
type Root struct {
	Kind RootKind
	Item types.ItemID
}

// Default limits and pack size. The limits are fixed bounds, not tuned by
// measurement.
const (
	DefaultMaxDepth = 64
	DefaultMaxCount = 100_000
	DefaultPackOps  = 20_000
)

// Options configure one collection. A zero MaxDepth, MaxCount or PackOps
// means its default.
type Options struct {
	// Profile: Release drops contract checks; Checked, optimized
	// as Release is, keeps them.
	Profile types.Profile
	Target  types.Target
	// MaxDepth is the deepest instantiation chain, 64.
	MaxDepth int
	// MaxCount is the most instances one build root may have, 100,000.
	MaxCount int
	// PackOps is the target average MIR operations per pack.
	PackOps int
	// Known names the compiler-known items (Scope[E]; mir.Env.Known
	// for validation). Nil leaves the rules that need it inert.
	Known *types.KnownItems
	// GeneralRegsOnly marks a bin built general-regs-only (gro.go): the walk
	// reports E1108 for a float or vector operation in an instance outside a
	// function whose cpu set holds sse2. Bin names the bin in E1108's facts.
	GeneralRegsOnly bool
	Bin             string
	// LibcMode is the build's system-libc mode, the value every
	// call of std.sys.LibcBuild() becomes (libcfold.go).
	LibcMode bool
	// tasks is the fold of std.rt.UsesTasks() (tasks.go), set by Collect on
	// its copy: the item (NoItem: no fold) and the value its calls become.
	tasks taskFold
	// libc is the fold of std.sys.LibcBuild() (libcfold.go), set by
	// Collect on its copy from LibcMode.
	libc taskFold
	// Validate runs mir.Validate on every built instance body;
	// go test and dev builds of mk itself set it. A violation is an ICE.
	Validate bool
	// SkipContent leaves every Instance.Content zero: no collection
	// decision, diagnostic or order reads it, so only its readers pay for
	// it. The driver sets it for the instance-limit walk, which drops its
	// instances, and for a build's collection, whose pack keys hash each
	// pack's content in parallel, per pack.
	SkipContent bool
	// LazySyms are the extern symbols the build binds lazily (the
	// driver's lazylink.go), and LazyRuntime the std functions their
	// compiler-emitted stubs call by symbol: the first bodiless extern
	// instance whose symbol LazySyms holds requests every LazyRuntime item,
	// address taken, so a build that reaches no lazy function collects
	// none of them. Nil binds nothing lazily.
	LazySyms    map[string]bool
	LazyRuntime []types.ItemID
}

func (o Options) withDefaults() Options {
	if o.MaxDepth <= 0 {
		o.MaxDepth = DefaultMaxDepth
	}
	if o.MaxCount <= 0 {
		o.MaxCount = DefaultMaxCount
	}
	if o.PackOps <= 0 {
		o.PackOps = DefaultPackOps
	}
	return o
}

// Instance is one monomorphic function. Body has GenericCount 0 and none of
// KParam, KSelf, CAParam, TermTypeTest, RvTypeIs and CallInterface (those
// are resolved per instance), and (in Release) no contract-predicate
// calls. Body may be the item's own body pointer when nothing needed
// rewriting; it is never modified. Every instance is emitted as its own
// function: mono folds nothing.
//
// Args is the instance's argument list: the item's combined generic arguments
// (one per generic of its body), then one CAValue const argument per const
// value parameter (types.Param Const) in parameter order. Key.Hash is
// InstanceName over exactly this list, so the symbol, the E0708 count and the
// pack order all see the const arguments.
type Instance struct {
	// Key is the canonical identity.
	Key  Key
	Item types.ItemID
	Args []types.GenericArg
	// Generics is how many of Args are generic arguments; the rest are const
	// value parameters' arguments.
	Generics int
	// Body is nil for an extern or intrinsic.
	Body *mir.Body
	// Symbol is the LLVM symbol.
	Symbol string
	// Name is the readable spelling (item key and arguments) for
	// diagnostics and --timings; never identity.
	Name string
	// Owner is the module whose pack holds it: the generic item's module.
	Owner types.ModuleID
	// Content is iface.Hasher.InstanceContentHash of Body; zero for a
	// bodiless instance, and for every instance under Options.SkipContent.
	Content [32]byte
	// Spawned: the target of some s.go, so codegen emits its thunk.
	Spawned bool
	// AddrTaken: a fn value, export extern, or callback: never internal.
	AddrTaken bool
	// CPU is the instance's feature set (cpu.go): its own cpu clause, united
	// with its caller's set when a direct call created it as a generic
	// instance. Sorted; the target's baseline is implied. The part beyond its
	// own clause and the baseline is the " @cpu(...)" suffix of Key.Text and
	// Name, and is in Key.Hash (iface.Hasher.InstanceNameCPU).
	CPU []string
	// Ops is statements plus terminators of Body.
	Ops   uint32
	Depth uint32
}

// Key is the nominal instance identity. Hash is iface.Hasher.InstanceName(
// item key, args) (domain iface.DomInstName); its first 16 hex digits are the
// symbol suffix. A fn literal is the exception: its Hash is its content,
// iface.Hasher.FnRef (mk.fn tag 2), so identical literals at different
// sites are one instance. Text is the readable sort key. mono declares no hash
// domain of its own.
type Key struct {
	Text string
	Hash [32]byte
}

// Instances is the build's instance table, sorted by Key.Text.
type Instances struct {
	List  []Instance
	index map[string]int
	// env is the collection's Env, for Find; nil for a table built by hand.
	env Env
	// usesTasks is the UsesTasks fact (tasks.go).
	usesTasks bool
}

// Find is the List index of the instance a reference to item at args names,
// by its key rather than by exact (item, args): SizeOf[T]() of a
// closed T is folded first, a fn literal is found by its content, and a
// const argument by its deep value hash, so a reference spelled with
// another (equal) literal or const still finds its instance. Instance bodies
// already name their callees exactly (canon.go); Find is for the
// references mono cannot rewrite, the fn relocations in the values of
// consts and globals that codegen emits from their declarations. It is safe
// for concurrent use (codegen's packs) once Collect has returned, and false
// for a table built by hand.
func (in *Instances) Find(item types.ItemID, args []types.GenericArg) (int, bool) {
	if in.env == nil || in.index == nil {
		return -1, false
	}
	if len(args) > 0 {
		s := substituter{tab: in.env.Table(), fold: func(t types.TypeID) types.ConstArg {
			l := in.env.Layout(t)
			if l == nil {
				return types.ConstArg{Kind: types.CASizeOf, Type: t}
			}
			return types.ConstArg{Kind: types.CAValue, Value: intConst(in.env.Consts(), int64(l.Shape.Size))}
		}}
		args = s.genericArgs(args)
	}
	i, ok := in.index[keyOf(in.env, item, args).Text]
	if !ok {
		return -1, false
	}
	return i, true
}

// Lookup finds an instance by key text.
func (in *Instances) Lookup(text string) (*Instance, bool) {
	if in.index == nil {
		in.index = make(map[string]int, len(in.List))
		for i := range in.List {
			in.index[in.List[i].Key.Text] = i
		}
	}
	i, ok := in.index[text]
	if !ok {
		return nil, false
	}
	return &in.List[i], true
}

// Pack is one object pack: its members in key order and the (instance name,
// content hash) list the driver passes to iface.PackKey. mono computes no pack
// key itself.
type Pack struct {
	Name      string
	Owner     types.ModuleID
	Members   []int
	Instances []iface.PackInstance
}
