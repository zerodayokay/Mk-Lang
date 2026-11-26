// Why: type layouts must agree between the checker, the evaluator, native code
// and the golden files, on every target.
// What: TestDiffLayout, TestLayoutOffsetsChecked and their helpers.
// How: layout.mk is extended with compile-time checks, a native Main, or one
// alias per row, and each result is compared with layout.<target>.golden.
//
// TestDiffLayout's evaluator half runs here: on each of the four targets,
// every SizeOf and AlignOf that layout.mk yields equals
// layout.<target>.golden, checked by the driver's evaluator while it checks
// layout.mk extended with one compile-time check per row (LayoutCheckSource).
// The native half runs layout.mk extended with a Main that compares the same
// values on the two runnable targets (LayoutProgramSource), and every golden
// offset that has a native probe (layoutNativeOffsets,
// diff_offsets_test.go: 44 of 50; the result rows Bad!i64, Bad!u8 and Big!u8
// are check-only, layoutOffsetsCheckOnly). The offsets are also compared with
// check's types.Layout by TestLayoutOffsetsChecked.

package corpus

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"mk/internal/diag"
	"mk/internal/driver"
	"mk/internal/types"
)

var diffTargets = []string{"x86_64-windows", "x86_64-linux", "aarch64-linux", "aarch64-macos"}

func TestDiffLayout(t *testing.T) {
	root := repoRoot(t)
	src, err := os.ReadFile(filepath.Join(root, DiffDir, "layout.mk"))
	if err != nil {
		t.Fatal(err)
	}
	funcs := LayoutFuncs(src)
	if len(funcs) == 0 {
		t.Fatal("layout.mk declares no Layout<Name> function")
	}
	for _, name := range diffTargets {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tgt, _, ok := driver.TargetNamed(name)
			if !ok {
				t.Fatalf("no target %s", name)
			}
			path := filepath.Join(root, DiffDir, "layout."+name+".golden")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			rows, err := ParseLayoutGolden(DiffDir+"/layout."+name+".golden", data)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, r := range rows {
				names = append(names, r.Name)
			}
			if !slices.Equal(names, funcs) {
				t.Fatalf("the golden's rows %v are not layout.mk's Layout functions %v, in order", names, funcs)
			}
			t.Run("eval", func(t *testing.T) {
				layoutEval(t, src, rows, tgt, layoutStd(t, tgt), path)
			})
			if tgt.Name == driver.HostTarget().Name || tgt.Name == "x86_64-linux" && driver.HostTarget().OS == types.OSWindows {
				t.Run("native", func(t *testing.T) {
					layoutNative(t, src, rows, name, path)
				})
			}
		})
	}
}

// layoutEval is TestDiffLayout's evaluator half on tgt over std ("" for
// the embedded std), for the golden at path. When std's own diagnostics keep
// the checks from running, the leaf skips, and fails under MK_GATE=1
// (the gate turns skips into t.Fatal).
func layoutEval(t *testing.T, src []byte, rows []LayoutRow, tgt types.Target, std, path string) {
	t.Helper()
	// The planted row proves the checks run: without it a check that never
	// reached the consts would pass.
	planted := slices.Clone(rows)
	planted[0].Size++
	got, stdErrs := layoutMismatches(t, src, planted, tgt, std)
	if !slices.Equal(got, []string{planted[0].Name}) {
		if len(stdErrs) > 0 {
			skipOrFatal(t, gateRun(), "BLOCKED: std has errors of its own on %s, so the checks did not run: %s, and %d more", tgt.Name, stdErrs[0], len(stdErrs)-1)
		}
		t.Fatalf("with %s's size planted wrong, the evaluator flags %v, want exactly [%s]", planted[0].Name, got, planted[0].Name)
	}
	if len(stdErrs) > 0 {
		t.Errorf("std has %d diagnostics of its own on %s, first %s", len(stdErrs), tgt.Name, stdErrs[0])
	}
	got, _ = layoutMismatches(t, src, rows, tgt, std)
	for _, n := range got {
		r := rows[slices.IndexFunc(rows, func(r LayoutRow) bool { return r.Name == n })]
		t.Errorf("%s:%d: the evaluator's SizeOf or AlignOf of %s is not size %d, align %d", path, r.Line, n, r.Size, r.Align)
	}
	t.Logf("%d rows: SizeOf and AlignOf by the evaluator equal the golden", len(rows))
}

