// Why: every symbol codegen emits needs one place that holds its linkage,
// visibility, comdat and section, so two packs never disagree about a name.
//
// What: Module and NewModule, Global, Function, Linkage and Visibility, the
// declarations (Declare, DeclareAs), @llvm.used and @llvm.compiler.used
// (Used, CompilerUsed), and the per-function settings SetDefAttrs and
// SetSection.
//
// How: common types and each symbol's quoted reference are computed once
// and cached; block text comes from a shared slab, so most blocks never
// allocate on their own.

package llvm

import (
	"strconv"
	"strings"
)

// Linkage is an LLVM linkage type.
type Linkage uint8

// Linkage values. Appending is for llvm.used only (Module.Used). Weak is a
// definition another object's strong one replaces at link time (the
// freestanding support pack's hook defaults).
const (
	External Linkage = iota
	Internal
	Private
	LinkOnceODR
	Appending
	Weak
)

func (l Linkage) String() string {
	switch l {
	case Internal:
		return "internal"
	case Private:
		return "private"
	case LinkOnceODR:
		return "linkonce_odr"
	case Appending:
		return "appending"
	case Weak:
		return "weak"
	}
	return ""
}

func (l Linkage) local() bool { return l == Internal || l == Private }

// Visibility is an LLVM visibility style.
type Visibility uint8

// Visibility values.
const (
	Default Visibility = iota
	Hidden
	// Protected is ELF's STV_PROTECTED: exported, but the defining module
	// binds its own references to its own definition, and a linker refuses
	// a copy relocation of it.
	Protected
)

func (v Visibility) String() string {
	switch v {
	case Hidden:
		return "hidden"
	case Protected:
		return "protected"
	}
	return ""
}

// DLLStorage is an LLVM DLL storage class (COFF only).
type DLLStorage uint8

// DLLStorage values.
const (
	NoDLLStorage DLLStorage = iota
	DLLExport
)

func (d DLLStorage) String() string {
	if d == DLLExport {
		return "dllexport"
	}
	return ""
}

// CallingConv is an LLVM calling convention. The zero value is the platform C
// ABI (ccc, omitted in printed IR).
type CallingConv uint8

// CallingConv values.
const (
	CCC CallingConv = iota
	PreserveNoneCC
)

func (cc CallingConv) String() string {
	if cc == PreserveNoneCC {
		return "preserve_nonecc"
	}
	return ""
}

// GlobalAttrs describes a global variable. A global with a zero Init is a
// declaration and must have External linkage; Type is its value type.
type GlobalAttrs struct {
	Linkage     Linkage
	Visibility  Visibility
	DSOLocal    bool
	Constant    bool // constant, else global
	UnnamedAddr bool
	Align       uint32 // 0: no align clause
	Type        Type
	Init        Value
	Section     string
	// Comdat puts a defined global in the comdat of that name (Module.Comdat
	// declares it): on COFF a global in another symbol's comdat is an
	// associative section, kept exactly when that comdat is.
	Comdat string
	// Associated is a function or global whose section this global's section
	// depends on: !associated, which ELF prints as SHF_LINK_ORDER, so
	// --gc-sections drops the global with it. Zero for none.
	Associated Value
}

// Global is a global variable of a Module.
type Global struct {
	m     *Module
	name  string
	ref   string // "@" + Quote(name), computed once (one Quote per name)
	a     GlobalAttrs
	assoc MD // the !associated node of a.Associated
}

// Name is the global's symbol, unquoted.
func (g *Global) Name() string { return g.name }

// SetInit sets the initializer of a defined global after creation, for
// globals that refer to each other; v has the global's value type.
func (g *Global) SetInit(v Value) {
	if v.Ty != g.a.Type {
		panic("llvm: SetInit of @" + g.name + ": " + v.String() + " is not " + g.a.Type.String())
	}
	g.a.Init = v
}

