// Why: before anything is checked, the driver must know which files form
// which modules, read them all, and parse them; each of these steps can
// stop the build, and their order fixes every file and module ID.
//
// What: the first loading steps: std's module table (listStd), the
// package's modules with target selection (listPackage, listTree,
// packageDirs), unsafe permission (permitUnsafe), case-only names
// (findCaseDups, caseDupDiags), reading with the read-failure stop and the
// size limit (readAll), the parallel parse (parseAll) and library marking
// (markLibrary).
//
// How: directories are walked in byte order and files read in module then
// file order, so IDs are deterministic. A read failure stops the build
// after the read step; parse errors stop it after the parse step; import,
// duplicate-path and case-only errors wait for the import step.

package driver

import (
	"fmt"
	"path"
	"slices"
	"strings"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/types"
)

// build is one set of modules loaded together: std, a package, or a corpus
// unit. A unit's build resolves std imports against the std build, which is
// read-only by then.
type build struct {
	w    *world
	mods []*modNode // in listing order until the import step, then by (depth, path)
	// byName finds this build's own modules by import name.
	byName map[string]*modNode
	// allNames keeps every listed module name of std even after lazy pruning
	// so E0202 case-mismatch suggestions stay identical to a full load.
	allNames []string
	// selDirs maps the import path of a target-selected directory to the
	// selection's name (E0202 target-selected).
	selDirs map[string]string
	std     *build // the std build; nil for std itself
	// cBuild holds the c.<name> modules this build's imports of c.* reach
	// (cimport.go): the package's own for the package and for std.
	cBuild *build
	// cSources: this is a C build whose package declares c_sources, the C a
	// std module's binding needs.
	cSources bool
	isStd    bool
	// unit is a corpus unit's build: lostContracts (walk.go) scans only its
	// own modules and std's, since other units may still be declaring items.
	unit bool
	core *modNode // std.core when std declares it
	// pending holds case-only E0206s until the files exist (import step).
	caseDups []caseDup
	// dupPaths holds the modules whose import path an earlier module of the
	// build already has, reported as E0206 at the import step.
	dupPaths []dupPath
	// reserved holds the directories that form no module because their
	// import path begins with std, c or pkg, E0202 at the import step.
	reserved []reservedDirEntry
	trace    Trace
	// prefix is a dependency build's "<name>.", put before every module
	// name it forms (doc/packages.md); "" for std and the package.
	prefix string
	// depBuilds are the builds of the dependencies this build's manifest
	// names, by name: the only other packages its modules may import.
	depBuilds map[string]*build
	// late holds a std build's late modules (cstd.go), in listing order,
	// and reached those an importer outside them reached; lateOf is the
	// std build of the late build that holds the reached ones.
	late    []*modNode
	reached map[*modNode]bool
	lateOf  *build
	// keepSyntax: the build's modules keep their front-end output after
	// their check (release.go): the manifest's, whose AST decode reads.
	keepSyntax bool
	// skipStdTests omits std unit-test files (tests_*.mk, *_test.mk) in
	// listStd when not running std tests or browsing std (stdlazy.go).
	skipStdTests bool
}

// reservedDirEntry is a directory refused because its import path would
// begin with a reserved segment: its path as cited and the import path it
// would have had.
type reservedDirEntry struct{ path, imp string }

// dupPath is a module whose import path equals an earlier module's.
type dupPath struct{ first, later *modNode }

// caseDup is a pair of names equal after ASCII lowercasing.
type caseDup struct {
	later        *modNode
	file         int    // the later file's index, -1 for a module directory
	first, other string // paths as listed
}

func newBuild(w *world, std *build) *build {
	return &build{w: w, byName: map[string]*modNode{}, selDirs: map[string]string{}, std: std, isStd: std == nil}
}

// addModule records one module of the build. A module whose import path an
// earlier one has is kept in mods, so its files are read and the import
// step's E0206 has a file to stand at, but byName keeps the first; the
// import step stops the build, so no two modules with one path ever reach
// the item table.
func (b *build) addModule(m *modNode) {
	m.done = make(chan struct{})
	b.mods = append(b.mods, m)
	if first := b.byName[m.name]; first != nil {
		b.dupPaths = append(b.dupPaths, dupPath{first, m})
		return
	}
	b.byName[m.name] = m
}

// reservedSegments are the first import path segments kept for std,
// C import and the manifest module.
var reservedSegments = []string{"std", "c", "pkg"}

// reservedImport reports whether import path p begins with a reserved
// segment.
func reservedImport(p string) bool {
	first, _, _ := strings.Cut(p, ".")
	return slices.Contains(reservedSegments, first)
}

