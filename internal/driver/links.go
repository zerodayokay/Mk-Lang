// Why: a package that calls a C library through FFI names it in pkg.mk's
// `links`; the driver must turn those entries into import libraries, copied
// DLLs and archives on the link line, without changing any build that has
// no link.
//
// What: the linking half of FFI: validateLinks (the manifest step's check
// of pkg.Link), collectLinks, linkPlan and linkPlanFor, the Windows DLL
// name lists (dllNames, peFuncExports) and their import libraries
// (namedImportLibs), copyDLLs, and the undefined-symbol errors of a failed
// link (linkErrWith, archiveLabel, archiveHint).
//
// How: an extern resolves in a fixed order: std's .def lists, then the
// named DLLs, then the implicit system set (syslibs.go). Each used DLL gets
// one import library holding only the used names, a named .dll is copied
// beside every executable that uses it, and static archives go on the link
// line after the objects. With no link that applies to the target, the link
// line, the import libraries and every key are the same as without links.

package driver

import (
	"bytes"
	"debug/elf"
	"debug/pe"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"mk/internal/cache"
	"mk/internal/codegen"
	"mk/internal/diag"
	"mk/internal/mono"
	"mk/internal/source"
	"mk/internal/toolchain"
	"mk/internal/types"
)

// The values of pkg.Link's kind and target.
const (
	linkDynamic = "dynamic"
	linkStatic  = "static"
)

// linkTargets are the targets a pkg.Link may name: one per link, never
// ""; a freestanding, Nutshell or macOS link is refused.
var linkTargets = []string{types.X86_64Windows.Name, types.X86_64Linux.Name, types.Aarch64Linux.Name}