// FuncSig is a function's LLVM signature and attributes. ParamAttrs is
// either empty or one Attrs per parameter.
type FuncSig struct {
	Linkage    Linkage
	Visibility Visibility
	DSOLocal   bool
	// DLLStorage is the COFF DLL storage class, printed after the
	// visibility: DLLExport for a dynamic library's export.
	DLLStorage DLLStorage
	// CC is the calling convention; zero is the platform C ABI (omitted).
	CC         CallingConv
	Ret        Type
	RetAttrs   Attrs
	Params     []Type
	ParamAttrs []Attrs
	Variadic   bool
	FnAttrs    Attrs // nounwind, noreturn, cold, hot, inlinehint, "key"="value"
}

type alias struct {
	name    string
	linkage Linkage
	vis     Visibility
	valueTy Type
	target  Value
}

// Module is one .ll file. Not safe for concurrent use; one per pack per
// worker. Printing is deterministic: the same calls give the same bytes.
type Module struct {
	sourceFilename, triple, dataLayout string

	types   *typeTable
	names   map[string]bool // every global symbol defined or declared
	globals []*Global
	funcs   []*Function // definitions and declarations, in creation order
	decls   map[string]*Function
	// byName are the module's functions, defined or declared, by name
	// (DeclareAs).
	byName  map[string]*Function
	aliases []alias
	asm     []string
	used    []Value
	// cused is @llvm.compiler.used (CompilerUsed); comdats the comdat
	// names in creation order (Comdat).
	cused   []Value
	comdats []string
	// comdatSet holds the names of comdats, so Comdat and SetComdat are
	// O(1) (a pack declares one per function in some profiles).
	comdatSet map[string]bool
	meta      *Meta
	// dbgDeclare: some block called llvm.dbg.declare (dbg.go), so the
	// module prints its declaration after the functions.
	dbgDeclare bool
	// noaliasDecl: some block called llvm.experimental.noalias.scope.decl
	// (alias.go), so the module prints its declaration after the functions.
	noaliasDecl bool

	// Caches: the common types (no map lookup or string per
	// use) and WriteTo's chunk buffer.
	ints                    [129]Type
	ptrT, voidT, fltT, dblT Type
	pbuf                    []byte
	tsc                     []byte // type text scratch
	slab                    []byte // backing of new blocks' text (newText)
}

// blockText is the initial capacity of a block's text, and slabSize the
// size of the slab new blocks take it from: most blocks never allocate on
// their own; a longer block grows by append.
const (
	blockText = 512
	slabSize  = 64 << 10
)

// newText is an empty buffer of capacity blockText cut from the slab.
func (m *Module) newText() []byte {
	if len(m.slab)+blockText > cap(m.slab) {
		m.slab = make([]byte, 0, slabSize)
	}
	n := len(m.slab)
	m.slab = m.slab[:n+blockText]
	return m.slab[n : n : n+blockText]
}

// NewModule starts an empty module. sourceFilename is the pack name, never
// an absolute path; triple and dataLayout are clang's for the
// target, printed as given.
func NewModule(sourceFilename, triple, dataLayout string) *Module {
	m := &Module{sourceFilename: sourceFilename, triple: triple, dataLayout: dataLayout,
		types: newTypeTable(), names: map[string]bool{}, decls: map[string]*Function{},
		byName: map[string]*Function{}}
	m.meta = &Meta{m: m, uniq: map[string]MD{}}
	return m
}

func (m *Module) claim(name, what string) {
	if name == "" {
		panic("llvm: " + what + " with an empty name")
	}
	if m.names[name] {
		panic("llvm: " + what + ": symbol @" + Quote(name) + " already defined")
	}
	m.names[name] = true
}