// reservedDir reports whether a package directory's import path is
// reserved: std/x, c, pkg.y and std.z/ all are; the root never is.
func reservedDir(dir string) bool { return dir != "" && reservedImport(dirToImport(dir)) }

// walkSkips is the walk's rule by directory name: hidden directories
// (.mk-cache among them) and gen are never walked.
func walkSkips(name string) bool { return strings.HasPrefix(name, ".") || name == "gen" }

// outsideWalk reports whether dir lies where the package walk forms no
// module: under a directory walkSkips, or at or under a selected directory.
func outsideWalk(dir string, selected []string) bool {
	return skippedDir(dir, func(rel, name string) bool { return walkSkips(name) || slices.Contains(selected, rel) })
}

// packageDirs walks a package tree: the directories holding
// .mk files, in byte order, with their files; the root only when it holds
// a .mk file besides pkg.mk. Reserved directories are listed; the caller
// refuses them.
func packageDirs(t *srcTree, man *Manifest) (dirs []string, byDir map[string][]string, err error) {
	selected := man.walkSkipped()
	files, err := t.mkFiles("", false, func(rel, name string) bool {
		return walkSkips(name) || slices.Contains(selected, rel)
	})
	if err != nil {
		return nil, nil, err
	}
	// A directory holding its own pkg.mk is another package, and
	// nothing under it is a module of this one (a dogfood's repro/<id>/).
	var nested []string
	for _, f := range files {
		if d := dirOf(f); d != "" && f == d+"/pkg.mk" {
			nested = append(nested, d)
		}
	}
	if len(nested) > 0 {
		files = slices.DeleteFunc(slices.Clone(files), func(f string) bool {
			for _, d := range nested {
				if strings.HasPrefix(f, d+"/") {
					return true
				}
			}
			return false
		})
	}
	dirs, byDir = groupByDir(files)
	if fs, ok := byDir[""]; ok {
		fs = slices.DeleteFunc(slices.Clone(fs), func(f string) bool { return f == "pkg.mk" })
		byDir[""] = fs
		if len(fs) == 0 {
			dirs = slices.DeleteFunc(dirs, func(d string) bool { return d == "" })
		}
	}
	return dirs, byDir, nil
}

// moduleFrom forms a module of the files directly in dir.
func moduleFrom(t *srcTree, dir string, files []string) *modNode {
	m := &modNode{dir: dir, tree: t}
	for _, f := range files {
		m.files = append(m.files, &fileNode{rel: f})
	}
	return m
}

// groupByDir splits a walk-ordered file list into directories, in order.
func groupByDir(files []string) (dirs []string, byDir map[string][]string) {
	byDir = map[string][]string{}
	for _, f := range files {
		d := dirOf(f)
		if _, ok := byDir[d]; !ok {
			dirs = append(dirs, d)
		}
		byDir[d] = append(byDir[d], f)
	}
	slices.Sort(dirs)
	return dirs, byDir
}

// listStd forms std's modules for the build's target OS.
func (b *build) listStd(t *srcTree, man *Manifest) error {
	osName := targetOS(b.w.cfg.Target)
	excluded := append([]string(nil), stdLayout.TargetDirs...)
	if man != nil {
		for _, sel := range man.Targets {
			for _, ms := range sel.Modules {
				excluded = append(excluded, ms.Dir)
			}
		}
	}
	files, err := t.mkFiles("", false, func(rel, name string) bool {
		return strings.HasPrefix(name, ".") || slices.Contains(stdLayout.NotModules, rel) || slices.Contains(excluded, rel)
	})
	if err != nil {
		return err
	}
	if b.skipStdTests {
		files = slices.DeleteFunc(files, isStdTestFile)
	}
	dirs, byDir := groupByDir(files)
	for _, d := range dirs {
		if d == "" { // std/pkg.mk and nothing else lives at the root
			continue
		}
		// On freestanding only the modules of freeStd are formed, and
		// std.free only there (free.go).
		if !stdFormed(b.w.cfg.Target, "std."+dirToImport(d)) {
			continue
		}
		m := moduleFrom(t, d, byDir[d])
		m.name, m.std = "std."+dirToImport(d), true
		b.addModule(m)
	}
	if man != nil {
		for _, sel := range man.Targets {
			if sel.OS != osName {
				continue
			}
			for _, ms := range sel.Modules {
				fs, err := t.mkFiles(ms.Dir, true, nil)
				if err != nil {
					return err
				}
				if b.skipStdTests {
					fs = slices.DeleteFunc(fs, isStdTestFile)
				}
				m := moduleFrom(t, ms.Dir, fs)
				m.name, m.std, m.selected = "std."+ms.Name, true, true
				b.addModule(m)
			}
		}
	}
	b.permitUnsafe(man)
	for _, m := range b.mods {
		m.path = m.name
		for _, f := range m.files {
			f.bodiless = true
		}
	}
	b.findCaseDups()
	return nil
}

