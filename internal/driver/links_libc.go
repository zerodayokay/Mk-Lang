// Why: two link rules depend on what a bin's package graph links. A Windows
// bin with a dynamic link must not load DLLs from the current directory or
// PATH; a Linux bin with a dynamic link must run on the system libc
// (system-libc mode) instead of Mk's static, libc-free link.
//
// What: safeDLLSearch (the DependentLoadFlags 0xA00 load configuration),
// libcMode and libcBuild, the stub shared objects linked from the links'
// name lists, the libc-mode entry, linkPlan.apply and optionKeys, and the
// mode's checks (soVarFaults, soDefFaults, soAddrFaults, directRefs,
// libcModeHint).
//
// How: in system-libc mode each dynamic link becomes a deterministic stub
// .so built from its name list with a version script, the executable
// exports nothing and std.libc is not linked (one allocator: the system's);
// Mk's own _start calls __libc_start_main. The checks reject what would
// link but bind wrongly: data reached as a function, a name the executable
// also defines.

package driver

import (
	"bytes"
	"context"
	"debug/elf"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"mk/internal/codegen"
	"mk/internal/diag"
	"mk/internal/mono"
	"mk/internal/source"
	"mk/internal/toolchain"
	"mk/internal/types"
)

// safeDLLSearch is the Windows load configuration's DependentLoadFlags:
// LOAD_LIBRARY_SEARCH_APPLICATION_DIR | LOAD_LIBRARY_SEARCH_SYSTEM32, so
// the loader takes an import-table DLL only from KnownDLLs, the
// executable's directory and System32, never the current directory or
// PATH.
const safeDLLSearch = 0xA00

// libcModeBuilt is system-libc mode's switch. False refuses the mode:
// every dynamic link for x86_64-linux is E0202 reason link when a bin for
// that target is built.
const libcModeBuilt = true

// libcSoname is the system-libc mode's libc, the first shared object of
// every bin in the mode.
const libcSoname = "libc.so.6"

// libcSpec is a target's system-libc facts: the dynamic linker (PT_INTERP),
// std's name list for libc.so.6 (in the std tree) and glibc's base symbol
// version there, the version of every name the C standard library had
// before glibc 2.34 moved libpthread and libdl into libc.
type libcSpec struct {
	interp, list, base string
}

// libcSpecs are the targets that have system-libc mode.
var libcSpecs = map[string]libcSpec{
	types.X86_64Linux.Name:  {interp: "/lib64/ld-linux-x86-64.so.2", list: "sys/linux/libc.so.6.def", base: "GLIBC_2.2.5"},
	types.Aarch64Linux.Name: {interp: "/lib/ld-linux-aarch64.so.1", list: "sys/linux/libc.so.6.aarch64.def", base: "GLIBC_2.17"},
}

// libcFor is t's system-libc facts; ok is false for a target with no
// system-libc mode.
func libcFor(t types.Target) (libcSpec, bool) {
	if !libcModeBuilt {
		return libcSpec{}, false
	}
	sp, ok := libcSpecs[t.Name]
	return sp, ok
}

// testLinks are links the root package's graph gains in every session,
// set only by internal/driver's tests, never in parallel with another
// build: a driver test builds std's own runtime test blocks, whose manifest
// stays links: [], in system-libc mode.
var testLinks []Link

// testPkgLinks are testLinks as links of the graph, with no package
// directory and an empty manifest for their spans; linkPlanFor adds them
// whatever the session's root (a std test session collects no links).
func testPkgLinks() []pkgLink {
	var out []pkgLink
	for _, l := range testLinks {
		out = append(out, pkgLink{Link: l, man: &Manifest{}})
	}
	return out
}

// soLib is one shared object of a Linux bin in system-libc mode: the
// loader's exact name (DT_NEEDED), its list's names in first-seen order,
// each "name" or "name@VERSION", and the links that name it.
type soLib struct {
	name   string
	names  []string
	seen   map[string]bool     // bare names already listed
	from   map[string]*pkgLink // the link whose list first named each bare name; nil for std's list
	labels []string
}