// AddGlobal defines (or, with a zero Init, declares) a global variable.
func (m *Module) AddGlobal(name string, g GlobalAttrs) *Global {
	m.checkType(g.Type, "AddGlobal @"+name)
	if g.Init.IsZero() {
		if g.Linkage != External {
			panic("llvm: AddGlobal @" + name + ": a declaration must have external linkage")
		}
	} else if g.Init.Ty != g.Type {
		panic("llvm: AddGlobal @" + name + ": initializer " + g.Init.String() + " is not " + g.Type.String())
	}
	if g.Linkage.local() && g.Visibility != Default {
		panic("llvm: AddGlobal @" + name + ": local linkage needs default visibility")
	}
	if g.Comdat != "" {
		if g.Init.IsZero() {
			panic("llvm: AddGlobal @" + name + ": a declaration has no comdat")
		}
		m.checkComdat(g.Comdat, func() string { return "AddGlobal @" + name })
	}
	var assoc MD
	if !g.Associated.IsZero() {
		m.checkValue(g.Associated, "AddGlobal @"+name)
		if !g.Associated.Ty.IsPtr() || g.Init.IsZero() {
			panic("llvm: AddGlobal @" + name + ": !associated needs a definition and a global or function")
		}
		assoc = m.meta.Tuple(MDVal(g.Associated))
	}
	m.claim(name, "AddGlobal")
	gl := &Global{m: m, name: name, ref: "@" + Quote(name), a: g, assoc: assoc}
	m.globals = append(m.globals, gl)
	return gl
}

func (m *Module) newFunction(name string, sig FuncSig, decl bool) *Function {
	what := "function @" + Quote(name)
	m.checkType(sig.Ret, what)
	if len(sig.ParamAttrs) != 0 && len(sig.ParamAttrs) != len(sig.Params) {
		panic("llvm: " + what + ": ParamAttrs length differs from Params")
	}
	m.checkAttrs(sig.RetAttrs, what)
	m.checkAttrs(sig.FnAttrs, what)
	for _, a := range sig.ParamAttrs {
		m.checkAttrs(a, what)
	}
	if sig.CC > PreserveNoneCC {
		panic("llvm: " + what + ": unknown calling convention")
	}
	if sig.Linkage.local() && sig.Visibility != Default {
		panic("llvm: " + what + ": local linkage needs default visibility")
	}
	if decl && sig.Linkage != External {
		panic("llvm: " + what + ": a declaration must have external linkage")
	}
	f := &Function{m: m, name: name, ref: "@" + Quote(name), sig: sig, decl: decl, labels: map[string]bool{},
		intrinsic: strings.HasPrefix(name, "llvm.")}
	f.ty = m.FuncType(sig.Ret, sig.Params, sig.Variadic)
	f.params = make([]Value, len(sig.Params))
	for i, p := range sig.Params {
		f.params[i] = Value{Ty: p, kind: vkParam, n: int64(i)}
	}
	return f
}

// AddFunction defines a function; its body is built with NewBlock.
func (m *Module) AddFunction(name string, sig FuncSig) *Function {
	f := m.newFunction(name, sig, false)
	m.claim(name, "AddFunction")
	m.funcs = append(m.funcs, f)
	m.byName[name] = f
	return f
}

// Declare declares an external function, deduplicated by name: a second
// Declare of the same name returns the first, and panics if its type or
// calling convention differs. Declaring a name the module defines is an error.
func (m *Module) Declare(name string, sig FuncSig) *Function {
	if f, ok := m.decls[name]; ok {
		if f.ty != m.FuncType(sig.Ret, sig.Params, sig.Variadic) {
			panic("llvm: Declare @" + Quote(name) + " with a second type " +
				m.FuncType(sig.Ret, sig.Params, sig.Variadic).String() + ", first " + f.ty.String())
		}
		if f.sig.CC != sig.CC {
			panic("llvm: Declare @" + Quote(name) + " with a second calling convention")
		}
		return f
	}
	f := m.newFunction(name, sig, true)
	m.claim(name, "Declare")
	m.decls[name] = f
	m.funcs = append(m.funcs, f)
	m.byName[name] = f
	return f
}

