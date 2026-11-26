// Why: a package may import C headers (pkg.CImport) and ship C and
// assembly sources (c_sources, asm_sources); the driver must turn those
// into Mk modules and link objects as a cached native step.
//
// What: validateC (the manifest's C fields), cIncludeDirs (the -I
// directories of a package's C), cImports and cImportsOf (the
// translated imports of one package), prune, translateC and vendoredViews,
// the C object builds (cObjects, cObjectsOf, asmSourcesFor) with their
// cache keys (cobjObjKey, cobjCandidate, saveCobjCandidate), and
// Session.CImports for the tools.
//
// How: the package's C imports are translated once per target
// (internal/cimport) into one memory build whose modules are c.<name>; the
// readable binding is written to gen/<target>/c/<name>.mk, and the shim,
// the C sources and the assembly sources become objects of the link. A C
// object's key covers the content hash of every header its -MD file lists.
// The translation, the shim and the C sources search the same -I
// directories: the package root, then every C import's include_dirs (the
// package's C imports are one translation unit, so they share one path).

package driver

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"mk/internal/cache"
	"mk/internal/cimport"
	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/toolchain"
	"mk/internal/types"
)

// validateC checks the manifest's C fields: a CImport's name is one
// import path segment, unique, with at least one header, and each of its
// include_dirs a directory inside the package other than its root, written
// relative with / separators; a C source is a
// package file ending in .c, an assembly source one ending in .s or .S
// whose target is "" or a target mk knows.
func validateC(m *Manifest, t *srcTree) []*diag.Diagnostic {
	var out []*diag.Diagnostic
	fail := func(field, format string, args ...any) {
		out = append(out, diag.New(diag.E0202, m.spanOf(field), fmt.Sprintf(format, args...)).
			Text("manifest", field).Text("reason", "invalid"))
	}
	seen := map[string]bool{}
	for _, ci := range m.CImports {
		switch {
		case !validDepName(ci.Name):
			fail(ci.Field+".name", "C import name %q is not one import path segment", ci.Name)
		case seen[ci.Name]:
			fail(ci.Field+".name", "C import %s is declared twice", ci.Name)
		}
		seen[ci.Name] = true
		if len(ci.Headers) == 0 {
			fail(ci.Field+".headers", "C import %s names no header", ci.Name)
		}
		for i, h := range ci.Headers {
			if h == "" || strings.ContainsAny(h, "<>\"\n") {
				fail(ci.Field+".headers["+strconv.Itoa(i)+"]", "header %q is no #include name", h)
			}
		}
		for i, d := range ci.Defines {
			name, _, _ := strings.Cut(d, "=")
			if name == "" || strings.ContainsAny(name, " \t\n()") {
				fail(ci.Field+".defines["+strconv.Itoa(i)+"]", "define %q is not NAME or NAME=VALUE", d)
			}
		}
		for i, d := range ci.IncludeDirs {
			f := ci.Field + ".include_dirs[" + strconv.Itoa(i) + "]"
			c := path.Clean(d)
			switch {
			case d == "" || strings.ContainsAny(d, "\\:\n") || path.IsAbs(d):
				fail(f, "include directory %q is not a relative directory with / separators", d)
			case c == "..", strings.HasPrefix(c, "../"):
				fail(f, "include directory %q is not a directory inside the package", d)
			case c == ".":
				fail(f, "include directory %q is the package root, which is always searched first", d)
			case t.osRoot != "" && !t.isDir(c):
				fail(f, "include directory %s does not exist (as spelled)", d)
			}
		}
	}
	for _, cs := range m.CSources {
		if !strings.HasSuffix(cs.Path, ".c") {
			fail(cs.Field+".path", "C source %s does not end in .c", cs.Path)
		} else if _, err := t.readFile(cs.Path); err != nil && t.osRoot != "" {
			fail(cs.Field+".path", "C source %s does not exist (as spelled)", cs.Path)
		}
	}
	for _, as := range m.AsmSources {
		if !strings.HasSuffix(as.Path, ".s") && !strings.HasSuffix(as.Path, ".S") {
			fail(as.Field+".path", "assembly source %s does not end in .s or .S", as.Path)
		} else if _, err := t.readFile(as.Path); err != nil && t.osRoot != "" {
			fail(as.Field+".path", "assembly source %s does not exist (as spelled)", as.Path)
		}
		if as.Target != "" {
			// The freestanding target is a target of the manifest
			// though no hosted build takes it.
			if _, _, ok := TargetNamed(as.Target); !ok && as.Target != types.X86_64Freestanding.Name {
				fail(as.Field+".target", "assembly source %s: unknown target %q", as.Path, as.Target)
			}
		}
	}
	return out
}

