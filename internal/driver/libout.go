// Why: a bin whose pkg.Bin kind is "dynamic" or "static" is a library for C
// hosts, not an executable, so its build ends differently: other exports,
// other link steps and files beside the output.
//
// What: the library half of mk build: libJob, libKind, libFileName,
// buildLibrary, the export lists (libExports, libVars), finishLibrary,
// versionScript, fromCRoots, libNameOf and defMarkData.
//
// How: a library exports exactly its own package's export extern functions
// and vars plus the runtime's two entries. A dynamic library is linked with
// a .def and an import library on Windows, or a soname and a version script
// on Linux; a static one is archived from every object, with the system
// import libraries and the package's static links merged in. The C header
// (libheader.go) goes beside it.

package driver

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"mk/internal/cache"
	"mk/internal/codegen"
	"mk/internal/diag"
	"mk/internal/mono"
	"mk/internal/source"
	"mk/internal/toolchain"
	"mk/internal/types"
)

// libJob is a library bin's output: its kind, ABI number and the final
// path of the library file; header and implib are written beside it.
type libJob struct {
	kind toolchain.OutputKind
	abi  string
	dest string
	// exports are the library's own package's export extern functions
	// (libExports of own); notes are the build's notes on it
	// (exports with no C form, a static library's links), which
	// buildLibrary adds to the result.
	exports []types.ItemID
	// vars are its own package's export extern vars with a C form
	// (libVars): data exports, listed after the functions.
	vars  []types.ItemID
	notes []string
}

// libKind is a pkg.Bin kind as an output kind; ok is false for "" (an
// executable). "object" is not built; the manifest step refuses it and every
// unknown kind (manifest.go), so none reaches here.
func libKind(kind string) (toolchain.OutputKind, bool) {
	switch kind {
	case "dynamic":
		return toolchain.Dynamic, true
	case "static":
		return toolchain.Static, true
	}
	return toolchain.Executable, false
}

// libFileName is the library's file name for target t: name.dll or
// name.lib on Windows, libname.so.<abi> or libname.a on Linux.
func libFileName(name, abi string, k toolchain.OutputKind, t types.Target) string {
	if abi == "" {
		abi = "1"
	}
	switch {
	case t.OS == types.OSWindows && k == toolchain.Dynamic:
		return name + ".dll"
	case t.OS == types.OSWindows:
		return name + ".lib"
	case k == toolchain.Dynamic:
		return "lib" + name + ".so." + abi
	}
	return "lib" + name + ".a"
}

// libTarget reports whether mk builds a library output for t: x86_64
// Windows and Linux, and aarch64 Linux.
func libTarget(t types.Target) bool {
	return t.Arch == types.ArchX64 && (t.OS == types.OSWindows || t.OS == types.OSLinux) ||
		t.Arch == types.ArchArm64 && t.OS == types.OSLinux
}