// libcMode reports whether the build's target has system-libc mode
// (libcSpecs) and the session's package graph holds a dynamic link for it:
// the bin is in system-libc mode, so std.libc is not linked (deps.go).
func (s *Session) libcMode() bool {
	if _, ok := libcFor(s.cfg.Target); !ok {
		return false
	}
	has := func(m *Manifest) bool {
		if m == nil {
			return false
		}
		for _, l := range m.Links {
			if l.Kind == linkDynamic && l.Target == s.cfg.Target.Name {
				return true
			}
		}
		return false
	}
	if has(s.pkgMan) || has(&Manifest{Links: testLinks}) {
		return true
	}
	for _, dp := range s.deps {
		if has(dp.man) {
			return true
		}
	}
	return false
}

// libcBuild is libcMode read from the world, for the builds and the walk:
// the value of every std.sys.LibcBuild() call (mono's LibcMode).
// Its inputs are linkPlanFor's own, the graph's links and testLinks for
// the target, so it holds exactly when the plan adds a shared object and
// links the libc-mode entry (native.go checks the two agree). It is
// Session.libcMode for a package session; a session with no package (a
// file, a std test, the corpus) has only testLinks.
func (w *world) libcBuild() bool {
	if _, ok := libcFor(w.cfg.Target); !ok {
		return false
	}
	for _, l := range linksFor(append(append([]pkgLink(nil), w.links...), testPkgLinks()...), w.cfg.Target.Name) {
		if l.Kind == linkDynamic {
			return true
		}
	}
	return false
}

// addShared adds the dynamic Linux link l to the plan: the first one also
// adds std's libc.so.6 list, so libc.so.6 is always the first shared
// object. Links with one name merge their lists; a name listed twice keeps
// its first version. A fault is E0202 reason link at the link, or E0001
// tool fs for std's own list.
func (p *linkPlan) addShared(w *world, l pkgLink) *diag.Diagnostic {
	if !libcModeBuilt {
		return linkFail(l, ".kind", "link %s: a dynamic link on %s needs system-libc mode, which this build of mk does not have", l.Name, l.Target)
	}
	if _, ok := libcFor(w.cfg.Target); !ok {
		return linkFail(l, ".target", "link %s: %s has no system-libc mode", l.Name, w.cfg.Target.Name)
	}
	if d := p.ensureLibc(w); d != nil {
		return d
	}
	var names []string
	if l.Path != "" {
		b, err := os.ReadFile(l.file())
		if err != nil {
			return linkFail(l, ".path", "link %s: cannot read %s: %s", l.Name, l.Path, pathErrText(err))
		}
		if names, err = parseLinkDef(string(b), l.Target); err != nil {
			return linkFail(l, ".path", "link %s: %s is not a name list: %s", l.Name, l.Path, err.Error())
		}
	}
	var so *soLib
	for _, s := range p.sos {
		if s.name == l.Name {
			so = s
		}
	}
	if so == nil {
		so = &soLib{name: l.Name, seen: map[string]bool{}, from: map[string]*pkgLink{}}
		p.sos = append(p.sos, so)
	}
	so.add(names, &l)
	so.labels = append(so.labels, l.label())
	p.searched = append(p.searched, l.label())
	return nil
}

// ensureLibc installs the stub and boot entry together, even when a folded
// branch removes the call which selected system-libc mode.
func (p *linkPlan) ensureLibc(w *world) *diag.Diagnostic {
	if len(p.sos) != 0 {
		return nil
	}
	sp, _ := libcFor(w.cfg.Target)
	raw, err := w.stdTree().readFile(sp.list)
	if err != nil {
		return diag.Toolchain("fs", "read std/"+sp.list, err.Error(), "cannot read std's libc.so.6 list")
	}
	names, err := parseLinkDef(string(raw), w.cfg.Target.Name)
	if err != nil {
		return diag.Toolchain("ld.lld", "", err.Error(), "std/"+sp.list+" is not a name list")
	}
	p.libc = sp
	p.arch = w.cfg.Target.Arch
	p.sos = []*soLib{{name: libcSoname, seen: map[string]bool{}, from: map[string]*pkgLink{}, labels: []string{"std's " + libcSoname}}}
	p.sos[0].add(names, nil)
	p.searched = append(p.searched, "std's "+libcSoname)
	return nil
}

