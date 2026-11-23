// Why: symbols, caches, read sets and error identities must be the same in
// every process and schedule, so every entity needs a stable key, and every
// pass needs one shape for the checked declarations.
//
// What: ModuleID, ItemID, Key and its constructors (ItemKey, MemberKey,
// ImplKey, TestKey, GenKey, LiteralKey, ContractKey, InvariantKey),
// ErrorCode, Item, Module, the Items table, the checked declarations
// (StructDecl .. GenDecl) and KnownItems.
//
// How: a key is text built from the module path, the owner and the name,
// with separators (':', '#') chosen so that no two kinds of key can collide.
// An error's run-time code is a hash of its key, so it never depends on
// which modules a build contains.

package types

import (
	"strconv"
	"strings"
	"sync"

	"mk/internal/source"
)

// ModuleID names a module of a session; 0 is none. Never ordered or hashed:
// modules are ordered by Path.
type ModuleID uint32

// NoModule is the zero ModuleID.
const NoModule ModuleID = 0

// ItemID names an item of a session; 0 is none. Never ordered or hashed:
// items are ordered by Key.
type ItemID uint32

// NoItem is the zero ItemID.
const NoItem ItemID = 0

// Key is an entity's stable key: the same text in every process, run and
// goroutine schedule, built from the module path, the owner's name for
// members, and the item's name (or, for items with no name, a canonical
// description). Keys order every module-wide list, name symbols, give errors
// their identity, and are what read sets and caches record. Key is comparable;
// the zero Key is none.
type Key struct{ s string }

// String is the key's canonical text, e.g. "std.fs.File" or "std.fs.File:Close".
func (k Key) String() string { return k.s }

// IsZero reports whether k is the zero Key.
func (k Key) IsZero() bool { return k.s == "" }

// Compare orders keys bytewise.
func (k Key) Compare(o Key) int { return strings.Compare(k.s, o.s) }

// Hash is the key's stable hash, in the domain "mk.key.v1".
func (k Key) Hash() source.Hash { return source.HashString("mk.key.v1", k.s) }

// ModuleKey is the key of a module: its path, "std.fs" or "main".
func ModuleKey(path string) Key { return Key{path} }

// ItemKey is the key of a top-level named item of a module: "std.fs.File".
func ItemKey(module, name string) Key { return Key{module + "." + name} }

// MemberKey is the key of a method or other member in a type's namespace:
// "std.fs.File:Close". The ':' before the member keeps keys injective:
// method Point.Add of module geo is "geo.Point:Add", while item Add of
// module geo.Point is "geo.Point.Add". No module path or item name contains
// ':' (both are dotted identifiers), so ItemKey text never does.
func MemberKey(module, owner, name string) Key { return Key{module + "." + owner + ":" + name} }

// ImplKey is the key of impl I for T in a module: the interface's key and
// the canonical spelling of the target type constructor (gap 13: a built-in
// bound such as Share is spelled by its name).
func ImplKey(module string, iface string, target string) Key {
	return Key{module + "#impl(" + iface + " for " + target + ")"}
}

// TestKey is the key of test "name": the name quoted.
func TestKey(module, name string) Key { return Key{module + "#test(" + strconv.Quote(name) + ")"} }

// GenKey is the key of the n-th gen line of a module (0-based, in file path
// then source order).
func GenKey(module string, n int) Key { return Key{module + "#gen(" + strconv.Itoa(n) + ")"} }

// LiteralKey is the key of the n-th fn literal inside an item (0-based, source
// order), an item of its own after resolution.
func LiteralKey(enclosing Key, n int) Key { return Key{enclosing.s + "#fn" + strconv.Itoa(n)} }

// ImplMemberKey is the key of method name of an impl: the impl's key, ':', the
// name, as "geo#impl(std.core.Eq for geo.Point):Eq". The impl key holds '#',
// which no type name does, so it never equals a MemberKey.
func ImplMemberKey(impl Key, name string) Key { return Key{impl.s + ":" + name} }

// ContractKey is the key of the n-th (0-based, source order) predicate of
// kind "requires" or "ensures" of function fn: "geo.Div#requires0",
// "geo.Point:Norm#ensures1". Any other kind is an ICE.
func ContractKey(fn Key, kind string, n int) Key {
	if kind != "requires" && kind != "ensures" {
		source.ICEf(source.Span{}, "types.ContractKey: kind %q", kind)
	}
	return Key{fn.s + "#" + kind + strconv.Itoa(n)}
}

// InvariantKey is the key of the n-th (0-based, source order) invariant
// member of struct owner: "geo.Range#invariant0".
func InvariantKey(owner Key, n int) Key { return Key{owner.s + "#invariant" + strconv.Itoa(n)} }

