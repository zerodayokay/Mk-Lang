// Why: std.core's byte paths (ValidUtf8, Find, Index, StartsWith, EndsWith)
// call std.simd and CopyFrom is one raw.Move; they must answer as the scalar.
// What: TestCore_BytePathsMatchScalar, FuzzCore_BytePaths,
// TestCore_BytePathsCallSimd and TestCore_CompileTimeWitnesses.
// How: a --release driver against Go's unicode/utf8, bytes and strings;
// go test -fuzz FuzzCore_BytePaths -parallel 2 explores (5 minutes a run).

package std

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// core11BytesDriver reads cases of four lines (the data in hex after an x,
// the byte c, the offset from, and the needle in hex after an x) and
// writes one line per case: v whether str.From accepts the data, i the
// first c at or after from by Slice[u8].Index, c whether CopyFrom copied
// every byte, and, when the data is text, f the one-byte Find of c (when c
// is ASCII), and, when the needle is text too, s, e and n: StartsWith,
// EndsWith and Find of the needle.
const core11BytesDriver = `Nib(c: u8): u8 {
    if c >= 97 {
        return c - 87
    }
    return c - 48
}

Hex(a: &Arena, line: str): OutOfMemory![]u8 from a {
    b := line.AsBytes()
    z := try a.Slice[u8]((b.Len() - 1) / 2)
    for k in 0..z.Len() {
        z[k] = (Nib(b[1 + 2 * k]) << 4) | Nib(b[2 + 2 * k])
    }
    return z[..]
}

Opt(out: mut Writer, k: str, x: ?int): WriteError!void {
    if i := x {
        try Print(out, " {k}={i}", (k: k, i: i))
    } else {
        try Print(out, " {k}=-", (k: k))
    }
}

Bit(b: bool): int {
    if b {
        return 1
    }
    return 0
}

Case(a: &Arena, out: mut Writer, lines: mut Lines, first: str): WriteError|ParseError|OutOfMemory!void {
    data := try Hex(a, first)
    c := Trunc[u8](try lines.Next().Must().ParseInt())
    from := Cast[int](try lines.Next().Must().ParseInt())
    needle := try Hex(a, lines.Next().Must())
    var ok := true
    _ = str.From(data) else {
        ok = false
    }
    try Print(out, "v={v}", (v: Bit(ok)))
    var at: ?int = none
    if k := data[from..].Index(&c) {
        at = some(from + k)
    }
    try Opt(out, "i", at)
    copy := try a.Slice[u8](data.Len())
    copy.CopyFrom(data)
    var same := true
    for j in 0..data.Len() {
        if copy[j] != data[j] {
            same = false
        }
    }
    try Print(out, " c={c}", (c: Bit(same)))
    s := str.From(data) else {
        try out.Str("\n")
        return
    }
    if c < 0x80 {
        var one: [1]u8 = [c]
        try Opt(out, "f", s.Find(str.From(one[..]).Must()))
    }
    match str.From(needle) {
        ok(t) {
            try Print(out, " s={s} e={e}", (s: Bit(s.StartsWith(t)), e: Bit(s.EndsWith(t))))
            try Opt(out, "n", s.Find(t))
        }
        err(_) {}
    }
    try out.Str("\n")
}

export Main(sys: &System): ReadError|WriteError|Utf8Error|ParseError|OutOfMemory!void {
    var inp := sys.Stdin()
    buf := try sys.arena.Slice[u8](1 << 26)
    n := try inp.ReadAll(buf)
    text := try str.From(buf[0..n])
    var out := sys.Stdout()
    var lines := text.Lines()
    var w := try sys.arena.Sub()
    defer w.Free()
    for first := lines.Next() {
        try Case(&w, mut out, mut lines, first)
        w.Reset()
    }
    try out.Flush()
}
`

