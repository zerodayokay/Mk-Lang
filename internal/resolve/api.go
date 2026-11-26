// Why: name resolution is used by the driver, check, gen and the tools, and
// they should share one stable set of input and result types.
//
// What: the public API: Config and Catalog (per build), ModuleInput and
// Implicit (per module), GeneratedFile with its origins, and the Info
// result (FileInfo side tables, Ref, RefKind, Reading, Binding, ItemInfo,
// Capture, Body, Local, GenLine).
//
// How: results are flat side tables indexed by ast.NodeID; a Ref is 12
// pointer-free bytes whose A and B fields mean what its RefKind says.

package resolve

import (
	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/types"
)

// Config is what every module of one build shares. Immutable after NewSession.
type Config struct {
	Interner *source.Interner
	Sources  *source.SourceMap
	Items    *types.Items
	// Catalog answers the E0201 import suggestion; nil disables the
	// suggestion (the diagnostic still has facts.name).
	Catalog Catalog
	// PreludePath is the prelude module's path, std.PreludeModule
	// ("std.core"); "" when the build has no prelude (unit tests).
	// KnownItems keys are spelled from it.
	PreludePath string
	// PreludeNames is the language's prelude list, std.PreludeNames, passed
	// in by the driver because resolve cannot import std. The prelude scope
	// is exactly these names of the prelude module; a listed name the
	// prelude module does not declare is in no scope.
	PreludeNames []string
}

// Catalog lists exported top-level names across every module this build can
// load: the package's modules and the std modules this std provides, never
// a documented std module this std does not build. The driver implements
// it, lazily, on the error path only. It may be called from many goroutines
// at once.
type Catalog interface {
	// ExportedBy returns the module paths, sorted bytewise, that export an
	// item whose name is exactly name, and the package (non-std) module paths
	// it searched, sorted, which resolve records as ReadExports reads.
	ExportedBy(name string) (paths []string, searched []string)
}

// ModuleInput is one module ready for resolution. The driver has created
// the types.Module (Path, Files, Std, UnsafePermitted) and parsed the files.
type ModuleInput struct {
	Module types.ModuleID
	// Files are the module's parsed files in byte order of their paths,
	// Files[i] the tree of Module.Files[i].
	Files []*ast.File
	// Imports maps each import path written in Files to its module. A path
	// the driver could not load (it reported E0202) maps to NoModule, and the
	// binding is poisoned: its uses resolve to RefPoison with no diagnostic.
	// A path absent from the map is treated the same way.
	Imports map[string]types.ModuleID
	// Implicit are bindings every file has without an import line: pkg.mk's
	// `pkg` for std.pkg, empty elsewhere.
	Implicit []Implicit
	// Prelude is the prelude module (Config.PreludePath, std.core), or
	// NoModule for the prelude itself and the std modules it imports (they
	// cannot see it) and for tests without a prelude.
	Prelude types.ModuleID
	// BinRoot is set by the driver for a module that is the root of a bin.
	// Resolve then requires a module-level Main.
	BinRoot bool
	// TestPrelude is the corpus test prelude file of this module, or NoFile.
	// Its items are module items; it is exempt from E0208 and is never
	// offered as an E0201 candidate.
	TestPrelude source.FileID
	// HasGen reports for an imported module whether it has gen lines that are
	// not yet settled. Nil means none has.
	HasGen func(types.ModuleID) bool
}

// Implicit is an import binding with no import line.
type Implicit struct {
	Name   source.Symbol
	Module types.ModuleID
}

// GeneratedFile is one parsed generator output with its provenance. The
// driver converts gen's Output (with its token origins) into it,
// since gen and resolve do not import each other.
type GeneratedFile struct {
	// File is the parsed output; its span names its source.File.
	File *ast.File
	// Origins give the gen origin of each generated top-level item, by the
	// item node's ID, in node order.
	Origins []NodeOrigin
	// Names say how each name token of the file resolves, by byte offset,
	// sorted; a token with no entry resolves like handwritten code.
	Names []NameOrigin
}

// NodeOrigin is the origin of one generated item.
type NodeOrigin struct {
	Node   ast.NodeID
	Origin types.GenOrigin
}

// NameOrigin is the lookup rule of one name token of generated code.
type NameOrigin struct {
	Offset uint32
	Kind   NameKind
	// Module is the generator's module for NameLiteral, the bound module for
	// NamePinnedModule; Item the pinned item for NamePinnedItem.
	Module types.ModuleID
	Item   types.ItemID
}

// NameKind mirrors gen.OriginKind.
type NameKind uint8

