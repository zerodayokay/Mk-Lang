// Why: a module's resolution runs in several passes and many parallel tasks;
// their shared state, and the helpers every pass uses to build diagnostics
// and fixes, need one home.
//
// What: Module (the per-module state), the item records (itemRec, genParam,
// nsEntry), fileState, task, fixup, pendingName and candidate, and the
// helpers keywordSpan, lineSpan, dup, nextFree, naming, setRef, primTypes
// and declarable.
//
// How: Begin fills the records and namespaces once; each Finish task writes
// only its own slot (refs, pending names, fixups, habits), and the slots
// are merged in key order. Fixes that need every reference are kept as
// fixups and completed after the walks.

package resolve

import (
	"sort"
	"strconv"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/types"
)

// Module is the resolution of one module in progress, then its result.
type Module struct {
	s       *Session
	in      *ModuleInput
	mod     *types.Module
	rec     *modRecord
	prelude *preludeScope
	info    *Info
	files   []*fileState
	// recs are the module's item records in declaration order (generated and
	// lifted ones appended).
	recs  []*itemRec
	byID  map[types.ItemID]*itemRec
	tasks []*task
	// ns are the type namespaces of the module's type items: folded member
	// name to the first declaration's span (E0206).
	ns map[types.ItemID]map[string]nsEntry
	// keys are the stable keys this module declared, so a repeat is E0206
	// before AddItem's ICE.
	keys     map[types.Key]source.Span
	tests    map[string]source.Span
	genCount int
	hasGen   bool
	// preludeGen is whether the prelude module had unsettled gen lines when
	// this module began: a bare miss is then pending too.
	preludeGen bool
	finished   bool
	settled    bool
	// genNames are the module-level names generated files added (Settle's
	// re-resolution); genDiags the diagnostics of AddGenerated, which
	// Settle returns.
	genNames []source.Symbol
	genDiags []*diag.Diagnostic
	adding   bool
	// fixups are recase and rename fixes completed after the walks, when
	// every reference is known (E0206 and E0207 fixes).
	fixups []fixup
	// pendOwners are the Type.Name methods whose owner Settle decides.
	pendOwners []pendOwner
	// imported caches the scope records of the modules this module's files
	// bind and its prelude, so a worker reads them without the session lock
	// (written once at the end of Begin).
	imported map[types.ModuleID]*modRecord
}

// nsEntry is one member name of a type namespace.
type nsEntry struct {
	name string
	at   source.Span
	// file is the declaring file's index, for the later-wins rule.
	file int
	// method marks a Type.Name or impl method: an exact repeat of one is the
	// repeated-key case, not a namespace collision.
	method bool
}

// fileState is one file of the module.
type fileState struct {
	fi          *FileInfo
	idx         int
	testPrelude bool
	// origins are a generated file's name rules by token offset.
	origins map[uint32]NameOrigin
	// generated reports the tasks of a generated file.
	generated bool
}

// itemRec is one declared item and what its task walks.
type itemRec struct {
	info   *ItemInfo
	item   *types.Item
	node   ast.Node
	file   int
	parent *itemRec
	// deps are the dependents resolved inside this item's task: invariants of
	// a struct, contract predicates of a function, methods of an impl.
	deps []*itemRec
	// gens are the generic parameters in scope for the declaration, owner
	// arguments first. Empty for most items.
	gens []genParam
	// contractN counts requires and ensures per kind, for ContractKey.
	litN int
	task *task
}

// genParam is one generic parameter of a declaration.
type genParam struct {
	sym   source.Symbol
	idx   int
	item  types.ItemID
	at    source.Span
	konst bool
	// owner marks an owner argument (Stack2[T].Push's T, an impl's params).
	owner bool
	// inherited marks a generic the item takes from its impl: checked when
	// the impl is walked, not again for each method.
	inherited bool
	// kindPending marks an owner argument whose owner is pending: its
	// kind is unknown until Settle, so its case is not judged before.
	kindPending bool
}

// task is one unit of Finish: a declaration that is not a
// dependent, with its dependents and the literals lifted while walking it.
type task struct {
	root  *itemRec
	file  int
	diags []*diag.Diagnostic
	used  []bool
	// quoted are the names, bound by an import the module's quotes see
	// (lit), that a quote of this task writes as the root of a literal
	// name: the imports they resolve through are used.
	quoted   []source.Symbol
	pending  []pendingName
	literals []*itemRec
	// pastModule are the spellings this task found exactly past the module
	// scope (imports, prelude, universe), for Settle's re-resolution.
	pastModule map[source.Symbol]bool
	// casePend are the E0207s of pattern bindings whose names a gen line
	// may still declare: Settle reports them, or reruns the task when the
	// name has appeared (patternCase).
	casePend []casePend
	fixups   []fixup
	// habits are the task's habit diagnostics, merged per line.
	habits []*diag.Diagnostic
	// failed marks a task whose walk raised an ICE: its nodes are RefPoison
	// and Settle retries none of its names.
	failed bool
}