// cIncludeDirs are the -I directories of man's C, absolute: the package
// root osRoot, then each C import's include_dirs in order, each once.
func cIncludeDirs(man *Manifest, osRoot string) []string {
	out := []string{osRoot}
	seen := map[string]bool{}
	for _, c := range man.CImports {
		for _, d := range c.IncludeDirs {
			d = path.Clean(d)
			if !seen[d] {
				seen[d] = true
				out = append(out, filepath.Join(osRoot, filepath.FromSlash(d)))
			}
		}
	}
	return out
}

// cImports is the translated C imports of the package for the session's
// target: one build of c.<name> modules, and what the link adds.
type cImports struct {
	b      *build
	shim   string
	root   string // the package's OS directory
	stats  []cimport.Stats
	man    *Manifest
	hit    bool
	sysdir []string
	// names, full and trees: each import's name, its whole binding, and
	// the memory tree its module reads, which prune fills before the read step.
	names []string
	full  []string
	trees []*srcTree
	// others are the bindings of the other targets under
	// Config.AllTargets, and mismatch the E0213s prune found.
	others   []otherBinding
	mismatch []*diag.Diagnostic
	at       source.Span
	// dep is the dependency's name for a dependency's C fields, "" for
	// the package's; treePrefix is its SourceMap prefix.
	dep        string
	treePrefix string
	// vendoredShim are the externs (c.<name>.<item>) only a shim defines
	// in vendored views read in place of a translation.
	vendoredShim []string
}

// otherBinding is a package's C bindings for another target.
type otherBinding struct {
	target string
	full   []string
}

// cTargets are the targets --all-targets compares C bindings across: the
// hosted targets. The freestanding target is not compared.
var cTargets = []types.Target{types.X86_64Windows, types.X86_64Linux}

// cTarget is the target name a sysroot variable and the views use.
func cTarget(s *Session) string { return s.cfg.Target.Name }

// sysrootFor finds the system header directories of target:
// MK_SYSROOT_<target> (a list in the OS's separator; "-" may be
// written "_"), else $MK_EVIDENCE/c-import/sysroots/<target>, else none.
// Each directory D gives D/include when that exists, else D.
func sysrootFor(target string, getenv func(string) string) []string {
	v := getenv("MK_SYSROOT_" + target)
	if v == "" {
		v = getenv("MK_SYSROOT_" + strings.ReplaceAll(target, "-", "_"))
	}
	var dirs []string
	if v != "" {
		dirs = filepath.SplitList(v)
	} else if ev := evidenceDir(getenv); ev != "" {
		d := filepath.Join(ev, "c-import", "sysroots", target)
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			dirs = []string{d}
		}
	}
	var out []string
	for _, d := range dirs {
		if d == "" {
			continue
		}
		d, _ = filepath.Abs(d)
		if fi, err := os.Stat(filepath.Join(d, "include")); err == nil && fi.IsDir() {
			d = filepath.Join(d, "include")
		}
		out = append(out, d)
	}
	return out
}

// evidenceDir is $MK_EVIDENCE, else Mk-evidence beside the repository the
// running mk was built in is not known, so "" (CLAUDE.md names both).
func evidenceDir(getenv func(string) string) string {
	return getenv("MK_EVIDENCE")
}