// add appends the names not yet listed, by bare name; from is the link
// whose list they come from, nil for std's own.
func (s *soLib) add(names []string, from *pkgLink) {
	for _, n := range names {
		bare, _, _ := strings.Cut(n, "@")
		if !s.seen[bare] {
			s.seen[bare] = true
			s.from[bare] = from
			s.names = append(s.names, n)
		}
	}
}

// supplies reports whether the library's list holds the bare name n.
func (s *soLib) supplies(n string) bool { return s.seen[n] }

// soVarFaults is "functions only" for a shared object: an extern
// var that no object or archive member defines and a stub would supply is
// E0001 at its declaration, since ld.lld would bind it to the stub's
// function by a copy relocation or a canonical PLT entry, with no error.
func (p *linkPlan) soVarFaults(w *world, vars []externVar) []*diag.Diagnostic {
	var ds []*diag.Diagnostic
	for _, v := range vars {
		if p.defs[v.name] {
			continue
		}
		for _, so := range p.sos {
			if so.supplies(v.name) {
				ds = append(ds, diag.New(diag.E0001, v.span, fmt.Sprintf("extern var %s resolves to the shared object %s; only functions resolve to a library", v.name, so.name)).
					Text("tool", "mk").Text("symbol", v.name).Text("library", so.name))
				break
			}
		}
	}
	return ds
}

// apply puts the plan into the link job. The archives follow the
// objects. On Windows, a graph that holds a dynamic link (used or not:
// "holds", not "uses") gets the safe DLL search load configuration. On
// Linux in system-libc mode, the stubs are written and linked in a mklibc-
// directory of work, the entry is assembled (codegen.LibcEntryAsm), and the
// job becomes the dynamic line. A nil plan changes nothing, so a bin with
// no link keeps the plain line (with /osversion and the dev /opt:noicf on
// Windows).
func (p *linkPlan) apply(ctx context.Context, tools *toolchain.Tools, work string, job *toolchain.LinkJob) []*diag.Diagnostic {
	if p == nil {
		return nil
	}
	job.Archives = p.archives
	if p.windows && len(p.dlls) > 0 {
		job.DependentLoadFlags = safeDLLSearch
	}
	if len(p.sos) == 0 {
		return nil
	}
	// Its own directory: a bin's executable is work/<bin name>.
	dir, err := os.MkdirTemp(work, "mklibc-")
	if err != nil {
		return []*diag.Diagnostic{diag.Toolchain("fs", "mkdir in "+work, err.Error(), "cannot make the stub directory")}
	}
	asm := func(name, text string) (string, *diag.Diagnostic) {
		src, obj := filepath.Join(dir, name+".s"), filepath.Join(dir, name+".o")
		if err := os.WriteFile(src, []byte(text), 0o644); err != nil {
			return "", diag.Toolchain("fs", "write "+src, err.Error(), "cannot write "+src)
		}
		if err := tools.CompileAsm(ctx, toolchain.CJob{Triple: p.triple, Input: src, Output: obj}); err != nil {
			return "", toolErrIn(err, "clang", "the system-libc stub "+name+" did not assemble", work)
		}
		return obj, nil
	}
	entry, d := asm("entry", codegen.LibcEntryAsm(p.arch))
	if d != nil {
		return []*diag.Diagnostic{d}
	}
	var shared []string
	for i, so := range p.sos {
		stem := fmt.Sprintf("so%03d", i)
		obj, d := asm(stem, so.stubAsm())
		if d != nil {
			return []*diag.Diagnostic{d}
		}
		ver := ""
		if text := so.versionScript(); text != "" {
			ver = filepath.Join(dir, stem+".ver")
			if err := os.WriteFile(ver, []byte(text), 0o644); err != nil {
				return []*diag.Diagnostic{diag.Toolchain("fs", "write "+ver, err.Error(), "cannot write "+ver)}
			}
		}
		out := filepath.Join(dir, stem+".so")
		if err := tools.LinkStub(ctx, obj, so.name, ver, out); err != nil {
			return []*diag.Diagnostic{toolErrIn(err, "lld", "the stub of "+so.name+" did not link", work)}
		}
		shared = append(shared, out)
	}
	// The executable exports nothing: without the script ld.lld exports a
	// definition a stub also defines, and the executable's copy would then
	// serve every library of the process.
	ver := filepath.Join(dir, "exe.ver")
	if err := os.WriteFile(ver, []byte(exeVersionScript), 0o644); err != nil {
		return []*diag.Diagnostic{diag.Toolchain("fs", "write "+ver, err.Error(), "cannot write "+ver)}
	}
	job.Objects = append(append([]string(nil), job.Objects...), entry)
	job.Entry, job.Interp, job.Shared, job.VersionScript = codegen.LibcEntry, p.libc.interp, shared, ver
	return nil
}