// casePend is one deferred E0207 of a pattern binding.
type casePend struct {
	sym source.Symbol
	d   *diag.Diagnostic
}

// fixup completes a fix once every reference is resolved: every Ident whose
// Ref equals target (in file, or in every file when file is -1) is rewritten
// to text, with the declaration's own edit already in the fix.
type fixup struct {
	d      *diag.Diagnostic
	file   int
	target Ref
	text   string
	why    string
	decl   source.Span
	uses   []source.Span
	// blocked, when set and true once every declaration is known, turns the
	// fix into the alternative alt (a recase whose name is taken).
	blocked func() bool
	alt     string
}

// pendingName is a name that missed in a module with unsettled gen lines, or
// a m.X whose m has them.
type pendingName struct {
	file int
	node ast.NodeID // the node that holds the RefPending
	id   *ast.Ident // the missing name token
	pos  pos
	// mod is the module of a qualified miss m.X, NoModule for a bare name.
	mod    types.ModuleID
	item   *itemRec
	outer  []ast.NodeID // enclosing chain nodes to update (FieldExpr, Path)
	covers []int        // how many segments each outer node covers
	// folded are the folded candidates visible only at the use (locals,
	// type parameters) whose folded spelling equals the name's, computed at
	// the miss (only the matches are kept).
	folded []candidate
	// tail is the next segment when the name is the root of a qualified
	// chain: a root that settles to a module binding looks it up.
	tail *ast.Ident
	// args are the bracket arguments when the name heads an IndexExpr
	// (argsNode) or a PathType's TypeArgs (pathType); argsCovers is how many
	// segments the bracket's X spans.
	args       []ast.TypeOrExpr
	argsNode   ast.NodeID
	argsCovers int
	pathType   bool
}

// candidate is one entry the folded fallback may offer.
type candidate struct {
	name string
	ref  Ref
	at   source.Span // NoFile span for universe and primitive names
	// head is "ok" or "err" for the pattern-head entries of the miss path.
	head bool
	typ  bool // usable in a type or bound position
	// binding is, when ref is RefModule, the index of the import binding in
	// the bindings the use sees (the folded match marks it used).
	binding int
}

// fileOf returns the FileID of the module's i-th file.
func (m *Module) fileID(i int, f *ast.File) source.FileID {
	if i < len(m.mod.Files) {
		return m.mod.Files[i]
	}
	return f.Span().File
}

func newFileInfo(f *ast.File, id source.FileID) *FileInfo {
	return &FileInfo{
		File:     f,
		FileID:   id,
		Refs:     make([]Ref, f.NodeCount+1),
		Readings: make([]Reading, f.NodeCount+1),
	}
}

// str spells a symbol.
func (m *Module) str(s source.Symbol) string { return m.s.in.Str(s) }

// keywordSpan finds the first token of kind at or after off in f: the span
// an unnamed item is reported at.
func keywordSpan(f *ast.File, id source.FileID, off uint32, kind ast.TokenKind) source.Span {
	toks := f.Tokens
	i := sort.Search(len(toks), func(i int) bool { return toks[i].Start >= off })
	for ; i < len(toks); i++ {
		if toks[i].Kind == kind {
			return source.Span{File: id, Start: toks[i].Start, End: toks[i].Start + toks[i].Len}
		}
		if toks[i].Kind == ast.TokEOF {
			break
		}
	}
	return source.Span{File: id, Start: off, End: off}
}

// withSpaceBefore extends a span back over the whitespace before it on its
// line: the edit that deletes ` e` from `else e {` (E0208's fix).
func withSpaceBefore(f *ast.File, s source.Span) source.Span {
	toks := f.Tokens
	j := sort.Search(len(toks), func(i int) bool { return toks[i].Start >= s.Start })
	for j > 0 && toks[j-1].Kind == ast.TokWhitespace {
		j--
		s.Start = toks[j].Start
	}
	return s
}

