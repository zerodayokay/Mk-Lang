// Why: some link mistakes produce a program that links clean and then
// misbehaves: an extern var bound to a code thunk, an archive member whose
// constructors or thread-local storage no Mk runtime sets up, an import
// library named as a static archive.
//
// What: checks on the linking half of FFI: externVars, externVarFaults and
// varLibrary for extern vars on Windows, memberFault for archive members,
// importLibraryOf for misnamed import libraries, and the reading of every
// undefined symbol in lld's report.
//
// How: each check reads the objects, archives and import libraries the link
// already has and reports E0001 at the Mk declaration. With no extern var,
// no link and no failed link, nothing here runs, so hello world's build is
// unchanged.

package driver

import (
	"bytes"
	"debug/elf"
	"debug/pe"
	"encoding/binary"
	"fmt"
	"slices"
	"strings"

	"mk/internal/codegen"
	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/types"
)

// dllimportPrefix is how lld-link writes an undefined __imp_ reference.
const dllimportPrefix = "__declspec(dllimport) "

// externVar is one extern var of the bin: its symbol and its declaration.
type externVar struct {
	name string
	span source.Span
}

// externVars lists the bin's extern vars among its globals, less those it
// defines itself (an export extern var).
func (w *world) externVars(globals []codegen.Global) []externVar {
	var out []externVar
	for _, g := range globals {
		it := w.items.Get(g.Item)
		if gd, ok := it.Decl.(*types.GlobalDecl); ok && gd.Extern && !g.Define {
			out = append(out, externVar{name: externVarName(it.Key), span: it.Span})
		}
	}
	return out
}

// externVarFaults applies "functions only" (an extern var never binds to a
// DLL through an import library) to every import library a Windows link
// takes, not only the named DLLs: an extern var that no object, pulled
// archive member or the compiler defines, and that std's lists, a named DLL
// (a function or a data export) or the system set holds, would bind to an
// import's code thunk with no error from lld-link. Each is E0001 at the
// var's declaration. libs are the link's import libraries; plan is nil when
// no link applies.
func (w *world) externVarFaults(globals []codegen.Global, cobjs []string, plan *linkPlan, libs []importLib) []*diag.Diagnostic {
	vars := w.externVars(globals)
	if len(vars) == 0 {
		return nil
	}
	defs := map[string]bool{}
	for _, n := range compilerDefined {
		defs[n] = true
	}
	for _, o := range append(append([]string(nil), w.cfg.ExtraObjects...), cobjs...) {
		d, _ := objectSymbols(o)
		for _, n := range d {
			defs[n] = true
		}
	}
	if plan != nil {
		for n := range plan.defs {
			defs[n] = true
		}
	}
	var ds []*diag.Diagnostic
	for _, v := range vars {
		if defs[v.name] {
			continue
		}
		lib := varLibrary(v.name, plan, libs)
		if lib == "" {
			continue
		}
		ds = append(ds, diag.New(diag.E0001, v.span, fmt.Sprintf(
			"extern var %s would resolve to %s; only extern functions resolve to a library: declare it in C or read it through a function",
			v.name, lib)).Text("tool", "mk").Text("symbol", v.name).Text("library", lib))
	}
	return ds
}

// varLibrary names the library that would supply name in extern resolution
// order: std's lists (the link's import libraries without a named file), the
// named DLLs, then the system set; "" when none does.
func varLibrary(name string, plan *linkPlan, libs []importLib) string {
	for _, l := range libs {
		if (l.file == "" || l.file == l.dll) && slices.Contains(l.names, name) {
			return l.fileName() + " (std's list or the system DLLs)"
		}
	}
	if plan != nil {
		for _, dl := range plan.dlls {
			if dl.names[name] || dl.data[name] {
				return strings.Join(dl.labels, ", ")
			}
		}
	}
	if dll, ok := systemExports()[name]; ok {
		return dll + ".dll (the system DLLs)"
	}
	return ""
}