// core11BytesCase is one case's input lines and the model's output line.
func core11BytesCase(data []byte, c byte, from int, needle []byte) (in, want string) {
	if from < 0 {
		from = 0
	}
	if from > len(data) {
		from = len(data)
	}
	in = fmt.Sprintf("x%s\n%d\n%d\nx%s\n", hex.EncodeToString(data), c, from, hex.EncodeToString(needle))
	var b strings.Builder
	fmt.Fprintf(&b, "v=%d", core11Bit(utf8.Valid(data)))
	opt := func(k string, i int) {
		if i < 0 {
			fmt.Fprintf(&b, " %s=-", k)
		} else {
			fmt.Fprintf(&b, " %s=%d", k, i)
		}
	}
	at := bytes.IndexByte(data[from:], c)
	if at >= 0 {
		at += from
	}
	opt("i", at)
	b.WriteString(" c=1")
	if utf8.Valid(data) {
		s := string(data)
		if c < 0x80 {
			opt("f", strings.IndexByte(s, c))
		}
		if utf8.Valid(needle) {
			t := string(needle)
			fmt.Fprintf(&b, " s=%d e=%d", core11Bit(strings.HasPrefix(s, t)), core11Bit(strings.HasSuffix(s, t)))
			opt("n", strings.Index(s, t))
		}
	}
	return in, b.String()
}

func core11Bit(b bool) int {
	if b {
		return 1
	}
	return 0
}

// core11BytesBuild builds the driver --release over a copy of the live std
// in dir and returns it, or skips when the toolchain is missing. It takes a
// testing.TB so the fuzz target builds before f.Fuzz, once per process,
// outside any input's run.
func core11BytesBuild(t testing.TB, dir string) string {
	t.Helper()
	return core11BytesBuildWith(t, dir, nil, "--release")
}

// core11BytesBuildWith is core11BytesBuild over a std with fixtures fx
// (sdFixtures), built with flags.
func core11BytesBuildWith(t testing.TB, dir string, fx map[string]string, flags ...string) string {
	t.Helper()
	stdDir := copyStd(t, dir, fx)
	if err := os.WriteFile(filepath.Join(dir, "main.mk"), []byte(core11BytesDriver), 0o644); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "main")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	args := append(append([]string{"build"}, flags...), "-o", exe, "main.mk")
	out, status := mkCmd(t, dir, stdDir, args...)
	if status != 0 {
		if strings.Contains(out, "no usable LLVM") {
			t.Skipf("needs the LLVM toolchain: %s", out)
		}
		t.Fatalf("mk build: exit %d\n%s", status, out)
	}
	return exe
}

// core11Text is random bytes that are mostly UTF-8: whole scalars of every
// length, with a stray byte (a continuation, a C0, C1 or F5 to FF, a
// surrogate's lead) one time in k.
func core11Text(r *rand.Rand, n, k int) []byte {
	var b []byte
	for len(b) < n {
		if k > 0 && r.Intn(k) == 0 {
			bad := [][]byte{{0x80}, {0xBF}, {0xC0, 0x80}, {0xC1}, {0xF5}, {0xFF}, {0xED, 0xA0, 0x80}, {0xE0, 0x80}, {0xF4, 0x90, 0x80, 0x80}, {0xE2, 0x82}}
			b = append(b, bad[r.Intn(len(bad))]...)
			continue
		}
		var v rune
		switch r.Intn(5) {
		case 0, 1:
			v = rune('a' + r.Intn(4))
		case 2:
			v = rune(0x80 + r.Intn(0x780))
		case 3:
			v = rune(0x800 + r.Intn(0xF800))
			if v >= 0xD800 && v <= 0xDFFF {
				v = 0xE000
			}
		default:
			v = rune(0x10000 + r.Intn(0x100000))
		}
		b = utf8.AppendRune(b, v)
	}
	return b[:n:n]
}

