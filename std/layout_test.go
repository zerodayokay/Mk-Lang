// Why: std's directory layout, manifest and prelude must stay in step with
// the reference manual and with each other, or modules silently go missing.
// What: TestEmbedLayout, TestPerTargetSysModule, TestManifestScanIsOrderFree,
// TestManifestListsUnsafeModules, TestPreludeNamesMatchReference,
// TestTestPreludeMatchesReference, TestPreludeDeclared,
// TestGeneratorAPIDeclared and TestRefImpls, plus readText and referenceSection.
// How: text scans of pkg.mk, std's .mk files and doc/reference.md, relative to
// std/; the package imports only the Go standard library.

package std

import (
	"io/fs"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// readText reads a repository file relative to std/, with CRLF normalized
// to LF (the checkout may carry either).
func readText(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(rel)
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

// referenceSection returns the text of doc/reference.md from the
// heading that starts with head up to the next "## " heading.
func referenceSection(t *testing.T, head string) string {
	t.Helper()
	ref := readText(t, "../doc/reference.md")
	i := strings.Index(ref, "\n"+head)
	if i < 0 {
		t.Fatalf("doc/reference.md has no heading %q", head)
	}
	rest := ref[i+1:]
	if j := strings.Index(rest[len(head):], "\n## "); j >= 0 {
		rest = rest[:len(head)+j+1]
	}
	return rest
}

// TestPreludeNamesMatchReference: PreludeNames equals the prelude
// list of doc/reference.md's module map, in order.
func TestPreludeNamesMatchReference(t *testing.T) {
	sec := referenceSection(t, "## 17. Module map")
	m := regexp.MustCompile("Prelude, no import needed: `([^`]*)`").FindStringSubmatch(sec)
	if m == nil {
		t.Fatal("the module map has no prelude list")
	}
	want := strings.Fields(m[1])
	if !slices.Equal(PreludeNames[:], want) {
		t.Errorf("PreludeNames = %q\nthe module map = %q", PreludeNames, want)
	}
	if !slices.Contains(want, "Path") {
		t.Error("the module map no longer lists Path; the reserved-name rule needs revisiting")
	}
}

// TestTestPreludeMatchesReference: corpus/testprelude.mk equals the
// block of doc/reference.md's test prelude heading after its verdict line,
// byte for byte.
func TestTestPreludeMatchesReference(t *testing.T) {
	sec := referenceSection(t, "## 19. Test prelude")
	start := strings.Index(sec, "```mk\n")
	if start < 0 {
		t.Fatal("the reference's test prelude has no mk block")
	}
	body := sec[start+len("```mk\n"):]
	end := strings.Index(body, "```")
	if end < 0 {
		t.Fatal("the reference's test prelude block is not closed")
	}
	body = body[:end]
	nl := strings.IndexByte(body, '\n')
	if !strings.HasPrefix(body, "// verdict:") || nl < 0 {
		t.Fatalf("the reference's test prelude block does not start with a verdict line: %q", body)
	}
	want := body[nl+1:]
	got := readText(t, TestPreludePath)
	if got != want {
		t.Errorf("%s differs from the reference's test prelude:\n--- got\n%s--- want\n%s", TestPreludePath, got, want)
	}
	// The embedded copy is the same file.
	emb, err := FS.ReadFile(TestPreludePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ReplaceAll(string(emb), "\r\n", "\n") != want {
		t.Error("the embedded test prelude differs from the file on disk")
	}
}

// moduleDirs is std's module table: the directories that are
// modules under their own path ("std." + dir), the per-target directories
// pkg.mk's targets map to std.sys, and the one directory that is no module.
var moduleDirs = struct {
	modules, targetDirs []string
	notModule           string
}{
	modules: []string{"core", "raw", "fs", "net", "time", "proc", "sync", "atomic", "math", "derive", "rt", "task", "pkg", "coll", "hash", "mem", "json", "http", "regex", "crypto", "compress", "cli", "log", "text", "dyn", "sim", "simd", "mmio", "db", "libc", "free", "db/sqlite", "dyn/erased", "big", "csv", "dl", "encoding", "encoding/cjk", "url", "simd/kernel", "time/tzdata", "x509", "term", "tls", "websocket", "target", "runtime", "vm", "testing", "build", "http/fields", "http/h2", "http/h3", "net/pipe", "sync/lead", "quic"}, // a nested path names the module with dots: db/sqlite is std.db.sqlite, dyn/erased is std.dyn.erased
	// free/sys is std.sys on x86_64-freestanding, sys/nutshell on
	// the Nutshell targets, sys/macos on macOS; linux/linux is std.linux
	// for os "linux", linux/other its empty placeholder elsewhere;
	// lc/macos is std.sys.lc (libSystem's names) on macOS, lc/other its
	// empty placeholder elsewhere.
	targetDirs: []string{"sys/windows", "sys/linux", "free/sys", "sys/nutshell", "sys/macos", "linux/linux", "linux/other", "lc/macos", "lc/other"},
	notModule:  "corpus",
}

// mkDirs walks the embedded tree and returns every directory that holds a
// .mk file directly, byte-sorted.
func mkDirs(t *testing.T) []string {
	t.Helper()
	seen := map[string]bool{}
	err := fs.WalkDir(FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".mk") && path.Dir(p) != "." {
			seen[path.Dir(p)] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for d := range seen {
		out = append(out, d)
	}
	slices.Sort(out)
	return out
}

// TestEmbedLayout: every directory under std/ with .mk files is in
// moduleDirs, as a module directory or a per-target directory; no name on
// disk begins with "." or "_" (go:embed would skip it silently); corpus/
// holds only the test prelude.
func TestEmbedLayout(t *testing.T) {
	for _, d := range mkDirs(t) {
		if !slices.Contains(moduleDirs.modules, d) && !slices.Contains(moduleDirs.targetDirs, d) && d != moduleDirs.notModule {
			t.Errorf("std/%s holds .mk files but is not in moduleDirs", d)
		}
	}
	err := fs.WalkDir(os.DirFS("."), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d != nil && d.IsDir() && path.Base(p) == ".mk-cache" {
			// mk's build cache, made when mk runs inside std (gitignored).
			return fs.SkipDir
		}
		if b := path.Base(p); p != "." && (strings.HasPrefix(b, ".") || strings.HasPrefix(b, "_")) {
			t.Errorf("std/%s: a name beginning with %q is not embedded", p, b[:1])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ents, err := FS.ReadDir(moduleDirs.notModule)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if moduleDirs.notModule+"/"+e.Name() != TestPreludePath {
			t.Errorf("std/%s/%s: corpus/ holds only the test prelude", moduleDirs.notModule, e.Name())
		}
	}
	if _, err := FS.ReadFile("pkg.mk"); err != nil {
		t.Errorf("std/pkg.mk is not embedded: %v", err)
	}
}

// manifestField returns the text inside the brackets of pkg.mk's list field
// name, wherever it stands among the fields: the scan counts bracket depth
// and skips string literals, so nested lists such as a target's modules do
// not end it early.
func manifestField(text, name string) (string, bool) {
	loc := regexp.MustCompile(`(?m)^\s*` + name + `:\s*\[`).FindStringIndex(text)
	if loc == nil {
		return "", false
	}
	start := loc[1]
	depth, inStr := 1, false
	for i := start; i < len(text); i++ {
		c := text[i]
		switch {
		case inStr && c == '\\':
			i++
		case c == '"':
			inStr = !inStr
		case inStr:
		case c == '[':
			depth++
		case c == ']':
			depth--
			if depth == 0 {
				return text[start:i], true
			}
		}
	}
	return "", false
}

// manifestList returns the string list of field name in pkg.mk.
func manifestList(t *testing.T, text, name string) []string {
	t.Helper()
	body, ok := manifestField(text, name)
	if !ok {
		t.Fatalf("std/pkg.mk has no list field %s", name)
	}
	var out []string
	for _, s := range regexp.MustCompile(`"([^"]*)"`).FindAllStringSubmatch(body, -1) {
		out = append(out, s[1])
	}
	return out
}

// targetSelections returns each (os, module name, dir) of a manifest's
// targets field, in order; ok is false when there is no targets field.
func targetSelections(text string) (sel [][]string, body string, ok bool) {
	body, ok = manifestField(text, "targets")
	if !ok {
		return nil, "", false
	}
	// A selection may hold several modules (std.sys and, since rt10's
	// std.linux, a second one): one (os, name, dir) per module, in order.
	re := regexp.MustCompile(`pkg\.Select\(os: "([a-z]+)", modules: \[((?:pkg\.Module\(name: "[a-z.]+", dir: "[a-z/]+"\)(?:, )?)+)\]\)`)
	mod := regexp.MustCompile(`pkg\.Module\(name: "([a-z.]+)", dir: "([a-z/]+)"\)`)
	for _, m := range re.FindAllStringSubmatch(body, -1) {
		for _, x := range mod.FindAllStringSubmatch(m[2], -1) {
			sel = append(sel, []string{m[1], x[1], x[2]})
		}
	}
	return sel, body, true
}

// hostTargets is the base mapping of std.sys per hosted target OS.
var hostTargets = [][]string{{"windows", "sys", "sys/windows"}, {"linux", "sys", "sys/linux"}}

// wantTargets is pkg.mk's: hostTargets' two, free/sys for the freestanding
// target, sys/macos for macOS and sys/nutshell for the Nutshell targets;
// and, beside each std.sys, std.linux (linux/linux for linux, the empty
// linux/other elsewhere) and std.sys.lc (lc/macos for macOS, the empty
// lc/other elsewhere).
var wantTargets = [][]string{
	{"windows", "sys", "sys/windows"}, {"windows", "linux", "linux/other"}, {"windows", "sys.lc", "lc/other"},
	{"linux", "sys", "sys/linux"}, {"linux", "linux", "linux/linux"}, {"linux", "sys.lc", "lc/other"},
	{"freestanding", "sys", "free/sys"}, {"freestanding", "linux", "linux/other"}, {"freestanding", "sys.lc", "lc/other"},
	{"macos", "sys", "sys/macos"}, {"macos", "linux", "linux/other"}, {"macos", "sys.lc", "lc/macos"},
	{"nutshell", "sys", "sys/nutshell"}, {"nutshell", "linux", "linux/other"}, {"nutshell", "sys.lc", "lc/other"},
}

// TestPerTargetSysModule: pkg.mk's targets map the name sys to
// sys/windows for windows and to sys/linux for linux, exactly, and list no
// other directory. The driver refuses a selection naming a
// directory that does not exist (E0202 reason invalid), so when
// std/sys/windows and std/sys/linux are missing, targets is empty
// and the test checks that instead.
func TestPerTargetSysModule(t *testing.T) {
	got, body, ok := targetSelections(readText(t, "pkg.mk"))
	if !ok {
		t.Fatal("std/pkg.mk has no targets field")
	}
	_, errW := os.Stat("sys/windows")
	_, errL := os.Stat("sys/linux")
	if errW != nil || errL != nil {
		if strings.TrimSpace(body) != "" {
			t.Errorf("targets names sys directories that do not exist yet: %q", body)
		}
		t.Log("std/sys/windows and std/sys/linux do not exist: targets is empty")
		return
	}
	if !slices.EqualFunc(got, wantTargets, slices.Equal[[]string]) {
		t.Errorf("targets = %q, want %q", got, wantTargets)
	}
	for _, d := range mkDirs(t) {
		if (strings.HasPrefix(d, "sys/") || strings.HasPrefix(d, "free/")) && !slices.Contains(moduleDirs.targetDirs, d) {
			t.Errorf("std/%s: only sys/windows, sys/linux, sys/macos, free/sys and sys/nutshell form std.sys", d)
		}
	}
}

// orderSampleManifest is a std manifest with unsafe_modules before targets
// and a nested module list in each target, the shape the scan must read.
const orderSampleManifest = `const Package = pkg.Package(
    name: "std",
    bins: [],
    deps: [],
    c_sources: [],
    c_imports: [],
    asm_sources: [],
    unsafe_modules: ["core", "sync", "rt", "sys/windows", "sys/linux", "task"],
    targets: [
        pkg.Select(os: "windows", modules: [pkg.Module(name: "sys", dir: "sys/windows")]),
        pkg.Select(os: "linux", modules: [pkg.Module(name: "sys", dir: "sys/linux")]),
    ],
    links: [],`

// TestManifestScanIsOrderFree: the scan TestPerTargetSysModule uses reads a
// manifest where unsafe_modules comes before targets and each target holds
// a nested list, as the two windows and linux selections, and its
// unsafe_modules as six names; and today's pkg.mk as no selection or exactly
// those two. A regex that wanted targets first once failed on such text.
func TestManifestScanIsOrderFree(t *testing.T) {
	manifest := orderSampleManifest
	got, _, ok := targetSelections(manifest)
	if !ok || !slices.EqualFunc(got, hostTargets, slices.Equal[[]string]) {
		t.Errorf("orderSampleManifest's targets scan as %q (ok %v), want %q", got, ok, hostTargets)
	}
	if mods := manifestList(t, manifest, "unsafe_modules"); !slices.Equal(mods, []string{"core", "sync", "rt", "sys/windows", "sys/linux", "task"}) {
		t.Errorf("orderSampleManifest's unsafe_modules scan as %q", mods)
	}
	reordered := strings.Replace(manifest, "    targets: [", "    zz: [],\n    targets: [", 1)
	if got, _, _ := targetSelections(reordered); len(got) != 2 {
		t.Errorf("a field before targets changes the scan: %q", got)
	}
	if got, body, ok := targetSelections(readText(t, "pkg.mk")); !ok || len(got) != 0 || strings.TrimSpace(body) != "" {
		if !ok || !slices.EqualFunc(got, wantTargets, slices.Equal[[]string]) {
			t.Errorf("today's pkg.mk targets scan as %q %q (ok %v), want empty or %q", got, body, ok, wantTargets)
		}
	}
}

// unsafeUse is the text of an unsafe block or an unsafe impl in Mk source.
var unsafeUse = regexp.MustCompile(`(?m)\bunsafe ("|impl\b)`)

// TestManifestListsUnsafeModules: every directory with an unsafe
// block or unsafe impl is in pkg.mk's unsafe_modules, and no listed
// directory lacks both.
func TestManifestListsUnsafeModules(t *testing.T) {
	listed := manifestList(t, readText(t, "pkg.mk"), "unsafe_modules")
	uses := map[string]bool{}
	err := fs.WalkDir(FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".mk") {
			return err
		}
		b, err := FS.ReadFile(p)
		if err != nil {
			return err
		}
		if unsafeUse.Match(b) {
			uses[path.Dir(p)] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for d := range uses {
		if !slices.Contains(listed, d) {
			t.Errorf("std/%s uses unsafe but is not in pkg.mk's unsafe_modules", d)
		}
	}
	for _, d := range listed {
		if !uses[d] {
			t.Errorf("pkg.mk lists %q in unsafe_modules, but no .mk file there uses unsafe", d)
		}
	}
}

// TestPreludeDeclared: every prelude name is exported by a file of std/core
// (the module map's prelude list); Path is not reserved. A text
// scan; the driver's own count (StdMissing) is the semantic check.
func TestPreludeDeclared(t *testing.T) {
	ents, err := FS.ReadDir("core")
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".mk") {
			b, err := FS.ReadFile("core/" + e.Name())
			if err != nil {
				t.Fatal(err)
			}
			all.Write(b)
		}
	}
	text := all.String()
	for _, n := range PreludeNames {
		decl := regexp.MustCompile(`(?m)^export ((?:linear |mut |view |open )*)(struct|enum|error|interface|type) ` + n + `\b|^export ` + n + `[\[(]`)
		found := decl.MatchString(text)
		if !found {
			t.Errorf("std.core does not export the prelude name %s", n)
		}
	}
}

// genSurface is the generator API that core/core_gen.mk declares: the three
// enums with their variants in order, and every method's signature. The
// evaluator implements these methods; change this table together with it.
const genSurface = `enum TypeKind {
    Struct
    Enum
    Error
    Interface
    Record
    Scalar
    Ref
    Slice
    Array
    Optional
    Fallible
    Fn
    RawPtr
    Opaque
}

enum Receiver {
    Static
    Value
    Ref
    Mut
}

enum FlowKind {
    From
    Into
    Ends
    Store
}

Gen.Emit(mut self, c: Code)
Gen.Block(&self): Block
Gen.Arena(&self): &Arena
Gen.Fail(mut self, msg: str)
Gen.FailAt(mut self, f: Field, msg: str)
Gen.FailOn(mut self, t: Type, msg: str)
Gen.Fresh(mut self, base: str): Code
Gen.Ident(&self, name: str): Code
Gen.Str(&self, s: str): Code
Gen.Input(&self, path: str): []u8

Block.Add(mut self, c: Code)
Code.Then(self, c: Code): Code

Type.Name(&self): Code
Type.Text(&self): str
Type.Kind(&self): TypeKind
Type.Exported(&self): bool
Type.Same(&self, other: Type): bool
Type.Args(&self): []Type
Type.Elem(&self): Type
Type.Fields(&self): []Field
Type.Variants(&self): []Variant
Type.Methods(&self): []Method

Field.Name(&self): str
Field.Type(&self): Type
Field.Exported(&self): bool
Field.Doc(&self): str

Variant.Name(&self): str
Variant.Fields(&self): []Field

Method.Name(&self): str
Method.Receiver(&self): Receiver
Method.Params(&self): []Param
Method.Result(&self): Type
Method.Generic(&self): bool
Method.Flows(&self): []Flow
Method.HasFlows(&self): bool
Method.Signature(&self): Code

Flow.Kind(&self): FlowKind
Flow.Param(&self): str
Flow.Path(&self): str
Flow.Field(&self): str

Param.Name(&self): str
Param.Type(&self): Type
Param.Const(&self): bool
`

// TestGeneratorAPIDeclared: core/core_gen.mk declares the generator API
// of genSurface as part of the prelude: every type a plain exported
// struct with one private u32 handle, TypeKind, Receiver and FlowKind
// exported enums with genSurface's variants in order, and every method of
// genSurface exported, bodiless, with its signature.
func TestGeneratorAPIDeclared(t *testing.T) {
	b, err := FS.ReadFile("core/core_gen.mk")
	if err != nil {
		t.Fatal(err)
	}
	have := strings.ReplaceAll(string(b), "\r\n", "\n")
	surface := genSurface
	for _, n := range []string{"Gen", "Type", "Field", "Variant", "Method", "Flow", "Param", "Code", "Block"} {
		if !slices.Contains(PreludeNames[:], n) {
			t.Errorf("PreludeNames lacks the generator API type %s", n)
		}
		if !strings.Contains(have, "\nexport struct "+n+" {\n    h: u32\n}\n") {
			t.Errorf("core_gen.mk does not declare `export struct %s { h: u32 }`", n)
		}
	}
	enum := regexp.MustCompile(`(?m)^enum (\w+) \{\n((?:    \w+\n)+)\}`)
	enums := enum.FindAllStringSubmatch(surface, -1)
	if len(enums) != 3 {
		t.Fatalf("genSurface declares %d enums, want TypeKind, Receiver and FlowKind", len(enums))
	}
	for _, e := range enums {
		if !slices.Contains(PreludeNames[:], e[1]) {
			t.Errorf("PreludeNames lacks the generator API enum %s", e[1])
		}
		if want := "\nexport enum " + e[1] + " {\n" + e[2] + "}\n"; !strings.Contains(have, want) {
			t.Errorf("core_gen.mk does not declare %q", want)
		}
	}
	methods := 0
	for _, line := range strings.Split(surface, "\n") {
		if !regexp.MustCompile(`^(Gen|Type|Field|Variant|Method|Flow|Param|Code|Block)\.\w+\(`).MatchString(line) {
			continue
		}
		methods++
		if !strings.Contains(have, "\nexport "+line+"\n") {
			t.Errorf("core_gen.mk does not declare the bodiless `export %s`", line)
		}
	}
	if methods < 30 {
		t.Errorf("genSurface lists %d methods; the scan is broken", methods)
	}
}

// TestRefImpls: Eq, Ord and Hash hold for &T whenever they hold for T
// as Format does, and each &T impl reaches T's impl through **self,
// never through *self (a &T, which would name the &T impl itself).
func TestRefImpls(t *testing.T) {
	b, err := FS.ReadFile("core/core_impls.mk")
	if err != nil {
		t.Fatal(err)
	}
	have := strings.ReplaceAll(string(b), "\r\n", "\n")
	for _, c := range []struct{ iface, sig, call string }{
		{"Format", "Format(&self, w: mut Writer): WriteError!void {", "try (**self).Format(w)"},
		{"Eq", "Eq(&self, other: &&T): bool {", "return (**self).Eq(*other)"},
		{"Ord", "Less(&self, other: &&T): bool {", "return (**self).Less(*other)"},
		{"Hash", "Hash(&self, h: mut Hasher) {", "(**self).Hash(h)"},
	} {
		want := "impl[T: " + c.iface + "] " + c.iface + " for &T {\n    " + c.sig + "\n        " + c.call + "\n    }\n}\n"
		if !strings.Contains(have, want) {
			t.Errorf("core_impls.mk lacks\n%s", want)
		}
	}
}