// buildLibrary builds one library bin: out names the library file (one
// bin) or the directory for every output (several bins, or "": the
// current directory). The library is never stored under <cache>/bin: its
// header and import library travel with it, so it is linked every build.
func (s *Session) buildLibrary(bin binTarget, out string, several bool) (string, []*diag.Diagnostic) {
	k, _ := libKind(bin.kind)
	file := libFileName(bin.name, bin.abi, k, s.cfg.Target)
	dest := out
	if dest == "" || several || outIsDir(out) {
		dest = filepath.Join(out, file)
	}
	if d := notBuilt(s.cfg.Target); d != nil {
		return "", []*diag.Diagnostic{d}
	}
	if !libTarget(s.cfg.Target) {
		return "", []*diag.Diagnostic{diag.New(diag.E0202, source.Span{}, "a library output is built for x86_64-windows, x86_64-linux and aarch64-linux only").
			Text("target", s.cfg.Target.Name).Text("reason", "not-built").Alt("build for x86_64-windows, x86_64-linux or aarch64-linux")}
	}
	if s.cfg.Target.OS == types.OSLinux && s.libcMode() {
		// A library links no C library and has no interpreter: a dynamic
		// links entry for the target (system-libc mode) is a hosted
		// executable's (never E0001 at the link).
		return "", []*diag.Diagnostic{diag.New(diag.E0202, source.Span{}, "bin "+bin.name+": a library is not built in system-libc mode: the package has a dynamic links entry for "+s.cfg.Target.Name+", which only an executable takes").
			Text("target", s.cfg.Target.Name).Text("reason", "not-built").Alt("link the shared object into the host program instead, or use a static links entry")}
	}
	tools, d := s.tools()
	if d != nil {
		return "", []*diag.Diagnostic{d}
	}
	cdir := ""
	if !s.cfg.NoCache {
		cdir = s.cacheDir()
	}
	s.w.display = s.displayNames(nil)
	p := s.p
	mods := s.binMods(bin.mod)
	// The exports are the root package's own: the bin's modules less std's
	// and the dependencies'. binMods already left out another bin's modules,
	// so an executable's export extern functions never join a library of the
	// same package.
	own := map[*modNode]bool{}
	for _, m := range s.pkgB.mods {
		own[m] = true
	}
	var ownMods []*modNode
	for _, m := range mods {
		if own[m] {
			ownMods = append(ownMods, m)
		}
	}
	lib := &libJob{kind: k, abi: bin.abi, dest: dest, exports: s.w.libExports(ownMods)}
	var vleft []cExport
	lib.vars, vleft = s.w.libVars(ownMods)
	_, left := s.w.cSplit(lib.exports)
	left = append(left, vleft...)
	for _, e := range left {
		where := "its export list and " + bin.name + ".h"
		if k == toolchain.Static {
			where = bin.name + ".h"
		}
		lib.notes = append(lib.notes, "note: library "+bin.name+" leaves "+e.name+" out of "+where+": "+e.why+
			", which has no C form across the library boundary")
	}
	out, ds := s.w.native(nativeJob{tools: tools, name: bin.name, mainMod: bin.mod.path, pkg: mods, timed: p.timed,
		pkgRoot: s.root.dir, cache: cdir, gro: generalRegsOnly(s.cfg.Target, bin.cpu), subsystem: bin.subsystem,
		place: func(string) (string, []*diag.Diagnostic) { return "", nil },
		lib:   lib})
	p.notes = append(p.notes, lib.notes...)
	return out, ds
}

// libExports are the export extern functions with a body of the modules
// pkg, sorted by key. buildLibrary passes the library's own package's
// modules alone (never std's or a dependency's).
func (w *world) libExports(pkg []*modNode) []types.ItemID {
	var out []types.ItemID
	for _, m := range pkg {
		if m.mod == nil {
			continue
		}
		for _, id := range m.mod.Items {
			it := w.items.Get(id)
			if fd, ok := it.Decl.(*types.FuncDecl); ok && fd.Sig.Extern && it.Exported && !fd.Bodiless && len(fd.Sig.Generics) == 0 {
				out = append(out, id)
			}
		}
	}
	slices.SortFunc(out, func(a, b types.ItemID) int { return cmpKey(w.items.Get(a).Key, w.items.Get(b).Key) })
	return out
}

// libVars are the export extern vars of the modules pkg that the build
// defines (definesVar), sorted by key: a library's data exports,
// exported as its functions are. One whose type has no C form is left
// out, with its reason, as a function is.
func (w *world) libVars(pkg []*modNode) (keep []types.ItemID, left []cExport) {
	c := newCForm(w)
	var all []types.ItemID
	for _, m := range pkg {
		if m.mod == nil {
			continue
		}
		for _, id := range m.mod.Items {
			if w.definesVar(id) {
				all = append(all, id)
			}
		}
	}
	slices.SortFunc(all, func(a, b types.ItemID) int { return cmpKey(w.items.Get(a).Key, w.items.Get(b).Key) })
	for _, id := range all {
		if e := c.exportVar(id, false); e.decl != "" {
			keep = append(keep, id)
		} else {
			left = append(left, e)
		}
	}
	return keep, left
}

