// Why: a package's manifest, pkg.mk, is Mk code: its `const Package` must
// be checked and evaluated by the compiler itself before the build knows
// its modules, dependencies or bins, and its errors must be reported alone.
//
// What: Manifest and its parts (Bin, TargetSel, CImport, Link), the field
// tables (manifestFields, manifestFieldsExt), and the manifest step
// (manifestStep, newManifestStep, load, checkPkg, round, decode with its
// readers for fields, strings, slices, bins, deps and links).
//
// How: std.pkg is checked once per session, alone; std/pkg.mk and pkg.mk
// are one-file modules whose only binding is the implicit `pkg` for
// std.pkg. Each is checked through stage 6 and its `const Package`
// evaluated (with no Io), then decoded by the layout of std.pkg.Package
// (strings and slices through their relocations) and validated
// (manifest_step.go). Newer fields are read by name, so an older std.pkg
// decodes them empty.

package driver

import (
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"sync"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/resolve"
	"mk/internal/source"
	"mk/internal/types"
)

// Manifest is a decoded pkg.mk.
type Manifest struct {
	Name          string
	Bins          []Bin       // name order
	UnsafeModules []string    // package-relative directories, "/" separators
	Targets       []TargetSel // the targets field: per-OS module directories
	// Deps are the pkg.Dep values, in the list's order; deps.go walks
	// them.
	Deps []Dep
	// CImports, CSources and AsmSources are the C fields, in the lists'
	// order; cimport.go reads them.
	CImports   []CImport
	CSources   []CSource
	AsmSources []AsmSource
	// Links are the pkg.Link values, in the list's order; empty when the
	// field is `links: []` or this std.pkg has no such field. links.go
	// reads them.
	Links []Link
	// Mk, TestDeps, UnsafeDeps, Members and Replace are the newest fields
	// at the end of pkg.Package (doc/packages.md), each read by name, so a
	// std.pkg without one decodes it empty: the oldest mk that builds the
	// package ("" for none), the test-only dependencies, the fetched
	// packages granted unsafe code, the workspace's member directories and
	// the sources replaced in the whole build. Members and Replace are
	// read in the root manifest only.
	Mk         string
	TestDeps   []Dep
	UnsafeDeps []string
	Members    []string
	Replace    []Dep
	// Deferred are the fields with a non-empty value that this build does
	// not support; each draws E0202. No field is deferred.
	Deferred []string
	// Fields maps a field path to the hash of its value's canonical text
	// (ReadPkg); read by key only.
	Fields map[string]source.Hash
	Span   source.Span // the const Package item
	// spans holds the source span of every field path the initializer writes
	// as a literal (a named argument, an array element), for the validation
	// diagnostics; spanOf falls back to the nearest enclosing path, then to
	// Span.
	spans map[string]source.Span
	// binSrc is each bin's index in the value's list, parallel to Bins
	// (which is in name order), so a bin's field path is bins[<index>].
	binSrc []int
}

// Bin is one pkg.Bin. LinkScript and CPU are its link_script and cpu fields: ""
// when the manifest's std.pkg has no such field yet (free.go). Subsystem is
// "console" (or "") or "windows". Kind and ABI are the library outputs: "" (an
// executable), "dynamic" or "static", and the ABI number ("" is "1");
// libout.go gives them meaning.
type Bin struct{ Name, Root, LinkScript, CPU, Subsystem, Kind, ABI string }

// TargetSel is one pkg.Select: the module directories of one OS.
type TargetSel struct {
	OS      string
	Modules []ModuleSel
}

// ModuleSel is one pkg.Module: an import name and its directory.
type ModuleSel struct{ Name, Dir string }

// CImport is one pkg.CImport (its import path and macros); Field is its
// field path, c_imports[<index>]. IncludeDirs are package-relative, as
// written.
type CImport struct {
	Name                     string
	Headers, Defines, Macros []string
	Vendor                   bool
	IncludeDirs              []string
	SDK                      bool // the headers are read with the host's SDK (hostsdk.go)
	Field                    string
}

