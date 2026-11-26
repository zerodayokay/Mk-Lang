// Why: a package's c_sources link against std.libc, never a C runtime, so
// the C headers they include must declare exactly what std.libc defines.
// No C header text is in the repository.
//
// What: the generated C headers (libcHeaderNames, writeLibcHeaders), the
// C17 prototypes std.libc may define (c17Fn, c17Libc), and the mapping from
// Mk types to C types (libcCType, sameShape).
//
// How: the headers come from std.libc's export extern signatures, plus
// memcpy, memmove, memset and memcmp, which codegen's support pack defines
// in every executable; they go into a directory of the build that the
// c_sources compiles take as -isystem. Raw pointers have one Mk spelling,
// *T, where C17 writes const char *, char *, const void * or void *; for a
// C17 function whose Mk shape agrees with the standard's (c17Libc), the
// standard's spelling is used, so a C caller sees the qualifiers it
// expects. Any other export extern gets the mechanical prototype, with
// void * for every raw pointer.

package driver

import (
	"path/filepath"
	"slices"
	"strings"

	"mk/internal/cache"
	"mk/internal/cimport"
	"mk/internal/types"
)

// c17Fn is one C17 library function: the header that declares it and its
// prototype's types, the result first.
type c17Fn struct {
	header string
	types  []string
}

// c17Libc are the C17 functions std.libc may define, with the standard's
// types (C17 7.22, 7.24, 7.12).
var c17Libc = map[string]c17Fn{
	"malloc":  {"stdlib.h", []string{"void *", "size_t"}},
	"calloc":  {"stdlib.h", []string{"void *", "size_t", "size_t"}},
	"realloc": {"stdlib.h", []string{"void *", "void *", "size_t"}},
	"free":    {"stdlib.h", []string{"void", "void *"}},
	"abs":     {"stdlib.h", []string{"int", "int"}},
	"memchr":  {"string.h", []string{"void *", "const void *", "int", "size_t"}},
	"strlen":  {"string.h", []string{"size_t", "const char *"}},
	"strcmp":  {"string.h", []string{"int", "const char *", "const char *"}},
	"strncmp": {"string.h", []string{"int", "const char *", "const char *", "size_t"}},
	"strchr":  {"string.h", []string{"char *", "const char *", "int"}},
	"strrchr": {"string.h", []string{"char *", "const char *", "int"}},
	"strspn":  {"string.h", []string{"size_t", "const char *", "const char *"}},
	"strcspn": {"string.h", []string{"size_t", "const char *", "const char *"}},
	"strcpy":  {"string.h", []string{"char *", "char *", "const char *"}},
	"strncpy": {"string.h", []string{"char *", "char *", "const char *", "size_t"}},
	"strstr":  {"string.h", []string{"char *", "const char *", "const char *"}},
	"fabs":    {"math.h", []string{"double", "double"}},
	"sqrt":    {"math.h", []string{"double", "double"}},
	"floor":   {"math.h", []string{"double", "double"}},
	"ceil":    {"math.h", []string{"double", "double"}},
}

// supportPackProtos are the four functions codegen's support pack defines
// in every executable (internal/codegen/support.go), which std.libc
// therefore does not.
const supportPackProtos = "void *memcpy(void *, const void *, size_t);\n" +
	"void *memmove(void *, const void *, size_t);\n" +
	"void *memset(void *, int, size_t);\n" +
	"int memcmp(const void *, const void *, size_t);\n"

// libcCType is the C type of Mk type t in a prototype; false when t has no
// C spelling (a slice, a str, a struct by value).
func libcCType(tab *types.Table, t types.TypeID) (string, bool) {
	ty := tab.Get(t)
	switch ty.Kind {
	case types.KVoid:
		return "void", true
	case types.KBool:
		return "_Bool", true
	case types.KIntPtr:
		if ty.Signed {
			return "ptrdiff_t", true
		}
		return "size_t", true
	case types.KInt:
		name := map[int]string{8: "char", 16: "short", 32: "int", 64: "long long"}[int(ty.Bits)]
		switch {
		case name == "":
			return "", false
		case ty.Bits == 8 && ty.Signed:
			return "signed char", true
		case !ty.Signed:
			name = "unsigned " + name
		}
		return name, true
	case types.KFloat:
		if ty.Bits == 32 {
			return "float", true
		}
		return "double", true
	case types.KRawPtr, types.KFn, types.KOptional:
		// A fn and a ?fn are C function pointers; the header declares them
		// as void *, which C converts.
		return "void *", true
	}
	return "", false
}

// libcProto is one export extern's prototype and the header it goes in.
type libcProto struct {
	name, header, text string
}