// exeVersionScript makes every symbol a libc-mode executable defines
// local, so its .dynsym holds only what it imports.
const exeVersionScript = "{ local: *; };\n"

// stubAsm defines each listed name as a function that returns: the stub
// is linked against, never run.
func (s *soLib) stubAsm() string {
	var b strings.Builder
	b.WriteString("\t.text\n")
	for _, n := range s.names {
		bare, _, _ := strings.Cut(n, "@")
		fmt.Fprintf(&b, "\t.globl %s\n\t.type %s,@function\n%s:\n\tret\n", bare, bare, bare)
	}
	b.WriteString("\t.section .note.GNU-stack,\"\",@progbits\n")
	return b.String()
}

// versionScript is one version node per @VERSION of the list, in sorted
// order, holding its names; "" when no name is versioned. A name with no
// version is in no node and stays unversioned.
func (s *soLib) versionScript() string {
	nodes := map[string][]string{}
	for _, n := range s.names {
		if bare, v, ok := strings.Cut(n, "@"); ok {
			nodes[v] = append(nodes[v], bare)
		}
	}
	vs := make([]string, 0, len(nodes))
	for v := range nodes {
		vs = append(vs, v)
	}
	slices.Sort(vs)
	var b strings.Builder
	for _, v := range vs {
		fmt.Fprintf(&b, "%s { global: %s; };\n", v, strings.Join(nodes[v], "; "))
	}
	return b.String()
}

// optionKeys are the link options the plan adds, for the link key: the safe
// DLL search flag, which a .def link nothing uses would otherwise leave out
// of the key (its file is read for names, not hashed), and in system-libc
// mode every shared object's name and list and the entry's text.
func (p *linkPlan) optionKeys() []source.Hash {
	if p == nil {
		return nil
	}
	var out []source.Hash
	if p.windows && len(p.dlls) > 0 {
		h := source.NewHasher("mk.driver.dependentloadflag.v1")
		h.Uvarint(safeDLLSearch)
		out = append(out, h.Sum())
	}
	if len(p.sos) > 0 {
		h := source.NewHasher("mk.driver.libcmode.v2")
		h.String(p.libc.interp)
		h.String(codegen.LibcEntryAsm(p.arch))
		h.String(exeVersionScript)
		for _, so := range p.sos {
			h.String(so.name)
			h.Uvarint(uint64(len(so.names)))
			for _, n := range so.names {
				h.String(n)
			}
		}
		out = append(out, h.Sum())
	}
	return out
}