// CSource is one pkg.CSource: a package-relative .c file and its flags.
type CSource struct {
	Path  string
	Flags []string
	Field string
}

// AsmSource is one pkg.AsmSource: a package-relative .s or .S file and the
// target it is assembled for ("" for every target).
type AsmSource struct {
	Path, Target, Field string
}

// Link is one pkg.Link: a library the package's extern functions
// resolve to, for one target. Field is its field path, links[<index>].
type Link struct{ Name, Kind, Path, Target, Field string }

// Dep is one pkg.Dep: exactly one of Path, URL and Source set, Hash with
// URL, Version exactly with Source (validateDeps). Field is its field path,
// deps[<index>] (or test_deps[<index>], replace[<index>]).
type Dep struct{ Name, Path, URL, Hash, Source, Version, Field string }

// manifestFields is every field of pkg.Package, in the reference's order;
// each is required. links is read by name (hasField), so a std.pkg
// without it decodes as `links: []`.
var manifestFields = []string{"name", "bins", "deps", "c_sources", "c_imports", "asm_sources", "targets",
	"unsafe_modules", "links"}

// manifestFieldsExt are the newest fields at the end of pkg.Package, read
// by name (hasField) like links.
var manifestFieldsExt = []string{"mk", "test_deps", "unsafe_deps", "members", "replace"}

// emptyListCanon is canon's text of an empty list, the field hash input of
// a links field this std.pkg lacks.
const emptyListCanon = "[]"

// pkgModule is std.pkg's import path and directory (stdLayout.NotModules:
// the std load never forms it).
const (
	pkgModule = "std.pkg"
	pkgDir    = "pkg"
)

// manifestStep is the manifest step for one session (or one corpus session):
// its own tables (mw), so the manifest's types, items and module IDs never
// enter the build's, with the session's SourceMap and Interner, so its
// diagnostics print with the build's. std.pkg is checked on the first manifest
// that needs it and never again. Safe for concurrent use: corpus units with a
// pkg.mk evaluate it one at a time.
type manifestStep struct {
	w    *world
	mw   *world
	stdT *srcTree

	mu sync.Mutex
	// pkgDone: std.pkg's round ran; pkgMod is std.pkg (nil when std has no
	// pkg directory); pkgDiags its diagnostics, returned by the first load
	// that needed it (the alone rule reports them once).
	pkgDone  bool
	pkgMod   *modNode
	pkgBuild *build
	pkgDiags []*diag.Diagnostic
	// checked are the module paths each round resolved and checked, in
	// order (Trace.ManifestChecked: std.pkg exactly once).
	checked []string
	// loose: load decodes the manifest and checks no directory it names
	// (the version walk reads a version's pkg.mk alone).
	loose bool
	// running is the mk version manifests are compared with; ""
	// is Release. Tests set it, so none depends on the real version.
	running string
	// notes are the step's notes (an ignored members or replace field),
	// printed after the diagnostics.
	notes []string
}

// newManifestStep makes the step's world: the session's SourceMap and
// Interner, tables of its own, no module cache, and stage 7's verdict not
// reported (the step checks a manifest through stage 6; a manifest's
// static slices are its initializer's own data). Only PanicIn of the test
// hooks applies inside.
func (w *world) newManifestStep(stdT *srcTree) *manifestStep {
	cfg := w.cfg
	cfg.TestHooks = TestHooks{PanicIn: w.cfg.TestHooks.PanicIn}
	mw := &world{cfg: cfg, sm: w.sm, in: w.in, items: types.NewItems(), sem: make(chan struct{}, max(cfg.Jobs, 1)),
		mods:    make(chan struct{}, max(cfg.Jobs, 1)),
		maxFile: w.maxFile, noFlow: true}
	mw.cat = &catalog{in: w.in}
	mw.sitePaths = map[source.FileID]string{}
	mw.tab = types.NewTable(w.in, mw.items, types.NewConstTable())
	return &manifestStep{w: w, mw: mw, stdT: stdT}
}

