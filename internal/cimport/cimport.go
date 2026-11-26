// Why: Mk code calls C libraries through `import c.<name>`, and writing the
// bindings by hand for large headers is error-prone; the compiler reads the
// headers with Clang and writes the bindings itself.
//
// What: the package's entry point: Import, Request, Result, the Clang
// interface, Error, ParseTriple, SearchDirs and Translate, which runs the
// Clang steps and maps their output; also the -MD dependency file parser.
//
// How: Translate runs the four Clang steps listed in the package comment,
// the AST dump beside the macro chain, so the critical path is the longer
// of the two; the results are mapped into one Mk module per import.

// Package cimport implements `import c.<name>`: Clang as a process reads every
// C import of a package as one translation unit for one target, and the
// declarations become one Mk module per import, written as readable Mk source
// the front end checks like any module.
//
// The steps, each one Clang run (parse, macros, layout);
// step 2 runs beside steps 1, 3 and 4:
//  1. `-E -dD` over the translation unit: the macros, by import, and the
//     -MD dependency list (the cache key's header hashes);
//  2. `-fsyntax-only -Xclang -ast-dump=json` over the unit: the
//     declarations, streamed;
//  3. the same with `-ast-dump-filter=__mk_m_` over the probe file (the
//     unit plus one line per object-like macro): the type of each macro;
//     Clang's errors name the macros that are no constant;
//  4. `-S -emit-llvm -Xclang -fdump-record-layouts-complete` over the probe
//     without those lines: each macro's value, and every record's layout.
package cimport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"mk/internal/source"
	"mk/internal/toolchain"
	"mk/internal/types"
)

// Import is one pkg.CImport.
type Import struct {
	Name    string   // the segment after c.
	Headers []string // as #include <h> names them
	Defines []string // NAME or NAME=VALUE
	Macros  []string // function-like macro prototypes, "int MAX(int a, int b)"
}

// Request is one package's translation unit for one target.
type Request struct {
	Target  string // the Mk target name, x86_64-windows or x86_64-linux
	Triple  string // the Clang triple the headers are read for (HeaderTriple)
	Imports []Import
	Include []string // -I directories, absolute (the package root)
	// Sysroot are the target's system header directories, absolute; the
	// first is also --sysroot. Empty: only Clang's own headers and Include.
	Sysroot []string
	WorkDir string // an absolute directory this run may write
}

// Result is the bindings of a Request.
type Result struct {
	Modules []string // the Mk source of c.<name>, one per Import, in order
	Shim    string   // the shim C file of every binding item; "" when there is nothing to shim
	// ShimFuncs are Shim's functions one by one, so a build can define
	// only those the program names (KeptShim).
	ShimFuncs []ShimFunc
	Stats     []Stats             // one per Import
	FuncLike  [][]string          // sorted un-prototyped function-like macro names per Import
	Merged    []map[string]string // C typedef name -> Mk name it merged into, per Import
	// FunctionRedirects names functions whose headers assign another linker symbol.
	FunctionRedirects map[string]string
	// Deps are every file the translation unit read (-MD), absolute, in
	// Clang's order; SearchDirs the ordered header search path.
	Deps       []string
	SearchDirs []string
	// Unit is the text of the unit mk wrote (tu.c: the defines and the
	// #includes), which Deps leave out; the cache reads its lookups.
	Unit string
	// Steps are the wall times of the translation's steps (measurement
	// only; never part of a cache entry or a binding).
	Steps []Step
}

// Clang is what a translation needs of the toolchain (toolchain.Tools).
type Clang interface {
	RunClang(ctx context.Context, args []string, dir string) ([]byte, []byte, error)
	StreamClang(ctx context.Context, args []string, dir string, read func(io.Reader) error) ([]byte, error)
}

// Error is a translation that failed on the headers themselves: a header
// not found, or a C error in one. The driver reports it as E0001 with
// Command and Stderr.
type Error struct {
	Msg     string
	Command string
	Stderr  string
	// Missing: Clang could not find a header (or another file) the unit
	// includes, the case a vendored binding stands in for.
	Missing bool
}

func (e *Error) Error() string { return e.Msg }

// ParseTriple uses the native jobs' triple so the headers are translated
// with the same target macros and ABI as the C sources and the shim.
// A target already spelled as a Clang triple passes through unchanged.
func ParseTriple(target string) string {
	if t, ok := types.TargetByName(target); ok {
		return t.LLVMTriple
	}
	return target
}