// cImportsOf translates the C imports of man, the manifest of the package
// at osRoot (cached in st), into one build. The diagnostics are E0001 for a
// toolchain or header failure, at the first CImport.
func (s *Session) cImportsOf(stdB *build, man *Manifest, osRoot, dep, treePrefix string) (*cImports, []*diag.Diagnostic) {
	if !hasC(man) {
		return nil, nil
	}
	w := s.w
	target := cTarget(s)
	ci := &cImports{root: osRoot, man: man, dep: dep, treePrefix: treePrefix}
	// The C and assembly sources see the target's system headers
	// whether or not the package also imports C.
	ci.sysdir = sysrootFor(target, os.Getenv)
	if len(man.CImports) == 0 {
		// Only c_sources or asm_sources: nothing to translate, objects to
		// compile (cObjects).
		return ci, nil
	}
	var imports []cimport.Import
	for _, c := range man.CImports {
		imports = append(imports, cimport.Import{Name: c.Name, Headers: c.Headers, Defines: c.Defines, Macros: c.Macros})
	}
	at := man.spanOf(man.CImports[0].Field)
	modules, shim, ds := s.translateC(imports, man, osRoot, target, at, ci)
	if len(ds) > 0 {
		return nil, ds
	}
	ci.shim = shim
	ci.at = at
	if s.cfg.AllTargets {
		for _, t := range cTargets {
			if t.Name == target {
				continue
			}
			if len(sysrootFor(t.Name, os.Getenv)) == 0 && !allVendored(man) {
				return nil, []*diag.Diagnostic{cimport.NoSysroot(at, t.Name)}
			}
			mods, _, ds := s.translateC(imports, man, osRoot, t.Name, at, &cImports{dep: dep})
			if len(ds) > 0 {
				return nil, ds
			}
			ci.others = append(ci.others, otherBinding{target: t.Name, full: mods})
		}
	}
	b := newBuild(w, stdB)
	ci.full = modules
	for i, c := range man.CImports {
		file := c.Name + ".mk"
		t := &srcTree{mem: map[string]string{file: modules[i]}, prefix: treePrefix + "gen/" + target + "/c/", sitePrefix: treePrefix + "gen/" + target + "/c/"}
		ci.names, ci.trees = append(ci.names, c.Name), append(ci.trees, t)
		m := moduleFrom(t, "", []string{file})
		m.name, m.path = cModPath(dep, c.Name), cModPath(dep, c.Name)
		m.unsafe, m.cimport = true, true
		b.addModule(m)
	}
	// A later import's binding names an earlier one's module (one
	// translation unit per package).
	b.cBuild, b.prefix = b, prefixOfDep(dep)
	b.cSources = len(man.CSources) > 0
	ci.b = b
	return ci, nil
}

// hasC reports whether man declares C imports, C sources or assembly
// sources.
func hasC(man *Manifest) bool {
	return man != nil && len(man.CImports)+len(man.CSources)+len(man.AsmSources) > 0
}

// prefixOfDep is the module name prefix of a dependency's modules, "" for
// the package's own.
func prefixOfDep(dep string) string {
	if dep == "" {
		return ""
	}
	return dep + "."
}

// cModPath is the module path of C import name: c.<name> in the package,
// <dep>.c.<name> in a dependency (a dependency's modules carry its name).
func cModPath(dep, name string) string { return prefixOfDep(dep) + "c." + name }

// prune gives each binding module only the items the other builds'
// files use, transitively (lazy items); the views on disk stay whole, and
// every kept line keeps its line there. It runs in the read step
// just before the C build is read: the builds before it are read, and the
// package's files, read after it, are read here too.
func (ci *cImports) prune(w *world, builds []*build) {
	if w.cfg.TestHooks.CImportWhole {
		return
	}
	want := cimport.Want{}
	for _, b := range builds {
		if b == ci.b {
			continue
		}
		for _, m := range b.mods {
			for _, f := range m.files {
				if f.src != nil {
					cimport.Uses(f.src.Text, want)
					continue
				}
				if raw, err := m.tree.readFile(f.rel); err == nil {
					cimport.Uses(source.Normalize(raw), want)
				}
			}
		}
	}
	used := cloneWant(want)
	pruned := cimport.PruneAll(ci.names, ci.full, want)
	for i, text := range pruned {
		ci.trees[i].mem[ci.names[i]+".mk"] = text
	}
	target := w.cfg.Target.Name
	for _, o := range ci.others {
		theirs := cimport.PruneAll(ci.names, o.full, cloneWant(used))
		for i, name := range ci.names {
			diff := cimport.APIDiff(cimport.API(pruned[i]), cimport.API(theirs[i]))
			if len(diff) == 0 {
				continue
			}
			ci.mismatch = append(ci.mismatch, cimport.Mismatch(ci.at, name, target, o.target, diff))
		}
	}
}

// cloneWant copies a Want, which PruneAll extends.
func cloneWant(w cimport.Want) cimport.Want {
	out := cimport.Want{}
	for imp, names := range w {
		out[imp] = map[string]bool{}
		for n := range names {
			out[imp][n] = true
		}
	}
	return out
}

// allVendored reports whether every C import of man says vendor: true.
func allVendored(man *Manifest) bool {
	for _, c := range man.CImports {
		if !c.Vendor {
			return false
		}
	}
	return len(man.CImports) > 0
}