// load reads, checks, evaluates and decodes the manifest at rel in t.
// rootName is the module the package root's files form; "" marks std's
// manifest, which may be missing (then (nil, nil)). Any diagnostic means
// the manifest step failed; the caller reports the step's diagnostics
// alone.
func (ms *manifestStep) load(t *srcTree, rel, rootName string) (*Manifest, []*diag.Diagnostic) {
	required := rootName != ""
	p := t.path(rel)
	raw, err := t.readFile(rel)
	if err != nil {
		if !required && errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, []*diag.Diagnostic{diag.Toolchain("fs", "read "+p, pathErrText(err),
			fmt.Sprintf("cannot read manifest %s", p))}
	}
	ms.mu.Lock()
	defer ms.mu.Unlock()
	src := ms.w.sm.Add(p, raw)
	ms.w.noteSitePath(src.ID, t.sitePath(rel))
	if ds := ms.checkPkg(); len(ds) > 0 {
		return nil, ds
	} else if !ms.pkgOK() {
		return nil, nil // reported by the load that ran std.pkg's round
	}
	if ms.pkgMod == nil {
		return nil, []*diag.Diagnostic{diag.New(diag.E0202, source.Span{File: src.ID},
			"this standard library has no std/pkg directory, so the manifest cannot be evaluated").
			Text("manifest", "Package").Text("reason", "invalid")}
	}
	m := &modNode{name: p, path: p, tree: t, files: []*fileNode{{rel: rel, src: src}}}
	m.implicit = []resolve.Implicit{{Name: ms.w.in.Intern("pkg"), Module: ms.pkgMod.id}}
	b := newBuild(ms.mw, ms.pkgBuild)
	b.keepSyntax = true // decode reads the manifest's AST after the round
	b.addModule(m)
	ms.w.cfg.hook("manifest")
	if ds := ms.round(b, func() { m.deps = append(m.deps, ms.pkgMod) }); len(ds) > 0 {
		return nil, ds
	}
	man, ds := ms.decode(m)
	if len(ds) > 0 {
		return nil, ds
	}
	man.cleanDirs()
	if d := ms.tooOld(man); d != nil {
		return nil, []*diag.Diagnostic{d} // a computed mk the pre-check could not read
	}
	if ms.loose {
		// A manifest read alone for the version walk (deps_source.go): its
		// package's files are not here, so only its decoded value counts.
		return man, nil
	}
	if ds := validateManifest(man, t, rootName); len(ds) > 0 {
		return nil, ds
	}
	if ds := validateBins(man, t, ms.w.cfg.Target); len(ds) > 0 {
		return nil, ds
	}
	if required {
		ds, notes := validateManifestExt(man, t, rootName)
		if len(ds) > 0 {
			return nil, ds
		}
		ms.notes = append(ms.notes, notes...)
	}
	return man, nil
}

// pkgOK reports whether std.pkg's round ran clean.
func (ms *manifestStep) pkgOK() bool { return ms.pkgDone && len(ms.pkgDiags) == 0 }

// checkPkg is the step's first part, run once: std/pkg loaded, parsed, resolved
// and checked alone. It returns std.pkg's diagnostics the first time only.
func (ms *manifestStep) checkPkg() []*diag.Diagnostic {
	if ms.pkgDone {
		return nil
	}
	ms.pkgDone = true
	files, err := ms.stdT.mkFiles(pkgDir, true, nil)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		ms.pkgDiags = []*diag.Diagnostic{listFailure(ms.stdT, err)}
		return ms.pkgDiags
	}
	if len(files) == 0 {
		return nil
	}
	b := newBuild(ms.mw, nil)
	m := moduleFrom(ms.stdT, pkgDir, files)
	m.name, m.path, m.std = pkgModule, pkgModule, true
	for _, f := range m.files {
		f.bodiless = true
	}
	b.addModule(m)
	ms.pkgMod, ms.pkgBuild = m, b
	if fails, big := b.readAll(); len(fails) > 0 || len(big) > 0 {
		ms.pkgDiags = append(fails, big...)
		return ms.pkgDiags
	}
	ms.pkgDiags = ms.round(b, nil)
	return ms.pkgDiags
}