const (
	// NameLiteral: written literally in a quote. After the generated code's
	// own locals and type parameters, it resolves in the import bindings of
	// Module's files, then the prelude, then the universe; never in Module's
	// own items nor in the output module's. Module itself is a binding after
	// its imports, named by the last segment of its path unless an import
	// takes that name, through which its exported items are reachable.
	NameLiteral NameKind = iota + 1
	// NameSpliced: from a spliced str, Gen.Ident or Gen.Fresh; resolves in
	// the output module like handwritten code.
	NameSpliced
	// NamePinnedItem, NamePinnedModule: a token of a spliced Type, a resolved
	// reference that needs no import.
	NamePinnedItem
	NamePinnedModule
)

// Info is one module's resolution. Read-only after Finish (and Settle);
// safe for concurrent readers.
type Info struct {
	Module types.ModuleID
	// Files are the module's files, input order, then generated files in
	// AddGenerated order.
	Files []*FileInfo
	// Items are the module's items with their resolution, in key order.
	Items []*ItemInfo
	// Gens are the module's gen lines in key order (GenKey numbering).
	Gens []GenLine
	// DuplicateImpls are impl blocks whose key equals an earlier one's; they
	// get no Item, and check reports E0704 for them.
	DuplicateImpls []DuplicateImpl
	// Diags are the diagnostics of Begin and Finish, unordered; the driver
	// orders them (diag.Order).
	Diags []*diag.Diagnostic
}

// FileInfo is the side tables of one file, indexed by ast.NodeID and sized
// File.NodeCount+1 (slot 0 unused).
type FileInfo struct {
	File   *ast.File
	FileID source.FileID
	// Refs holds the resolution of every Ident, Path, PathType, FieldExpr
	// whose left side is a module binding, NamePattern, PathPattern, and of
	// every declaring node (its item). RefNone elsewhere.
	Refs []Ref
	// Readings decides each TypeOrExpr slot and each IndexExpr (Reading).
	Readings []Reading
	// Imports are the file's import bindings in source order, implicit ones
	// first.
	Imports []Binding
	// Generated is true for a file from AddGenerated.
	Generated bool
}

// RefKind is what a name resolved to.
type RefKind uint8

const (
	RefNone RefKind = iota
	// RefLocal: A is the LocalID in the body of item B.
	RefLocal
	// RefParam: A is the types.ParamIndex (receiver first) in item B; equal
	// to the parameter's LocalID and its MIR local. Inside a
	// requires or ensures predicate, B is the function the clause belongs to
	// and A that function's ParamIndex.
	RefParam
	// RefItem: A is the types.ItemID.
	RefItem
	// RefModule: A is the types.ModuleID of an import binding.
	RefModule
	// RefTypeParam: A is the index in item B's generics, owner's first, then
	// the item's own (types.Signature.Generics order).
	RefTypeParam
	// RefPrim: A is the fixed types.TypeID of a primitive type name.
	RefPrim
	// RefUniverse: A is the source.Symbol of a universe name (true .. EvalArena).
	RefUniverse
	// RefSelfType: Self inside an interface or impl of item B.
	RefSelfType
	// RefResult: result inside an ensures clause of function item B.
	RefResult
	// RefField: a bare field name inside an invariant; A is the field's index
	// in declaration order, B the struct item.
	RefField
	// RefLabel: a break or continue label; A is the NodeID of the labeled
	// ForStmt.
	RefLabel
	// RefDeferred: the root resolved, the rest is a member check looks up
	// (set on a FieldExpr or a Path whose tail needs a type).
	RefDeferred
	// RefPoison: a diagnostic was reported, or the name depends on one;
	// every later pass accepts it silently.
	RefPoison
	// RefPending: only between Finish and Settle; never seen
	// by check.
	RefPending
	// RefCapture: inside fn literal item B, the name is a local or parameter
	// of an enclosing body that B captures; A indexes B's ItemInfo.Captures.
	// Appended after RefPending.
	RefCapture
)

var refKindNames = [...]string{"none", "local", "param", "item", "module", "typeparam", "prim",
	"universe", "selftype", "result", "field", "label", "deferred", "poison", "pending", "capture"}

// String names the kind.
func (k RefKind) String() string {
	if int(k) < len(refKindNames) {
		return refKindNames[k]
	}
	return "refkind?"
}

// Ref is one resolution, 12 bytes and pointer-free.
type Ref struct {
	Kind RefKind
	// Segs is how many segments of a Path or a FieldExpr chain this Ref
	// covers; the rest are members (RefDeferred tail). 1 for an Ident.
	Segs uint8
	A    uint32
	B    uint32
}

// Reading is resolve's decision for a TypeOrExpr slot or an IndexExpr.
type Reading uint8