// SearchDirs is the ordered header search path of r: -I, Clang's resource
// headers, then the sysroot's (the order Clang searches them in).
func SearchDirs(r Request, resourceDir string) []string {
	var out []string
	out = append(out, r.Include...)
	out = append(out, filepath.Join(resourceDir, "include"))
	out = append(out, r.Sysroot...)
	return out
}

// baseArgs are the flags every run shares.
func baseArgs(r Request, resourceDir string) []string {
	args := []string{toolchain.ClangTargetArg(r.Triple), "-nostdinc", "-x", "c", "-std=gnu11", "-fno-caret-diagnostics",
		"-fmacro-backtrace-limit=0", "-ferror-limit=0", "-Wno-everything", "-fno-color-diagnostics"}
	args = append(args, TripleFlags(r.Triple)...)
	if len(r.Sysroot) > 0 {
		args = append(args, "--sysroot="+r.Sysroot[0])
	}
	for _, d := range r.Include {
		args = append(args, "-I", d)
	}
	args = append(args, "-isystem", filepath.Join(resourceDir, "include"))
	for _, d := range r.Sysroot {
		args = append(args, "-isystem", d)
	}
	return args
}

// unitText is the translation unit: per import, its marker, its defines
// and its headers.
func unitText(imports []Import) string {
	var b strings.Builder
	for i, im := range imports {
		fmt.Fprintf(&b, "typedef int %s%d;\n", markerPrefix, i)
		for _, d := range im.Defines {
			name, val, has := strings.Cut(d, "=")
			if !has {
				val = "1"
			}
			fmt.Fprintf(&b, "#define %s %s\n", name, val)
		}
		for _, h := range im.Headers {
			fmt.Fprintf(&b, "#include <%s>\n", h)
		}
	}
	return b.String()
}

// Step is the wall time of one step of a translation, for measurement.
type Step struct {
	Name string
	Took time.Duration
}