// round runs one build of the step through stage 6 and its declaration
// evaluations: the parse step, the import step (a manifest and std.pkg
// import nothing: every import line is E0202), then the per-module
// pipeline. deps, when set, adds the implicit dependencies after the
// import step. The phase rule applies: a names diagnostic leaves the
// types stage unreported.
func (ms *manifestStep) round(b *build, deps func()) []*diag.Diagnostic {
	mw := ms.mw
	if ds := b.parseAll(); len(ds) > 0 {
		return ds
	}
	if deps != nil {
		// A manifest (not std.pkg's own round): E0216 before anything a
		// newer std.pkg's fields would cause, then field omission.
		if ds := ms.minimumFirst(b); len(ds) > 0 {
			return ds
		}
		ms.supplyOmitted(b)
	}
	if ds := b.link(); len(ds) > 0 {
		return ds
	}
	if deps != nil {
		deps()
	}
	b.depths()
	b.register()
	b.preludeFor(nil)
	if mw.rs == nil {
		mw.startResolve(nil)
	}
	if mw.sem5 == nil {
		mw.startSemantics(false)
	}
	b.pipelineAll(true)
	defer b.finish()
	for _, m := range b.mods {
		if m.resolved {
			ms.checked = append(ms.checked, m.path)
		}
	}
	ds := b.stageDiags()
	if !earlierThanTypes(ds) {
		ds = append(ds, b.checkDiags()...)
	}
	addManifestFixes(b, mw.in, ds)
	return ds
}

// decode is the manifest's decoding: the value of m's `const Package`, of
// type std.pkg.Package, read by the layouts check gives std.pkg's structs
// (fields matched by name), strings and slices through their relocations.
// A violation is E0202 at the field, fact manifest the field path.
func (ms *manifestStep) decode(m *modNode) (*Manifest, []*diag.Diagnostic) {
	mw := ms.mw
	file := m.files[0]
	at := source.Span{File: file.src.ID}
	fail := func(sp source.Span, msg string) []*diag.Diagnostic {
		return []*diag.Diagnostic{diag.New(diag.E0202, sp, msg).Text("manifest", "Package").Text("reason", "invalid")}
	}
	id, ok := mw.items.ByKey(types.ItemKey(m.path, "Package"))
	if !ok || mw.items.Get(id).Kind != types.KindConst {
		return nil, fail(at, "the manifest declares no `const Package`")
	}
	it := mw.items.Get(id)
	d, _ := it.Decl.(*types.ConstDecl)
	pkgT, okT := mw.items.ByKey(types.ItemKey(pkgModule, "Package"))
	if d == nil || !okT || d.Type != mw.tab.Named(pkgT, nil) {
		return nil, fail(it.Span, "`Package` must be a `pkg.Package(...)`")
	}
	if !d.HasValue {
		return nil, fail(it.Span, "`Package` has no value") // an evaluation failure is reported before this
	}
	man := &Manifest{Span: it.Span, Fields: map[string]source.Hash{}, spans: map[string]source.Span{"": it.Span}}
	for _, x := range file.ast.Items {
		if c, ok := x.(*ast.ConstDecl); ok && c.Name != nil && mw.in.Str(c.Name.Name) == "Package" {
			manifestSpans(c.Value, "", man.spans, mw.in)
		}
	}
	dec := &manDecoder{w: mw, man: man, ptr: uint64(mw.cfg.Target.PointerBytes)}
	var out []*diag.Diagnostic
	if ie := source.Guard("decode "+m.path, it.Span, func() {
		dec.pkg(cview{v: *mw.tab.Consts().Get(d.Value)}, d.Type)
	}); ie != nil {
		return nil, []*diag.Diagnostic{diag.FromInternal(ie)}
	}
	out = dec.diags
	if len(out) > 0 {
		return nil, out
	}
	man.sortBins()
	return man, nil
}