// translateC runs (or finds in the cache) the translation, writes the
// views, and falls back to a vendored view when the headers cannot be
// read and the import says vendor: true.
func (s *Session) translateC(imports []cimport.Import, man *Manifest, osRoot, target string, at source.Span, ci *cImports) ([]string, string, []*diag.Diagnostic) {
	w := s.w
	views := make([]string, len(imports))
	for i, im := range imports {
		views[i] = filepath.Join(osRoot, "gen", target, "c", im.Name+".mk")
	}
	// A vendored view stands in only when the headers cannot be
	// read at all (no clang, or a header not found); it has no shim, so a
	// build that needs one reports it (cObjectsOf).
	vendored := func() ([]string, bool) {
		mods, ok := vendoredViews(man, views)
		if ok {
			ci.vendoredShim = shimmedItems(man, mods)
		}
		return mods, ok
	}
	tools, d := s.tools()
	if d != nil {
		if mods, ok := vendored(); ok {
			return mods, "", nil
		}
		return nil, "", []*diag.Diagnostic{d}
	}
	ctx := context.Background()
	rd, err := s.resourceDir(ctx, tools)
	if err != nil {
		return nil, "", []*diag.Diagnostic{toolErr(err, "clang did not report its resource directory")}
	}
	req := cimport.Request{Target: target, Triple: cimport.ParseTriple(target), Imports: imports,
		Include: cIncludeDirs(man, osRoot), Sysroot: sysrootFor(target, os.Getenv)}
	ci.sysdir = req.Sysroot
	st := w.openStore(s.cacheDir())
	// The build ID is in the key: the mapping is code inside the mk binary.
	id := source.NewHasher("mk.driver.cimport.v1")
	id.Hash(tools.Identity())
	id.Hash(w.buildID())
	key := cimport.Key(id.Sum(), rd, req)
	res, hit := cimport.Load(st, key)
	if !hit {
		work, err := os.MkdirTemp(tempRoot(st), "mk-cimport-")
		if err != nil {
			return nil, "", []*diag.Diagnostic{diag.Toolchain("fs", "mkdir", err.Error(), "cannot make a work directory for the C import")}
		}
		defer os.RemoveAll(work)
		req.WorkDir = work
		res, err = cimport.Translate(ctx, tools, rd, req)
		if err != nil {
			var ce *cimport.Error
			if errors.As(err, &ce) && ce.Missing {
				if mods, ok := vendored(); ok {
					return mods, "", nil
				}
			}
			if errors.As(err, &ce) {
				d := diag.Toolchain("clang", scrubWork(ce.Command, work), scrubWork(ce.Stderr, work), "C import: "+scrubWork(ce.Msg, work))
				d.At = at
				return nil, "", []*diag.Diagnostic{d}
			}
			return nil, "", []*diag.Diagnostic{toolErrIn(err, "clang", "C import failed: "+err.Error(), work)}
		}
		cimport.Save(st, key, res)
	}
	ci.hit, ci.stats = hit, res.Stats
	for i := range imports {
		if ci.dep == "" {
			writeCView(views[i], res.Modules[i], target)
		}
	}
	return res.Modules, res.Shim, nil
}

// vendoredViews reads the views of every import when each says vendor:
// true and each view is mk's (--all-targets needs a sysroot or
// pkg.CImport(..., vendor: true)).
func vendoredViews(man *Manifest, views []string) ([]string, bool) {
	out := make([]string, len(views))
	for i, c := range man.CImports {
		if !c.Vendor {
			return nil, false
		}
		data, err := os.ReadFile(views[i])
		if err != nil || !genViewOwned(data) {
			return nil, false
		}
		text := string(data)
		body := text[:len(text)-1]
		out[i] = body[:strings.LastIndexByte(body, '\n')+1]
	}
	return out, true
}

// shimMarkers are the comments emit writes above an extern the shim
// defines (internal/cimport: a static inline function, a declared macro).
var shimMarkers = []string{"// static inline: defined by the shim of this package\n", "; defined by the shim\n"}

// shimmedItems lists, as "c.<name>.<item>", the externs of vendored views
// mods that only a shim defines.
func shimmedItems(man *Manifest, mods []string) []string {
	var out []string
	for i, text := range mods {
		for _, mk := range shimMarkers {
			rest := text
			for {
				k := strings.Index(rest, mk)
				if k < 0 {
					break
				}
				rest = rest[k+len(mk):]
				line, _, _ := strings.Cut(rest, "\n")
				if name, ok := strings.CutPrefix(line, "export extern "); ok {
					name, _, _ = strings.Cut(name, "(")
					out = append(out, "c."+man.CImports[i].Name+"."+name)
				}
			}
		}
	}
	slices.Sort(out)
	return out
}