// core11Clustered is n bytes of ASCII holding one run of 4 to 16 non-ASCII
// scalars, packed or up to three ASCII bytes apart, at a random place,
// followed by ASCII or by two-byte scalars, cut at n (which may split a
// scalar), and half the time with a stray byte after the run's start: the
// shapes an earlier density gate in ValidUtf8 keyed on, since removed, kept
// so a hand-off after a run below 256 bytes stays checked.
func core11Clustered(r *rand.Rand, n int) []byte {
	b := make([]byte, 0, n+64)
	at := r.Intn(n/2 + 1)
	for len(b) < at {
		b = append(b, byte('a'+r.Intn(26)))
	}
	k := 4 + r.Intn(13)
	sep := r.Intn(4)
	for j := 0; j < k; j++ {
		var v rune
		switch r.Intn(3) {
		case 0:
			v = rune(0x80 + r.Intn(0x780))
		case 1:
			v = rune(0x800 + r.Intn(0xF800))
			if v >= 0xD800 && v <= 0xDFFF {
				v = 0xE000
			}
		default:
			v = rune(0x10000 + r.Intn(0x100000))
		}
		b = utf8.AppendRune(b, v)
		for s := 0; s < sep; s++ {
			b = append(b, 'x')
		}
	}
	dense := r.Intn(2) == 0
	for len(b) < n {
		if dense {
			b = append(b, 0xD0, 0xB0)
		} else {
			b = append(b, 'y')
		}
	}
	b = b[:n:n]
	if r.Intn(2) == 0 && n > at {
		bad := []byte{0x80, 0xBF, 0xC0, 0xC1, 0xE0, 0xED, 0xF4, 0xF5, 0xFF}
		b[at+r.Intn(n-at)] = bad[r.Intn(len(bad))]
	}
	return b
}

// TestCore_BytePathsMatchScalar runs 5,000 cases, lengths 0 to 700 on
// both sides of the vector widths and of the 256-byte dispatch, in one run
// of the release driver; the last 1,000 are core11Clustered texts of 64 to
// 255 bytes. The same cases run again, at dev and release, over a std
// whose freestanding forks are forced to std.core's own forms
// (sdCoreFree), so the forms x86_64-freestanding runs
// are checked natively on this target.
func TestCore_BytePathsMatchScalar(t *testing.T) {
	t.Parallel()
	in, want := core11Cases()
	free := sdFixtures(t, sdCoreFree)
	for _, c := range []struct {
		name  string
		fx    map[string]string
		flags []string
	}{
		{"hosted/release", nil, []string{"--release"}},
		{"coreforms/dev", free, nil},
		{"coreforms/release", free, []string{"--release"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			core11Compare(t, core11BytesBuildWith(t, t.TempDir(), c.fx, c.flags...), in, want)
		})
	}
}

// core11Cases is TestCore_BytePathsMatchScalar's input and Go's answers.
func core11Cases() (string, string) {
	r := rand.New(rand.NewSource(1124))
	var in, want strings.Builder
	for i := 0; i < 5000; i++ {
		n := r.Intn(700)
		if i < 200 {
			n = i
		}
		data := core11Text(r, n, []int{0, 3, 50, 1000}[i%4])
		if i >= 4000 {
			n = 64 + r.Intn(192)
			data = core11Clustered(r, n)
		}
		var c byte = 'a' + byte(r.Intn(6))
		if i%7 == 0 && n > 0 {
			c = data[r.Intn(n)]
		}
		from := r.Intn(n + 1)
		m := r.Intn(40)
		if m > n {
			m = n
		}
		at := r.Intn(n - m + 1)
		needle := append([]byte(nil), data[at:at+m]...)
		if i%3 == 0 && m > 0 {
			needle[r.Intn(m)] ^= byte(1 + r.Intn(127))
		}
		if i%5 == 0 {
			// A needle that is the data's own head or tail.
			if i%10 == 0 {
				needle = append([]byte(nil), data[:m]...)
			} else {
				needle = append([]byte(nil), data[n-m:]...)
			}
		}
		ci, cw := core11BytesCase(data, c, from, needle)
		in.WriteString(ci)
		want.WriteString(cw + "\n")
	}
	return in.String(), want.String()
}