// libcProtos are the prototypes of std.libc's export externs, by name.
func (w *world) libcProtos() []libcProto {
	var mod *types.Module
	for i := 1; i < w.items.NumModules(); i++ {
		if m := w.items.Module(types.ModuleID(i)); m != nil && m.Path == libcModule {
			mod = m
			break
		}
	}
	if mod == nil {
		return nil
	}
	var out []libcProto
	for _, id := range mod.Items {
		it := w.items.Get(id)
		fd, ok := it.Decl.(*types.FuncDecl)
		if !ok || !fd.Sig.Extern || !it.Exported || fd.Bodiless || len(fd.Sig.Generics) > 0 || fd.Sig.Errors != types.EmptyErrors {
			continue
		}
		name := w.in.Str(fd.Sig.Name)
		mech := make([]string, 0, len(fd.Sig.Params)+1)
		ok = true
		for _, t := range append([]types.TypeID{fd.Sig.Result}, paramTypes(fd.Sig.Params)...) {
			c, good := libcCType(w.tab, t)
			ok = ok && good
			mech = append(mech, c)
		}
		if !ok {
			continue // no C spelling: C cannot call it by a prototype
		}
		header, spell := "stdlib.h", mech
		if std, found := c17Libc[name]; found && sameShape(mech, std.types) {
			header, spell = std.header, std.types
		}
		params := "void"
		if len(spell) > 1 {
			params = strings.Join(spell[1:], ", ")
		}
		res := spell[0]
		if !strings.HasSuffix(res, "*") {
			res += " "
		}
		out = append(out, libcProto{name: name, header: header, text: res + name + "(" + params + ");\n"})
	}
	slices.SortFunc(out, func(a, b libcProto) int { return strings.Compare(a.name, b.name) })
	return out
}

func paramTypes(ps []types.Param) []types.TypeID {
	out := make([]types.TypeID, len(ps))
	for i, p := range ps {
		out[i] = p.Type
	}
	return out
}

// sameShape reports whether a mechanical prototype agrees with a C17 one:
// the same arity, a pointer where it has a pointer, else the same type.
func sameShape(mech, std []string) bool {
	if len(mech) != len(std) {
		return false
	}
	for i := range mech {
		if mech[i] == "void *" {
			if !strings.HasSuffix(std[i], "*") {
				return false
			}
		} else if mech[i] != std[i] {
			return false
		}
	}
	return true
}

// libcHeaderNames are the headers generated: the ones the sqlite
// amalgamation includes under std.db.sqlite's build flags beyond clang's
// resource directory.
var libcHeaderNames = []string{"assert.h", "ctype.h", "math.h", "stdio.h", "stdlib.h", "string.h", "time.h"}

// libcHeaders are the generated headers' texts, by file name, for os.
func libcHeaders(protos []libcProto, osName string) map[string]string {
	by := map[string]string{}
	for _, p := range protos {
		by[p.header] += p.text
	}
	guard := func(name, body string) string {
		g := "MK_LIBC_" + strings.ToUpper(strings.NewReplacer(".", "_").Replace(name))
		return "/* Generated by mk from std.libc's export extern signatures. */\n" +
			"#ifndef " + g + "\n#define " + g + "\n" + body + "#endif\n"
	}
	filenameMax := "4096"
	if osName == "windows" {
		filenameMax = "260"
	}
	out := map[string]string{
		"stdlib.h": guard("stdlib.h", "#include <stddef.h>\n"+by["stdlib.h"]),
		"string.h": guard("string.h", "#include <stddef.h>\n"+supportPackProtos+by["string.h"]),
		"math.h": guard("math.h", by["math.h"]+
			"#define INFINITY __builtin_inff()\n#define NAN __builtin_nanf(\"\")\n#define HUGE_VAL __builtin_huge_val()\n"),
		"stdio.h": guard("stdio.h", "#include <stddef.h>\n#define FILENAME_MAX "+filenameMax+"\n"),
		"ctype.h": guard("ctype.h", ""),
		"time.h":  guard("time.h", ""),
		// C17 7.2: assert is redefined at every inclusion, by NDEBUG then.
		"assert.h": "/* Generated by mk: std.libc has no abort, so a failed assert traps. */\n" +
			"#undef assert\n#ifdef NDEBUG\n#define assert(e) ((void)0)\n#else\n" +
			"#define assert(e) ((e) ? (void)0 : __builtin_trap())\n#endif\n" +
			"#ifndef static_assert\n#define static_assert _Static_assert\n#endif\n",
	}
	return out
}

// writeLibcHeaders writes the headers into a directory named by their
// content, under the cache's libch directory (or work's, with no cache),
// and returns it. Its path is on the C compiles' command line, which is
// part of a C object's key, and -MD lists every header a compile read, so
// a change of std.libc's signatures is a new key. "" when std has
// no std.libc.
func (w *world) writeLibcHeaders(st *cache.Store, work string) (string, error) {
	protos := w.libcProtos()
	if protos == nil {
		return "", nil
	}
	files := libcHeaders(protos, targetOS(w.cfg.Target))
	base := filepath.Join(work, "libc-h")
	if st != nil {
		d, err := st.Dir("libch")
		if err != nil {
			return "", err
		}
		base = d
	}
	return cimport.WriteHeaderDir(base, "mk.driver.libch.v1", libcHeaderNames, files)
}