// listPackage forms the package's modules. In single-file and
// directory mode there is one module, "main", the bin root.
func (b *build) listPackage(t *srcTree, root rootInfo, man *Manifest) error {
	b.w.cfg.hook("list")
	switch root.kind {
	case rootFile:
		m := moduleFrom(t, "", t.overlayFiles([]string{root.file}, "", true, nil))
		m.name = "main"
		b.addModule(m)
	case rootDir:
		files, err := t.mkFiles("", true, nil)
		if err != nil {
			return err
		}
		m := moduleFrom(t, "", files)
		m.name, m.binRoot = "main", true
		b.addModule(m)
	case rootNone:
		// mk test --std: no package module; the runner is added after.
	default:
		if err := b.listTree(t, man, "main"); err != nil {
			return err
		}
	}
	for _, m := range b.mods {
		m.path = m.name
	}
	b.permitUnsafe(man)
	b.markLibrary(man)
	if man != nil {
		for _, bin := range man.Bins {
			for _, m := range b.mods {
				// A library's root needs no Main (libout.go), nor
				// a cut "object" bin's, which the build refuses.
				if m.dir == bin.Root && bin.Kind == "" {
					m.binRoot = true
				}
			}
		}
	}
	b.findCaseDups()
	return nil
}

// listTree walks a package tree: every directory holding .mk files is a
// module named by its directory; the root's own files, pkg.mk
// excepted, form the module rootName. Directories a target selection names
// are skipped with everything under them, and the selection for the build's
// OS adds its directory under the selection's name.
func (b *build) listTree(t *srcTree, man *Manifest, rootName string) error {
	osName := targetOS(b.w.cfg.Target)
	if man != nil {
		for _, sel := range man.Targets {
			for _, ms := range sel.Modules {
				b.selDirs[b.prefix+dirToImport(ms.Dir)] = ms.Name
			}
		}
	}
	dirs, byDir, err := packageDirs(t, man)
	if err != nil {
		return err
	}
	for _, d := range dirs {
		if reservedDir(d) {
			// A reserved path: never a module, never read (a std/
			// directory's files would also take std's own source paths).
			b.reserved = append(b.reserved, reservedDirEntry{t.path(d), dirToImport(d)})
			continue
		}
		m := moduleFrom(t, d, byDir[d])
		m.name = b.prefix + dirToImport(d)
		if d == "" {
			m.name = rootName
		}
		b.addModule(m)
	}
	if man != nil {
		for _, sel := range man.Targets {
			if sel.OS != osName {
				continue
			}
			for _, ms := range sel.Modules {
				fs, err := t.mkFiles(ms.Dir, true, nil)
				if err != nil {
					return err
				}
				m := moduleFrom(t, ms.Dir, fs)
				m.name, m.selected = b.prefix+ms.Name, true
				b.addModule(m)
			}
		}
	}
	return nil
}

// permitUnsafe sets UnsafePermitted from the manifest's unsafe_modules
// (pkg.mk). Single-file and directory mode have no manifest.
func (b *build) permitUnsafe(man *Manifest) {
	if man == nil {
		return
	}
	for _, m := range b.mods {
		m.unsafe = slices.Contains(man.UnsafeModules, m.dir)
	}
}

// findCaseDups finds file names and module directory names of one directory
// that are equal after ASCII lowercasing, from the listing alone, so it
// fires on a case-insensitive file system too.
func (b *build) findCaseDups() {
	for _, m := range b.mods {
		seen := map[string]string{}
		for i, f := range m.files {
			name := f.rel[len(m.dir):]
			k := strings.ToLower(name)
			if first, ok := seen[k]; ok {
				b.caseDups = append(b.caseDups, caseDup{later: m, file: i, first: m.tree.path(first), other: m.tree.path(f.rel)})
				continue
			}
			seen[k] = f.rel
		}
	}
	type dirEntry struct {
		parent, lower string
	}
	seen := map[dirEntry]*modNode{}
	byDir := slices.Clone(b.mods)
	slices.SortFunc(byDir, func(x, y *modNode) int { return strings.Compare(x.dir, y.dir) })
	for _, m := range byDir {
		if m.dir == "" || m.selected {
			continue
		}
		k := dirEntry{dirOf(m.dir), strings.ToLower(m.dir[strings.LastIndexByte(m.dir, '/')+1:])}
		if first, ok := seen[k]; ok {
			b.caseDups = append(b.caseDups, caseDup{later: m, file: -1, first: m.tree.path(first.dir), other: m.tree.path(m.dir)})
			continue
		}
		seen[k] = m
	}
}