// KeyFromString rebuilds a key from its canonical text, as String gave it
// (when decoding an interface). It does not check that the text is one a
// constructor above can produce; the zero Key is "".
func KeyFromString(s string) Key { return Key{s} }

// ErrorCode is an error's run-time identity: the low 31 bits of the first 8
// bytes of its key's hash, read big-endian, plus one, so it is never 0
// (success) and never depends on which modules a build contains.
func ErrorCode(k Key) uint32 { return uint32(k.Hash().Uint64()&0x7fffffff) + 1 }

// ItemKind is what an item declares.
type ItemKind uint8

const (
	KindNone ItemKind = iota
	KindStruct
	KindEnum
	KindError
	KindInterface
	KindImpl
	KindAlias
	KindConst
	KindGlobal
	// KindFunc is a function or method; an extern is a KindFunc whose
	// Signature has Extern set (there is no separate extern kind).
	KindFunc
	KindTest
	KindGen
	// KindLiteral is a fn literal, lifted to an item by resolution.
	KindLiteral
)

var itemKindNames = [...]string{"none", "struct", "enum", "error", "interface", "impl", "alias",
	"const", "global", "func", "test", "gen", "literal"}

// String names the kind.
func (k ItemKind) String() string {
	if int(k) < len(itemKindNames) {
		return itemKindNames[k]
	}
	return "itemkind(" + strconv.Itoa(int(k)) + ")"
}

// GenOrigin locates generated code (for a generator's trace): the output, the
// emitting quote and the gen line.
type GenOrigin struct {
	Output  source.Span
	Emitter source.Span
	Gen     source.Span
}

// Item is one entity of a module. The resolver creates it with ID, Module,
// Owner, Name, Kind, Exported, Span and Key; the checker fills Decl at stage 5.
// After stage 5 an Item is read-only and may be read from any goroutine.
type Item struct {
	ID     ItemID
	Module ModuleID
	// Owner is the item a dependent item belongs to, NoItem otherwise: the
	// type whose namespace holds a Type.Name method, the impl for an impl
	// method, the enclosing item for a lifted fn literal, the function for a
	// requires or ensures predicate, the struct for an invariant. A method of
	// a primitive type (str.Len) has Owner NoItem and OwnerPrim set.
	Owner    ItemID
	Name     source.Symbol
	Kind     ItemKind
	Exported bool
	// Span is the item's name, or its keyword when it has no name.
	Span source.Span
	Key  Key
	// Origin is set on generated items.
	Origin *GenOrigin
	// Decl is the checked declaration, nil until stage 5.
	Decl Decl
	// OwnerPrim is the primitive type whose namespace holds a std method
	// (str.Len: types.Str), NoType otherwise.
	OwnerPrim TypeID
}

// Module is one module: a directory of .mk files that all see each other.
type Module struct {
	ID ModuleID
	// Path is the module path, "std.fs" or "main"; it is the module's Key.
	Path  string
	Files []source.FileID
	// Items are the module's items in key order once resolution finishes.
	Items []ItemID
	// UnsafePermitted comes from pkg.mk's unsafe_modules (E1102).
	UnsafePermitted bool
	// Std is true for a module of the embedded standard library.
	Std bool
	// Imports are the module's direct imports, each once, ordered by Path
	// (never by ID). The driver sets them from the import graph before any
	// stage reads them and never changes them after; iface encodes them, so
	// a module loaded from the cache has them too (check's Load).
	Imports []ModuleID
}

// Items is the session's table of modules and items. AddModule and AddItem
// may be called from many goroutines; Get and Module take no lock. Slot 0 of
// each table is reserved. The zero Items is not usable; call NewItems.
type Items struct {
	mu      sync.Mutex
	items   source.AppendList[*Item]
	modules source.AppendList[*Module]
	byKey   map[Key]ItemID
}

// NewItems returns empty tables.
func NewItems() *Items {
	t := &Items{byKey: make(map[Key]ItemID)}
	t.items.Append(nil)
	t.modules.Append(nil)
	return t
}

// AddModule records a module and sets its ID.
func (t *Items) AddModule(m *Module) ModuleID {
	t.mu.Lock()
	defer t.mu.Unlock()
	m.ID = ModuleID(t.modules.Append(m))
	return m.ID
}