// finishLibrary is native's last step for a library bin, after the packs'
// objects and the import libraries: a dynamic library is linked with its
// .def and import library (COFF) or soname and version script (ELF), a
// static one archived from every object; then the library, its import
// library and its header are moved beside lib.dest.
func (w *world) finishLibrary(ctx context.Context, tools *toolchain.Tools, st *cache.Store, j nativeJob, job toolchain.LinkJob,
	objs, libs, cobjs []string, plan *linkPlan, work string) (string, []*diag.Diagnostic) {
	lib := j.lib
	// Only exports with a C form are listed; the header names
	// the others in a comment.
	listed, _ := w.cSplit(lib.exports)
	var names []string
	for _, id := range listed {
		names = append(names, w.in.Str(w.items.Get(id).Name))
	}
	var data []string // the data exports
	for _, id := range lib.vars {
		data = append(data, w.in.Str(w.items.Get(id).Name))
	}
	// The runtime's two entries, exported with the functions.
	if s, t := codegen.LibEntryNames(cIdent(j.name)); true {
		names = append(names, s, t)
	}
	file := filepath.Base(lib.dest)
	built := filepath.Join(work, file)
	implib := ""
	all := append(append(append([]string(nil), objs...), w.cfg.ExtraObjects...), cobjs...)
	fsErr := func(err error, what string) []*diag.Diagnostic {
		return []*diag.Diagnostic{diag.Toolchain("fs", what, pathErrText(err), "cannot write the library "+filepath.ToSlash(lib.dest))}
	}
	switch lib.kind {
	case toolchain.Static:
		var err error
		// On COFF the system DLLs' import libraries are merged into the
		// archive (llvm-lib copies their members), so a host links the
		// library with nothing else. The package's static links (archives)
		// are merged as well, their members copied in. A shared link cannot
		// be: the host links it, and a note says so.
		in := all
		if job.Format == toolchain.COFF {
			in = append(append([]string(nil), all...), libs...)
		}
		if plan != nil {
			in = append(append([]string(nil), in...), plan.archives...)
			for _, so := range plan.sos {
				lib.notes = append(lib.notes, "note: static library "+j.name+" does not hold the shared library "+so.name+
					"; a host that links it links "+so.name+" too")
			}
		}
		j.timed("archive", func() { err = tools.StaticLib(ctx, job.Format, in, built) })
		if err != nil {
			return "", []*diag.Diagnostic{toolErrIn(err, "llvm-lib", "the static library failed", work)}
		}
	case toolchain.Dynamic:
		job.Kind, job.Output, job.Subsystem = toolchain.Dynamic, built, ""
		job.StackReserve = 0
		job.Objects, job.Libs = all, libs
		if ds := plan.apply(ctx, tools, work, &job); len(ds) > 0 {
			return "", ds
		}
		job.Entry, job.StackReserve = "", 0
		if job.Format == toolchain.COFF {
			text := "LIBRARY " + file + "\nEXPORTS\n"
			if len(names) > 0 {
				var err error
				if text, err = toolchain.DefText(file, append(slices.Clone(names), data...)); err != nil {
					return "", []*diag.Diagnostic{toolErrIn(err, "lld-link", "the library's export list failed", work)}
				}
				text = defMarkData(text, data)
			}
			job.Def = filepath.Join(work, j.name+".def")
			if err := os.WriteFile(job.Def, []byte(text), 0o644); err != nil {
				return "", fsErr(err, "write "+j.name+".def")
			}
			implib = j.name + ".lib"
			if strings.EqualFold(implib, file) {
				implib = j.name + ".dll.lib"
			}
			job.ImpLib = filepath.Join(work, implib)
		} else {
			job.Soname = file
			job.VersionScript = filepath.Join(work, j.name+".ver")
			if err := os.WriteFile(job.VersionScript, []byte(versionScript(j.name, lib.abi, append(slices.Clone(names), data...))), 0o644); err != nil {
				return "", fsErr(err, "write "+j.name+".ver")
			}
		}
		if job.LTO {
			job.LTOCacheDir, job.LTOCacheSize = ltoDir(st)
		}
		if h := w.cfg.TestHooks.OnLink; h != nil {
			h(job)
		}
		var lerr error
		j.timed("link", func() { lerr = tools.Link(ctx, job) })
		if lerr != nil {
			return "", w.sysViewLinkHints(plan.linkErrWith(lerr, work), lerr, work)
		}
	}
	header := filepath.Join(work, j.name+".h")
	if err := os.WriteFile(header, []byte(w.libHeader(j.name, lib.exports, lib.vars, lib.kind == toolchain.Dynamic && job.Format == toolchain.COFF,
		lib.kind == toolchain.Dynamic && job.Format == toolchain.ELF)), 0o644); err != nil {
		return "", fsErr(err, "write "+j.name+".h")
	}
	if j.place != nil {
		if _, ds := j.place(built); len(ds) > 0 {
			return "", ds // an E0708 stops the placement (walktail.go)
		}
	}
	out, ds := placeExe(built, lib.dest)
	if len(ds) > 0 {
		return "", ds
	}
	dir := filepath.Dir(out)
	moves := [][2]string{{header, filepath.Join(dir, j.name+".h")}}
	if implib != "" {
		moves = append(moves, [2]string{job.ImpLib, filepath.Join(dir, implib)})
	}
	if pdb := pdbOf(job); pdb != "" && lib.kind == toolchain.Dynamic {
		moves = append(moves, [2]string{pdb, filepath.Join(dir, filepath.Base(pdb))})
	}
	for _, m := range moves {
		if _, ds := placeExe(m[0], m[1]); len(ds) > 0 {
			return "", ds
		}
	}
	return out, nil
}