// Has reports whether the module defines or declares a global symbol name.
func (m *Module) Has(name string) bool { return m.names[name] }

// DeclareAs is Declare for a C symbol that several declarations of one module
// may name with different types: two Mk externs of one C name, or a Mk extern
// of a name codegen calls itself, such as memcmp. When the module already
// declares or defines a function name of another type, DeclareAs returns a
// view of it: a Function with sig's type and attributes and the same symbol,
// which is never printed (the first declaration or the definition is). A call
// through the view prints its own function type, which LLVM's opaque pointers
// allow; its address is ptr @name. Same type, or no function of that name: as
// Declare. A view has no body, parameters' values or comdat.
func (m *Module) DeclareAs(name string, sig FuncSig) *Function {
	f, ok := m.byName[name]
	if !ok {
		return m.Declare(name, sig)
	}
	if f.ty == m.FuncType(sig.Ret, sig.Params, sig.Variadic) && f.sig.CC == sig.CC {
		return f
	}
	return m.newFunction(name, sig, true) // not in funcs: never printed
}

// AddAlias defines @name as an alias of target (a global or function
// address, possibly a ConstGEP of one) whose value type is valueTy.
func (m *Module) AddAlias(name string, linkage Linkage, vis Visibility, valueTy Type, target Value) {
	m.checkType(valueTy, "AddAlias")
	m.checkValue(target, "AddAlias")
	if !target.Ty.IsPtr() {
		panic("llvm: AddAlias @" + name + " of " + target.String())
	}
	if linkage.local() && vis != Default {
		panic("llvm: AddAlias @" + name + ": local linkage needs default visibility")
	}
	m.claim(name, "AddAlias")
	m.aliases = append(m.aliases, alias{name, linkage, vis, valueTy, target})
}

// Used adds pointer constants (functions or globals) to @llvm.used, which
// keeps them from being discarded by the optimizer (the memory functions of
// the support pack). Printed once, in call order, as an appending global in
// section "llvm.metadata".
func (m *Module) Used(vs ...Value) {
	for _, v := range vs {
		m.checkValue(v, "Used")
		if !v.Ty.IsPtr() {
			panic("llvm: Used of " + v.String())
		}
	}
	m.used = append(m.used, vs...)
}

// Metadata is the module's numbered and named metadata.
func (m *Module) Metadata() *Meta { return m.meta }

// Function is a defined or declared function of a Module.
type Function struct {
	m    *Module
	name string
	ref  string // "@" + Quote(name), computed once
	// intrinsic is an llvm.* declaration, whose immarg is not repeated at
	// call sites.
	intrinsic bool
	sig       FuncSig
	ty        Type
	decl      bool
	params    []Value
	blocks    []*Block
	labels    map[string]bool
	nextV     int
	prof      MD
	dbg       MD
	comdat    string // SetComdat
	section   string // SetSection
	defAttrs  Attrs  // SetDefAttrs: printed on the definition only
}

// Name is the function's symbol, unquoted.
func (f *Function) Name() string { return f.name }

// Type is the function's LLVM function type, what Call takes as fnTy.
func (f *Function) Type() Type { return f.ty }

// Sig is the signature the function was made with.
func (f *Function) Sig() FuncSig { return f.sig }

// Ref is the function's address, ptr @name.
func (f *Function) Ref() Value { return f.m.FuncRef(f) }

// Param is parameter i, printed %p<i>.
func (f *Function) Param(i int) Value {
	if i < 0 || i >= len(f.params) {
		panic("llvm: Param " + strconv.Itoa(i) + " of @" + Quote(f.name))
	}
	return f.params[i]
}

// SetProf attaches !prof to the definition (function_entry_count).
func (f *Function) SetProf(md MD) { f.m.meta.check(md, "SetProf"); f.prof = md }