// validateLinks checks the manifest's links against pkg.Link's table of
// kinds and targets: each fault is E0202 at the field with reason link. A
// file is read from t, so a missing one is caught here; the host checks (a
// "" Windows path off Windows) and the availability of Linux shared
// libraries are the build's (linkPlanFor), since mk check builds nothing.
func validateLinks(m *Manifest, t *srcTree) []*diag.Diagnostic {
	var out []*diag.Diagnostic
	fail := func(field, format string, args ...any) {
		out = append(out, diag.New(diag.E0202, m.spanOf(field), fmt.Sprintf(format, args...)).
			Text("manifest", field).Text("reason", "link"))
	}
	for _, l := range m.Links {
		f := l.Field
		switch {
		case l.Name == "":
			fail(f+".name", "link name is empty")
		case l.Kind == linkDynamic && strings.ContainsAny(l.Name, `/\`):
			fail(f+".name", "dynamic link name %q holds a path separator; name the file the loader looks up, and put its location in path", l.Name)
		case l.Kind == linkDynamic && l.Target == types.X86_64Windows.Name && !validDLLName(l.Name):
			fail(f+".name", "dynamic link name %q is not a DLL file name an import table can hold (letters, digits, _, - and .)", l.Name)
		}
		if l.Kind != linkDynamic && l.Kind != linkStatic {
			fail(f+".kind", "link %s: kind %q is not \"dynamic\" or \"static\"", l.Name, l.Kind)
			continue
		}
		if !slices.Contains(linkTargets, l.Target) {
			fail(f+".target", "link %s: target %q is not \"x86_64-windows\", \"x86_64-linux\" or \"aarch64-linux\" (one target per link, never \"\")", l.Name, l.Target)
			continue
		}
		ext := strings.ToLower(path.Ext(l.Path))
		var allowed []string
		switch {
		case l.Kind == linkStatic && l.Target == types.X86_64Windows.Name:
			allowed = []string{".lib"}
		case l.Kind == linkStatic:
			allowed = []string{".a"}
		case l.Target == types.X86_64Windows.Name:
			allowed = []string{"", ".def", ".dll"}
		default:
			allowed = []string{"", ".def"}
		}
		switch {
		case l.Path == "" && l.Kind == linkStatic:
			fail(f+".path", "static link %s names no archive; path is the package's .lib or .a", l.Name)
			continue
		case l.Path == "":
			continue
		case !slices.Contains(allowed, ext) || l.Path == ext:
			fail(f+".path", "link %s: path %s is not one of %s for a %s link on %s", l.Name, l.Path,
				extList(allowed), l.Kind, l.Target)
			continue
		case !packageRelative(l.Path):
			fail(f+".path", "link %s: path %s is not package-relative", l.Name, l.Path)
			continue
		}
		raw, err := t.readFile(l.Path)
		if err != nil {
			if t.osRoot != "" {
				fail(f+".path", "link %s: %s does not exist (as spelled)", l.Name, l.Path)
			}
			continue
		}
		if ext == ".def" {
			if _, err := parseLinkDef(string(raw), l.Target); err != nil {
				fail(f+".path", "link %s: %s is not a name list: %v", l.Name, l.Path, err)
			}
		}
		if l.Kind == linkStatic {
			if dll := importLibraryOf(raw); dll != "" {
				fail(f+".path", "link %s: %s is an import library for %s, not an archive of objects: name the .dll, or a .def of its names, in a dynamic link", l.Name, l.Path, dll)
			}
		}
	}
	return out
}

// extList spells the allowed path forms of a link for its message.
func extList(exts []string) string {
	var parts []string
	for _, e := range exts {
		if e == "" {
			parts = append(parts, `""`)
		} else {
			parts = append(parts, e)
		}
	}
	return strings.Join(parts, ", ")
}

// packageRelative reports whether p is a slash path inside the package:
// not absolute, no drive, no "..".
func packageRelative(p string) bool {
	if strings.HasPrefix(p, "/") || strings.Contains(p, `\`) || strings.Contains(p, ":") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return false
		}
	}
	return true
}

// validDLLName reports whether n can be the LIBRARY token of an import
// library and so an import table's DLL name: letters, digits, '_', '-' and
// '.', not starting with '.' (toolchain's validDLL).
func validDLLName(n string) bool {
	if n == "" || n[0] == '.' {
		return false
	}
	for i := 0; i < len(n); i++ {
		c := n[i]
		if !(c == '_' || c == '-' || c == '.' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// parseLinkDef reads a link's .def name list: std's format, one name per
// line (toolchain.ParseDefList); on Linux a name may carry
// @VERSION, kept in the returned name.
func parseLinkDef(text, target string) ([]string, error) {
	if target == types.X86_64Windows.Name {
		return toolchain.ParseDefList(text)
	}
	var names []string
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, ver, versioned := strings.Cut(line, "@")
		if _, err := toolchain.ParseDefList(name); err != nil || strings.Contains(name, "\n") || versioned && !validVersion(ver) {
			return nil, fmt.Errorf("line %d: %q is not one name or name@VERSION", i+1, line)
		}
		names = append(names, line)
	}
	return names, nil
}

// validVersion reports whether v is a symbol version node name.
func validVersion(v string) bool {
	if v == "" {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if !(c == '_' || c == '.' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// pkgLink is one pkg.Link of the bin's package graph, with the package it
// belongs to: dir is that package's directory on disk (a fetched one's
// unpacked copy, depCDir), man its manifest, for the span of a fault.
type pkgLink struct {
	Link
	pkg string // "" for the root package, else the dependency's name
	dir string
	man *Manifest
}

// label names the link in a message: its name and field, and the
// dependency it comes from.
func (l pkgLink) label() string {
	s := l.Name + " (" + l.Field
	if l.pkg != "" {
		s += " of dependency " + l.pkg
	}
	return s + ")"
}

// file is the link's path on disk.
func (l pkgLink) file() string { return filepath.Join(l.dir, filepath.FromSlash(l.Path)) }

// collectLinks lists the links of the bin's package graph in resolution order:
// the root package's in manifest order, then each dependency's in build
// order. A dependency with a link file and no directory (fetched) is
// unpacked as its C sources are.
func (s *Session) collectLinks(pkgMan *Manifest, rootDir string) ([]pkgLink, []*diag.Diagnostic) {
	var out []pkgLink
	if pkgMan != nil {
		for _, l := range pkgMan.Links {
			out = append(out, pkgLink{Link: l, dir: rootDir, man: pkgMan})
		}
	}
	for _, dp := range s.deps {
		if dp.man == nil || len(dp.man.Links) == 0 {
			continue
		}
		dir := dp.osDir
		for _, l := range dp.man.Links {
			if dir == "" && l.Path != "" {
				d, dd := s.depCDir(dp)
				if dd != nil {
					return nil, []*diag.Diagnostic{dd}
				}
				dir = d
			}
			out = append(out, pkgLink{Link: l, pkg: dp.name, dir: dir, man: dp.man})
		}
	}
	return out, nil
}

// linksFor are the links that apply to target.
func linksFor(all []pkgLink, target string) []pkgLink {
	var out []pkgLink
	for _, l := range all {
		if l.Target == target {
			out = append(out, l)
		}
	}
	return out
}

// hasStaticLink reports whether the session's package graph holds a
// static link for its target: std.libc is then linked.
func (s *Session) hasStaticLink() bool {
	target := s.cfg.Target.Name
	has := func(m *Manifest) bool {
		if m == nil {
			return false
		}
		for _, l := range m.Links {
			if l.Kind == linkStatic && l.Target == target {
				return true
			}
		}
		return false
	}
	if has(s.pkgMan) {
		return true
	}
	for _, dp := range s.deps {
		if has(dp.man) {
			return true
		}
	}
	return false
}

// linkFail is E0202 at a link of the graph, reason link.
func linkFail(l pkgLink, field, format string, args ...any) *diag.Diagnostic {
	return diag.New(diag.E0202, l.man.spanOf(l.Field+field), fmt.Sprintf(format, args...)).
		Text("manifest", l.Field+field).Text("reason", "link")
}

// dynLib is one dynamic Windows library of the graph: the exact file
// name, the names it supplies, and the .dll to copy beside the
// executable ("" when the path is "" or a .def).
type dynLib struct {
	file   string
	names  map[string]bool
	data   map[string]bool // data exports, never resolved; named in an extern var's E0001
	copy   string
	labels []string
}

// linkPlan is what the links add to one bin's link: the archives, in the
// graph's order, with their content keys; the named DLLs, whose
// used names neededImportLibs places; and the DLLs used, to copy.
type linkPlan struct {
	archives    []string
	archiveKeys []source.Hash
	labels      map[string]string // archive path to its link's label
	dlls        []*dynLib
	// cand are the names the named DLLs may supply: the Mk externs, the
	// shim's, the C sources' and the pulled archive members' undefined
	// names, less what an object or archive defines. Only the Mk externs
	// and the shim's go on to the system set.
	cand []string
	// copies are the .dll files of the used DLLs, filled by
	// neededImportLibs; copyKeys hashes every named .dll, used or not,
	// since the link key is computed first.
	copies   []*dynLib
	searched []string // the labels of every library searched, for E0001
	windows  bool     // the target is x86_64-windows
	// defs are the names an object, a pulled archive member or the
	// compiler defines, for externVarFaults (links_check.go).
	defs map[string]bool
	// memberDefs are the names a pulled archive member defines, so
	// soDefFaults can tell a member's definition from the compiler's
	// weak 128-bit routines (compilerWeak), which p.defs also holds.
	memberDefs map[string]bool
	// sos are the shared objects of a Linux bin in system-libc mode,
	// libc.so.6 first (links_libc.go); triple is the target's, and libc
	// and arch the mode's facts for it (set with sos[0]).
	sos    []*soLib
	triple string
	libc   libcSpec
	arch   types.Arch
}

// empty reports whether the plan changes nothing in the link.
func (p *linkPlan) empty() bool {
	return p == nil || len(p.archives) == 0 && len(p.dlls) == 0 && len(p.sos) == 0
}

// linkPlanFor reads the links of the bin's graph for the build's target.
// insts are the bin's instances, globals its var globals and cobjs its C
// objects. Faults: E0202 reason link at a link this host or build cannot
// take; E0001 for an extern var a named DLL would supply; a file
// that cannot be read, E0001 tool fs.
func (w *world) linkPlanFor(insts *mono.Instances, globals []codegen.Global, cobjs []string) (*linkPlan, []*diag.Diagnostic) {
	links := linksFor(append(append([]pkgLink(nil), w.links...), testPkgLinks()...), w.cfg.Target.Name)
	if len(links) == 0 {
		return nil, nil
	}
	p := &linkPlan{labels: map[string]string{}, windows: w.cfg.Target.OS == types.OSWindows, triple: w.cfg.Target.LLVMTriple}
	var ds []*diag.Diagnostic
	byFile := map[string]*dynLib{}
	for _, l := range links {
		if l.Kind == linkStatic {
			b, err := os.ReadFile(l.file())
			if err != nil {
				ds = append(ds, linkFail(l, ".path", "link %s: cannot read %s: %s", l.Name, l.Path, pathErrText(err)))
				continue
			}
			h := source.NewHasher("mk.driver.archive.v1")
			h.Bytes(b)
			abs, _ := filepath.Abs(l.file())
			p.archives, p.archiveKeys = append(p.archives, abs), append(p.archiveKeys, h.Sum())
			p.labels[abs] = l.label()
			p.searched = append(p.searched, "archive "+l.label())
			continue
		}
		if w.cfg.Target.OS != types.OSWindows {
			// System-libc mode (links_libc.go); refused while
			// libcModeBuilt is false.
			if d := p.addShared(w, l); d != nil {
				ds = append(ds, d)
			}
			continue
		}
		names, data, d := dllNames(l)
		if d != nil {
			ds = append(ds, d)
			continue
		}
		key := strings.ToLower(l.Name)
		dl := byFile[key]
		if dl == nil {
			dl = &dynLib{file: l.Name, names: map[string]bool{}, data: map[string]bool{}}
			byFile[key] = dl
			p.dlls = append(p.dlls, dl)
		}
		for _, n := range names {
			dl.names[n] = true
		}
		for _, n := range data {
			dl.data[n] = true
		}
		if strings.EqualFold(path.Ext(l.Path), ".dll") && dl.copy == "" {
			dl.copy, _ = filepath.Abs(l.file())
		}
		dl.labels = append(dl.labels, l.label())
		p.searched = append(p.searched, l.label())
	}
	if len(ds) > 0 {
		return nil, ds
	}
	// The names the links may supply: what the bin references and
	// no object of it defines; an archive member counts once the link
	// pulls it.
	refs, defs := map[string]bool{}, map[string]bool{}
	for _, n := range compilerDefined {
		defs[n] = true
	}
	for i := range insts.List {
		fd, ok := w.items.Get(insts.List[i].Item).Decl.(*types.FuncDecl)
		if !ok || !fd.Sig.Extern {
			continue
		}
		if insts.List[i].Body == nil {
			refs[insts.List[i].Symbol] = true
		} else {
			defs[insts.List[i].Symbol] = true
		}
	}
	// An extern var pulls the archive member that defines it, as lld
	// pulls it, but only functions go on to the DLLs: the var
	// itself is externVarFaults' (links_check.go).
	isVar := map[string]bool{}
	for _, v := range w.externVars(globals) {
		isVar[v.name] = true
		refs[v.name] = true
	}
	for _, o := range cobjs {
		d, u := objectSymbols(o)
		for _, n := range u {
			refs[n] = true
		}
		for _, n := range d {
			defs[n] = true
		}
	}
	pulled := archivePull(p.archives, refs, defs)
	for n := range refs {
		if !defs[n] && !isVar[n] {
			p.cand = append(p.cand, n)
		}
	}
	slices.Sort(p.cand)
	p.defs = defs
	p.memberDefs = map[string]bool{}
	for _, m := range pulled {
		d, _ := memberSymbols(m.data)
		for _, n := range d {
			p.memberDefs[n] = true
		}
	}
	if len(p.sos) > 0 {
		ds = append(ds, p.soVarFaults(w, w.externVars(globals))...)
		ds = append(ds, p.soDefFaults(w, insts, cobjs)...)
		ds = append(ds, p.soAddrFaults(cobjs, pulled)...)
	}
	// A pulled member whose constructors or thread-local storage the
	// runtime would never set up.
	for _, m := range pulled {
		if what, hint := memberFault(m.data); what != "" {
			label := p.labels[m.archive]
			ds = append(ds, diag.New(diag.E0001, source.Span{}, fmt.Sprintf("archive %s, member %s, has %s; %s", label, m.name, what, hint)).
				Text("tool", "mk").Text("archive", label).Text("member", m.name))
		}
	}
	return p, ds
}

// compilerMemFuncs are the memory functions every executable defines in the
// support pack (internal/codegen/support.go), which an archive or C source
// may call.
var compilerMemFuncs = []string{"memcpy", "memmove", "memset", "memcmp", "bcmp"}

// compilerWeak are the 128-bit division routines the support pack defines
// as weak aliases (internal/codegen/support_ti.go): any object's
// definition, a pulled archive member's included, takes the name instead.
var compilerWeak = []string{"__udivti3", "__umodti3", "__divti3", "__modti3"}

// compilerDefined are the names the support pack defines for any object of
// the link to call: the memory functions and compilerWeak. As for a name a
// C source defines, the link pulls no archive member for one of them alone
// (lld-link and ld.lld do not fetch a member for a name an object defines,
// weak or not), so archivePull is told they are defined. System-libc
// mode's duplicate check (soDefFaults) takes the memory functions alone: a
// shared object's own division routine beside the executable's weak one is
// what C dividing __int128 linked against (libgcc_s's __udivti3).
var compilerDefined = append(append([]string(nil), compilerMemFuncs...), compilerWeak...)

// externVarName is an extern var's symbol: its own name, the last part of
// its key (codegen's symbolName).
func externVarName(k types.Key) string {
	s := k.String()
	if i := strings.LastIndexAny(s, ".:"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// dllNames is the name list of a dynamic Windows link: its .def, the
// functions its .dll exports, or, for "", those of the host's
// %SystemRoot%\System32\<name> (E0202 off Windows).
func dllNames(l pkgLink) (funcs, data []string, d *diag.Diagnostic) {
	switch strings.ToLower(path.Ext(l.Path)) {
	case ".def":
		raw, err := os.ReadFile(l.file())
		if err != nil {
			return nil, nil, linkFail(l, ".path", "link %s: cannot read %s: %s", l.Name, l.Path, pathErrText(err))
		}
		names, err := parseLinkDef(string(raw), l.Target)
		if err != nil {
			return nil, nil, linkFail(l, ".path", "link %s: %s is not a name list: %v", l.Name, l.Path, err)
		}
		return names, nil, nil
	case ".dll":
		funcs, data, err := peFuncExports(l.file())
		if err != nil {
			return nil, nil, linkFail(l, ".path", "link %s: cannot read the export table of %s: %v", l.Name, l.Path, err)
		}
		return funcs, data, nil
	}
	if runtime.GOOS != "windows" || testNoSystem32 {
		return nil, nil, linkFail(l, ".path", "link %s: path \"\" reads %%SystemRoot%%\\System32\\%s, which only a Windows host has; ship a .def name list and name it in path", l.Name, l.Name)
	}
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	funcs, data, err := peFuncExports(filepath.Join(root, "System32", l.Name))
	if err != nil {
		return nil, nil, linkFail(l, ".path", "link %s: cannot read System32\\%s's export table: %v", l.Name, l.Name, err)
	}
	return funcs, data, nil
}

// testNoSystem32 makes dllNames and systemExports act as on a host that
// is not Windows; set by sequential tests only.
var testNoSystem32 bool

// namedImportLibs places the plan's candidate names: a name std's
// lists hold (in libs) stays there; otherwise the first named DLL that
// supplies it gets it, merged into std's or a system DLL's library when
// the file names agree without case. It returns libs with the named
// DLLs' libraries added (only the names used, sorted), the names left for
// the system set (Mk externs and the shim's only, from sysCand), and
// records each used .dll to copy.
func (p *linkPlan) namedImportLibs(libs []importLib, sysCand []string) ([]importLib, []string) {
	have := map[string]bool{}
	for _, l := range libs {
		for _, n := range l.names {
			have[n] = true
		}
	}
	used := map[*dynLib][]string{}
	taken := map[string]bool{}
	for _, n := range p.cand {
		if have[n] {
			continue
		}
		for _, dl := range p.dlls {
			if dl.names[n] {
				used[dl] = append(used[dl], n)
				taken[n] = true
				break
			}
		}
	}
	for _, dl := range p.dlls {
		names := used[dl]
		if len(names) == 0 {
			continue
		}
		if dl.copy != "" {
			p.copies = append(p.copies, dl)
		}
		key := strings.ToLower(dl.file)
		merged := false
		for i := range libs {
			if libs[i].dll+".dll" == key && libs[i].file == "" {
				libs[i].names = slices.Compact(slices.Sorted(slices.Values(append(append([]string(nil), libs[i].names...), names...))))
				merged = true
			}
		}
		if !merged {
			slices.Sort(names)
			libs = append(libs, importLib{dll: strings.TrimSuffix(key, ".dll"), file: dl.file, names: names})
		}
	}
	slices.SortStableFunc(libs, func(a, b importLib) int { return strings.Compare(a.dll, b.dll) })
	var rest []string
	for _, n := range sysCand {
		if !taken[n] {
			rest = append(rest, n)
		}
	}
	return libs, rest
}

// copyDLLs copies each .dll of the plan's used DLLs beside exe under the
// link's name, the one the import table holds and the loader looks up,
// whatever the path's own base name. A file there with the same bytes is
// kept, so a running copy is never touched. With st (exe is in
// the store's bin directory), a kept copy is marked as its executable is
// and a new one counted, so GC ages the two together.
func (p *linkPlan) copyDLLs(exe string, st *cache.Store) []*diag.Diagnostic {
	if p == nil {
		return nil
	}
	for _, dl := range p.copies {
		src := dl.copy
		dst := filepath.Join(filepath.Dir(exe), dl.file)
		want, err := os.ReadFile(src)
		if err == nil {
			if have, rerr := os.ReadFile(dst); rerr == nil && bytes.Equal(have, want) {
				markExe(st, dst)
				continue
			}
			// Two builds of one executable, in this process or in two,
			// copy its DLLs at once (writeSameFile).
			var wrote bool
			if wrote, err = writeSameFile(dst, want); wrote {
				countExe(st, dst)
			} else if err == nil {
				markExe(st, dst)
			}
		}
		if err != nil {
			return []*diag.Diagnostic{diag.Toolchain("fs", "copy "+dl.file, pathErrText(err),
				"cannot copy "+dl.file+" beside the executable").Alt(altCloseExe).Alt(altOtherOut)}
		}
	}
	return nil
}

// copyKeys are the content keys of the .dll files a used link copies, for
// the link key: a changed DLL is a new executable directory.
func (p *linkPlan) copyKeys() []source.Hash {
	var out []source.Hash
	for _, dl := range p.dlls {
		if dl.copy == "" {
			continue
		}
		b, _ := os.ReadFile(dl.copy)
		h := source.NewHasher("mk.driver.linkdll.v1")
		h.String(strings.ToLower(dl.file))
		h.Bytes(b)
		out = append(out, h.Sum())
	}
	return out
}

// linkErrWith is linkErr for a link the plan changed: an undefined symbol
// an archive member references is an E0001 naming the archive, the
// member and the symbol, with a hint for the usual causes; another
// undefined symbol names the libraries searched.
//
// Each undefined symbol an archive member references is its own E0001, and the
// rest give one E0001 for the first of them. lld-link writes a dllimport
// reference as "__declspec(dllimport) X"; the symbol fact and the hint read X.
func (p *linkPlan) linkErrWith(err error, work string) []*diag.Diagnostic {
	d := linkErr(err, work)
	if p.empty() {
		return []*diag.Diagnostic{d}
	}
	var te *toolchain.ToolError
	if !errors.As(err, &te) {
		return []*diag.Diagnostic{d}
	}
	var out []*diag.Diagnostic
	other := ""
	for _, raw := range undefinedSymbols(te.Stderr) {
		sym, imported := strings.CutPrefix(raw, dllimportPrefix)
		arch, member := archiveReferrer(te.Stderr, raw)
		if arch == "" {
			if other == "" {
				other = sym
			}
			continue
		}
		label := p.archiveLabel(arch)
		lib := "std.libc"
		if len(p.sos) > 0 {
			lib = "std's " + libcSoname + " list" // system-libc mode links no std.libc
		}
		msg := fmt.Sprintf("the link failed: archive %s, member %s, calls %s, which %s and this bin's libraries do not provide",
			label, member, sym, lib)
		if h := archiveHint(sym, imported); h != "" {
			msg += "; " + h
		} else if h := p.libcModeHint(sym); h != "" {
			msg += "; " + h
		}
		out = append(out, toolErrIn(err, "lld", msg, work).Text("symbol", sym).Text("archive", label).Text("member", member))
	}
	if other == "" {
		if len(out) == 0 {
			return []*diag.Diagnostic{d}
		}
		return out
	}
	msg := "the link failed: undefined symbol " + other + "; searched " + strings.Join(p.searched, ", ")
	if h := p.libcModeHint(other); h != "" {
		msg += "; " + h
	}
	if p.windows {
		// Resolution order: std's .def lists, the links, the implicit ten
		// (read only on a Windows host).
		msg = "the link failed: undefined symbol " + other + "; searched std's DLL lists, " + strings.Join(p.searched, ", ")
		if runtime.GOOS == "windows" {
			msg += ", then the system DLLs"
		}
	}
	return append(out, toolErrIn(err, "lld", msg, work).Text("symbol", other))
}

// archiveLabel is the label of the archive lld names arch: its path as
// given, a --debug link's staged name (toolchain.StagedArchive),
// or, failing both, an archive of that base name.
func (p *linkPlan) archiveLabel(arch string) string {
	if l := p.labels[arch]; l != "" {
		return l
	}
	for i, a := range p.archives {
		if strings.EqualFold(filepath.Base(arch), toolchain.StagedArchive(i)) {
			return p.labels[a]
		}
	}
	for a, l := range p.labels {
		if strings.EqualFold(filepath.Base(a), filepath.Base(arch)) {
			return l
		}
	}
	return filepath.Base(arch)
}

// archiveHint is the hint for an undefined symbol of an archive
// member: the usual reasons an archive asks for what std.libc lacks.
// imported is a dllimport reference (lld-link's "__declspec(dllimport)
// X", ld.lld's "__imp_X").
func archiveHint(sym string, imported bool) string {
	if s, ok := strings.CutPrefix(sym, "__imp_"); ok {
		sym, imported = s, true
	}
	switch {
	case sym == "__security_cookie" || sym == "__security_check_cookie" || sym == "__GSHandlerCheck":
		return "the object was built with a stack protector: rebuild it with /GS- (clang: -fno-stack-protector)"
	case strings.HasPrefix(sym, "_RTC_"):
		return "the object was built with run-time checks: rebuild it without /RTC"
	case sym == "__acrt_iob_func":
		return "the archive needs a C runtime, which an Mk program does not link"
	case imported:
		return "the member imports it from a DLL (__declspec(dllimport)) that no link supplies: if it is the C runtime's, the archive needs a C runtime, which an Mk program does not link; otherwise name its DLL in links"
	case sym == "___chkstk_ms" || sym == "__main":
		return "this is a MinGW object, which mk does not link: rebuild it with clang or MSVC for the target"
	}
	return ""
}

// archiveReferrer finds, in lld's report of the undefined symbol sym, the
// archive and member that reference it: lld-link writes
// ">>> referenced by <archive>(<member>):(<function>)" and ld.lld
// ">>>               <member>:(<function>) in archive <archive>". "" when
// no archive member references it.
func archiveReferrer(stderr, sym string) (archive, member string) {
	i := strings.Index(stderr, "undefined symbol: "+sym)
	if i < 0 {
		return "", ""
	}
	rest := stderr[i:]
	lines := strings.Split(strings.ReplaceAll(rest, "\r", ""), "\n")
	for _, line := range lines[1:] {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, ">>>") {
			break
		}
		t = strings.TrimSpace(strings.TrimPrefix(t, ">>>"))
		t = strings.TrimPrefix(t, "referenced by ")
		if a := after(t, " in archive "); a != "" {
			m, _, _ := strings.Cut(t, ":(")
			return strings.TrimSpace(a), filepath.Base(strings.TrimSpace(m))
		}
		// lld-link: <archive>(<member>):(<function>)
		if j := strings.Index(t, ".lib("); j >= 0 {
			k := strings.Index(t[j:], ")")
			if k > 0 {
				return t[:j+4], t[j+5 : j+k]
			}
		}
		if j := strings.Index(t, ".a("); j >= 0 {
			k := strings.Index(t[j:], ")")
			if k > 0 {
				return t[:j+2], t[j+3 : j+k]
			}
		}
	}
	return "", ""
}

// after is the text of s after sep, or "" when s holds no sep.
func after(s, sep string) string {
	if i := strings.Index(s, sep); i >= 0 {
		return s[i+len(sep):]
	}
	return ""
}

// peFuncExports lists the functions in a PE file's export directory: an
// export whose RVA lies inside the directory is a forwarder and resolves
// like a function; one whose RVA lies in a section that is not executable
// is data and is left out, so it is never resolved.
// Names an import library cannot carry (ordinal-only, decorated) are left
// out too. The data exports are listed apart, for an extern var's E0001.
func peFuncExports(file string) (funcs, data []string, err error) {
	f, err := pe.Open(file)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	oh, ok := f.OptionalHeader.(*pe.OptionalHeader64)
	if !ok || oh.NumberOfRvaAndSizes == 0 {
		return nil, nil, errors.New("not a 64-bit image with an export directory")
	}
	dir := oh.DataDirectory[pe.IMAGE_DIRECTORY_ENTRY_EXPORT]
	if dir.VirtualAddress == 0 {
		return nil, nil, errors.New("no export directory")
	}
	secData := map[*pe.Section][]byte{}
	section := func(rva uint32) *pe.Section {
		for _, s := range f.Sections {
			if rva >= s.VirtualAddress && rva < s.VirtualAddress+max(s.VirtualSize, s.Size) {
				return s
			}
		}
		return nil
	}
	at := func(rva uint32) []byte {
		s := section(rva)
		if s == nil {
			return nil
		}
		d, ok := secData[s]
		if !ok {
			d, _ = s.Data()
			secData[s] = d
		}
		if off := rva - s.VirtualAddress; off < uint32(len(d)) {
			return d[off:]
		}
		return nil
	}
	ed := at(dir.VirtualAddress)
	if len(ed) < 40 {
		return nil, nil, errors.New("export directory outside the sections")
	}
	nfuncs := binary.LittleEndian.Uint32(ed[20:])
	nnames := binary.LittleEndian.Uint32(ed[24:])
	addrs := at(binary.LittleEndian.Uint32(ed[28:]))
	names := at(binary.LittleEndian.Uint32(ed[32:]))
	ords := at(binary.LittleEndian.Uint32(ed[36:]))
	if nnames > 1<<16 || nfuncs > 1<<16 || uint64(len(names)) < 4*uint64(nnames) ||
		uint64(len(ords)) < 2*uint64(nnames) || uint64(len(addrs)) < 4*uint64(nfuncs) {
		return nil, nil, errors.New("export tables outside the sections")
	}
	for i := uint32(0); i < nnames; i++ {
		raw := at(binary.LittleEndian.Uint32(names[4*i:]))
		j := bytes.IndexByte(raw, 0)
		if j <= 0 {
			continue
		}
		name := string(raw[:j])
		if _, err := toolchain.ParseDefList(name); err != nil {
			continue
		}
		idx := uint32(binary.LittleEndian.Uint16(ords[2*i:]))
		if idx >= nfuncs {
			continue
		}
		rva := binary.LittleEndian.Uint32(addrs[4*idx:])
		if rva >= dir.VirtualAddress && rva < dir.VirtualAddress+dir.Size {
			funcs = append(funcs, name) // a forwarder
			continue
		}
		s := section(rva)
		if s == nil || s.Characteristics&(pe.IMAGE_SCN_MEM_EXECUTE|pe.IMAGE_SCN_CNT_CODE) == 0 {
			data = append(data, name)
			continue
		}
		funcs = append(funcs, name)
	}
	return funcs, data, nil
}

// objectSymbols lists an object file's defined and undefined external
// symbols, COFF or ELF; a file mk cannot read gives none (the linker
// reports what matters). COFF's dllimport prefix __imp_ is removed.
func objectSymbols(file string) (defined, undefined []string) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, nil
	}
	return memberSymbols(b)
}

// memberSymbols is objectSymbols over an object's bytes.
func memberSymbols(b []byte) (defined, undefined []string) {
	if bytes.HasPrefix(b, []byte("\x7fELF")) {
		f, err := elf.NewFile(bytes.NewReader(b))
		if err != nil {
			return nil, nil
		}
		syms, _ := f.Symbols()
		for _, s := range syms {
			bind := elf.ST_BIND(s.Info)
			if bind != elf.STB_GLOBAL && bind != elf.STB_WEAK || s.Name == "" {
				continue
			}
			if s.Section == elf.SHN_UNDEF {
				if bind == elf.STB_GLOBAL {
					undefined = append(undefined, s.Name)
				}
			} else {
				defined = append(defined, s.Name)
			}
		}
		return defined, undefined
	}
	if len(b) >= 4 && binary.LittleEndian.Uint16(b) == 0 && binary.LittleEndian.Uint16(b[2:]) == 0xFFFF {
		return nil, nil // a short import member, or bitcode in a wrapper
	}
	f, err := pe.NewFile(bytes.NewReader(b))
	if err != nil {
		return nil, nil
	}
	for _, s := range f.Symbols {
		if s.StorageClass != 2 /* IMAGE_SYM_CLASS_EXTERNAL */ && s.StorageClass != 105 /* WEAK_EXTERNAL */ {
			continue
		}
		name := strings.TrimPrefix(s.Name, "__imp_")
		switch {
		case s.SectionNumber > 0 || s.SectionNumber == 0 && s.Value > 0 || s.StorageClass == 105:
			defined = append(defined, name)
		case s.SectionNumber == 0:
			undefined = append(undefined, name)
		}
	}
	return defined, undefined
}

// arMember is one object of a static archive.
type arMember struct {
	name string
	data []byte
}

// readArchive lists the object members of a Unix ar archive (GNU, BSD or
// the COFF .lib form): the symbol tables and the long-name table are
// skipped.
func readArchive(file string) ([]arMember, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	return arMembers(b)
}

// arMembers is readArchive over the archive's bytes.
func arMembers(b []byte) ([]arMember, error) {
	const magic = "!<arch>\n"
	if !bytes.HasPrefix(b, []byte(magic)) {
		return nil, errors.New("not an ar archive")
	}
	var out []arMember
	var longNames []byte
	for off := len(magic); off+60 <= len(b); {
		hdr := b[off : off+60]
		size, err := strconv.ParseUint(strings.TrimSpace(string(hdr[48:58])), 10, 64)
		if err != nil || uint64(off+60)+size > uint64(len(b)) {
			return nil, errors.New("bad member header")
		}
		name := strings.TrimRight(string(hdr[0:16]), " ")
		data := b[off+60 : off+60+int(size)]
		off += 60 + int(size)
		if off%2 == 1 {
			off++
		}
		switch {
		case name == "/" || name == "/SYM64/" || name == "__.SYMDEF" || name == "__.SYMDEF SORTED":
			continue
		case name == "//":
			longNames = data
			continue
		case strings.HasPrefix(name, "#1/"):
			n, err := strconv.Atoi(name[3:])
			if err != nil || n > len(data) {
				return nil, errors.New("bad BSD member name")
			}
			name, data = strings.TrimRight(string(data[:n]), "\x00"), data[n:]
		case strings.HasPrefix(name, "/"):
			n, err := strconv.Atoi(name[1:])
			if err != nil || n >= len(longNames) {
				return nil, errors.New("bad long member name")
			}
			// A GNU entry ends "/\n"; a COFF entry ends in NUL. Either may
			// hold a path with '/' in it (llvm-lib on Linux stores one).
			rest := longNames[n:]
			if j := bytes.IndexAny(rest, "\n\x00"); j >= 0 {
				rest = rest[:j]
			}
			name = strings.TrimSuffix(string(rest), "/")
		default:
			name = strings.TrimSuffix(name, "/")
		}
		out = append(out, arMember{name: name, data: data})
	}
	return out, nil
}

// archivePull adds to refs the undefined names of the members of archives
// that the link pulls: a member is pulled when it defines a name refs
// holds and defs does not, and its own references then join refs. defs
// gains what pulled members define. The members of every archive are
// visited together until nothing more is pulled, so a member of one
// archive that only a later archive's member references is pulled too, as
// lld pulls it. An archive mk cannot read pulls nothing here; the linker
// reports what it lacks. It returns the pulled members, in archive and
// member order.
func archivePull(archives []string, refs, defs map[string]bool) []pulledMember {
	type syms struct{ d, u []string }
	var tab []syms
	var members []pulledMember
	for _, a := range archives {
		ms, err := readArchive(a)
		if err != nil {
			continue
		}
		for _, m := range ms {
			var s syms
			s.d, s.u = memberSymbols(m.data)
			tab = append(tab, s)
			members = append(members, pulledMember{archive: a, name: m.name, data: m.data})
		}
	}
	pulled := make([]bool, len(members))
	for changed := true; changed; {
		changed = false
		for i := range members {
			if pulled[i] {
				continue
			}
			for _, n := range tab[i].d {
				if refs[n] && !defs[n] {
					pulled[i], changed = true, true
					for _, d := range tab[i].d {
						defs[d] = true
					}
					for _, u := range tab[i].u {
						refs[u] = true
					}
					break
				}
			}
		}
	}
	var out []pulledMember
	for i, m := range members {
		if pulled[i] {
			out = append(out, m)
		}
	}
	return out
}

// pulledMember is an archive member the link pulls: its archive's path
// (a key of linkPlan.labels), its name and its bytes.
type pulledMember struct {
	archive, name string
	data          []byte
}

// keys folds the plan into the link key's extra keys: the
// archives' contents and the copied DLLs'. Nil for an empty plan, so a
// build with no links keeps the key it has without links.
func (p *linkPlan) keys() []source.Hash {
	if p.empty() {
		return nil
	}
	out := append([]source.Hash(nil), p.archiveKeys...)
	out = append(out, p.copyKeys()...)
	return append(out, p.optionKeys()...)
}