// Translate runs the Clang steps of the package comment and maps the
// result. resourceDir is Clang's (toolchain.Tools.ResourceDir). The AST of
// the unit (step 2) runs beside the macro chain (steps 1, 3 and 4), which
// is the unit parsed three more times but each run small: the critical
// path is the longer of the two.
func Translate(ctx context.Context, cl Clang, resourceDir string, r Request) (*Result, error) {
	t0 := time.Now()
	var steps []Step
	mark := func(name string, from time.Time) { steps = append(steps, Step{name, time.Since(from)}) }
	if r.WorkDir == "" || !filepath.IsAbs(r.WorkDir) {
		return nil, errors.New("cimport: the work directory must be absolute")
	}
	if err := os.MkdirAll(r.WorkDir, 0o755); err != nil {
		return nil, err
	}
	tu := unitText(r.Imports)
	tuPath := filepath.Join(r.WorkDir, "tu.c")
	if err := os.WriteFile(tuPath, []byte(tu), 0o644); err != nil {
		return nil, err
	}
	base := baseArgs(r, resourceDir)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Step 2, beside the rest: the AST of the unit, streamed.
	type astOut struct {
		u    *unit
		args []string
		serr []byte
		err  error
		took time.Duration
	}
	astc := make(chan astOut, 1)
	astArgs := append(append([]string{}, base...), "-fsyntax-only", "-Xclang", "-ast-dump=json", "tu.c")
	astStart := time.Now()
	go func() {
		// A panic here is an error of the translation, never a crash. The send
		// is the last step, so the channel gets exactly one value.
		if ie := source.Guard("C import AST", source.Span{}, func() {
			var u *unit
			serr, err := cl.StreamClang(ctx, astArgs, r.WorkDir, func(rd io.Reader) error {
				var e error
				u, e = readUnit(rd)
				return e
			})
			astc <- astOut{u, astArgs, serr, err, time.Since(astStart)}
		}); ie != nil {
			astc <- astOut{args: astArgs, err: errors.New("cimport: internal error reading the AST: " + ie.String()), took: time.Since(astStart)}
		}
	}()
	waitAST := func() astOut { a := <-astc; astc <- a; return a }

	// Step 1: macros and dependencies.
	start := time.Now()
	dPath := filepath.Join(r.WorkDir, "tu.d")
	args := append(append([]string{}, base...), "-E", "-dD", "-MD", "-MF", dPath, "-MT", "tu", "tu.c")
	out, stderr, err := cl.RunClang(ctx, args, r.WorkDir)
	if err != nil {
		cancel()
		waitAST()
		return nil, clangError("the C headers did not preprocess", args, stderr, err)
	}
	objs, funcLike := readDefines(string(out))
	dtext, err := os.ReadFile(dPath)
	if err != nil {
		cancel()
		waitAST()
		return nil, err
	}
	deps := parseDepFile(string(dtext))
	mark("macros", start)

	// Step 3: the probe's types, from the probe variables' AST alone.
	start = time.Now()
	head := strings.Count(tu, "\n")
	var probe strings.Builder
	probe.WriteString(tu)
	for k, m := range objs {
		m.k = k
		if !m.noProbe {
			probe.WriteString(probeLine(m))
		}
		probe.WriteByte('\n')
	}
	probePath := filepath.Join(r.WorkDir, "probe.c")
	if err := os.WriteFile(probePath, []byte(probe.String()), 0o644); err != nil {
		cancel()
		waitAST()
		return nil, err
	}
	var probes map[int]*decl
	args = append(append([]string{}, base...), "-fsyntax-only", "-Xclang", "-ast-dump=json", "-Xclang", "-ast-dump-filter="+probePrefix, "probe.c")
	serr, err := cl.StreamClang(ctx, args, r.WorkDir, func(rd io.Reader) error {
		var e error
		probes, e = readProbes(rd)
		return e
	})
	bad, other := failedLines(string(serr), "probe.c", head+1)
	if err != nil {
		var te *toolchain.ToolError
		if !errors.As(err, &te) || !te.Started() || te.Cancelled || other != "" || probes == nil {
			// The unit's own run says best what failed in the headers.
			if a := waitAST(); a.err != nil {
				return nil, clangError("the C headers did not parse", a.args, a.serr, a.err)
			}
			return nil, clangError("the C headers did not parse", args, serr, err)
		}
	}
	for _, m := range objs {
		m.ok = !bad[head+1+m.k] && probes[m.k] != nil
	}
	mark("probe", start)

	// Step 4: values and layouts.
	start = time.Now()
	lines := strings.Split(probe.String(), "\n")
	for _, m := range objs {
		if !m.ok {
			lines[head+m.k] = ""
		}
	}
	if err := os.WriteFile(probePath, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		cancel()
		waitAST()
		return nil, err
	}
	llPath := filepath.Join(r.WorkDir, "probe.ll")
	args = append(append([]string{}, base...), "-S", "-emit-llvm", "-O0", "-o", llPath, "-Xclang", "-fdump-record-layouts-complete", "probe.c")
	dump, stderr, err := cl.RunClang(ctx, args, r.WorkDir)
	if err != nil {
		cancel()
		if a := waitAST(); a.err != nil {
			return nil, clangError("the C headers did not parse", a.args, a.serr, a.err)
		}
		return nil, clangError("the C import's probe did not compile", args, stderr, err)
	}
	ir, err := os.ReadFile(llPath)
	if err != nil {
		cancel()
		waitAST()
		return nil, err
	}
	mark("values", start)

	a := waitAST()
	if a.err != nil {
		var te *toolchain.ToolError
		_, aother := failedLines(string(a.serr), "tu.c", 1<<30)
		if !errors.As(a.err, &te) || !te.Started() || te.Cancelled || aother != "" || a.u == nil {
			return nil, clangError("the C headers did not parse", a.args, a.serr, a.err)
		}
	}
	u := a.u
	u.probes = probes
	steps = append(steps, Step{"ast", a.took})

	start = time.Now()
	e := &emitter{u: u, lay: parseLayouts(string(dump)), long64: !strings.Contains(r.Triple, "windows"), win: strings.Contains(r.Triple, "windows"), imports: r.Imports,
		mkName: map[*decl]string{}, skip: map[*decl]string{}, emitted: map[*decl]bool{},
		ptrOnly: map[*decl]string{}, sseRec: map[*decl]string{},
		structFields: map[*decl][]mkField{}, constVals: map[string]constScalar{}, constValsByName: map[string]constScalar{}}
	for range r.Imports {
		e.scopes = append(e.scopes, newScope())
		e.mods = append(e.mods, &modOut{uses: map[int]bool{}})
		e.stats = append(e.stats, Stats{})
		e.merged = append(e.merged, map[string]string{})
	}
	e.name()
	vals := irValues(string(ir))
	for _, m := range objs {
		if !m.ok {
			continue
		}
		m.typ = u.probes[m.k].vt
		m.mk = e.macroType(m.typ, m.imp)
		if m.mk != "" {
			m.value = mkValue(vals[m.k], m.mk, e.scalarPrimMk(e.resolve(m.typ)))
		}
	}
	e.macros = objs
	e.funcLike = funcLike
	mods := e.emit()
	mark("emit", start)
	steps = append(steps, Step{"total", time.Since(t0)})
	fs := shimDefs(e.shims)
	flByImp := make([][]string, len(r.Imports))
	for name, imp := range funcLike {
		if imp >= 0 && imp < len(flByImp) {
			flByImp[imp] = append(flByImp[imp], name)
		}
	}
	for i := range flByImp {
		slices.Sort(flByImp[i])
	}
	return &Result{Modules: mods, Shim: ShimText(r.Imports, fs), ShimFuncs: fs, Stats: e.stats, FuncLike: flByImp, Merged: e.merged, FunctionRedirects: u.redirects, Deps: deps,
		SearchDirs: SearchDirs(r, resourceDir), Unit: tu, Steps: steps}, nil
}