// SetDbg attaches !dbg to the definition (its DISubprogram).
func (f *Function) SetDbg(md MD) { f.m.meta.check(md, "SetDbg"); f.dbg = md }

// isReservedLabel reports whether label has the form of a value name the
// builder generates (p<digits> or v<digits>); labels and values share one
// namespace per function in LLVM.
func isReservedLabel(label string) bool {
	if len(label) < 2 || (label[0] != 'p' && label[0] != 'v') {
		return false
	}
	for i := 1; i < len(label); i++ {
		if label[i] < '0' || label[i] > '9' {
			return false
		}
	}
	return true
}

// NewBlock appends a basic block. Labels are unique per function and are
// printed in creation order; the first block is the entry block. A label
// of the form p<n> or v<n> is reserved for values.
func (f *Function) NewBlock(label string) *Block {
	if f.decl {
		panic("llvm: NewBlock on the declaration @" + Quote(f.name))
	}
	if label == "" || f.labels[label] || isReservedLabel(label) {
		panic("llvm: NewBlock: label " + strconv.Quote(label) + " empty, duplicate or reserved in @" + Quote(f.name))
	}
	f.labels[label] = true
	b := &Block{f: f, label: label, ref: "%" + Quote(label), text: f.m.newText()}
	f.blocks = append(f.blocks, b)
	return b
}

func (f *Function) newValue(t Type) Value {
	v := Value{Ty: t, kind: vkLocal, n: int64(f.nextV)}
	f.nextV++
	return v
}

// Comdat declares the comdat name, printed $name = comdat nodeduplicate (a
// second definition of its leader is a link error, as for any definition);
// declaring a name twice is a no-op. A function enters one with SetComdat,
// a global with GlobalAttrs.Comdat.
func (m *Module) Comdat(name string) {
	if m.comdatSet[name] {
		return
	}
	if m.comdatSet == nil {
		m.comdatSet = map[string]bool{}
	}
	m.comdatSet[name] = true
	m.comdats = append(m.comdats, name)
}

// checkComdat panics unless comdat name is declared; what names the caller
// (built only for the panic).
func (m *Module) checkComdat(name string, what func() string) {
	if !m.comdatSet[name] {
		panic("llvm: " + what() + ": comdat $" + Quote(name) + " is not declared")
	}
}

// SetDefAttrs sets function attributes printed on the definition after its
// signature's, and never at a call site, which CallFunc gives the
// signature's only. A later call replaces the list. It is for a fact known
// only once the body is emitted: codegen's Win64 probe of a frame aligned
// over a page.
func (f *Function) SetDefAttrs(a Attrs) {
	if f.decl {
		panic("llvm: SetDefAttrs of declaration " + f.ref)
	}
	f.m.checkAttrs(a, "SetDefAttrs of "+f.ref)
	f.defAttrs = a
}

// SetSection places a defined function in section name, printed
// section "name" after its attributes.
func (f *Function) SetSection(name string) {
	if f.decl {
		panic("llvm: SetSection of declaration " + f.ref)
	}
	f.section = name
}

// SetComdat puts a defined function in the declared comdat name, printed
// comdat($name) after its attributes.
func (f *Function) SetComdat(name string) {
	if f.decl {
		panic("llvm: SetComdat of declaration " + f.ref)
	}
	f.m.checkComdat(name, func() string { return "SetComdat of " + f.ref })
	f.comdat = name
}

// CompilerUsed adds pointer constants to @llvm.compiler.used: kept by the
// optimizer (ThinLTO's dead stripping included) but not marked as used for
// the linker, so an associated section can still be discarded with its
// function (no SHF_GNU_RETAIN, no /INCLUDE).
func (m *Module) CompilerUsed(vs ...Value) {
	for _, v := range vs {
		m.checkValue(v, "CompilerUsed")
		if !v.Ty.IsPtr() {
			panic("llvm: CompilerUsed of " + v.String())
		}
	}
	m.cused = append(m.cused, vs...)
}