// soDefFaults checks what the executable itself defines in system-libc
// mode. A bin that reaches std.libc, through an import, is E0001:
// the mode does not link it, and its malloc beside the system's would be a
// second allocator. A name a shared object's list supplies that the
// executable also defines (the compiler's memory functions, an export
// extern, a C source's, an archive member's or an extra object's) is E0001
// at the link whose list names it: the executable exports nothing, so the
// program would call its own copy while every library calls the system's.
// cobjs are the bin's C objects; p.defs is already set.
func (p *linkPlan) soDefFaults(w *world, insts *mono.Instances, cobjs []string) []*diag.Diagnostic {
	var ds []*diag.Diagnostic
	by := map[string]string{} // a defined name to what defines it
	for _, n := range compilerMemFuncs {
		by[n] = "the compiler's memory functions, which every executable defines"
	}
	var libc []string
	fromLibc := map[string]bool{}
	for i := range insts.List {
		in := &insts.List[i]
		if in.Body == nil {
			continue
		}
		it := w.items.Get(in.Item)
		fd, ok := it.Decl.(*types.FuncDecl)
		if !ok || !fd.Sig.Extern {
			continue
		}
		if strings.HasPrefix(it.Key.String(), libcModule+".") {
			fromLibc[in.Symbol] = true
			libc = append(libc, in.Symbol)
			continue
		}
		by[in.Symbol] = "export extern " + it.Key.String()
	}
	if len(libc) > 0 {
		slices.Sort(libc)
		libc = slices.Compact(libc)
		ds = append(ds, diag.New(diag.E0001, source.Span{}, fmt.Sprintf("this bin reaches std.libc (%s), which system-libc mode does not link: the system libc serves C here, and std.libc's allocator beside it would be a second one; declare what you call as an extern, which the system libc supplies", strings.Join(libc, ", "))).
			Text("tool", "mk").Text("symbol", libc[0]).Text("library", libcModule))
	}
	objs := func(files []string, what func(string, []byte) string) {
		for _, o := range files {
			b, err := os.ReadFile(o)
			if err != nil {
				continue
			}
			d, _ := memberSymbols(b)
			for _, n := range d {
				if by[n] == "" {
					by[n] = what(o, b)
				}
			}
		}
	}
	objs(cobjs, func(o string, b []byte) string { return "C source " + objectLabel(o, b) })
	objs(w.cfg.ExtraObjects, func(o string, _ []byte) string { return "object " + filepath.Base(o) })
	for _, so := range p.sos {
		for _, n := range so.names {
			bare, _, _ := strings.Cut(n, "@")
			if !p.defs[bare] && by[bare] == "" || fromLibc[bare] {
				continue
			}
			// The support pack's weak 128-bit routine is in p.defs
			// but yields to the shared object's; only an object's or a
			// pulled member's definition is a fault.
			if by[bare] == "" && !p.memberDefs[bare] && slices.Contains(compilerWeak, bare) {
				continue
			}
			what := by[bare]
			if what == "" {
				what = "an archive member the link pulls"
			}
			span, list := source.Span{}, "std's "+libcSoname+" list"
			if l := so.from[bare]; l != nil {
				span, list = l.man.spanOf(l.Field+".path"), "the list of "+l.label()
			}
			ds = append(ds, diag.New(diag.E0001, span, fmt.Sprintf("%s names %s, which this executable defines (%s); in system-libc mode the executable exports nothing, so the program would call its own %s and every library the system's: drop %s from the list, or rename the definition", list, bare, what, bare, bare)).
				Text("tool", "mk").Text("symbol", bare).Text("library", so.name))
		}
	}
	return ds
}

// objectLabel names an object in a message: the source file its STT_FILE
// symbol names, else its base name.
func objectLabel(file string, b []byte) string {
	if f, err := elf.NewFile(bytes.NewReader(b)); err == nil {
		syms, _ := f.Symbols()
		for _, s := range syms {
			if elf.ST_TYPE(s.Info) == elf.STT_FILE && s.Name != "" {
				return s.Name
			}
		}
	}
	return filepath.Base(file)
}