// macroType is the Mk type of a macro whose probe variable has type t: an
// integer, float or bool type (or an alias of one), str or []u16/[]i32/[]u32
// for a string literal, or uint for a pointer constant; "" for anything else.
func (e *emitter) macroType(t *ctype, imp int) string {
	r := e.resolve(t)
	if r == nil {
		return ""
	}
	if r.kind == tArray {
		if el := e.resolve(r.elem); el != nil && el.kind == tBuiltin {
			if strings.HasSuffix(el.name, "char") {
				return "str"
			}
			if m, _, ok := e.builtin(el.name); ok && (m == "u16" || m == "i32" || m == "u32") {
				return "[]" + m
			}
		}
		return ""
	}
	if r.kind == tPointer {
		return "uint"
	}
	if r.kind != tBuiltin && r.kind != tEnum {
		return ""
	}
	s, why := e.mkType(t, imp)
	if why != "" || s == "void" || strings.ContainsAny(s, "*[(") {
		return ""
	}
	return s
}

// declaredMacros binds each declared function-like macro through the shim.
func (e *emitter) declaredMacros() {
	for i, im := range e.imports {
		for _, proto := range im.Macros {
			st := &e.stats[i]
			st.Decls++
			name, ret, ps, ok := parseProto(proto)
			if !ok {
				e.skippedDecl(i, "macro", proto, "macro prototype")
				continue
			}
			if _, isFL := e.funcLike[name]; !isFL || !shimmable(ret, ps) || !e.scopes[i].claim(name) {
				e.skippedDecl(i, "macro", name, "declared macro")
				continue
			}
			sig, why := e.signature(name, ret, ps, false, i)
			if why != "" {
				e.skippedDecl(i, "macro", name, why)
				continue
			}
			fmt.Fprintf(&e.mods[i].items, "\n// C: #define %s(...), declared in pkg.mk as %s; defined by the shim\nexport extern %s\n", name, proto, sig)
			e.shims = append(e.shims, shimFunc{name: name, ret: ret, ps: ps, macro: true, imp: i})
			st.Generated++
		}
	}
}

// clangError wraps a failed Clang run on the headers.
func clangError(msg string, args []string, stderr []byte, err error) error {
	var te *toolchain.ToolError
	if errors.As(err, &te) && te.Cancelled {
		return err
	}
	cmd := "clang " + strings.Join(args, " ")
	if te != nil && te.Command != "" {
		cmd = te.Command
	}
	return &Error{Msg: msg + ": " + firstError(string(stderr), err), Command: cmd, Stderr: string(stderr),
		Missing: strings.Contains(string(stderr), " file not found")}
}

// firstError is the first error line of Clang's stderr, or err's text.
func firstError(stderr string, err error) string {
	for _, ln := range strings.Split(stderr, "\n") {
		if strings.Contains(ln, "error:") {
			return strings.TrimSpace(ln)
		}
	}
	return err.Error()
}

// parseDepFile reads the unit's dependency file, without the unit itself,
// which Clang names as its command line does: a header that is also named
// tu.c is a dependency.
func parseDepFile(s string) []string {
	var deps []string
	for _, d := range ParseDepFile(s) {
		if d != "tu.c" {
			deps = append(deps, d)
		}
	}
	return deps
}

// ParseDepFile reads a make-style dependency file of one target (clang
// -MD): "tu: a.h b\ c.h \", every prerequisite in order.
func ParseDepFile(s string) []string {
	s = strings.ReplaceAll(s, "\\\r\n", " ")
	s = strings.ReplaceAll(s, "\\\n", " ")
	if c := strings.Index(s, ": "); c >= 0 {
		s = s[c+2:]
	}
	var out []string
	var cur bytes.Buffer
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s) && s[i+1] == ' ':
			cur.WriteByte(' ')
			i++
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteByte(c)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// Count is a convenience for reports: "<bound>/<decls>".
func (s Stats) Count() string {
	return strconv.Itoa(s.Bound()) + "/" + strconv.Itoa(s.Decls)
}