// writeCView writes a binding's view when it changed, unless the file there
// is not mk's (a hand edit is kept, and the compiler never reads it unless
// the import is vendored).
func writeCView(path, text, target string) {
	want := cViewBytes(text, target)
	have, err := os.ReadFile(path)
	if err == nil && (string(have) == string(want) || !genViewOwned(have)) {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	writeGenView(path, want)
}

// cViewBytes is a binding's view: the text and the trailer line of a gen
// view, naming the C import.
func cViewBytes(text, target string) []byte {
	var b strings.Builder
	b.WriteString(text)
	if !strings.HasSuffix(text, "\n") {
		b.WriteByte('\n')
	}
	b.WriteString(genViewTrailer)
	b.WriteString(genViewHash(text).String())
	b.WriteString(" generated by mk from this package's C imports for ")
	b.WriteString(target)
	b.WriteString("; edit pkg.mk, not this file\n")
	return []byte(b.String())
}

// tempRoot is the store's temporary directory, else the OS's.
func tempRoot(st *cache.Store) string {
	if st != nil {
		if err := os.MkdirAll(st.TmpDir(), 0o755); err == nil {
			return st.TmpDir()
		}
	}
	return os.TempDir()
}

// resourceDir is clang's resource directory, asked once per session.
func (s *Session) resourceDir(ctx context.Context, tools *toolchain.Tools) (string, error) {
	if s.cres != "" {
		return s.cres, nil
	}
	rd, err := tools.ResourceDir(ctx)
	if err == nil {
		s.cres = rd
	}
	return rd, err
}

// cObjects compiles the shim, every C source and every assembly source of
// the package for triple into work, each object kept in the store by its
// command line and its input's content (kind cobj). It returns the
// objects in link order (shim, C sources, assembly sources, as the
// manifest lists them) and their keys.
func (w *world) cObjects(ctx context.Context, tools *toolchain.Tools, st *cache.Store, triple string, release bool, work string, cflags []string) ([]string, []source.Hash, []*diag.Diagnostic) {
	var objs []string
	var keys []source.Hash
	for i, ci := range w.allCImports() {
		o, k, ds := w.cObjectsOf(ctx, ci, filepath.Join(work, "c"+strconv.Itoa(i)), tools, st, triple, release, work, cflags)
		if len(ds) > 0 {
			return nil, nil, ds
		}
		objs, keys = append(objs, o...), append(keys, k...)
	}
	return objs, keys, nil
}

// allCImports are the package's C fields, then each dependency's, in
// build order.
func (w *world) allCImports() []*cImports {
	var out []*cImports
	if w.cimports != nil {
		out = append(out, w.cimports)
	}
	return append(out, w.depCImports...)
}

// cObjectsOf compiles one package's C objects into dir (cObjects).
func (w *world) cObjectsOf(ctx context.Context, ci *cImports, dir string, tools *toolchain.Tools, st *cache.Store, triple string, release bool, work string, cflags []string) ([]string, []source.Hash, []*diag.Diagnostic) {
	if ci == nil || ci.man == nil {
		return nil, nil, nil
	}
	if d := ci.missingShim(); d != nil {
		return nil, nil, []*diag.Diagnostic{d}
	}
	rd, err := tools.ResourceDir(ctx)
	if err != nil {
		return nil, nil, []*diag.Diagnostic{toolErr(err, "clang did not report its resource directory")}
	}
	opt := toolchain.O0
	if release {
		opt = toolchain.O3
	}
	type job struct {
		in    string
		asm   bool
		shim  bool
		flags []string
		name  string
	}
	var jobs []job
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, []*diag.Diagnostic{diag.Toolchain("fs", "mkdir", err.Error(), "cannot make the C work directory")}
	}
	if ci.shim != "" {
		p := filepath.Join(dir, "mk_shim.c")
		if err := os.WriteFile(p, []byte(ci.shim), 0o644); err != nil {
			return nil, nil, []*diag.Diagnostic{diag.Toolchain("fs", "write", err.Error(), "cannot write the C shim")}
		}
		jobs = append(jobs, job{in: p, shim: true, name: "mk_shim"})
	}
	for i, cs := range ci.man.CSources {
		jobs = append(jobs, job{in: filepath.Join(ci.root, filepath.FromSlash(cs.Path)), flags: cs.Flags, name: "c" + strconv.Itoa(i)})
	}
	for _, i := range asmSourcesFor(ci.man, w.cfg.Target.Name) {
		jobs = append(jobs, job{in: filepath.Join(ci.root, filepath.FromSlash(ci.man.AsmSources[i].Path)), asm: true, name: "s" + strconv.Itoa(i)})
	}
	var sysroot string
	if len(ci.sysdir) > 0 {
		sysroot = ci.sysdir[0] // as the translation's --sysroot
	}
	// std.libc's generated headers, for the package's own C. They
	// come after the package root's -I and before a sysroot's -isystem:
	// the program links std.libc, not the sysroot's C library, so its
	// declarations are the ones a C source must see. The shim reads the
	// headers as the binding did and the assembly sources include
	// nothing of C, so neither gets them.
	var libcDir string
	if len(ci.man.CSources) > 0 {
		d, err := w.writeLibcHeaders(st, work)
		if err != nil {
			return nil, nil, []*diag.Diagnostic{diag.Toolchain("fs", "write", err.Error(), "cannot write std.libc's C headers")}
		}
		libcDir = d
	}
	include := cIncludeDirs(ci.man, ci.root)
	dh := cimport.NewDepHasher(st)
	defer dh.Save()
	var objs []string
	var keys []source.Hash
	for _, j := range jobs {
		out := filepath.Join(dir, j.name+".o")
		system := ci.sysdir
		if libcDir != "" && !j.shim && !j.asm {
			system = append([]string{libcDir}, ci.sysdir...)
		}
		cj := toolchain.CJob{Triple: triple, Input: j.in, Output: out, Opt: opt, ResourceDir: rd,
			Include: include, System: system, Sysroot: sysroot, Flags: j.flags, Hosted: j.shim}
		if !j.asm {
			// The bin's target flags come before the manifest's own (cTargetFlags).
			cj.Flags = append(append([]string{}, cflags...), j.flags...)
		}
		src, err := os.ReadFile(j.in)
		if err != nil {
			return nil, nil, []*diag.Diagnostic{diag.Toolchain("fs", "read "+filepath.Base(j.in), pathErrText(err), "cannot read "+filepath.Base(j.in))}
		}
		if !j.asm || strings.HasSuffix(j.in, ".S") {
			// The object depends on every header the compile reads.
			cj.DepFile = filepath.Join(dir, j.name+".d")
		}
		args, _ := toolchain.CArgs(cj, j.asm)
		h := source.NewHasher("mk.driver.cobj.v2")
		h.Hash(tools.Identity())
		for _, a := range args {
			if a == j.in || a == out || a == cj.DepFile {
				continue
			}
			h.String(a)
		}
		h.Bytes(src)
		base := h.Sum()
		if st != nil {
			if key, ok := cobjCandidate(st, dh, base); ok {
				if p, ok, _ := st.FilePath(cache.KindCObj, key); ok {
					if data, err := os.ReadFile(p); err == nil && os.WriteFile(out, data, 0o644) == nil {
						objs, keys = append(objs, out), append(keys, key)
						continue
					}
				}
			}
		}
		var cerr error
		if j.asm {
			cerr = tools.CompileAsm(ctx, cj)
		} else {
			cerr = tools.CompileC(ctx, cj)
		}
		if cerr != nil {
			what := "the C source " + filepath.Base(j.in)
			if j.asm {
				what = "the assembly source " + filepath.Base(j.in)
			} else if j.shim {
				what = "the C import shim"
			}
			return nil, nil, []*diag.Diagnostic{toolErrIn(cerr, "clang", what+" did not compile", work)}
		}
		key, cand, ok := cobjKey(dh, base, cj.DepFile, j.in, cobjSearchDirs(cj))
		if st != nil && ok {
			saveCobjCandidate(st, base, cand)
			_ = st.PutFile(cache.KindCObj, key, out)
			if _, err := os.Stat(out); err != nil {
				// PutFile moved it into the store: copy it back for the link.
				if p, ok, _ := st.FilePath(cache.KindCObj, key); ok {
					if data, err := os.ReadFile(p); err == nil {
						_ = os.WriteFile(out, data, 0o644)
					}
				}
			}
		}
		objs, keys = append(objs, out), append(keys, key)
	}
	return objs, keys, nil
}