// layoutNative builds LayoutProgramSource for target in dev and release and
// runs it: it must exit 0, every SizeOf and AlignOf natively equal to the
// golden. The same program with the first row's size planted wrong must exit
// 1, so a Main that checks nothing cannot pass. Then every golden offset
// that has a native probe (all but the result rows' 6,
// layoutOffsetsCheckOnly) is checked by layoutNativeOffsets, over a std copy
// with std/zzlayout added, since Mk has no OffsetOf and user code cannot
// import std.raw.
func layoutNative(t *testing.T, src []byte, rows []LayoutRow, target, golden string) {
	planted := slices.Clone(rows)
	planted[0].Size++
	tools := needLLVM(t)
	types, err := layoutTypeArgs(src)
	if err != nil {
		t.Fatal(err)
	}
	probeStd := layoutProbeStd(t)
	for _, prof := range []string{"dev", "release"} {
		t.Run(prof, func(t *testing.T) {
			t.Parallel()
			for _, c := range []struct {
				rows []LayoutRow
				want int
			}{{rows, 0}, {planted, 1}} {
				gen, err := LayoutProgramSource(src, c.rows)
				if err != nil {
					t.Fatal(err)
				}
				dir := t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, "layout.mk"), gen, 0o644); err != nil {
					t.Fatal(err)
				}
				run := testRunner(t, dir, tools)
				if _, err := run.Build("layout.mk", target, prof); err != nil {
					t.Fatal(err)
				}
				needWSLRun(t, target, true)
				got, err := run.Run(ProgramCase{Program: "layout.mk", Timeout: DefaultCaseTimeout}, target, prof)
				switch {
				case ErrNoRunner(err):
					if gateRun() {
						t.Fatal(err)
					}
					t.Skip(err)
				case err != nil:
					t.Fatal(err)
				case got.TimedOut:
					t.Fatalf("timed out after %v", DefaultCaseTimeout)
				case got.Exit == c.want && len(got.Stdout) == 0 && len(got.Stderr) == 0:
				case c.want == 1:
					t.Errorf("with %s's size planted wrong: exit %d, stderr %q; want exit 1", planted[0].Name, got.Exit, got.Stderr)
				case got.Exit >= 1 && got.Exit <= len(rows):
					r := rows[got.Exit-1]
					t.Errorf("%s:%d: the native SizeOf or AlignOf of %s is not size %d, align %d", golden, r.Line, r.Name, r.Size, r.Align)
				default:
					t.Errorf("exit %d, stdout %q, stderr %q; want exit 0", got.Exit, got.Stdout, got.Stderr)
				}
			}
			dir := t.TempDir()
			r := NewRunner(dir, driver.Config{Tools: tools, CacheDir: t.TempDir(), StdDir: probeStd}, t.TempDir())
			r.WSLSlots = testWSLSlots
			layoutNativeOffsets(t, r, dir, src, rows, types, target, prof, golden)
		})
	}
}