const (
	ReadingNone Reading = iota
	// ReadType: the slot's node is read as a type.
	ReadType
	// ReadExpr: the slot is a value (a const generic argument, a gen literal).
	ReadExpr
	// ReadGeneric: an IndexExpr whose X is generic: its Args are generic
	// arguments, each with its own ReadType or ReadExpr.
	ReadGeneric
	// ReadIndex: an IndexExpr that indexes a value; one argument, ReadExpr.
	ReadIndex
	// ReadUndecided: X is a member check must find first (x.M[T]); check
	// decides with the same rule.
	ReadUndecided
)

// Binding is one import binding of a file.
type Binding struct {
	Name   source.Symbol
	Module types.ModuleID // NoModule when poisoned
	Path   string
	// Node is the Import node, NoNode for an implicit binding.
	Node ast.NodeID
	// Used is the union of the per-task used sets, written once
	// by Finish's merge and again by Settle's, never by a worker.
	Used bool
}

// ItemInfo is the resolution of one item.
type ItemInfo struct {
	Item types.ItemID
	// File indexes Info.Files; Node is the item's declaring node there (the
	// Decl, TestItem, GenItem, FnLit, Contract or InvariantMember).
	File int
	Node ast.NodeID
	// Parent is the item a lifted fn literal, contract predicate or invariant
	// belongs to, NoItem otherwise (also Item.Owner).
	Parent types.ItemID
	// Body is the item's locals, nil when it has none (a struct, an alias).
	Body *Body
	// Reads are the reads resolve made, with zero hashes; the driver fills
	// them in later.
	Reads types.ReadSet
	// Literals are the fn literals lifted from this item, in source order.
	Literals []types.ItemID
	// PrimOwner mirrors types.Item.OwnerPrim: the primitive owner type of a
	// Type.Name method on a builtin type, NoType otherwise.
	PrimOwner types.TypeID
	// Captures of a fn literal item, in byte order of the names (the order
	// of its environment record's fields); nil when it captures
	// nothing and for every other item.
	Captures []Capture
	// Synth marks an item no source declares: Node is then the
	// declaration it is made from, which declares another item too.
	Synth SynthKind
}

// SynthKind marks an item resolve declares that no source declares.
type SynthKind uint8

const (
	SynthNone SynthKind = iota
	// SynthErrorFormat is the impl of std.core.Format for an error, or that
	// impl's Format method. ItemInfo.Node is the *ast.ErrorDecl. Check fills
	// its Decl or drops both items from the module's lists.
	SynthErrorFormat
)

// Capture is one name a fn literal reads from an enclosing body.
type Capture struct {
	Name source.Symbol
	// Of is the name's resolution in the literal's parent body: RefLocal or
	// RefParam of the declaring item, or RefCapture of the parent literal
	// when the parent captures the name too (a chain).
	Of Ref
	// First is the first use in the literal's own text, nested literals
	// included.
	First source.Span
	// FieldsLoop is set when the name is declared by, or inside, a fields
	// loop whose body also contains the literal (E0609 fields-loop).
	FieldsLoop bool
	// ConstFn is set when the name is a const parameter of fn type of the
	// function that declares it (E0705).
	ConstFn bool
}

// LocalID numbers the locals of one body from 1; parameters come first, in
// ParamIndex order (receiver first), then other locals in declaration order.
// 0 is none.
type LocalID uint32

// Body is the locals of one function, method, test, literal, contract
// predicate or invariant.
type Body struct {
	Item types.ItemID
	// Locals[0] is unused; Locals[i] is LocalID i.
	Locals []Local
}

// Local is one local of a body.
type Local struct {
	Name source.Symbol
	// Decl is the declaring Ident's node in the item's file; NoNode for self.
	Decl  ast.NodeID
	Kind  LocalKind
	Var   bool // var binding, var parameter, or every binding of var (a, b) := e
	Const bool // const value parameter
	Used  bool
}

// LocalKind is how a local was introduced.
type LocalKind uint8

const (
	LocalNone    LocalKind = iota
	LocalSelf              // the receiver
	LocalParam             // a parameter, including const and var ones, and a test's sys
	LocalBind              // x := e, var x := e, x: T, x: T = e
	LocalDestr             // a field of (a, b) := e or Point(x, y) := p
	LocalPattern           // a match, if, for or else binding: NamePattern, PatField
	LocalLoop              // for i, x in ..., for x := e
	LocalScope             // scope s := e
	LocalElse              // e else x { }
)

// GenLine is one gen line, resolved.
type GenLine struct {
	Item types.ItemID // KindGen, key types.GenKey
	File int
	Node ast.NodeID
	// Generator is the generator function's item, NoItem when it did not
	// resolve (a diagnostic was reported).
	Generator types.ItemID
}

// DuplicateImpl names an impl block with no item of its own.
type DuplicateImpl struct {
	File  int
	Node  ast.NodeID
	First types.ItemID
}