// AddItem records an item with a nonempty, unique key and sets its ID. A
// zero or duplicate key is an ICE (the resolver reports E0206 first).
func (t *Items) AddItem(it *Item) ItemID {
	if it.Key.IsZero() {
		source.ICEf(it.Span, "types.Items.AddItem: no key")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, dup := t.byKey[it.Key]; dup {
		source.ICEf(it.Span, "types.Items.AddItem: duplicate key %s", it.Key)
	}
	it.ID = ItemID(t.items.Append(it))
	t.byKey[it.Key] = it.ID
	return it.ID
}

// Get returns an item; an unknown ID is an ICE.
func (t *Items) Get(id ItemID) *Item {
	it, ok := t.items.Get(uint32(id))
	if !ok || id == NoItem {
		source.ICEf(source.Span{}, "types.Items.Get: unknown item %d", id)
	}
	return it
}

// Module returns a module; an unknown ID is an ICE.
func (t *Items) Module(id ModuleID) *Module {
	m, ok := t.modules.Get(uint32(id))
	if !ok || id == NoModule {
		source.ICEf(source.Span{}, "types.Items.Module: unknown module %d", id)
	}
	return m
}

// ByKey finds an item by its stable key.
func (t *Items) ByKey(k Key) (ItemID, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	id, ok := t.byKey[k]
	return id, ok
}

// NumItems is the number of item slots in use, slot 0 included.
func (t *Items) NumItems() int { return int(t.items.Len()) }

// NumModules is the number of module slots in use, slot 0 included.
func (t *Items) NumModules() int { return int(t.modules.Len()) }

// Decl is a checked declaration: one of *StructDecl, *EnumDecl, *ErrorDecl,
// *InterfaceDecl, *ImplDecl, *AliasDecl, *ConstDecl, *GlobalDecl, *FuncDecl,
// *TestDecl or *GenDecl.
type Decl interface{ isDecl() }

// FieldDecl is one field of a struct, an enum variant or an error.
type FieldDecl struct {
	Name     source.Symbol
	Type     TypeID
	Exported bool
	Span     source.Span
}

// StructDecl is a checked struct. Fields are in declaration
// order, which is the layout order.
type StructDecl struct {
	Generics []GenericParam
	Fields   []FieldDecl
	View     bool
	// MutView is true for `mut view struct`: a view that writes or stores into
	// hidden storage through `&self`. MutView implies View.
	MutView bool
	Linear  bool
	// Packed is 0 for a padded struct, else the alignment cap: 1 for bare
	// packed, n for packed(n).
	Packed uint32
	// Invariants are the invariant members, each a MIR body over self, by the
	// item that holds it (an item of kind KindFunc owned by the struct).
	Invariants []ItemID
	// Shape is the struct's shape term over its generics, filled by the
	// shape pass.
	Shape *ShapeTerm
	// Align is n of align(n), 0 when absent: the struct's alignment is
	// max(n, natural) and its size a multiple of it.
	Align uint32
}

// VariantDecl is one enum variant: its fields and its discriminant (the
// declared value as the tag type's bit pattern, else the previous plus one,
// starting at 0).
type VariantDecl struct {
	Name   source.Symbol
	Fields []FieldDecl
	Discr  uint64
	Span   source.Span
}

// EnumDecl is a checked enum.
type EnumDecl struct {
	Generics []GenericParam
	Variants []VariantDecl
	// Tag is the declared tag type (enum E: u8), NoType when absent.
	Tag   TypeID
	Shape *ShapeTerm
	// Open is the declaration's word open: outside its package a match
	// counts one more variant, which no pattern names.
	Open bool
}

// ErrorDecl is a checked error: its payload fields, plain and reference-free.
type ErrorDecl struct {
	Fields []FieldDecl
	// Code is ErrorCode(Key) of the item, cached.
	Code uint32
}

// InterfaceDecl is a checked interface; method signatures may mention KSelf.
type InterfaceDecl struct {
	Generics []GenericParam
	Methods  []Signature
}

// ImplDecl is a checked impl. Bound is the interface implemented, or the
// built-in bound an unsafe impl asserts (gap 13).
type ImplDecl struct {
	Generics []GenericParam
	Bound    Bound
	For      TypeID
	Methods  []ItemID
	// Unsafe is true for unsafe impl, which is how a built-in bound (Send,
	// Share) is asserted.
	Unsafe bool
}

// AliasDecl is a checked alias, resolved away at every use (gap 5: it may
// have generics, expanded by substitution).
type AliasDecl struct {
	Generics []GenericParam
	Type     TypeID
	// Open is the declaration's word open, on an alias of an error set:
	// written only in the package that declares every member and in a
	// module that declares one of them, it makes open, outside that
	// package, every set that holds all its members, itself, its twins and
	// a set that names it or holds more, which a match then counts one more
	// member of.
	Open bool
}

// DefaultFuel is the fuel of a const with no fuel clause.
const DefaultFuel = 4_000_000

// ConstDecl is a checked const: its type, the item whose MIR is the
// initializer, its fuel, and its value once evaluated.
type ConstDecl struct {
	Type     TypeID
	Fuel     uint64
	Value    ConstID // NoConst until evaluated
	HasValue bool
	// Section is the object-file section of a section clause, "" when
	// absent.
	Section string
}

// GlobalDecl is a checked var global: one instance of a Share type, reached
// only through &. Its initializer is evaluated at compile time. With Extern
// it is an extern var: defined outside Mk, no initializer, and
// reading it or taking its address needs unsafe (E1101).
type GlobalDecl struct {
	Type     TypeID
	Value    ConstID
	HasValue bool
	// Extern is true for an extern var.
	Extern bool
	// Section is the section clause's string, "" when absent.
	Section string
}

// FuncDecl is a checked function, method, extern or fn literal.
type FuncDecl struct {
	Sig Signature
	// Bodiless is true for a bodiless extern, a std intrinsic, an interface
	// method, or a test-prelude declaration.
	Bodiless bool
	// Intrinsic is true for a bodiless std function the compiler implements.
	Intrinsic bool
	// Naked is true for a naked function: no prologue, a body of one unsafe
	// block holding one asm.
	Naked bool
	// Section is the section clause's string, "" when absent.
	Section string
}

// TestDecl is a checked test block.
type TestDecl struct {
	Name string
	// TakesSystem is true for test "x" (sys: &System) { }.
	TakesSystem bool
}

// GenArg is one argument of a gen line: a type, an interface, a literal, or
// a non-generic function. Item is the interface or the function.
type GenArg struct {
	Kind  GenArgKind
	Type  TypeID
	Item  ItemID
	Value ConstID
}

// GenArgKind tags a GenArg.
type GenArgKind uint8

const (
	GenType GenArgKind = iota
	GenInterface
	GenLiteral
	// GenFunction is a non-generic function named as a gen argument: Item is
	// the function, and the generator receives it as a Method whose Receiver
	// is Static (NoReceiver); the driver records a read of its signature,
	// never of its body (a public fact never depends on a function body).
	// Appended last, as iface encodes the kind as a byte.
	GenFunction
)

// GenDecl is a checked gen line: the generator function, its arguments, fuel.
type GenDecl struct {
	Generator ItemID
	Args      []GenArg
	Fuel      uint64
}

func (*StructDecl) isDecl()    {}
func (*EnumDecl) isDecl()      {}
func (*ErrorDecl) isDecl()     {}
func (*InterfaceDecl) isDecl() {}
func (*ImplDecl) isDecl()      {}
func (*AliasDecl) isDecl()     {}
func (*ConstDecl) isDecl()     {}
func (*GlobalDecl) isDecl()    {}
func (*FuncDecl) isDecl()      {}
func (*TestDecl) isDecl()      {}
func (*GenDecl) isDecl()       {}

// KnownItems are the library items the rules name, found by
// their stable keys in std by one resolver function. NoItem when the prelude
// is absent, and the rule that needs it is inert.
//
// Atomic is std.raw.Atomic, the "atomic" of the Freeze rule. Scope is the
// one item of the Scope and Scope[E] family. TypeInfo is the generator API's
// type handle, the item std declares for the generator API's Type; there is
// no separate Type field. Target is the const item of type TargetInfo whose
// value the driver sets per build.
type KnownItems struct {
	Arena, Alloc, Heap, Cell, Scope, Io, System            ItemID
	Slice, Format, Eq, Ord, Hash, Writer                   ItemID
	Mutex, Chan, SpinCore, Atomic, Box                     ItemID
	OutOfMemory, Canceled, Closed                          ItemID
	Gen, TypeInfo, TargetInfo, Arch, Os, Limits, StackSize ItemID
	// The rest of the generator API, compile-time only.
	Field, Variant, Method, Param, Code, Block, TypeKind, Receiver ItemID
	// Flow and FlowKind are the flow clauses of a Method, appended after
	// Receiver.
	Flow, FlowKind ItemID
	// Target is the per-build const of type TargetInfo.
	Target ItemID
	// Formatting and C strings: check's format check and E1101 rule, lower's
	// Print synthesis.
	Print, ArenaFmt, FmtRun, FmtArena, FmtValue, FmtPiece, FmtArg, StrFromC ItemID
	// The compiler-known SIMD items of std.simd: the vector and mask
	// structs, the lane bound, and the feature set Cpu() returns.
	Vec, Mask, Lane, CpuFeatures ItemID
}