// manifestSpans records the span of every field path the initializer e
// writes out: each named argument of a call (`bins`, `bins[0].root`) and
// each element of an array literal (`unsafe_modules[1]`). A computed
// value has the span of the expression that computes it.
func manifestSpans(e ast.Expr, path string, out map[string]source.Span, in *source.Interner) {
	switch x := e.(type) {
	case *ast.ParenExpr:
		manifestSpans(x.X, path, out, in)
	case *ast.CallExpr:
		for _, a := range x.Args {
			if a == nil || a.Name == nil || a.Value == nil || a.Span().IsEmpty() {
				// An argument the step supplied (an omitted field) has no text:
				// its field's diagnostics go to the call that left it out.
				continue
			}
			p := in.Str(a.Name.Name)
			if path != "" {
				p = path + "." + p
			}
			out[p] = a.Value.Span()
			manifestSpans(a.Value, p, out, in)
		}
	case *ast.ArrayLit:
		for i, el := range x.Elems {
			p := path + "[" + strconv.Itoa(i) + "]"
			out[p] = el.Span()
			manifestSpans(el, p, out, in)
		}
	}
}

// spanOf is the span of field path p, or of the nearest enclosing path the
// source writes out.
func (m *Manifest) spanOf(p string) source.Span {
	for {
		if sp, ok := m.spans[p]; ok {
			return sp
		}
		i := strings.LastIndexAny(p, ".[")
		if i < 0 {
			return m.Span
		}
		p = p[:i]
	}
}

// sortBins puts Bins in name order, keeping binSrc parallel.
func (m *Manifest) sortBins() {
	order := make([]int, len(m.Bins))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { return strings.Compare(m.Bins[a].Name, m.Bins[b].Name) })
	bins, src := make([]Bin, len(order)), make([]int, len(order))
	for k, i := range order {
		bins[k], src[k] = m.Bins[i], m.binSrc[i]
	}
	m.Bins, m.binSrc = bins, src
}

// binField is the field path of Bins[k] in the value.
func (m *Manifest) binField(k int) string { return "bins[" + strconv.Itoa(m.binSrc[k]) + "]" }

// cview is a value inside an interned const: the const and the offset of
// the value in its bytes (an element of a [N]T pointee).
type cview struct {
	v    types.ConstValue
	base uint64
}

// manDecoder reads a pkg.Package value (decode).
type manDecoder struct {
	w     *world
	man   *Manifest
	ptr   uint64
	diags []*diag.Diagnostic
}

func (d *manDecoder) fail(path, reason, format string, args ...any) {
	d.diags = append(d.diags, diag.New(diag.E0202, d.man.spanOf(path), fmt.Sprintf(format, args...)).
		Text("manifest", path).Text("reason", reason))
}

// field is the offset and type of the field called name of struct type t
// (declaration order is layout order).
func (d *manDecoder) field(t types.TypeID, name string) (uint64, types.TypeID) {
	ty := d.w.tab.Get(t)
	sd, ok := d.w.items.Get(ty.Item).Decl.(*types.StructDecl)
	if ty.Kind != types.KNamed || !ok {
		source.ICEf(source.Span{}, "driver: manifest type %s is no struct", d.w.tab.TypeString(t, nil))
	}
	lay := d.w.sem5.chk.Layout(t)
	for i, f := range sd.Fields {
		if d.w.in.Str(f.Name) == name && i < len(lay.Fields) {
			return lay.Fields[i].Offset, lay.Fields[i].Type
		}
	}
	source.ICEf(source.Span{}, "driver: std.pkg's %s has no field %s", d.w.tab.TypeString(t, nil), name)
	return 0, types.NoType
}

// hasField is field for a field std.pkg's struct type t may lack: its
// offset and true, or false when t has no field called name.
func (d *manDecoder) hasField(t types.TypeID, name string) (uint64, bool) {
	ty := d.w.tab.Get(t)
	sd, ok := d.w.items.Get(ty.Item).Decl.(*types.StructDecl)
	if ty.Kind != types.KNamed || !ok {
		return 0, false
	}
	lay := d.w.sem5.chk.Layout(t)
	for i, f := range sd.Fields {
		if d.w.in.Str(f.Name) == name && i < len(lay.Fields) {
			return lay.Fields[i].Offset, true
		}
	}
	return 0, false
}