// memberFault reports what a pulled archive member holds that an Mk
// program never sets up: static constructors or destructors,
// which no C runtime runs, and thread-local storage, which the runtime
// never gives a thread (no TLS directory on Windows, no FS base on Linux).
// It returns what the member has and the hint, or "" when nothing.
func memberFault(b []byte) (what, hint string) {
	const (
		ctorHint = "an Mk program runs no constructors or destructors: initialize the state from a function the program calls"
		tlsHint  = "an Mk program sets up no thread-local storage for C: keep the state in memory the caller passes"
	)
	if bytes.HasPrefix(b, []byte("\x7fELF")) {
		f, err := elf.NewFile(bytes.NewReader(b))
		if err != nil {
			return "", ""
		}
		for _, s := range f.Sections {
			switch {
			case s.Flags&elf.SHF_TLS != 0 || strings.HasPrefix(s.Name, ".tdata") || strings.HasPrefix(s.Name, ".tbss"):
				return "thread-local storage (" + s.Name + ")", tlsHint
			case s.Type == elf.SHT_INIT_ARRAY || s.Type == elf.SHT_FINI_ARRAY || s.Type == elf.SHT_PREINIT_ARRAY ||
				hasAnyPrefix(s.Name, ".init_array", ".fini_array", ".preinit_array", ".ctors", ".dtors"):
				return "static constructors or destructors (" + s.Name + ")", ctorHint
			}
		}
		syms, _ := f.Symbols()
		for _, s := range syms {
			if elf.ST_TYPE(s.Info) == elf.STT_TLS {
				return "thread-local storage (" + s.Name + ")", tlsHint
			}
		}
		return "", ""
	}
	if len(b) >= 4 && binary.LittleEndian.Uint16(b) == 0 && binary.LittleEndian.Uint16(b[2:]) == 0xFFFF {
		return "", "" // a short import member, or an anonymous object
	}
	f, err := pe.NewFile(bytes.NewReader(b))
	if err != nil {
		return "", ""
	}
	for _, s := range f.Sections {
		switch {
		case strings.HasPrefix(s.Name, ".tls") || strings.HasPrefix(s.Name, ".CRT$XL"):
			return "thread-local storage (" + s.Name + ")", tlsHint
		case strings.HasPrefix(s.Name, ".CRT$X"):
			return "static constructors or destructors (" + s.Name + ")", ctorHint
		}
	}
	for _, s := range f.Symbols {
		if s.SectionNumber == 0 && (s.Name == "_tls_index" || s.Name == "_tls_used") {
			return "thread-local storage (" + s.Name + ")", tlsHint
		}
	}
	return "", ""
}

// hasAnyPrefix reports whether s starts with one of the prefixes.
func hasAnyPrefix(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// importLibraryOf reports whether the ar archive b is an import library:
// the DLL its first import member names (a short import member, as
// lld-link, llvm-lib and MSVC write them), "a DLL" for the long form
// (members with .idata$ sections, as MinGW's dlltool writes them), or ""
// when it holds no import member.
func importLibraryOf(b []byte) string {
	members, err := arMembers(b)
	if err != nil {
		return ""
	}
	found := ""
	for _, m := range members {
		d := m.data
		if len(d) >= 20 && binary.LittleEndian.Uint16(d) == 0 && binary.LittleEndian.Uint16(d[2:]) == 0xFFFF &&
			binary.LittleEndian.Uint16(d[4:]) == 0 {
			// IMPORT_OBJECT_HEADER, then the symbol and the DLL, each
			// NUL-terminated.
			rest := d[20:]
			if i := bytes.IndexByte(rest, 0); i >= 0 {
				rest = rest[i+1:]
				if j := bytes.IndexByte(rest, 0); j > 0 {
					return string(rest[:j])
				}
			}
			found = "a DLL"
			continue
		}
		if found != "" || bytes.HasPrefix(d, []byte("\x7fELF")) || len(d) >= 4 && binary.LittleEndian.Uint16(d[2:]) == 0xFFFF {
			continue
		}
		// The import descriptor objects of a short-form library, or every
		// member of a long-form one; a short member may still follow.
		if f, err := pe.NewFile(bytes.NewReader(d)); err == nil {
			for _, s := range f.Sections {
				if strings.HasPrefix(s.Name, ".idata$") {
					found = "a DLL"
					break
				}
			}
		}
	}
	return found
}

// undefinedSymbols lists every "undefined symbol: X" of lld's output, in
// order, each once (undefinedSymbol is the first).
func undefinedSymbols(stderr string) []string {
	const m = "undefined symbol: "
	var out []string
	for rest := stderr; ; {
		i := strings.Index(rest, m)
		if i < 0 {
			return out
		}
		rest = rest[i+len(m):]
		s := rest
		if j := strings.IndexAny(s, "\r\n"); j >= 0 {
			s = s[:j]
		}
		if s = strings.TrimSpace(s); s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
}