// asmSourcesFor are the indexes of man's assembly sources that a build for
// target assembles: those with no target and those naming exactly it
// (another architecture of the same OS is not the target).
func asmSourcesFor(man *Manifest, target string) []int {
	var out []int
	for i, as := range man.AsmSources {
		if as.Target == "" || as.Target == target {
			out = append(out, i)
		}
	}
	return out
}

// cobjCand is one candidate of a C object's KindManifest entry: the files
// the compile read (-MD), with their content hashes, and the negative
// lookups (cimport.Negatives over the compile's search path), none of
// which may exist: a header added to an earlier search directory would
// shadow a dependency. The object's key is cobjObjKey of the entry's base
// and the dependencies (the negatives follow from them and the search
// path, which base holds).
type cobjCand struct {
	Deps []cobjDep
	Neg  []string `json:",omitempty"`
}

type cobjDep struct {
	Path string
	Hash source.Hash
}

// maxCobjCands bounds an entry's candidates; the oldest go.
const maxCobjCands = 4

// cobjObjKey is the object's key: the command line and source (base),
// then every dependency's path and content.
func cobjObjKey(base source.Hash, c cobjCand) source.Hash {
	h := source.NewHasher("mk.driver.cobj.obj.v1")
	h.Hash(base)
	h.Uvarint(uint64(len(c.Deps)))
	for _, d := range c.Deps {
		h.String(d.Path)
		h.Hash(d.Hash)
	}
	return h.Sum()
}