// core11Compare runs the driver exe over in and compares each line with
// want's.
func core11Compare(t *testing.T, exe, in, want string) {
	t.Helper()
	got := runNative(t, exe, in)
	if got.code != 0 {
		t.Fatalf("driver exit %d\n%s", got.code, got.stderr)
	}
	gl := strings.Split(strings.ReplaceAll(got.stdout, "\r\n", "\n"), "\n")
	wl := strings.Split(want, "\n")
	if len(gl) != len(wl) {
		t.Fatalf("driver wrote %d lines, want %d", len(gl), len(wl))
	}
	bad := 0
	for i := range wl {
		if gl[i] != wl[i] {
			if bad < 5 {
				t.Errorf("case %d: got %q, want %q", i, gl[i], wl[i])
			}
			bad++
		}
	}
	if bad > 0 {
		t.Errorf("%d of %d cases differ", bad, len(wl)-1)
	}
}

func FuzzCore_BytePaths(f *testing.F) {
	r := rand.New(rand.NewSource(24))
	for i := 0; i < 24; i++ {
		n := []int{0, 1, 7, 15, 16, 17, 31, 32, 63, 64, 65, 255, 256, 257, 300, 1000}[i%16]
		data := core11Text(r, n, []int{0, 4, 40}[i%3])
		f.Add(data, byte('a'+i%4), uint16(i%17), []byte("ab"))
	}
	f.Add([]byte("\xed\xa0\x80"), byte(0xed), uint16(0), []byte("\xed"))
	f.Add([]byte("abababababababababababababababababababc"), byte('c'), uint16(3), []byte("ababc"))
	f.Add([]byte(strings.Repeat("\u20ac", 40)+"\xe2\x82"), byte(0x82), uint16(5), []byte("\u20ac\u20ac"))
	dir, err := os.MkdirTemp("", "mk-core11-fuzz")
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { os.RemoveAll(dir) })
	exe := core11BytesBuild(f, dir)
	f.Fuzz(func(t *testing.T, data []byte, c byte, from uint16, needle []byte) {
		if len(data) > 1<<15 {
			data = data[:1<<15]
		}
		if len(needle) > 1<<10 {
			needle = needle[:1<<10]
		}
		in, want := core11BytesCase(data, c, int(from), needle)
		got := runNative(t, exe, in)
		if got.code != 0 {
			t.Fatalf("driver exit %d on %q\n%s", got.code, in, got.stderr)
		}
		if g := strings.TrimSuffix(strings.ReplaceAll(got.stdout, "\r\n", "\n"), "\n"); g != want {
			t.Fatalf("%q:\ngot  %s\nwant %s", in, g, want)
		}
	})
}

// core11Body is the text of the Mk function whose declaration line starts
// with head in std/core/<file>, up to the closing brace at column 0.
func core11Body(t *testing.T, file, head string) string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("core", file))
	if err != nil {
		t.Fatal(err)
	}
	s := strings.ReplaceAll(string(src), "\r\n", "\n")
	i := strings.Index(s, "\n"+head)
	if i < 0 {
		t.Fatalf("std/core/%s has no %q", file, head)
	}
	body := s[i+1:]
	if j := strings.Index(body, "\n}\n"); j >= 0 {
		body = body[:j+2]
	}
	return body
}

// core11ValidUtf8Fork is the head of ValidUtf8's body: the hosted fork that
// hands a text of 256 bytes or more to std.simd.
const core11ValidUtf8Fork = "ValidUtf8(b: []u8): bool {\n    n := b.Len()\n    if Target.os != Os.Freestanding {\n        if n >= 256 {\n            return simd.ValidUtf8(b)\n        }\n    }\n"