// versionScript is a Linux library's version script: one node
// <NAME>_<abi> whose global list is the exports, every other symbol local.
func versionScript(name, abi string, exports []string) string {
	if abi == "" {
		abi = "1"
	}
	node := strings.ToUpper(cIdent(name)) + "_" + abi
	if len(exports) == 0 {
		return node + " {\n  local: *;\n};\n"
	}
	return node + " {\n  global:\n    " + strings.Join(exports, ";\n    ") + ";\n  local: *;\n};\n"
}

// fromCRoots adds the runtime's calls-from-C entries to roots (codegen
// exports.go): std.task.FromCNeeded and RunFromC when a root is an
// export extern outside std that C may call (not a runtime hook), so its
// thunk can run it as a task; and a library's MkLibStart and MkLibStop,
// which its <name>_mk_start and <name>_mk_stop call. Not on freestanding.
// They are user roots (RootExport): they take task slots, so their reach
// makes UsesTasks true (internal/mono tasks.go).
func (w *world) fromCRoots(roots []mono.Root, lib bool) []mono.Root {
	if isFree(w.cfg.Target) {
		return roots
	}
	var keys []types.Key
	for _, r := range roots {
		if r.Kind != mono.RootExport {
			continue
		}
		it := w.items.Get(r.Item)
		if isStdPath(w.items.Module(it.Module).Path) {
			continue
		}
		switch w.in.Str(it.Name) {
		case "MkPanicHook", "MkThreadYield":
			continue
		}
		keys = append(keys, types.ItemKey("std.task", "FromCNeeded"), types.ItemKey("std.task", "RunFromC"))
		break
	}
	if lib {
		keys = append(keys, types.ItemKey("std.task", "MkLibStart"), types.ItemKey("std.task", "MkLibStop"))
	}
	for _, k := range keys {
		if id, ok := w.items.ByKey(k); ok {
			roots = append(roots, mono.Root{Kind: mono.RootExport, Item: id})
		}
	}
	return roots
}

// libNameOf is a library build's name for its runtime entries, "" for an
// executable.
func libNameOf(j nativeJob) string {
	if j.lib == nil {
		return ""
	}
	return cIdent(j.name)
}

// defMarkData marks each of data's names DATA in text, a .def DefText
// wrote, so the import library gives a host __imp_<name>, the address of
// the data, and no code thunk under its name. Only the lines
// after the LIBRARY and EXPORTS lines are names: a var named EXPORTS is
// marked on its own line, not on the header's.
func defMarkData(text string, data []string) string {
	lines := strings.SplitAfter(text, "\n")
	for i := 2; i < len(lines); i++ {
		if n := strings.TrimSuffix(lines[i], "\n"); n != "" && slices.Contains(data, n) {
			lines[i] = n + " DATA\n"
		}
	}
	return strings.Join(lines, "")
}