// cobjListKey is the KindManifest key of a C object's candidates.
func cobjListKey(base source.Hash) source.Hash {
	// v2: candidates carry their negative lookups; a v1 list, which has
	// none, is never read.
	h := source.NewHasher("mk.driver.cobj.cands.v2")
	h.Hash(base)
	return h.Sum()
}

// cobjCandidate finds, among base's candidates, the first whose every
// dependency still has its hash and whose every negative lookup still
// finds nothing, and returns that object's key.
func cobjCandidate(st *cache.Store, dh *cimport.DepHasher, base source.Hash) (source.Hash, bool) {
	raw, ok, _ := st.Get(cache.KindManifest, cobjListKey(base))
	if !ok {
		return source.Hash{}, false
	}
	var cands []cobjCand
	if json.Unmarshal(raw, &cands) != nil {
		return source.Hash{}, false
	}
next:
	for _, c := range cands {
		for _, d := range c.Deps {
			if h, ok := dh.Hash(d.Path); !ok || h != d.Hash {
				continue next
			}
		}
		for _, n := range c.Neg {
			if _, err := os.Stat(n); err == nil {
				continue next
			}
		}
		return cobjObjKey(base, c), true
	}
	return source.Hash{}, false
}

// cobjKey reads the dependency file a compile wrote (none for a plain .s
// file, whose only input is the source) and returns the object's key and
// its candidate, with the negative lookups of dirs, the compile's ordered
// header search path (cobjSearchDirs); false when a dependency cannot be
// read (the object is then not stored).
func cobjKey(dh *cimport.DepHasher, base source.Hash, depFile, input string, dirs []string) (source.Hash, cobjCand, bool) {
	var c cobjCand
	if depFile != "" {
		var deps []string
		text, err := os.ReadFile(depFile)
		if err != nil {
			return base, c, false
		}
		for _, p := range cimport.ParseDepFile(string(text)) {
			if !filepath.IsAbs(p) {
				p = filepath.Join(filepath.Dir(depFile), p)
			}
			p = filepath.Clean(p)
			if p == filepath.Clean(input) {
				continue // the source itself is in base
			}
			h, ok := dh.Hash(p)
			if !ok {
				return base, c, false
			}
			c.Deps = append(c.Deps, cobjDep{Path: p, Hash: h})
			deps = append(deps, p)
		}
		c.Neg = cimport.Negatives(deps, dirs)
	}
	return cobjObjKey(base, c), c, true
}

// cobjSearchDirs is the ordered header search path of the compile cj, as
// Clang searches it under -nostdinc: every -I, then the -isystem
// directories in command-line order (cArgs: the resource headers first,
// then cj.System). A directory the manifest's own flags add is not in it.
func cobjSearchDirs(cj toolchain.CJob) []string {
	out := append([]string{}, cj.Include...)
	if cj.ResourceDir != "" {
		out = append(out, filepath.Join(cj.ResourceDir, "include"))
	}
	return append(out, cj.System...)
}

// saveCobjCandidate puts c first among base's candidates.
func saveCobjCandidate(st *cache.Store, base source.Hash, c cobjCand) {
	lk := cobjListKey(base)
	cands := []cobjCand{c}
	if raw, ok, _ := st.Get(cache.KindManifest, lk); ok {
		var old []cobjCand
		if json.Unmarshal(raw, &old) == nil {
			for _, o := range old {
				if len(cands) < maxCobjCands && !slices.Equal(o.Deps, c.Deps) {
					cands = append(cands, o)
				}
			}
		}
	}
	if data, err := json.Marshal(cands); err == nil {
		_ = st.Put(cache.KindManifest, lk, data)
	}
}