// core11CallRe matches a call's callee in Mk text.
var core11CallRe = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_.]*)\(`)

// core11ValidUtf8HandOff reports why body is not ValidUtf8's ruled form,
// or nil: it opens with core11ValidUtf8Fork, names std.simd once (that
// fork's call), and past the fork calls only LoadWord and raw.Ctz64, so no
// gate or helper hands a text below 256 bytes, or the rest of one, to
// std.simd.
func core11ValidUtf8HandOff(body string) error {
	rest, ok := strings.CutPrefix(body, core11ValidUtf8Fork)
	if !ok {
		return fmt.Errorf("ValidUtf8 does not open with the hosted fork at 256 bytes:\n%s", core11ValidUtf8Fork)
	}
	if n := strings.Count(body, "simd."); n != 1 {
		return fmt.Errorf("ValidUtf8 names std.simd %d times, want once, in its fork", n)
	}
	for _, m := range core11CallRe.FindAllStringSubmatch(rest, -1) {
		if m[1] != "LoadWord" && m[1] != "raw.Ctz64" {
			return fmt.Errorf("ValidUtf8 calls %s past its fork: a hand-off other than the one at 256 bytes", m[1])
		}
	}
	return nil
}

// core11G9n is the g9n mutant of ValidUtf8: a density gate of
// k = 9 behind its own hosted fork, which every fuel guard, every block and
// the freestanding disassembly let through.
var core11G9n = [][2]string{
	{"        i += need + 1\n", "        i += need + 1\n        seen +%= 1\n        if Target.os != Os.Freestanding {\n            if seen >= 9 {\n                if n - i >= 64 {\n                    if seen << 2 >= i {\n                        if LoadWord(b, i) & 0x8080808080808080 != 0 {\n                            return simd.ValidUtf8(b[i..])\n                        }\n                    }\n                }\n            }\n        }\n"},
	{"    var i := 0\n    for i < n {", "    var i := 0\n    var seen := 0\n    for i < n {"},
}

// TestCore_BytePathsCallSimd fails when std.core's byte paths stop calling
// std.simd or CopyFrom stops being one raw.Move, which no differential test
// can show, since the SWAR forms give
// the same answers. Each of std.core's three byte paths calls its std.simd
// routine beside its short SWAR path, under a fork that keeps std.core's
// own forms on x86_64-freestanding, and CopyFrom
// is one raw.Move with no loop. ValidUtf8's one hand-off is the fork at 256
// bytes (core11ValidUtf8HandOff), checked to catch g9n and the thresholds
// 257, 512 and 1,024. Two dev builds of the same calls, disassembled: for
// x86_64-linux the three call mk.std.simd's ValidUtf8, IndexByte and Equal;
// for x86_64-freestanding none of the three calls into std.simd, and each
// CopyFrom instance calls memmove and has no back edge.
func TestCore_BytePathsCallSimd(t *testing.T) {
	t.Parallel()
	utf8Body := core11Body(t, "core_utf8.mk", "ValidUtf8(b: []u8): bool {")
	if err := core11ValidUtf8HandOff(utf8Body); err != nil {
		t.Errorf("std/core/core_utf8.mk: %v\n%s", err, utf8Body)
	}
	g9n := utf8Body
	for _, r := range core11G9n {
		if strings.Count(g9n, r[0]) != 1 {
			t.Fatalf("std/core/core_utf8.mk: ValidUtf8 holds %q %d times, want once: update core11G9n", r[0], strings.Count(g9n, r[0]))
		}
		g9n = strings.Replace(g9n, r[0], r[1], 1)
	}
	mutants := map[string]string{"g9n": g9n}
	for _, th := range []string{"257", "512", "1024"} {
		mutants["t"+th] = strings.Replace(utf8Body, "        if n >= 256 {\n", "        if n >= "+th+" {\n", 1)
	}
	for name, body := range mutants {
		if body == utf8Body || core11ValidUtf8HandOff(body) == nil {
			t.Errorf("core11ValidUtf8HandOff lets the %s mutant of ValidUtf8 through", name)
		}
	}
	for _, c := range []struct{ file, head, call string }{
		{"core_utf8.mk", "ValidUtf8(b: []u8): bool {", "simd.ValidUtf8("},
		{"core_iter.mk", "export IndexByteIn(", "simd.IndexByte("},
		{"core_str.mk", "export BytesEqual(", "simd.Equal("},
		{"core_slice.mk", "export Slice[T].CopyFrom(", "raw.Move("},
	} {
		body := core11Body(t, c.file, c.head)
		if !strings.Contains(body, c.call) {
			t.Errorf("std/core/%s: %s calls no %s\n%s", c.file, c.head, c.call, body)
		}
		if c.call == "raw.Move(" && strings.Contains(body, "for ") {
			t.Errorf("std/core/%s: CopyFrom has a loop beside raw.Move\n%s", c.file, body)
		}
		if c.call != "raw.Move(" && !strings.Contains(body, "if Target.os != Os.Freestanding {") {
			t.Errorf("std/core/%s: %s has no freestanding fork\n%s", c.file, c.head, body)
		}
	}
	objdump := cryptoObjdump()
	if objdump == "" {
		t.Skip("needs llvm-objdump (LLVM 21)")
	}
	disasm := func(target, src string) (map[string][]string, map[string]uint64, map[string]bool) {
		root := t.TempDir()
		stdDir := copyStd(t, root, nil)
		if err := os.WriteFile(filepath.Join(root, "main.mk"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		exe := filepath.Join(root, "main.elf")
		out, st := mkCmd(t, root, stdDir, "build", "--target", target, "-o", exe, "main.mk")
		if st != 0 {
			if strings.Contains(out, "no usable LLVM") {
				t.Skipf("needs the LLVM toolchain: %s", out)
			}
			t.Fatalf("mk build --target %s: exit %d\n%s", target, st, out)
		}
		dis, err := exec.Command(objdump, "-d", "--no-show-raw-insn", exe).Output()
		if err != nil {
			t.Fatalf("%s -d: %v", objdump, err)
		}
		calls := map[string][]string{}
		start := map[string]uint64{}
		back := map[string]bool{}
		for _, x := range ctParse(string(dis)) {
			if _, ok := start[x.fn]; !ok {
				start[x.fn] = x.addr
			}
			if strings.HasPrefix(x.op, "call") {
				calls[x.fn] = append(calls[x.fn], x.args)
			}
			if strings.HasPrefix(x.op, "j") {
				f := strings.Fields(x.args)
				if len(f) > 0 {
					if to, err := strconv.ParseUint(strings.TrimPrefix(f[0], "0x"), 16, 64); err == nil && to <= x.addr && to >= start[x.fn] {
						back[x.fn] = true
					}
				}
			}
		}
		return calls, start, back
	}
	paths := map[string]string{
		"mk.std.core.ValidUtf8":   "<mk.std.simd.ValidUtf8",
		"mk.std.core.IndexByteIn": "<mk.std.simd.IndexByte",
		"mk.std.core.BytesEqual":  "<mk.std.simd.Equal",
	}
	hostedSrc := strings.Replace(sdFreeCoreProgram, "export extern MkEntry() {", "export Main(sys: &System) {", 1)
	hosted, _, _ := disasm("x86_64-linux", hostedSrc)
	for fn, callee := range paths {
		cs, ok := hosted[fn]
		if !ok {
			t.Errorf("x86_64-linux: %s: no such function, or it calls nothing", fn)
			continue
		}
		if !slices.ContainsFunc(cs, func(a string) bool { return strings.Contains(a, callee) }) {
			t.Errorf("x86_64-linux: %s calls no %s>: %q", fn, callee, cs)
		}
	}
	calls, start, back := disasm("x86_64-freestanding", sdFreeCoreProgram)
	for fn := range paths {
		if _, ok := start[fn]; !ok {
			t.Errorf("%s: no such function in the freestanding build", fn)
			continue
		}
		if slices.ContainsFunc(calls[fn], func(a string) bool { return strings.Contains(a, "<mk.std.simd.") }) {
			t.Errorf("%s calls into std.simd on x86_64-freestanding, where std.core keeps its own forms: %q", fn, calls[fn])
		}
	}
	copies := 0
	for fn := range start {
		if !strings.HasPrefix(fn, "mk.std.core.Slice:CopyFrom") {
			continue
		}
		copies++
		if !slices.ContainsFunc(calls[fn], func(a string) bool { return strings.Contains(a, "<memmove>") }) {
			t.Errorf("%s calls no memmove: %q", fn, calls[fn])
		}
		if back[fn] {
			t.Errorf("%s has a back edge: a loop beside the move", fn)
		}
	}
	if copies < 2 {
		t.Errorf("%d CopyFrom instances, want those of u8 and u64", copies)
	}
}

// core11Fill is unit repeated while it fits in n bytes, then the ASCII
// byte a up to n bytes.
func core11Fill(unit string, n int) string {
	var b strings.Builder
	for b.Len()+len(unit) <= n {
		b.WriteString(unit)
	}
	b.WriteString(strings.Repeat("a", n-b.Len()))
	return b.String()
}

// core11Witness is the witness program over text: one str.From
// at compile time, with the const's fuel clause fuel (empty: the default).
func core11Witness(text, fuel string) string {
	return `const S: str = "` + text + `"

F(): int {
    _ = str.From(S.AsBytes()) else {
        return -1
    }
    return S.Len()
}

const K: int = F()` + fuel + `

export Main(sys: &System): WriteError!void {
    var out := sys.Stdout()
    try Print(mut out, "K={k}\n", (k: K))
    try out.Flush()
}
`
}

// core11WitnessNeed is each witness's need on a hosted target with this std,
// the least fuel at which mk check holds its const.
const core11WitnessNeed = 4_354_281

// TestCore_CompileTimeWitnesses pins the witnesses wit-fr and wit-p64, a
// known and accepted cost of the std.simd form: one str.From at compile
// time over 200,000 bytes of French prose, and over ASCII with one é every
// 64 bytes. From 256 bytes a hosted target's ValidUtf8 is std.simd's, about
// 21.8 steps per byte on such text in the evaluator, so each needs
// core11WitnessNeed, 4,354,281 steps (v1.2.0: 2,489,325 and 2,031,352;
// measured 2026-11-16), past the default 4 million. The const states a
// fuel, 5,500,000, which passes and also bounds the cost from above, and the
// need is checked exact: clean at it, E0902 one below. On
// x86_64-freestanding, where std.core keeps its own form, both fit the
// default fuel, as on v1.2.0.
func TestCore_CompileTimeWitnesses(t *testing.T) {
	t.Parallel()
	for _, w := range []struct{ name, unit string }{
		{"wit-fr", "Le cœur a ses raisons que la raison ne connaît point. "},
		{"wit-p64", strings.Repeat("a", 62) + "é"},
	} {
		t.Run(w.name, func(t *testing.T) {
			t.Parallel()
			text := core11Fill(w.unit, 200_000)
			root := t.TempDir()
			stdDir := copyStd(t, root, nil)
			for _, c := range []struct {
				target, fuel string
				clean        bool
			}{
				{"", " fuel 5_500_000", true},
				{"", fmt.Sprintf(" fuel %d", core11WitnessNeed), true},
				{"", fmt.Sprintf(" fuel %d", core11WitnessNeed-1), false},
				{"x86_64-freestanding", "", true},
			} {
				if err := os.WriteFile(filepath.Join(root, "main.mk"), []byte(core11Witness(text, c.fuel)), 0o644); err != nil {
					t.Fatal(err)
				}
				args := []string{"check"}
				if c.target != "" {
					args = append(args, "--target", c.target)
				}
				out, st := mkCmd(t, root, stdDir, append(args, "main.mk")...)
				if c.clean && st != 0 {
					t.Errorf("mk %s (const K: int = F()%s): exit %d\n%.2000s", strings.Join(args, " "), c.fuel, st, out)
				}
				if !c.clean && (st == 0 || !strings.Contains(out, "E0902")) {
					t.Errorf("mk %s (const K: int = F()%s): exit %d, want E0902, so the witness needs fewer steps than core11WitnessNeed\n%.2000s", strings.Join(args, " "), c.fuel, st, out)
				}
			}
		})
	}
}