// soAddrFaults is "functions only" for the C a libc-mode bin links.
// A list cannot say whether a name is a function or data, so
// the stub defines each as a function. Code that is not
// position-independent and takes such a name's address directly (an
// absolute or PC-relative relocation that is not a call, in a section
// ld.lld cannot give a dynamic relocation) gets a canonical PLT entry from
// ld.lld: right for a function, silently wrong for data, which then reads
// the PLT's code. Each such reference in a C object or a pulled archive
// member is E0001. A call (PLT32, or PC32 after a call or jump opcode), a
// GOT load (the position-independent form, which the loader binds by name
// whatever the symbol is) and a 64-bit word in a writable section (a
// dynamic relocation) stay.
func (p *linkPlan) soAddrFaults(cobjs []string, pulled []pulledMember) []*diag.Diagnostic {
	supplier := func(n string) *soLib {
		if p.defs[n] {
			return nil
		}
		for _, so := range p.sos {
			if so.supplies(n) {
				return so
			}
		}
		return nil
	}
	var ds []*diag.Diagnostic
	check := func(b []byte, label func(file string) string) {
		file, refs := directRefs(b, func(n string) bool { return supplier(n) != nil })
		for _, r := range refs {
			so := supplier(r.sym)
			ds = append(ds, diag.New(diag.E0001, source.Span{}, fmt.Sprintf("%s takes the address of %s, which the shared object %s supplies, in code that is not position-independent (%s): ld.lld would point it at a PLT entry, which is wrong if %s is data, and a list cannot say which it is (only functions resolve to a library); build it position-independent (clang's default for Linux: drop -fno-pic), or only call %s", label(file), r.sym, so.name, r.typ, r.sym, r.sym)).
				Text("tool", "mk").Text("symbol", r.sym).Text("library", so.name))
		}
	}
	for _, o := range cobjs {
		b, err := os.ReadFile(o)
		if err != nil {
			continue
		}
		check(b, func(file string) string {
			if file == "" {
				return "C object " + filepath.Base(o)
			}
			return "C source " + file
		})
	}
	for _, m := range pulled {
		check(m.data, func(string) string { return "archive " + p.labels[m.archive] + ", member " + m.name + "," })
	}
	return ds
}

// addrRef is one direct reference directRefs finds; typ is the
// relocation's name (R_X86_64_32, R_AARCH64_ADR_PREL_PG_HI21).
type addrRef struct {
	sym string
	typ string
}

// directRefs lists, once per name and type, the relocations of an x86_64
// or AArch64 ELF object b against an undefined name that want holds, of a
// kind that makes ld.lld emit a canonical PLT entry or a copy relocation
// for a shared symbol in a non-PIE executable. On x86_64: R_X86_64_32,
// 32S, 16, 8, PC16 and PC8 anywhere; PC32 unless the byte before it is a
// call (0xE8) or jump (0xE9) opcode; 64 and PC64 outside a writable
// section. On AArch64 (aarch64Direct): an absolute or PC-relative address
// that is not a branch (CALL26, JUMP26) or a GOT load (ADR_GOT_PAGE,
// LD64_GOT_LO12_NC). Only allocated sections count. file is the object's
// STT_FILE name, or "".
func directRefs(b []byte, want func(string) bool) (file string, out []addrRef) {
	f, err := elf.NewFile(bytes.NewReader(b))
	if err != nil || f.Machine != elf.EM_X86_64 && f.Machine != elf.EM_AARCH64 {
		return "", nil
	}
	syms, _ := f.Symbols()
	for _, s := range syms {
		if elf.ST_TYPE(s.Info) == elf.STT_FILE && s.Name != "" {
			file = s.Name
			break
		}
	}
	seen := map[addrRef]bool{}
	for _, rs := range f.Sections {
		if rs.Type != elf.SHT_RELA || int(rs.Info) >= len(f.Sections) {
			continue
		}
		tgt := f.Sections[rs.Info]
		if tgt.Flags&elf.SHF_ALLOC == 0 {
			continue
		}
		data, err := rs.Data()
		if err != nil {
			continue
		}
		writable := tgt.Flags&elf.SHF_WRITE != 0
		var body []byte // the target's bytes, read once a PC32 needs them
		for i := 0; i+24 <= len(data); i += 24 {
			off := binary.LittleEndian.Uint64(data[i:])
			info := binary.LittleEndian.Uint64(data[i+8:])
			si, rt := int(info>>32), uint32(info&0xffffffff)
			if si < 1 || si > len(syms) {
				continue
			}
			s := syms[si-1]
			if s.Section != elf.SHN_UNDEF || !want(s.Name) {
				continue
			}
			var name string
			if f.Machine == elf.EM_AARCH64 {
				typ := elf.R_AARCH64(rt)
				if !aarch64Direct(typ, writable) {
					continue
				}
				name = typ.String()
			} else {
				typ := elf.R_X86_64(rt)
				switch typ {
				case elf.R_X86_64_32, elf.R_X86_64_32S, elf.R_X86_64_16, elf.R_X86_64_8, elf.R_X86_64_PC16, elf.R_X86_64_PC8:
				case elf.R_X86_64_PC32:
					if body == nil && tgt.Type != elf.SHT_NOBITS {
						body, _ = tgt.Data()
					}
					if off >= 1 && off <= uint64(len(body)) && (body[off-1] == 0xE8 || body[off-1] == 0xE9) {
						continue
					}
				case elf.R_X86_64_64, elf.R_X86_64_PC64:
					if writable {
						continue
					}
				default:
					continue
				}
				name = typ.String()
			}
			r := addrRef{sym: s.Name, typ: name}
			if !seen[r] {
				seen[r] = true
				out = append(out, r)
			}
		}
	}
	return file, out
}