// caseDupDiags turns the case-only pairs into E0206s at line 1, column 1 of
// the later file (for a directory, its first file), group imports; then
// the modules that share an import path (E0206) and the reserved
// directories (E0202 reason reserved).
func (b *build) caseDupDiags() []*diag.Diagnostic {
	var out []*diag.Diagnostic
	for _, d := range b.dupPaths {
		first, later := dirLabel(d.first), dirLabel(d.later)
		at := source.Span{File: d.later.files[0].src.ID}
		out = append(out, diag.New(diag.E0206, at,
			fmt.Sprintf("module directories %s and %s both have the import path %s", first, later, d.later.name)).
			In(diag.PhaseImports).Text("first", first))
	}
	for _, r := range b.reserved {
		p := r.imp
		seg, _, _ := strings.Cut(p, ".")
		out = append(out, diag.New(diag.E0202, source.Span{},
			fmt.Sprintf("directory %s cannot be a module: its import path %s begins with %s, "+
				"which is reserved for %s; rename the directory", r.path, p, seg, reservedFor[seg])).
			Text("module", p).Text("reason", "reserved"))
	}
	for _, c := range b.caseDups {
		i := c.file
		what := "file names"
		if i < 0 {
			i, what = 0, "module directory names"
		}
		at := source.Span{File: c.later.files[i].src.ID}
		out = append(out, diag.New(diag.E0206, at,
			fmt.Sprintf("%s %s and %s differ only in case", what, c.first, c.other)).
			In(diag.PhaseImports).Text("first", c.first))
	}
	return out
}

// reservedFor says what each reserved segment belongs to.
var reservedFor = map[string]string{"std": "the standard library", "c": "C import", "pkg": "the manifest module"}

// dirLabel is a module's directory as a message and fact cite it: its tree
// path, "." for a package root.
func dirLabel(m *modNode) string {
	if l := strings.TrimSuffix(m.tree.path(m.dir), "/"); l != "" {
		return l
	}
	return "."
}

// readAll reads every file of the build (the read step), in module order then
// file order, so file IDs are deterministic. A file that cannot be read is
// E0001 tool fs; the caller stops after this step when any exists. A file over
// the size limit is added empty and gets E0101 file-too-large. The reads run in
// parallel (Config.ReadFile is called from many goroutines); the files enter
// the SourceMap in order.
func (b *build) readAll() (readFailures, tooLarge []*diag.Diagnostic) {
	b.w.cfg.hook("read")
	type job struct {
		m   *modNode
		f   *fileNode
		raw []byte
		err error
	}
	var jobs []job
	for _, m := range b.mods {
		for _, f := range m.files {
			jobs = append(jobs, job{m: m, f: f})
		}
	}
	faults := b.w.parallel(len(jobs), func(i int) string { return "read " + jobs[i].m.tree.path(jobs[i].f.rel) }, func(i int) {
		jobs[i].raw, jobs[i].err = jobs[i].m.tree.readFile(jobs[i].f.rel)
	})
	if len(faults) > 0 {
		return faults, nil
	}
	for _, j := range jobs {
		m, f, raw := j.m, j.f, j.raw
		p := m.tree.path(f.rel)
		if err := j.err; err != nil {
			readFailures = append(readFailures, diag.Toolchain("fs", "read "+p, pathErrText(err),
				fmt.Sprintf("cannot read source file %s", p)))
			continue
		}
		if uint64(len(raw)) > b.w.maxFile && uint64(len(source.Normalize(raw))) > b.w.maxFile {
			f.tooLarge = true
			f.src = b.w.sm.Add(p, nil)
			b.w.noteSitePath(f.src.ID, m.tree.sitePath(f.rel))
			tooLarge = append(tooLarge, diag.New(diag.E0101, source.Span{File: f.src.ID},
				fmt.Sprintf("source file %s is larger than %d bytes", p, b.w.maxFile)).Text("reason", "file-too-large"))
			continue
		}
		f.src = b.w.sm.Add(p, raw)
		b.w.noteSitePath(f.src.ID, m.tree.sitePath(f.rel))
	}
	return readFailures, tooLarge
}