// pointee is the const a pointer word at off points at.
func (d *manDecoder) pointee(c cview, off uint64) (types.ConstValue, bool) {
	at := uint32(c.base + off)
	for _, r := range c.v.Relocs {
		if r.Offset == at && r.Kind == types.RelocData {
			return *d.w.tab.Consts().Get(r.Data), true
		}
	}
	return types.ConstValue{}, false
}

// words are a str's or slice's data pointee and length at off.
func (d *manDecoder) words(c cview, off uint64) (types.ConstValue, uint64) {
	n := types.LoadUint(c.v.Bytes[c.base+off+d.ptr:], int(d.ptr))
	if n == 0 {
		return types.ConstValue{}, 0
	}
	p, ok := d.pointee(c, off)
	if !ok {
		source.ICEf(source.Span{}, "driver: manifest slice of length %d with no data", n)
	}
	return p, n
}

// str is the str at off.
func (d *manDecoder) str(c cview, off uint64) string {
	p, n := d.words(c, off)
	return string(p.Bytes[:n])
}

// slice is the elements of the []T at off, each a view into its pointee.
func (d *manDecoder) slice(c cview, off uint64, st types.TypeID) []cview {
	p, n := d.words(c, off)
	size := d.w.sem5.chk.Layout(d.w.tab.Get(st).Elem).Shape.Size
	out := make([]cview, n)
	for i := range out {
		out[i] = cview{v: p, base: uint64(i) * size}
	}
	return out
}

// text is the str field name of the struct at c (of type t), checked
// nonempty, at field path path.
func (d *manDecoder) text(c cview, t types.TypeID, name, path string) string {
	off, _ := d.field(t, name)
	s := d.str(c, off)
	if s == "" {
		d.fail(path, "invalid", "`%s` must not be empty", path)
	}
	return s
}

// strs is the []str field name of the struct at c (of type t).
func (d *manDecoder) strs(c cview, t types.TypeID, name string) []string {
	off, ft := d.field(t, name)
	var out []string
	for _, e := range d.slice(c, off, ft) {
		out = append(out, d.str(e, 0))
	}
	return out
}