// layoutMismatches checks layout.mk with one compile-time check per row on
// target tgt and returns the rows the evaluator disagrees with. Any other
// diagnostic in the unit, and any E0000, fails the test. std's diagnostics
// are returned as stdErrs (on aarch64-macos std has no std.sys), and the
// caller skips only when they kept the checks from running.
func layoutMismatches(t *testing.T, src []byte, rows []LayoutRow, tgt types.Target, std string) (bad, stdErrs []string) {
	t.Helper()
	gen, at := LayoutCheckSource(src, rows)
	file := filepath.Join(t.TempDir(), "layout.mk")
	if err := os.WriteFile(file, gen, 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := driver.NewSession(driver.Config{Root: file, Target: tgt, NoCache: true, StdDir: std})
	if err != nil {
		t.Fatal(err)
	}
	res := s.Check()
	for _, e := range res.Report.Diags {
		d := e.Diag
		pos := res.Sources.Position(d.At.StartPos())
		switch {
		case d.Code == diag.E0000:
			t.Fatalf("internal error: %s: %s", pos.Path, d.Message)
		case filepath.Clean(pos.Path) == filepath.Clean(file) && d.Code == diag.E0901 && strings.Contains(d.Message, "panicked: assert"):
			r, ok := at[int(pos.Line)]
			if !ok {
				t.Fatalf("E0901 at line %d, which holds no row's check: %s", pos.Line, d.Message)
			}
			bad = append(bad, r.Name)
		case strings.HasPrefix(filepath.ToSlash(pos.Path), "std/"):
			stdErrs = append(stdErrs, pos.String()+": "+d.Code.String()+" "+d.Message)
		default:
			t.Fatalf("unexpected diagnostic on %s: %s:%d:%d: %s %s", tgt.Name, pos.Path, pos.Line, pos.Col, d.Code, d.Message)
		}
	}
	return bad, stdErrs
}

func TestParseLayoutGolden(t *testing.T) {
	rows, err := ParseLayoutGolden("g", []byte("# header\n\nI8 1 1\r\nPair 16 8 0 8\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[1].Name != "Pair" || rows[1].Size != 16 || !slices.Equal(rows[1].Offsets, []int{0, 8}) || rows[1].Line != 4 {
		t.Fatalf("rows %+v", rows)
	}
	for in, want := range map[string]string{
		"I8 1\n":             "g:1: want",
		"i8 1 1\n":           "is not a type name",
		"I8 1 1\nI8 1 1\n":   "g:2: I8 is also on line 1",
		"I8 1 x\n":           "is not a non-negative integer",
		"I8 -1 1\n":          "is not a non-negative integer",
		"P 4 3\n":            "not a power of two",
		"P 4 4 4\n":          "offset 4 is not below the size 4",
		"# only a comment\n": "no rows",
		"P 8 8 0 4\nQ 1 1\n": "",
	} {
		_, err := ParseLayoutGolden("g", []byte(in))
		if want == "" {
			if err != nil {
				t.Errorf("%q: %v", in, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: error %v, want %q", in, err, want)
		}
	}
}

func TestLayoutCheckSource(t *testing.T) {
	src := []byte("struct Layout {\n    size: int\n    align: int\n}\nLayoutI8(): Layout {\n    return Layout(size: SizeOf[i8](), align: AlignOf[i8]())\n}")
	if got := LayoutFuncs(src); !slices.Equal(got, []string{"I8"}) {
		t.Fatalf("LayoutFuncs = %v", got)
	}
	gen, at := LayoutCheckSource(src, []LayoutRow{{Name: "I8", Size: 1, Align: 1}, {Name: "U8", Size: 1, Align: 1}})
	lines := strings.Split(string(gen), "\n")
	for n, r := range at {
		if want := "const DiffWant" + r.Name + ": bool = DiffWantLayout(Layout" + r.Name + "(), 1, 1)"; lines[n-1] != want {
			t.Errorf("line %d is %q, want %q", n, lines[n-1], want)
		}
	}
	if len(at) != 2 {
		t.Errorf("%d rows placed, want 2", len(at))
	}
}

func TestLayoutProgramSource(t *testing.T) {
	gen, err := LayoutProgramSource([]byte("struct Layout {\n    size: int\n    align: int\n}"), []LayoutRow{{Name: "I8", Size: 1, Align: 1}, {Name: "Pair", Size: 16, Align: 8}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"DiffRowDiffers(l: Layout, size: int, align: int): bool {\n    return l.size != size || l.align != align\n}",
		"    if DiffRowDiffers(LayoutI8(), 1, 1) {\n        return 1\n    }\n    if DiffRowDiffers(LayoutPair(), 16, 8) {\n        return 2\n    }\n    return 0\n}\n",
	} {
		if !strings.Contains(string(gen), want) {
			t.Errorf("generated source lacks %q:\n%s", want, gen)
		}
	}
	if _, err := LayoutProgramSource(nil, make([]LayoutRow, 101)); err == nil {
		t.Error("101 rows: no error")
	}
}

// Every SizeOf, AlignOf and field offset that layout.mk yields equals the
// golden, and the offsets are compared with the checker's types.Layout: on
// each target, layout.mk is checked with one alias per golden row appended,
// type DiffLayoutOf<Name> = T for the T of Layout<Name>'s SizeOf[T](), and
// driver.Session.LayoutOf gives check's layout of each alias. Every row's
// size, alignment and offsets (goldenOffsets, layout.mk's convention) must
// equal the golden's, and every offset of the goldens is compared. The same
// layouts against the golden with Mixed's last offset planted wrong (12
// to 13) must flag exactly Mixed, so a comparison that checks nothing
// cannot pass.
func TestLayoutOffsetsChecked(t *testing.T) {
	root := repoRoot(t)
	src, err := os.ReadFile(filepath.Join(root, DiffDir, "layout.mk"))
	if err != nil {
		t.Fatal(err)
	}
	args, err := layoutTypeArgs(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range diffTargets {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tgt, _, ok := driver.TargetNamed(name)
			if !ok {
				t.Fatalf("no target %s", name)
			}
			golden := DiffDir + "/layout." + name + ".golden"
			data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(golden)))
			if err != nil {
				t.Fatal(err)
			}
			rows, err := ParseLayoutGolden(golden, data)
			if err != nil {
				t.Fatal(err)
			}
			var want int
			for _, r := range rows {
				want += len(r.Offsets)
			}
			if want == 0 {
				t.Fatal("the golden holds no offset")
			}
			layouts := checkLayouts(t, src, args, rows, tgt, layoutStd(t, tgt))
			bad, compared := layoutOffsetMismatches(rows, layouts)
			for _, b := range bad {
				t.Errorf("%s:%s", golden, b)
			}
			if compared != want {
				t.Errorf("%d offsets compared, the golden holds %d", compared, want)
			}
			planted := slices.Clone(rows)
			i := slices.IndexFunc(planted, func(r LayoutRow) bool { return r.Name == "Mixed" })
			if i < 0 || len(planted[i].Offsets) == 0 || planted[i].Offsets[len(planted[i].Offsets)-1] != 12 {
				t.Fatalf("%s: no row Mixed whose last offset is 12 to plant", golden)
			}
			planted[i].Offsets = slices.Clone(planted[i].Offsets)
			planted[i].Offsets[len(planted[i].Offsets)-1] = 13
			pbad, _ := layoutOffsetMismatches(planted, layouts)
			if len(pbad) != 1 || !strings.Contains(pbad[0], " Mixed: ") {
				t.Errorf("with Mixed's last offset planted as 13, the comparison flags %q, want exactly Mixed", pbad)
			}
			t.Logf("%d rows, %d offsets: check's types.Layout equals the golden", len(rows), compared)
		})
	}
}

// layoutTypeArgs maps each Layout<Name> function of layout.mk to the type
// argument of the SizeOf call in its body, brackets matched.
func layoutTypeArgs(src []byte) (map[string]string, error) {
	out := map[string]string{}
	text := string(src)
	for _, name := range LayoutFuncs(src) {
		head := "\nLayout" + name + "(): Layout {"
		at := strings.Index(text, head)
		if at < 0 {
			return nil, fmt.Errorf("layout.mk: no %q", head[1:])
		}
		body := text[at+len(head):]
		if end := strings.Index(body, "\n}"); end >= 0 {
			body = body[:end]
		}
		k := strings.Index(body, "SizeOf[")
		if k < 0 {
			return nil, fmt.Errorf("layout.mk: Layout%s calls no SizeOf", name)
		}
		rest, depth := body[k+len("SizeOf["):], 1
		for j, c := range rest {
			switch c {
			case '[':
				depth++
			case ']':
				depth--
			}
			if depth == 0 {
				out[name] = rest[:j]
				break
			}
		}
		if out[name] == "" {
			return nil, fmt.Errorf("layout.mk: Layout%s: SizeOf[ is not closed", name)
		}
	}
	return out, nil
}

// checkLayouts checks layout.mk with one alias per row appended for tgt and
// returns check's layout of each row's type, by row name, over std (""
// for the embedded one). A target whose std does not check skips, and fails
// under MK_GATE=1, as TestDiffLayout's evaluator half does.
func checkLayouts(t *testing.T, src []byte, args map[string]string, rows []LayoutRow, tgt types.Target, std string) map[string]driver.TypeLayout {
	t.Helper()
	var b strings.Builder
	b.Write(src)
	b.WriteString("\n// Appended by internal/corpus (TestLayoutOffsetsChecked): one alias per golden row.\n")
	for _, r := range rows {
		arg, ok := args[r.Name]
		if !ok {
			t.Fatalf("golden row %s has no Layout%s in layout.mk", r.Name, r.Name)
		}
		fmt.Fprintf(&b, "type DiffLayoutOf%s = %s\n", r.Name, arg)
	}
	file := filepath.Join(t.TempDir(), "layout.mk")
	if err := os.WriteFile(file, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := driver.NewSession(driver.Config{Root: file, Target: tgt, NoCache: true, StdDir: std})
	if err != nil {
		t.Fatal(err)
	}
	res := s.Check()
	var stdErrs []string
	for _, e := range res.Report.Diags {
		d := e.Diag
		pos := res.Sources.Position(d.At.StartPos())
		if d.Code != diag.E0000 && strings.HasPrefix(filepath.ToSlash(pos.Path), "std/") {
			stdErrs = append(stdErrs, pos.String()+": "+d.Code.String()+" "+d.Message)
			continue
		}
		t.Fatalf("%s:%d:%d: %s %s", pos.Path, pos.Line, pos.Col, d.Code, d.Message)
	}
	if len(stdErrs) > 0 {
		skipOrFatal(t, gateRun(), "BLOCKED: std has errors of its own on %s, so check lays out nothing: %s, and %d more", tgt.Name, stdErrs[0], len(stdErrs)-1)
	}
	out := map[string]driver.TypeLayout{}
	for _, r := range rows {
		l, err := s.LayoutOf("DiffLayoutOf" + r.Name)
		if err != nil {
			t.Fatal(err)
		}
		out[r.Name] = l
	}
	return out
}

// layoutOffsetMismatches compares each row's size, alignment and offsets
// with check's layout, and returns one "<line>: <Name>: ..." per row that
// differs, and the number of offsets compared.
func layoutOffsetMismatches(rows []LayoutRow, layouts map[string]driver.TypeLayout) ([]string, int) {
	var bad []string
	var compared int
	for _, r := range rows {
		l := layouts[r.Name]
		got, err := goldenOffsets(l)
		if err != nil {
			bad = append(bad, fmt.Sprintf("%d: %s: %v", r.Line, r.Name, err))
			continue
		}
		size, align := l.Layout.Shape.Size, uint64(l.Layout.Shape.Align)
		if size != uint64(r.Size) || align != uint64(r.Align) || !slices.Equal(got, r.Offsets) {
			bad = append(bad, fmt.Sprintf("%d: %s: check lays it out as size %d, align %d, offsets %v; the golden says %d %d %v", r.Line, r.Name, size, align, got, r.Size, r.Align, r.Offsets))
			continue
		}
		compared += len(got)
	}
	return bad, compared
}

// goldenOffsets turns check's layout into the golden's offsets, by
// layout.mk's convention: a struct's fields in declaration order and a
// record's in name order (both as Layout.Fields lays them out); a slice's
// or str's pointer and length; an array's elements; for a tagged type (an
// enum, a ?T, an E!T) its tag, unless the tag is a niche, then where every
// variant's payload starts: one offset for an enum and a ?T, and for an E!T,
// whose variants each start at their own alignment, every distinct start in
// variant order, the ok payload first. A type of fewer than two parts lists no offset
// (scalars, references, fn, a niche ?&T, an enum with no payload, Cell[i32]).
func goldenOffsets(l driver.TypeLayout) ([]int, error) {
	var parts []int
	switch {
	case l.Layout.HasTag:
		if !l.Layout.Tag.Niche {
			parts = append(parts, int(l.Layout.Tag.Offset))
		}
		own := l.Kind == types.KErrorUnion || l.Kind == types.KErrorSet
		var starts []int
		for _, v := range l.Layout.Variants {
			if len(v.Fields) == 0 {
				continue
			}
			at := int(v.Fields[0].Offset)
			if len(starts) > 0 && !own && at != starts[0] {
				return nil, fmt.Errorf("variant payloads start at %d and %d", starts[0], at)
			}
			if !slices.Contains(starts, at) {
				starts = append(starts, at)
			}
		}
		parts = append(parts, starts...)
	case l.Kind == types.KStr || l.Kind == types.KSlice || l.Kind == types.KMutSlice:
		parts = []int{0, int(l.Layout.Shape.Size / 2)}
	case l.Kind == types.KArray:
		for i := range l.Len {
			parts = append(parts, int(i*l.Elem.Size))
		}
	default:
		for _, f := range l.Layout.Fields {
			parts = append(parts, int(f.Offset))
		}
	}
	if len(parts) < 2 {
		return nil, nil
	}
	return parts, nil
}

// layoutStd is the std directory the layout checks of tgt run over: ""
// (the embedded std) on every target std/pkg.mk selects a std.sys half for.
// std has no aarch64-macos std.sys, so on that target alone it is a copy of
// the repository's std whose pkg.mk also selects sys/linux for macos. layout.mk
// imports nothing and none of its types is std's, so which sys half std
// checks with changes no row: only check's target facts for aarch64-macos
// lay the rows out. The macOS golden is then verified instead of skipped.
func layoutStd(t *testing.T, tgt types.Target) string {
	t.Helper()
	if tgt.OS != types.OSMacos {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "std", "pkg.mk"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `os: "macos"`) {
		return ""
	}
	m := strings.ReplaceAll(string(b), "\r\n", "\n")
	// The copy maps std.linux to its empty placeholder, as every target but
	// os linux does: std.linux is x86_64-linux only.
	linux := `        pkg.Select(os: "linux", modules: [pkg.Module(name: "sys", dir: "sys/linux"), pkg.Module(name: "linux", dir: "linux/linux")]),` + "\n"
	if !strings.Contains(m, linux) {
		t.Fatalf("std/pkg.mk has no Linux target line %q to copy for macos", linux)
	}
	macos := strings.Replace(strings.Replace(linux, `os: "linux"`, `os: "macos"`, 1), `dir: "linux/linux"`, `dir: "linux/other"`, 1)
	m = strings.Replace(m, linux, linux+macos, 1)
	std := digitsStd(t, map[string]string{"pkg.mk": m})
	t.Logf("%s: std/pkg.mk selects no sys half for macos; checked over a std copy that selects sys/linux", tgt.Name)
	return std
}