// missingShim is the E0001 of a build whose vendored binding (read because
// the headers were not found) uses an extern only the shim defines: the
// shim is compiled from those headers, so it cannot be made.
func (ci *cImports) missingShim() *diag.Diagnostic {
	var used []string
	for _, item := range ci.vendoredShim {
		imp, name, _ := strings.Cut(strings.TrimPrefix(item, "c."), ".")
		for i, n := range ci.names {
			if n == imp && i < len(ci.trees) && strings.Contains(ci.trees[i].mem[n+".mk"], "export extern "+name+"(") {
				used = append(used, item)
			}
		}
	}
	if len(used) == 0 {
		return nil
	}
	d := diag.Toolchain("clang", "", "the C headers were not found, and a vendored binding has no shim",
		"C import: "+strings.Join(used, ", ")+" are defined by the C import's shim, which is compiled from the headers the vendored binding stands in for")
	if ci.at != (source.Span{}) {
		d.At = ci.at
	}
	return d
}

// cImportNames lists the package's C import names, for an E0202's
// alternatives.
func (b *build) cImportNames() []string {
	if b.cBuild == nil {
		return nil
	}
	var out []string
	for _, m := range b.cBuild.mods {
		out = append(out, "import "+strings.TrimPrefix(m.name, b.prefix))
	}
	slices.Sort(out)
	return out
}

// CImportInfo is one C import of the package after Check, as mk cimport
// prints it: the view written for the session's target and how much of
// the headers it binds.
type CImportInfo struct {
	Name, Target string
	View         string // the view's OS path, gen/<target>/c/<name>.mk
	Decls        int    // declarations considered
	Direct       int    // bound as plain Mk items
	Generated    int    // bound through accessors or shims
	Skipped      map[string]int
	CacheHit     bool // the translation came from the cache
}

// CImports describes the package's C imports after Check; nil when it
// declares none or the check stopped before translating them.
func (s *Session) CImports() []CImportInfo {
	ci := s.w.cimports
	if ci == nil {
		return nil
	}
	target := cTarget(s)
	out := make([]CImportInfo, len(ci.names))
	for i, n := range ci.names {
		out[i] = CImportInfo{Name: n, Target: target, View: filepath.Join(ci.root, "gen", target, "c", n+".mk"), CacheHit: ci.hit}
		if i < len(ci.stats) {
			st := ci.stats[i]
			out[i].Decls, out[i].Direct, out[i].Generated, out[i].Skipped = st.Decls, st.Direct, st.Generated, st.Skipped
		}
	}
	return out
}

// depCDir is the directory on disk Clang reads a dependency's headers and
// C sources from: a path or vendored dependency's own, and for a fetched
// one its archive unpacked once under the cache directory, named by its
// identity and its tree hash (a source identity holds every minor
// version), so the cimport key's search paths stay the same between
// sessions.
func (s *Session) depCDir(dp *depPkg) (string, *diag.Diagnostic) {
	if dp.osDir != "" {
		return dp.osDir, nil
	}
	if dp.archive == nil {
		return "", diag.Toolchain("fs", "", "no files", "dependency "+dp.name+": its C sources have no directory")
	}
	h := source.NewHasher("mk.driver.cdep.v1")
	h.String(dp.identity)
	th := dp.archive.Hash() // one identity holds every minor version
	h.Bytes(th[:])
	sum := h.Sum()
	dir := filepath.Join(s.cacheDir(), "cdeps", hex.EncodeToString(sum[:8]))
	if _, err := os.Stat(filepath.Join(dir, "pkg.mk")); err == nil {
		return dir, nil
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", diag.Toolchain("fs", "mkdir", err.Error(), "dependency "+dp.name+": cannot unpack it for its C sources")
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dir), ".tmp-")
	if err != nil {
		return "", diag.Toolchain("fs", "mkdir", err.Error(), "dependency "+dp.name+": cannot unpack it for its C sources")
	}
	_ = os.Remove(tmp)
	if err := dp.archive.Write(tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return "", diag.Toolchain("fs", "write", err.Error(), "dependency "+dp.name+": cannot unpack it for its C sources")
	}
	if err := os.Rename(tmp, dir); err != nil {
		_ = os.RemoveAll(tmp)
		if _, serr := os.Stat(filepath.Join(dir, "pkg.mk")); serr != nil {
			return "", diag.Toolchain("fs", "rename", err.Error(), "dependency "+dp.name+": cannot unpack it for its C sources")
		}
	}
	return dir, nil
}