// pkg decodes the Package value c of type t into d.man.
func (d *manDecoder) pkg(c cview, t types.TypeID) {
	m := d.man
	d.fieldsExt(c, t)
	for _, name := range manifestFields {
		if name == "links" {
			d.links(c, t)
			continue
		}
		off, ft := d.field(t, name)
		m.Fields[name] = source.HashString("mk.manifest.field.v1", d.canon(c, off, ft))
		switch name {
		case "name":
			m.Name = d.text(c, t, name, name)
		case "bins":
			bt := d.w.tab.Get(ft).Elem
			for i, e := range d.slice(c, off, ft) {
				p := name + "[" + strconv.Itoa(i) + "]"
				bin := Bin{Name: d.text(e, bt, "name", p+".name"), Root: d.text(e, bt, "root", p+".root")}
				// link_script and cpu, read by name so an older std.pkg
				// without them gives "" (free.go).
				if fo, ok := d.hasField(bt, "link_script"); ok {
					bin.LinkScript = d.str(e, fo)
				}
				if fo, ok := d.hasField(bt, "cpu"); ok {
					bin.CPU = d.str(e, fo)
				}
				// subsystem, read by name as cpu is, so a std.pkg
				// without it gives "" (console).
				if fo, ok := d.hasField(bt, "subsystem"); ok {
					bin.Subsystem = d.str(e, fo)
				}
				// kind is "", "dynamic" or "static" ("object" is
				// not built); abi is "" or a decimal number, as it ends
				// the soname and the version node.
				if fo, ok := d.hasField(bt, "kind"); ok {
					bin.Kind = d.str(e, fo)
					if k := bin.Kind; k != "" && k != "dynamic" && k != "static" {
						d.fail(p+".kind", "invalid", "`%s.kind` is %q; it must be \"\" (an executable), \"dynamic\" or \"static\"", p, k)
					}
				}
				if fo, ok := d.hasField(bt, "abi"); ok {
					bin.ABI = d.str(e, fo)
					if a := bin.ABI; a != "" && strings.Trim(a, "0123456789") != "" {
						d.fail(p+".abi", "invalid", "`%s.abi` is %q; it must be \"\" (meaning \"1\") or a decimal number", p, a)
					}
				}
				m.Bins = append(m.Bins, bin)
				m.binSrc = append(m.binSrc, i)
			}
		case "unsafe_modules":
			for i, e := range d.slice(c, off, ft) {
				p := name + "[" + strconv.Itoa(i) + "]"
				s := d.str(e, 0)
				if s == "" {
					d.fail(p, "invalid", "`%s` must not be empty", p)
				}
				m.UnsafeModules = append(m.UnsafeModules, s)
			}
		case "deps":
			m.Deps = d.deps(c, off, ft, name)
		case "targets":
			st := d.w.tab.Get(ft).Elem
			moff, mt := d.field(st, "modules")
			modT := d.w.tab.Get(mt).Elem
			for i, e := range d.slice(c, off, ft) {
				p := name + "[" + strconv.Itoa(i) + "]"
				sel := TargetSel{OS: d.text(e, st, "os", p+".os")}
				for j, me := range d.slice(e, moff, mt) {
					q := p + ".modules[" + strconv.Itoa(j) + "]"
					sel.Modules = append(sel.Modules, ModuleSel{Name: d.text(me, modT, "name", q+".name"),
						Dir: d.text(me, modT, "dir", q+".dir")})
				}
				m.Targets = append(m.Targets, sel)
			}
		case "c_imports":
			et := d.w.tab.Get(ft).Elem
			for i, e := range d.slice(c, off, ft) {
				p := name + "[" + strconv.Itoa(i) + "]"
				ci := CImport{Name: d.text(e, et, "name", p+".name"), Field: p}
				ci.Headers = d.strs(e, et, "headers")
				ci.Defines = d.strs(e, et, "defines")
				ci.Macros = d.strs(e, et, "macros")
				ci.IncludeDirs = d.strs(e, et, "include_dirs")
				vo, _ := d.field(et, "vendor")
				ci.Vendor = e.v.Bytes[e.base+vo] != 0
				if so, ok := d.hasField(et, "sdk"); ok {
					ci.SDK = e.v.Bytes[e.base+so] != 0
				}
				m.CImports = append(m.CImports, ci)
			}
		case "c_sources":
			et := d.w.tab.Get(ft).Elem
			for i, e := range d.slice(c, off, ft) {
				p := name + "[" + strconv.Itoa(i) + "]"
				m.CSources = append(m.CSources, CSource{Path: d.text(e, et, "path", p+".path"), Flags: d.strs(e, et, "flags"), Field: p})
			}
		case "asm_sources":
			et := d.w.tab.Get(ft).Elem
			for i, e := range d.slice(c, off, ft) {
				p := name + "[" + strconv.Itoa(i) + "]"
				to, _ := d.field(et, "target")
				m.AsmSources = append(m.AsmSources, AsmSource{Path: d.text(e, et, "path", p+".path"), Target: d.str(e, to), Field: p})
			}
		}
	}
}

// fieldsExt decodes the newest fields of pkg.Package, each read
// by name: a std.pkg without one reads it empty, with that value's field
// hash.
func (d *manDecoder) fieldsExt(c cview, t types.TypeID) {
	m := d.man
	for _, name := range manifestFieldsExt {
		off, ok := d.hasField(t, name)
		if !ok {
			canon := emptyListCanon
			if name == "mk" {
				canon = strconv.Quote("")
			}
			m.Fields[name] = source.HashString("mk.manifest.field.v1", canon)
			continue
		}
		_, ft := d.field(t, name)
		m.Fields[name] = source.HashString("mk.manifest.field.v1", d.canon(c, off, ft))
		switch name {
		case "mk":
			m.Mk = d.str(c, off)
		case "test_deps":
			m.TestDeps = d.deps(c, off, ft, name)
		case "replace":
			m.Replace = d.deps(c, off, ft, name)
		case "unsafe_deps", "members":
			var out []string
			for i, e := range d.slice(c, off, ft) {
				p := name + "[" + strconv.Itoa(i) + "]"
				s := d.str(e, 0)
				if s == "" {
					d.fail(p, "invalid", "`%s` must not be empty", p)
				}
				out = append(out, s)
			}
			if name == "members" {
				m.Members = out
			} else {
				m.UnsafeDeps = out
			}
		}
	}
}