// aarch64Direct reports whether an AArch64 relocation of type typ in a
// section (writable or not) addresses its symbol directly in a way ld.lld
// serves, for a shared symbol in a non-PIE executable, with a copy
// relocation or a canonical PLT entry: ABS32 and ABS16, PREL32 and PREL16,
// the MOVW_UABS group, ADR_PREL_LO21, ADR_PREL_PG_HI21 (the
// ADRP of an ADRP + ADD or load pair; its low-12 half is not counted again)
// and LD_PREL_LO19 anywhere; ABS64 and PREL64 outside a writable section,
// where ld.lld cannot leave a dynamic relocation.
func aarch64Direct(typ elf.R_AARCH64, writable bool) bool {
	switch typ {
	case elf.R_AARCH64_ABS32, elf.R_AARCH64_ABS16, elf.R_AARCH64_PREL32, elf.R_AARCH64_PREL16,
		elf.R_AARCH64_MOVW_UABS_G0, elf.R_AARCH64_MOVW_UABS_G0_NC, elf.R_AARCH64_MOVW_UABS_G1,
		elf.R_AARCH64_MOVW_UABS_G1_NC, elf.R_AARCH64_MOVW_UABS_G2, elf.R_AARCH64_MOVW_UABS_G2_NC,
		elf.R_AARCH64_MOVW_UABS_G3, elf.R_AARCH64_ADR_PREL_LO21,
		elf.R_AARCH64_ADR_PREL_PG_HI21, elf.R_AARCH64_ADR_PREL_PG_HI21_NC, elf.R_AARCH64_LD_PREL_LO19:
		return true
	case elf.R_AARCH64_ABS64, elf.R_AARCH64_PREL64:
		return !writable
	}
	return false
}

// libcModeHint is the hint for an undefined symbol of a libc-mode bin
// that std.libc may supply outside the mode: the mode does not link
// std.libc, so a C function std's libc.so.6 list does not name needs a
// link of its own. glibc 2.39 puts each of these at the target's base
// version (GLIBC_2.2.5 on x86_64, GLIBC_2.17 on aarch64), the <math.h> ones
// in libm.so.6 (read with llvm-nm -D from Ubuntu's libc.so.6 and
// libm.so.6). "" otherwise.
func (p *linkPlan) libcModeHint(sym string) string {
	fn, ok := c17Libc[sym]
	if len(p.sos) == 0 || !ok || p.sos[0].supplies(sym) {
		return ""
	}
	lib := libcSoname
	if fn.header == "math.h" {
		lib = "libm.so.6"
	}
	return fmt.Sprintf("system-libc mode does not link std.libc, and std's %s list does not name %s: glibc has it in %s, so add a link to %s whose .def lists %s@%s",
		libcSoname, sym, lib, lib, sym, p.libc.base)
}
