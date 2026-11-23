// Why: some language rules key on particular library items (the atomic, the
// SIMD vectors, the target items, the generator API, the formatting items),
// and they must be found by stable key, not by a name that could move.
//
// What: knownTable, one entry per types.KnownItems field with its module,
// owner and name, and Session.Known, which fills the struct.
//
// How: every key is spelled from Config.PreludePath except std.raw.Atomic,
// the std.simd items and the std.target items, so a move of the prelude
// module cannot leave the fields silently NoItem. An absent key is NoItem.

package resolve

import "mk/internal/types"

// knownRaw is the module of the compiler-known atomic.
const knownRaw = "std.raw"

// knownSimd is the module of the compiler-known SIMD items. A tree without
// that module leaves the four fields NoItem with no diagnostic: every rule
// keyed on them is inert at zero.
const knownSimd = "std.simd"

// knownTarget is the module of TargetInfo, Arch and Os, and of the
// synthesized const Target (the lowest module of std, below std.core
// and std.simd). A build whose std has no such module (the unit harnesses'
// stand-in preludes) finds them in the prelude module instead.
const knownTarget = "std.target"

// knownField is one KnownItems field: its address in the struct and the
// stable key it is found by.
type knownField struct {
	field  func(*types.KnownItems) *types.ItemID
	module string // "" means Config.PreludePath
	owner  string // a Type.Name member's owner, "" for a top-level item
	name   string
}