// deps decodes a []pkg.Dep field called name at off, of type ft. A Dep's
// source and version are read by name, so an older std.pkg gives "".
func (d *manDecoder) deps(c cview, off uint64, ft types.TypeID, name string) []Dep {
	var out []Dep
	dt := d.w.tab.Get(ft).Elem
	for i, e := range d.slice(c, off, ft) {
		p := name + "[" + strconv.Itoa(i) + "]"
		dep := Dep{Name: d.text(e, dt, "name", p+".name"), Field: p}
		for _, f := range []struct {
			name string
			to   *string
		}{{"path", &dep.Path}, {"url", &dep.URL}, {"hash", &dep.Hash}, {"source", &dep.Source}, {"version", &dep.Version}} {
			if fo, ok := d.hasField(dt, f.name); ok {
				*f.to = d.str(e, fo)
			}
		}
		out = append(out, dep)
	}
	return out
}

// links decodes the links field of the Package value c of type t,
// read by name: a std.pkg without the field reads as `links: []`, with
// that value's field hash. Each Link's strings are taken as written;
// links.go validates them (E0202 reason link).
func (d *manDecoder) links(c cview, t types.TypeID) {
	m := d.man
	off, ok := d.hasField(t, "links")
	if !ok {
		m.Fields["links"] = source.HashString("mk.manifest.field.v1", emptyListCanon)
		return
	}
	_, ft := d.field(t, "links")
	m.Fields["links"] = source.HashString("mk.manifest.field.v1", d.canon(c, off, ft))
	lt := d.w.tab.Get(ft).Elem
	for i, e := range d.slice(c, off, ft) {
		p := "links[" + strconv.Itoa(i) + "]"
		l := Link{Field: p}
		for _, f := range []struct {
			name string
			to   *string
		}{{"name", &l.Name}, {"kind", &l.Kind}, {"path", &l.Path}, {"target", &l.Target}} {
			fo, _ := d.field(lt, f.name)
			*f.to = d.str(e, fo)
		}
		m.Links = append(m.Links, l)
	}
}

// canon is the canonical text of the value of type t at off, the input of
// its field hash: strings quoted, lists in brackets, a std.pkg struct as
// pkg.Name(field:value, ...) in declaration order.
func (d *manDecoder) canon(c cview, off uint64, t types.TypeID) string {
	ty := d.w.tab.Get(t)
	switch ty.Kind {
	case types.KStr:
		return strconv.Quote(d.str(c, off))
	case types.KBool:
		return strconv.FormatBool(c.v.Bytes[c.base+off] != 0)
	case types.KSlice:
		var parts []string
		for _, e := range d.slice(c, off, t) {
			parts = append(parts, d.canon(e, 0, ty.Elem))
		}
		return "[" + strings.Join(parts, ",") + "]"
	case types.KNamed:
		sd, ok := d.w.items.Get(ty.Item).Decl.(*types.StructDecl)
		if !ok {
			break
		}
		lay := d.w.sem5.chk.Layout(t)
		parts := make([]string, len(sd.Fields))
		for i, f := range sd.Fields {
			parts[i] = d.w.in.Str(f.Name) + ":" + d.canon(c, off+lay.Fields[i].Offset, lay.Fields[i].Type)
		}
		return "pkg." + d.w.in.Str(d.w.items.Get(ty.Item).Name) + "(" + strings.Join(parts, ",") + ")"
	}
	source.ICEf(source.Span{}, "driver: manifest value of type %s", d.w.tab.TypeString(t, nil))
	return ""
}