// parseAll parses every file in parallel (the parse step), one Guard per file,
// and returns the parse diagnostics in file order. With the module cache on, a
// file whose parse summary is stored is not parsed (parsesum.go); under
// MK_CACHE_VERIFY it is, and the summaries are compared.
func (b *build) parseAll() []*diag.Diagnostic {
	type job struct {
		m *modNode
		f *fileNode
	}
	var files []job
	for _, m := range b.mods {
		for _, f := range m.files {
			if !f.tooLarge {
				files = append(files, job{m, f})
			}
		}
	}
	fc := b.w.front
	slots := make([][]*diag.Diagnostic, len(files))
	hit := make([]bool, len(files))
	// The parse indexes (parsesum.go): each file's key, its
	// summary's encoding when it may be stored, and put when its module's
	// index must be rewritten for it.
	mods := make([]*modNode, len(files))
	for i, j := range files {
		mods[i] = j.m
	}
	keys := make([]source.Hash, len(files))
	encs := make([][]byte, len(files))
	put := make([]bool, len(files))
	coldParse := fc != nil && fc.noManifests && !fc.verify
	faults := b.w.parallel(len(files), func(i int) string { return "parse " + files[i].f.src.Path }, func(i int) {
		m, f := files[i].m, files[i].f
		var key source.Hash
		var stored []byte
		if fc != nil && !coldParse {
			var content source.Hash
			key, content = fc.parseSummaryKeyOf(b.w, m, f)
			keys[i] = key
			p, enc, ok := fc.loadSummary(key, content, b.w, m, f)
			switch {
			case ok && !fc.verify:
				f.sum, hit[i], encs[i] = p, true, enc
				return
			case fc.verify:
				// Every file is parsed and compared with what is stored
				// under its key, a summary that failed loadSummary's
				// check included: parseFile reports a difference.
				stored = enc
			}
		}
		slots[i], encs[i], put[i] = b.parseFile(f, key, stored)
	})
	if coldParse {
		// On a cold cache build, defer encoding, key computation (which joins
		// async buildID), and parse-index writes to storeParseIndexesCold.
		fNodes := make([]*fileNode, len(files))
		for i, j := range files {
			fNodes[i] = j.f
			put[i] = len(slots[i]) == 0 && j.f.sum != nil
		}
		faults = append(faults, b.storeParseIndexesCold(mods, fNodes, put)...)
	} else {
		faults = append(faults, b.storeParseIndexes(mods, keys, encs, put)...)
	}
	releaseParseIndexes(mods)
	var out []*diag.Diagnostic
	for i, j := range files {
		f := j.f
		out = append(out, slots[i]...)
		if hit[i] {
			continue
		}
		b.trace.FilesParsed = append(b.trace.FilesParsed, f.src.Path)
		if f.ast == nil { // the worker faulted before ParseFile returned
			f.ast = &ast.File{Base: ast.Base{NID: 1, Sp: source.Span{File: f.src.ID}}, Path: f.src.Path, NodeCount: 1}
		}
		if f.sum == nil {
			f.sum = b.w.summarize(f)
		}
	}
	return append(out, faults...)
}

// markLibrary marks the modules of a library bin: those at or under the root
// of a pkg.Bin of kind dynamic or static, whose export extern functions
// cross the library boundary, so check judges their C form (E1109,
// ModuleInput.Library). libout.go exports from the package modules the
// library's root reaches; check runs before that closure is known, so a
// module under the library's root that the library does not reach is judged
// too, and one outside it that the library reaches is not. Only the root
// package is listed here: a dependency's library bin is not built. Nothing
// is marked for a target that builds no library output (only Windows and
// Linux build libraries), and a module that is another bin's root never is:
// a freestanding kernel's MkPanicHook and MkEntry, or an executable's own
// exports, do not cross a library's boundary.
func (b *build) markLibrary(man *Manifest) {
	if man == nil {
		return
	}
	if os := b.w.cfg.Target.OS; os != types.OSWindows && os != types.OSLinux {
		return
	}
	clean := func(root string) string {
		root = path.Clean(strings.ReplaceAll(root, "\\", "/"))
		if root == "." {
			return ""
		}
		return root
	}
	for _, bin := range man.Bins {
		if _, lib := libKind(bin.Kind); !lib {
			continue
		}
		root := clean(bin.Root)
	mods:
		for _, m := range b.mods {
			if root != "" && m.dir != root && !strings.HasPrefix(m.dir, root+"/") {
				continue
			}
			for _, other := range man.Bins {
				if _, lib := libKind(other.Kind); !lib && clean(other.Root) == m.dir {
					continue mods
				}
			}
			m.libBoundary = true
		}
	}
}