// lineSpan extends a span to its whole line: back over the indentation
// when only whitespace precedes it, forward to the end of the line, newline
// included. It is the edit that deletes an import line or a statement
// (E0208 fixes).
func lineSpan(f *ast.File, s source.Span) source.Span {
	toks := f.Tokens
	j := sort.Search(len(toks), func(i int) bool { return toks[i].Start >= s.Start })
	for j > 0 && toks[j-1].Kind == ast.TokWhitespace {
		j--
	}
	if j == 0 || toks[j-1].Kind == ast.TokNewline {
		if j < len(toks) {
			s.Start = toks[j].Start
		}
	}
	i := sort.Search(len(toks), func(i int) bool { return toks[i].Start >= s.End })
	for ; i < len(toks); i++ {
		switch toks[i].Kind {
		case ast.TokNewline:
			s.End = toks[i].Start + toks[i].Len
			return s
		case ast.TokEOF:
			s.End = toks[i].Start
			return s
		}
	}
	return s
}

// dup builds an E0206 at the later declaration with its "first" fact.
func dup(at, first source.Span, name string, what string) *diag.Diagnostic {
	d := diag.New(diag.E0206, at, what+" `"+name+"` is declared twice")
	if first.IsValid() {
		d.Loc("first", first)
	}
	return d
}

// nextFree appends the next free digit to a name for the E0206 rename fix.
func nextFree(name string, taken func(string) bool) string {
	for n := 2; n < 100; n++ {
		c := name + strconv.Itoa(n)
		if !taken(c) {
			return c
		}
	}
	return name + "2"
}

// naming builds an E0207 (b) for a declaration that breaks the case rule.
func naming(at source.Span, name, want, rule string) *diag.Diagnostic {
	d := diag.New(diag.E0207, at, "`"+name+"` should be "+rule+": `"+want+"`")
	d.Text("declared", name).Loc("declared_at", at)
	return d
}

// identSpan is an ident's span, the zero Span for nil.
func identSpan(id *ast.Ident) source.Span {
	if id == nil {
		return source.Span{}
	}
	return id.Span()
}

// setRef records a resolution in a file's side table.
func (fs *fileState) setRef(n ast.NodeID, r Ref) {
	if n != ast.NoNode && int(n) < len(fs.fi.Refs) {
		fs.fi.Refs[n] = r
	}
}

func (fs *fileState) setReading(n ast.NodeID, r Reading) {
	if n != ast.NoNode && int(n) < len(fs.fi.Readings) {
		fs.fi.Readings[n] = r
	}
}

// itemRef is the Ref of an item.
func itemRef(id types.ItemID) Ref { return Ref{Kind: RefItem, Segs: 1, A: uint32(id)} }

var poisonRef = Ref{Kind: RefPoison, Segs: 1}

// primTypes maps a primitive symbol to its fixed TypeID. The two orders
// differ (source: i8..str, void, never; types: void, never, bool, char,
// i8..str), so the table is explicit (TestPrimitiveTable).
var primTypes = [...]types.TypeID{
	source.SymI8 - source.SymI8:    types.I8,
	source.SymI16 - source.SymI8:   types.I16,
	source.SymI32 - source.SymI8:   types.I32,
	source.SymI64 - source.SymI8:   types.I64,
	source.SymU8 - source.SymI8:    types.U8,
	source.SymU16 - source.SymI8:   types.U16,
	source.SymU32 - source.SymI8:   types.U32,
	source.SymU64 - source.SymI8:   types.U64,
	source.SymInt - source.SymI8:   types.Int,
	source.SymUint - source.SymI8:  types.Uint,
	source.SymF32 - source.SymI8:   types.F32,
	source.SymF64 - source.SymI8:   types.F64,
	source.SymBool - source.SymI8:  types.Bool,
	source.SymChar - source.SymI8:  types.Char,
	source.SymStr - source.SymI8:   types.Str,
	source.SymVoid - source.SymI8:  types.Void,
	source.SymNever - source.SymI8: types.Never,
}

// primType returns the TypeID of a primitive type name.
func primType(s source.Symbol) (types.TypeID, bool) {
	if !source.IsPrimitiveType(s) {
		return types.NoType, false
	}
	return primTypes[s-source.SymI8], true
}

// declarable reports whether a name may be declared: a universe name other
// than ok and err, or a primitive type name, is the parser's E0102 and binds
// nothing here.
func declarable(s source.Symbol) bool {
	if !s.IsValid() || s == source.SymUnderscore {
		return false
	}
	if source.IsPrimitiveType(s) {
		return false
	}
	if source.IsUniverse(s) && s != source.SymOk && s != source.SymErr {
		return false
	}
	return true
}