// knownTable is the table of known items. Gen, TypeInfo and the rest of the
// generator API are looked up in the prelude module under the names the
// language reference uses (TypeInfo is `Type`); the formatting items
// likewise.
var knownTable = []knownField{
	{func(k *types.KnownItems) *types.ItemID { return &k.Arena }, "", "", "Arena"},
	{func(k *types.KnownItems) *types.ItemID { return &k.Alloc }, "", "", "Alloc"},
	{func(k *types.KnownItems) *types.ItemID { return &k.Heap }, "", "", "Heap"},
	{func(k *types.KnownItems) *types.ItemID { return &k.Cell }, "", "", "Cell"},
	{func(k *types.KnownItems) *types.ItemID { return &k.Scope }, "", "", "Scope"},
	{func(k *types.KnownItems) *types.ItemID { return &k.Io }, "", "", "Io"},
	{func(k *types.KnownItems) *types.ItemID { return &k.System }, "", "", "System"},
	{func(k *types.KnownItems) *types.ItemID { return &k.Slice }, "", "", "Slice"},
	{func(k *types.KnownItems) *types.ItemID { return &k.Format }, "", "", "Format"},
	{func(k *types.KnownItems) *types.ItemID { return &k.Eq }, "", "", "Eq"},
	{func(k *types.KnownItems) *types.ItemID { return &k.Ord }, "", "", "Ord"},
	{func(k *types.KnownItems) *types.ItemID { return &k.Hash }, "", "", "Hash"},
	{func(k *types.KnownItems) *types.ItemID { return &k.Writer }, "", "", "Writer"},
	{func(k *types.KnownItems) *types.ItemID { return &k.Mutex }, "", "", "Mutex"},
	{func(k *types.KnownItems) *types.ItemID { return &k.Chan }, "", "", "Chan"},
	{func(k *types.KnownItems) *types.ItemID { return &k.SpinCore }, "", "", "SpinCore"},
	{func(k *types.KnownItems) *types.ItemID { return &k.Atomic }, knownRaw, "", "Atomic"},
	{func(k *types.KnownItems) *types.ItemID { return &k.Vec }, knownSimd, "", "Vec"},
	{func(k *types.KnownItems) *types.ItemID { return &k.Mask }, knownSimd, "", "Mask"},
	{func(k *types.KnownItems) *types.ItemID { return &k.Lane }, knownSimd, "", "Lane"},
	{func(k *types.KnownItems) *types.ItemID { return &k.CpuFeatures }, knownSimd, "", "CpuFeatures"},
	{func(k *types.KnownItems) *types.ItemID { return &k.Box }, "", "", "Box"},
	{func(k *types.KnownItems) *types.ItemID { return &k.OutOfMemory }, "", "", "OutOfMemory"},
	{func(k *types.KnownItems) *types.ItemID { return &k.Canceled }, "", "", "Canceled"},
	{func(k *types.KnownItems) *types.ItemID { return &k.Closed }, "", "", "Closed"},
	{func(k *types.KnownItems) *types.ItemID { return &k.Gen }, "", "", "Gen"},
	{func(k *types.KnownItems) *types.ItemID { return &k.TypeInfo }, "", "", "Type"},
	{func(k *types.KnownItems) *types.ItemID { return &k.TargetInfo }, knownTarget, "", "TargetInfo"},
	{func(k *types.KnownItems) *types.ItemID { return &k.Arch }, knownTarget, "", "Arch"},
	{func(k *types.KnownItems) *types.ItemID { return &k.Os }, knownTarget, "", "Os"},
	{func(k *types.KnownItems) *types.ItemID { return &k.Limits }, "", "", "Limits"},
	{func(k *types.KnownItems) *types.ItemID { return &k.StackSize }, "", "", "StackSize"},
	{func(k *types.KnownItems) *types.ItemID { return &k.Field }, "", "", "Field"},
	{func(k *types.KnownItems) *types.ItemID { return &k.Variant }, "", "", "Variant"},
	{func(k *types.KnownItems) *types.ItemID { return &k.Method }, "", "", "Method"},
	{func(k *types.KnownItems) *types.ItemID { return &k.Param }, "", "", "Param"},
	{func(k *types.KnownItems) *types.ItemID { return &k.Code }, "", "", "Code"},
	{func(k *types.KnownItems) *types.ItemID { return &k.Block }, "", "", "Block"},
	{func(k *types.KnownItems) *types.ItemID { return &k.TypeKind }, "", "", "TypeKind"},
	{func(k *types.KnownItems) *types.ItemID { return &k.Receiver }, "", "", "Receiver"},
	{func(k *types.KnownItems) *types.ItemID { return &k.Flow }, "", "", "Flow"},
	{func(k *types.KnownItems) *types.ItemID { return &k.FlowKind }, "", "", "FlowKind"},
	{func(k *types.KnownItems) *types.ItemID { return &k.Target }, knownTarget, "", "Target"},
	{func(k *types.KnownItems) *types.ItemID { return &k.Print }, "", "", "Print"},
	{func(k *types.KnownItems) *types.ItemID { return &k.ArenaFmt }, "", "Arena", "Fmt"},
	{func(k *types.KnownItems) *types.ItemID { return &k.FmtRun }, "", "", "FmtRun"},
	{func(k *types.KnownItems) *types.ItemID { return &k.FmtArena }, "", "", "FmtArena"},
	{func(k *types.KnownItems) *types.ItemID { return &k.FmtValue }, "", "", "FmtValue"},
	{func(k *types.KnownItems) *types.ItemID { return &k.FmtPiece }, "", "", "FmtPiece"},
	{func(k *types.KnownItems) *types.ItemID { return &k.FmtArg }, "", "", "FmtArg"},
	{func(k *types.KnownItems) *types.ItemID { return &k.StrFromC }, "", "str", "FromC"},
}

// Known finds the library items rules key on (types.KnownItems) by
// their stable keys. A field whose key is absent is NoItem.
func (s *Session) Known() types.KnownItems {
	var k types.KnownItems
	for _, f := range knownTable {
		mod := f.module
		if mod == "" {
			mod = s.cfg.PreludePath
		}
		if mod == "" {
			continue
		}
		key := types.ItemKey(mod, f.name)
		if f.owner != "" {
			key = types.MemberKey(mod, f.owner, f.name)
		}
		id, ok := s.cfg.Items.ByKey(key)
		if !ok && mod == knownTarget && s.cfg.PreludePath != "" {
			id, ok = s.cfg.Items.ByKey(types.ItemKey(s.cfg.PreludePath, f.name))
		}
		if ok {
			*f.field(&k) = id
		}
	}
	return k
}
