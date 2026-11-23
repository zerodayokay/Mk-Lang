// Why: the app table (tests/bench/README.md "Applications") has row fields,
// output checks and memory readings no other table has, each of which can
// admit a wrong cell.
// What: TestAppRowFields to TestRenderAppRow; findings_appcontract_test.go
// checks a program's own files.
// How: the test binary stands in for most programs (BENCH_HELPER_APP modes
// below); TestAppCargoBuild builds a real crate.

package main

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"testing"
)

// appHelper is the test binary standing in for an app row's program
// (TestMain, BENCH_HELPER_APP=<mode>):
//   - "zlib IN OUT": refuses (exit 3) when OUT exists already, so a stale
//     file is caught; writes IN compressed by compress/zlib to OUT; prints
//     "wrote OUT" and MK_CARRIERS, with CR LF line ends;
//   - "bad IN OUT": writes bytes that are no zlib stream;
//   - "cat FILE", or "cat-then FILE" in any mode: prints the file;
//   - "size N": allocates and touches N MiB, then prints "ok"; "size N hwm"
//     then prints "hwm <KiB>", its own VmHWM read just before it exits, and
//     "size N hwmfree" gives the N MiB back to the system first
//     (TestSpawnPeakLinux);
//   - "arch IN OUT": writes IN compressed by compress/zlib to OUT, at the
//     best speed, or at the best compression when the executable's name
//     starts with "c", so two columns write different files; when it starts
//     with "odd", every byte of the stream is XORed with 0x5a, a format no
//     other column reads;
//   - "unarch-then FILE" in any mode: reads FILE back the same way (the
//     XOR undone by an "odd" executable only) and prints the decoded bytes.
//     An executable whose name starts with "cstrict" reads only a
//     best-compression stream (zlib FLEVEL 3, header byte 0xDA), a reader
//     stricter than the others; one whose name starts with "oddloose" reads
//     a file that is no XORed stream as a plain one and prints its text in
//     upper case, a reader that exits 0 and prints the wrong bytes.
func appHelper(mode string, args []string) int {
	if len(args) > 0 && args[0] == "cat-then" {
		mode, args = "cat", args[1:]
	}
	if len(args) > 0 && args[0] == "unarch-then" {
		mode, args = "unarch", args[1:]
	}
	odd := strings.HasPrefix(filepath.Base(os.Args[0]), "odd")
	flip := func(b []byte) {
		if odd {
			for i := range b {
				b[i] ^= 0x5a
			}
		}
	}
	switch mode {
	case "arch":
		in, err := os.ReadFile(args[0])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 4
		}
		level := zlib.BestSpeed
		if strings.HasPrefix(filepath.Base(os.Args[0]), "c") {
			level = zlib.BestCompression
		}
		var b bytes.Buffer
		zw, _ := zlib.NewWriterLevel(&b, level)
		zw.Write(in)
		zw.Close()
		out := b.Bytes()
		flip(out)
		if err := os.WriteFile(args[1], out, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 4
		}
	case "unarch":
		b, err := os.ReadFile(args[0])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 4
		}
		raw := bytes.Clone(b)
		flip(b)
		if strings.HasPrefix(filepath.Base(os.Args[0]), "cstrict") && (len(b) < 2 || b[1] != 0xDA) {
			fmt.Fprintln(os.Stderr, "not an archive: a strict reader")
			return 5
		}
		dec, err := unzlib(b)
		if err != nil && strings.HasPrefix(filepath.Base(os.Args[0]), "oddloose") {
			if dec, err = unzlib(raw); err == nil {
				dec = bytes.ToUpper(dec)
			}
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "not an archive:", err)
			return 5
		}
		os.Stdout.Write(dec)
	case "zlib", "bad":
		if _, err := os.Stat(args[1]); err == nil {
			fmt.Fprintln(os.Stderr, "stale output", args[1])
			return 3
		}
		in, err := os.ReadFile(args[0])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 4
		}
		var b bytes.Buffer
		if mode == "zlib" {
			zw := zlib.NewWriter(&b)
			zw.Write(in)
			zw.Close()
		} else {
			b.WriteString("not zlib")
		}
		if err := os.WriteFile(args[1], b.Bytes(), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 4
		}
		fmt.Printf("wrote %s\r\ncarriers=%s\r\n", args[1], os.Getenv("MK_CARRIERS"))
	case "cat":
		b, err := os.ReadFile(args[0])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 4
		}
		os.Stdout.Write(b)
	case "size":
		var n int
		fmt.Sscan(args[0], &n)
		buf := make([]byte, n<<20)
		for i := 0; i < len(buf); i += 4096 {
			buf[i] = 1
		}
		fmt.Println("ok", buf[len(buf)/2])
		if len(args) > 1 {
			if args[1] == "hwmfree" {
				buf = nil
				debug.FreeOSMemory()
			}
			st, _ := os.ReadFile("/proc/self/status")
			for _, l := range strings.Split(string(st), "\n") {
				if v, ok := strings.CutPrefix(l, "VmHWM:"); ok {
					fmt.Println("hwm", strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "kB")))
				}
			}
		}
		runtime.KeepAlive(buf)
	default:
		return 2
	}
	return 0
}

// unzlib decodes one zlib stream.
func unzlib(b []byte) ([]byte, error) {
	zr, err := zlib.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	return io.ReadAll(zr)
}

// appRepo is a throw-away repository root (go.mod of module mk) holding
// files, for loadRow's path checks; MK_EVIDENCE points beside it.
func appRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module mk\n\ngo 1.27\n")
	for p, s := range files {
		writeFile(t, filepath.Join(root, filepath.FromSlash(p)), s)
	}
	t.Setenv("MK_EVIDENCE", filepath.Join(root, "evidence"))
	return root
}

const appPkg = "const Package = pkg.Package(name: \"d\", bins: [pkg.Bin(name: \"d\", root: \"src/main\", link_script: \"\", cpu: \"\", subsystem: \"console\")], unsafe_modules: [], links: [])\n"

func TestAppRowFields(t *testing.T) {
	root := appRepo(t, map[string]string{
		"Dogfoods/one/pkg.mk":             appPkg,
		"Dogfoods/one/src/main/main.mk":   "Main() {}\n",
		"Dogfoods/two/pkg.mk":             strings.Replace(appPkg, "bins: [", "bins: [pkg.Bin(name: \"x\", root: \"src/x\", link_script: \"\", cpu: \"\", subsystem: \"console\"), ", 1),
		"tests/bench/app/r/r.c":           "// r, a C twin\n",
		"tests/bench/app/r/r.zig":         "// r, a Zig twin\n",
		"tests/bench/app/r/mk/pkg.mk":     appPkg,
		"tests/bench/app/r/rs/Cargo.toml": appCrateToml,
	})
	dir := filepath.Join(root, "tests", "bench", "app", "r")
	load := func(body string) (*row, error) {
		p := filepath.Join(dir, "bench.json")
		writeFile(t, p, body)
		return loadRow(p, "")
	}
	const head = `"name": "r", "algorithm": "A task.", "args": ["{out}"], "quick": ["{out}"], `
	good := `{"table": "app", ` + head + `"package": {"path": "Dogfoods/one"}, "sources": {"c": "r.c", "zig": "r.zig"},
		"kinds": {"mk": "std", "c": "hand", "zig": "eco"}, "hand": {"c": "a CSV splitter, 40 lines"},
		"libs": {"zig": [{"name": "z", "version": "1.2.3", "lang": "Zig", "inside": "none"}]},
		"env": {"MK_CARRIERS": "1"}, "digest": true, "output": {"file": "o.bin", "check": "digest"}}`
	r, err := load(good)
	if err != nil {
		t.Fatalf("a well-formed app row: %v", err)
	}
	if got, want := filepath.Join(r.Dir, r.Sources["mk"]), filepath.Join(root, "Dogfoods", "one"); got != want {
		t.Fatalf("the package's Mk source is %s, want %s", got, want)
	}
	if len(rowColumns(r)) != 4 { // mk, c, zig-fast, zig-safe
		t.Fatalf("columns %v", rowColumns(r))
	}
	bad := map[string]string{
		"app field on another table": `{"table": "idiomatic", "name": "r", "sources": {"c": "r.c"}, "env": {"A": "1"}}`,
		"a source with no kind":      `{"table": "app", "name": "r", "sources": {"c": "r.c"}}`,
		"an Mk cell not std":         `{"table": "app", "name": "r", "sources": {"mk": "mk"}, "kinds": {"mk": "eco"}, "libs": {"mk": [{"name": "a", "version": "1", "lang": "Mk", "inside": "none"}]}}`,
		"eco with no libs":           `{"table": "app", "name": "r", "sources": {"c": "r.c"}, "kinds": {"c": "eco"}}`,
		"libs on a std cell":         `{"table": "app", "name": "r", "sources": {"c": "r.c"}, "kinds": {"c": "std"}, "libs": {"c": [{"name": "a", "version": "1", "lang": "C", "inside": "none"}]}}`,
		"a library half named":       `{"table": "app", "name": "r", "sources": {"c": "r.c"}, "kinds": {"c": "eco"}, "libs": {"c": [{"name": "a", "version": "1"}]}}`,
		"hand with no text":          `{"table": "app", "name": "r", "sources": {"c": "r.c"}, "kinds": {"c": "hand"}}`,
		"the tool kind":              `{"table": "app", "name": "r", "sources": {"c": "r.c"}, "kinds": {"c": "tool"}}`,
		"a kind for no source":       `{"table": "app", "name": "r", "sources": {"c": "r.c"}, "kinds": {"c": "std", "go": "std"}}`,
		"env GOGC":                   `{"table": "app", "name": "r", "sources": {"c": "r.c"}, "kinds": {"c": "std"}, "env": {"GOGC": "off"}}`,
		"env MK_TEST_":               `{"table": "app", "name": "r", "sources": {"c": "r.c"}, "kinds": {"c": "std"}, "env": {"MK_TEST_X": "1"}}`,
		"MK_CARRIERS on concurrency": `{"table": "app", "name": "r", "sources": {"c": "r.c"}, "kinds": {"c": "std"}, "env": {"MK_CARRIERS": "1"}, "concurrency": {"mask": "0xff", "threads": 8}}`,
		"{out} with no output":       `{"table": "app", "name": "r", "args": ["{out}"], "sources": {"c": "r.c"}, "kinds": {"c": "std"}}`,
		"an output never named":      `{"table": "app", "name": "r", "args": ["x"], "sources": {"c": "r.c"}, "kinds": {"c": "std"}, "output": {"file": "o", "check": "digest"}}`,
		"a round trip with no data":  `{"table": "app", "name": "r", "args": ["{out}"], "sources": {"c": "r.c"}, "kinds": {"c": "std"}, "output": {"file": "o", "check": "zlib"}}`,
		"an output path":             `{"table": "app", "name": "r", "args": ["{out}"], "sources": {"c": "r.c"}, "kinds": {"c": "std"}, "output": {"file": "a/o", "check": "digest"}}`,
		"an unknown check":           `{"table": "app", "name": "r", "args": ["{out}"], "sources": {"c": "r.c"}, "kinds": {"c": "std"}, "output": {"file": "o", "check": "size"}}`,
		"check then with no then":    `{"table": "app", "name": "r", "args": ["{out}"], "sources": {"c": "r.c"}, "kinds": {"c": "std"}, "output": {"file": "o", "check": "then"}}`,
		"a then that reads nothing":  `{"table": "app", "name": "r", "args": ["{out}"], "sources": {"c": "r.c"}, "kinds": {"c": "std"}, "output": {"file": "o", "check": "then", "then": ["version"]}}`,
		"labels on another table":    `{"table": "idiomatic", "name": "r", "sources": {"c": "r.c"}, "labels": {"c": "x"}}`,
		"a label for no source":      `{"table": "app", "name": "r", "sources": {"c": "r.c"}, "kinds": {"c": "std"}, "labels": {"go": "x"}}`,
		"an empty label":             `{"table": "app", "name": "r", "sources": {"c": "r.c"}, "kinds": {"c": "std"}, "labels": {"c": ""}}`,
		"package and sources.mk":     `{"table": "app", "name": "r", "package": {"path": "Dogfoods/one"}, "sources": {"mk": "mk"}, "kinds": {"mk": "std"}}`,
		"two bins, none named":       `{"table": "app", "name": "r", "package": {"path": "Dogfoods/two"}, "kinds": {"mk": "std"}}`,
		"a package path with ..":     `{"table": "app", "name": "r", "package": {"path": "Dogfoods/../Dogfoods/one"}, "kinds": {"mk": "std"}}`,
		"a missing package":          `{"table": "app", "name": "r", "package": {"path": "Dogfoods/none"}, "kinds": {"mk": "std"}}`,
		"counts on an app row":       `{"table": "app", "name": "r", "sources": {"c": "r.c"}, "kinds": {"c": "std"}, "counts": ["x"]}`,
		"a variant output elsewhere": `{"table": "idiomatic", "name": "r", "sources": {"c": "r.c"}, "variants": [{"name": "a", "output": {"file": "o", "check": "digest"}}]}`,
		"a crate on another table":   `{"table": "idiomatic", "name": "r", "sources": {"rust": "rs"}}`,
	}
	for what, body := range bad {
		if _, err := load(body); err == nil {
			t.Errorf("%s: loaded", what)
		}
	}
	// Two bins with one named.
	r, err = load(`{"table": "app", "name": "r", "package": {"path": "Dogfoods/two", "bin": "x"}, "kinds": {"mk": "std"}}`)
	if err != nil {
		t.Fatal(err)
	}
	tc := &toolchain{Mk: "mk.exe", Work: t.TempDir()}
	cmd, err := tc.command(r, columns[0], "out.exe")
	if err != nil || !slices.Contains(cmd.Args, "--bin") || cmd.Args[slices.Index(cmd.Args, "--bin")+1] != "x" {
		t.Fatalf("the bin is not passed: %v %v", cmd.Args, err)
	}
	// The package's commit is read from git (no repository here: empty,
	// and not an error).
	if f := packageFact(root, r.Package); f.Path != "Dogfoods/two" || f.Bin != "x" {
		t.Fatalf("package fact %+v", f)
	}
}

func TestAppRowEnv(t *testing.T) {
	t.Setenv("MK_CARRIERS", "16")
	r := &row{Name: "r", Table: "app", Env: map[string]string{"MK_CARRIERS": "1", "B_X": "y"}}
	env := rowEnv(r)
	if v, _ := envValue(env, "MK_CARRIERS"); v != "1" {
		t.Fatalf("MK_CARRIERS %q", v)
	}
	n := 0
	for _, kv := range env {
		if envKeyIs(kv, "MK_CARRIERS") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("MK_CARRIERS set %d times", n)
	}
	if v, _ := envValue(env, "GOMAXPROCS"); v != "1" {
		t.Fatalf("GOMAXPROCS %q: the row's env replaced a pinned variable", v)
	}
	// Rows of the other tables are unchanged.
	if v, ok := envValue(rowEnv(&row{Name: "s", Table: "same"}), "MK_CARRIERS"); ok {
		t.Fatalf("a same row's program sees MK_CARRIERS=%s", v)
	}
}

func TestAppNormalise(t *testing.T) {
	data := `V:\w\data\tree`
	out := `V:\w\bin\verify\r\out-mk\o.z`
	text := "a\r\nV:/w/data/tree/x.txt:3\r\n" + data + `\y.txt:1` + "\nwrote " + out + "\n"
	got := string(normalise([]byte(text), data, out))
	want := "a\n$DATA/x.txt:3\n$DATA\\y.txt:1\nwrote $OUT\n"
	if got != want {
		t.Fatalf("normalise:\n%q\nwant\n%q", got, want)
	}
}

func TestAppCheckColumn(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.txt")
	writeFile(t, in, strings.Repeat("the quick brown fox\n", 1000))
	r := &row{Name: "z", Table: "app", Digest: true, Env: map[string]string{"BENCH_HELPER_APP": "zlib", "MK_CARRIERS": "1"},
		Output: &outputSpec{File: "o.z", Check: "zlib"}, dataPath: in}
	work := t.TempDir()
	b1 := &built{Row: r, Col: columns[0], Exe: os.Args[0]}
	b2 := &built{Row: r, Col: columns[4], Exe: os.Args[0]}
	// The executables' directory decides the output paths: two columns,
	// two paths.
	b1.Exe = filepath.Join(work, "mk.exe")
	b2.Exe = filepath.Join(work, "c.exe")
	for _, b := range []*built{b1, b2} {
		copyExe(t, os.Args[0], b.Exe)
	}
	if outPath(b1) == outPath(b2) {
		t.Fatal("two columns share an output path")
	}
	// A stale output file from an earlier run is deleted first.
	writeFile(t, outPath(b1), "stale")
	res, sum, ok := verify([]*built{b1, b2}, []string{in, outArg}, "")
	if !ok {
		t.Fatalf("verify: %+v", res)
	}
	wantStdout := "wrote $OUT\ncarriers=1\n"
	decoded, _ := os.ReadFile(in)
	want := "sha256:" + sha256Hex([]byte(wantStdout)) + " roundtrip sha256:" + sha256Hex(decoded)
	if sum != want {
		t.Fatalf("checksum %q, want %q", sum, want)
	}
	for _, cr := range res {
		if cr.OutBytes == 0 || cr.OutBytes >= int64(len(decoded)) {
			t.Errorf("%s: output bytes %d", cr.B.Col.ID, cr.OutBytes)
		}
		// On Linux a reading at or under the spawner's floor is a bound, a
		// reading all the same: the raw figure and the floor are
		// recorded either way.
		if runtime.GOOS == "windows" && cr.Peak.RSS == 0 || runtime.GOOS == "linux" && (cr.Peak.Maxrss == 0 || cr.Peak.Floor == 0) {
			t.Errorf("%s: no peak memory read: %+v", cr.B.Col.ID, cr.Peak)
		}
		if runtime.GOOS == "windows" && cr.Peak.Commit == 0 {
			t.Errorf("%s: no peak commit read", cr.B.Col.ID)
		}
	}
	// A column whose output does not decode to the input fails its check.
	r.Env["BENCH_HELPER_APP"] = "bad"
	res, _, ok = verify([]*built{b1}, []string{in, outArg}, want)
	if ok || res[0].Err == nil || !strings.Contains(res[0].Err.Error(), "round trip") {
		t.Fatalf("a bad output passed: %+v", res[0])
	}
	// The digest check, and Then: the checksum's stdout part is the
	// second run's (the helper's "cat" of the output file).
	r.Env["BENCH_HELPER_APP"] = "zlib"
	r.Output = &outputSpec{File: "o.z", Check: "digest", Then: []string{"cat-then", outArg}}
	r.runThen = r.Output.Then
	res, sum, ok = verify([]*built{b1}, []string{in, outArg}, "")
	if !ok {
		t.Fatalf("then: %+v", res)
	}
	file, _ := os.ReadFile(outPath(b1))
	if want := "sha256:" + sha256Hex(normalise(file, in, outPath(b1))) + " file sha256:" + sha256Hex(file); sum != want {
		t.Fatalf("then: checksum %q, want %q", sum, want)
	}
	// Check "then": the file's bytes join nothing, and the
	// checksum is the Then run's output alone (TestAppCheckThenCross runs
	// two columns whose files differ).
	r.Output = &outputSpec{File: "o.z", Check: "then", Then: []string{"cat-then", outArg}}
	res, sum, ok = verify([]*built{b1}, []string{in, outArg}, "")
	if !ok {
		t.Fatalf("check then: %+v", res)
	}
	file, _ = os.ReadFile(outPath(b1))
	if want := "sha256:" + sha256Hex(normalise(file, in, outPath(b1))); sum != want {
		t.Fatalf("check then: checksum %q, want %q", sum, want)
	}
	if res[0].OutBytes != int64(len(file)) {
		t.Fatalf("check then: output size %d, want %d", res[0].OutBytes, len(file))
	}
}

// TestAppCheckThenCross is check "then"'s cross-check: two columns whose
// files differ agree when each one's Then run reads the other's file, and a
// column whose create and open are wrong in the same way, which passes
// alone, fails beside the others and is the one marked.
func TestAppCheckThenCross(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.txt")
	text := strings.Repeat("the quick brown fox jumps over the lazy dog\n", 2000)
	writeFile(t, in, text)
	r := &row{Name: "a", Table: "app", Digest: true, Env: map[string]string{"BENCH_HELPER_APP": "arch", "MK_CARRIERS": "1"},
		Output: &outputSpec{File: "o.a", Check: "then", Then: []string{"unarch-then", outArg}}, dataPath: in}
	work := t.TempDir()
	mk := func(col, exe string) *built {
		c, ok := columnByID(col)
		if !ok {
			t.Fatalf("no column %s", col)
		}
		b := &built{Row: r, Col: c, Exe: filepath.Join(work, exe)}
		copyExe(t, os.Args[0], b.Exe)
		return b
	}
	b1, b2, odd := mk("mk", "mk.exe"), mk("go", "c.exe"), mk("zig-fast", "odd.exe")
	args := []string{in, outArg}
	want := "sha256:" + sha256Hex([]byte(text))

	res, sum, ok := verify([]*built{b1, b2}, args, "")
	if !ok || sum != want {
		t.Fatalf("two columns: ok %v, checksum %q, want %q: %+v", ok, sum, want, res)
	}
	if s := crossLine(res); s != "cross-check: 2 of 2 pairs printed the checksum" {
		t.Fatalf("two columns: crossLine %q", s)
	}
	f1, _ := os.ReadFile(outPath(b1))
	f2, _ := os.ReadFile(outPath(b2))
	if bytes.Equal(f1, f2) {
		t.Fatal("the two columns wrote the same file, so the test shows nothing")
	}

	// Alone, the odd column agrees with itself.
	if res, sum, ok := verify([]*built{odd}, args, want); !ok || sum != want {
		t.Fatalf("odd alone: ok %v, checksum %q: %+v", ok, sum, res)
	}

	// Beside the others it fails, and only it is marked: by its file,
	// which neither other reads, and by its reads of theirs.
	res, sum, ok = verify([]*built{b1, b2, odd}, args, "")
	if ok || sum != want {
		t.Fatalf("three columns: ok %v, checksum %q", ok, sum)
	}
	if res[0].Err != nil || res[1].Err != nil {
		t.Fatalf("a good column was marked: %v; %v", res[0].Err, res[1].Err)
	}
	e := fmt.Sprint(res[2].Err)
	for _, w := range []string{"mk's then run does not read this column's o.a", "go's then run does not read this column's o.a", "this column's then run does not read mk's o.a", "not an archive"} {
		if !strings.Contains(e, w) {
			t.Errorf("odd's error has no %q: %s", w, e)
		}
	}

	// With a recorded checksum the same holds.
	if _, _, ok := verify([]*built{b1, b2, odd}, args, want); ok {
		t.Fatal("three columns passed against the recorded checksum")
	}

	// Two columns that cannot read each other are both marked.
	res, _, ok = verify([]*built{b1, odd}, args, want)
	if ok || res[0].Err == nil || res[1].Err == nil {
		t.Fatalf("a pair that cannot read each other: ok %v: %v; %v", ok, res[0].Err, res[1].Err)
	}
	if !strings.Contains(res[0].Err.Error(), "both marked: 0 of 0 other languages") {
		t.Errorf("the pair's error: %v", res[0].Err)
	}

	// A pair is judged by languages, not columns, since a language's two
	// columns build one source and would vote twice. Each case: the columns
	// (id and executable name), the ones that must be marked, and a text
	// each marked error must hold.
	for _, c := range []struct {
		name   string
		cols   [][2]string
		marked []string
		text   string
	}{
		// One language wrong the same way in both its columns, beside one
		// other language: nothing settles which is wrong, so all are
		// marked (with columns counted, mk alone was).
		{"zig pair beside mk", [][2]string{{"mk", "mk.exe"}, {"zig-fast", "odd.exe"}, {"zig-safe", "odd2.exe"}}, []string{"mk", "zig-fast", "zig-safe"}, "both marked: 0 of 0"},
		// Beside two other languages, the Zig pair alone (with columns
		// counted, all four were).
		{"zig pair beside mk and go", [][2]string{{"mk", "mk.exe"}, {"go", "c.exe"}, {"zig-fast", "odd.exe"}, {"zig-safe", "odd2.exe"}}, []string{"zig-fast", "zig-safe"}, "cross-check: "},
		// The row's six columns.
		{"zig pair, six columns", [][2]string{{"mk", "mk.exe"}, {"rust", "c.exe"}, {"rust-ovf", "c2.exe"}, {"go", "mk2.exe"}, {"zig-fast", "odd.exe"}, {"zig-safe", "odd2.exe"}}, []string{"zig-fast", "zig-safe"}, "cross-check: "},
		// A reader stricter than the others, one way: go refuses mk's
		// file, mk reads go's. With no other language both are marked
		// (with columns counted, mk alone was).
		{"strict reader, two columns", [][2]string{{"mk", "mk.exe"}, {"go", "cstrict.exe"}}, []string{"mk", "go"}, "both marked"},
		// With a third language that reads mk's file, go alone.
		{"strict reader, three columns", [][2]string{{"mk", "mk.exe"}, {"rust", "mk3.exe"}, {"go", "cstrict.exe"}}, []string{"go"}, "this column's then run does not read"},
		// A reader that exits 0 and prints other bytes is caught by
		// the printed checksum, and it alone is marked.
		{"loose reader", [][2]string{{"mk", "mk.exe"}, {"go", "c.exe"}, {"rust", "oddloose.exe"}}, []string{"rust"}, "printed sha256:"},
	} {
		work := t.TempDir()
		var bs []*built
		for _, col := range c.cols {
			cl, ok := columnByID(col[0])
			if !ok {
				t.Fatalf("no column %s", col[0])
			}
			b := &built{Row: r, Col: cl, Exe: filepath.Join(work, col[1])}
			copyExe(t, os.Args[0], b.Exe)
			bs = append(bs, b)
		}
		res, sum, ok := verify(bs, args, want)
		if ok || sum != want {
			t.Errorf("%s: ok %v, checksum %q", c.name, ok, sum)
		}
		var got []string
		for _, cr := range res {
			if cr.Err != nil {
				got = append(got, cr.B.Col.ID)
				if !strings.Contains(cr.Err.Error(), c.text) {
					t.Errorf("%s: %s's error has no %q: %v", c.name, cr.B.Col.ID, c.text, cr.Err)
				}
			}
		}
		if !slices.Equal(got, c.marked) {
			t.Errorf("%s: marked %v, want %v", c.name, got, c.marked)
		}
		n := len(c.cols)
		if s, w := crossLine(res), fmt.Sprintf(" of %d pairs printed the checksum", n*(n-1)); !strings.HasSuffix(s, w) {
			t.Errorf("%s: crossLine %q, want the suffix %q", c.name, s, w)
		}
	}

	// Another check runs no cross-check: a digest check over the same two
	// columns compares the files, which differ.
	r.Output = &outputSpec{File: "o.a", Check: "digest"}
	if _, _, ok := verify([]*built{b1, b2}, args, ""); ok {
		t.Fatal("a digest check passed two different files")
	}

	if s := outputCheckText(&outputSpec{Check: "then"}); !strings.Contains(s, "every other column's then run on this column's file") || !strings.Contains(s, "its size is printed") {
		t.Fatalf("outputCheckText(then) = %q", s)
	}
}

// copyExe copies the test binary, so that each column has its own path.
func copyExe(t *testing.T, from, to string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copyVendorFile(from, to); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(to, 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestAppPrepare(t *testing.T) {
	p := `V:\w\bin\publish\r\out-mk\o.z`
	if got, _ := prepareCmd(p, "windows"); got != `if exist "`+p+`" del /f /q "`+p+`"` {
		t.Fatalf("windows prepare %q", got)
	}
	if got, _ := prepareCmd("/tmp/w/o.z", "linux"); got != "rm -f '/tmp/w/o.z'" {
		t.Fatalf("linux prepare %q", got)
	}
	for _, good := range []string{`C:\Users\Jane Doe\o.z`, `V:\a&b (c)\o.z`} {
		if _, err := prepareCmd(good, "windows"); err != nil {
			t.Errorf("prepare refused %q: %v", good, err)
		}
	}
	for _, bad := range []string{`V:\a%x%\o`, `V:\a"b\o`, "/w/it's/o", "/w/a\nb/o"} {
		if _, err := prepareCmd(bad, "windows"); err == nil {
			t.Errorf("prepare accepted %q", bad)
		}
	}
	cmd := hyperfineCmd("hyperfine", "x.exe", []string{"a"}, hfOptions{Warmup: 3, Runs: 10, Prepare: "rm -f o"}, "o.json")
	if i := slices.Index(cmd.Args, "--prepare"); i < 0 || cmd.Args[i+1] != "rm -f o" {
		t.Fatalf("hyperfine without --prepare: %q", cmd.Args)
	}
	if cmd := hyperfineCmd("hyperfine", "x.exe", nil, hfOptions{Warmup: 3, Runs: 10}, "o.json"); slices.Contains(cmd.Args, "--prepare") {
		t.Fatalf("a row with no output got --prepare: %q", cmd.Args)
	}
	// Through the platform shell hyperfine runs it in (cmd.exe on Windows,
	// sh elsewhere): the file goes, in a directory whose name holds a space
	// and characters both shells read, and a missing file is no failure.
	f := filepath.Join(t.TempDir(), "a b (c)&d;e", "o.z")
	writeFile(t, f, "old")
	prep, err := prepareCmd(f, runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	// os/exec writes an argument's quotes as \", which cmd.exe does not
	// read, so on Windows the line goes through a batch file instead.
	bat := filepath.Join(t.TempDir(), "prep.cmd")
	writeFile(t, bat, "@"+prep+"\r\n")
	shell := func() *exec.Cmd {
		if runtime.GOOS == "windows" {
			return exec.Command("cmd.exe", "/C", bat)
		}
		return exec.Command("sh", "-c", prep)
	}
	for i := range 2 {
		if out, err := shell().CombinedOutput(); err != nil {
			t.Fatalf("prepare run %d: %v %s", i, err, out)
		}
		if _, err := os.Stat(f); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("prepare run %d left the file: %v", i, err)
		}
	}
	// The prepare never reaches results.json.
	b, _ := json.Marshal(hfOptions{Warmup: 3, Runs: 10, Prepare: "x"})
	if string(b) != `{"Warmup":3,"Runs":10}` {
		t.Fatalf("hfOptions JSON %s", b)
	}
}

func TestAppPeakMemory(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skip("peak memory is read on Windows and Linux only")
	}
	env := append(runEnv(os.Environ()), "BENCH_HELPER_APP=size")
	// The small program is 16 MiB, so on Linux it clears the fallback
	// spawner's floor (the test binary's own peak) by a wide margin.
	_, _, small, err := runProgram(os.Args[0], []string{"16"}, env)
	if err != nil {
		t.Fatal(err)
	}
	_, _, big, err := runProgram(os.Args[0], []string{"256"}, env)
	if err != nil {
		t.Fatal(err)
	}
	if small.RSS == 0 || big.RSS < small.RSS+200<<20 {
		t.Fatalf("peak RSS 16 MiB program %d, 256 MiB program %d", small.RSS, big.RSS)
	}
	// A failing program is an error, with no peak.
	if _, _, _, err := runProgram(os.Args[0], nil, append(runEnv(os.Environ()), "BENCH_HELPER_APP=nosuchmode")); err == nil {
		t.Fatal("a failing program passed")
	}
}

// appCrate writes a crate into dir.
func appCrate(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for p, s := range files {
		writeFile(t, filepath.Join(dir, filepath.FromSlash(p)), s)
	}
}

const (
	appCrateToml = "[package]\nname = \"probe\"\nversion = \"0.1.0\"\nedition = \"2024\"\n\n[dependencies]\ncfg-if = \"=1.0.0\"\n"
	appCrateLock = "# This file is automatically @generated by Cargo.\n# It is not intended for manual editing.\nversion = 4\n\n[[package]]\nname = \"cfg-if\"\nversion = \"1.0.0\"\nsource = \"registry+https://github.com/rust-lang/crates.io-index\"\nchecksum = \"baf1de4339761588bc0619e3cbc0120ee582ebb74b53b4efbf79117bd2da40fd\"\n\n[[package]]\nname = \"probe\"\nversion = \"0.1.0\"\ndependencies = [\n \"cfg-if\",\n]\n"
	appCrateMain = "// probe, an app row's crate.\ncfg_if::cfg_if! { if #[cfg(windows)] { fn os() -> &'static str { \"windows\" } } else { fn os() -> &'static str { \"other\" } } }\nfn main() { println!(\"{} {}\", os(), (1..=10u64).sum::<u64>()); }\n"
)

func TestAppCargo(t *testing.T) {
	dir := t.TempDir()
	// The crate's command refuses a cargo configuration in CARGO_HOME
	// so the machine's own is kept out.
	t.Setenv("CARGO_HOME", t.TempDir())
	n := 0
	mkRow := func(files map[string]string, libs []lib) *row {
		n++
		d := filepath.Join(dir, fmt.Sprint(n))
		appCrate(t, filepath.Join(d, "rs"), files)
		kind := "std"
		if len(libs) > 0 {
			kind = "eco"
		}
		return &row{Name: "r", Table: "app", Dir: d, Sources: map[string]string{"rust": "rs"}, Kinds: map[string]string{"rust": kind}, Libs: map[string][]lib{"rust": libs}}
	}
	cfgIf := []lib{{Name: "cfg-if", Version: "1.0.0", Lang: "Rust", Inside: "none"}}
	ok := map[string]string{"Cargo.toml": appCrateToml, "Cargo.lock": appCrateLock, "src/main.rs": appCrateMain}
	if err := checkCargoSource(mkRow(ok, cfgIf)); err != nil {
		t.Fatalf("a well-formed crate: %v", err)
	}
	with := func(k, v string) map[string]string {
		m := maps.Clone(ok)
		if v == "" {
			delete(m, k)
		} else {
			m[k] = v
		}
		return m
	}
	for what, c := range map[string]struct {
		files map[string]string
		libs  []lib
	}{
		"no lockfile":           {with("Cargo.lock", ""), cfgIf},
		"a profile of its own":  {with("Cargo.toml", appCrateToml+"\n[profile.release]\nlto = false\n"), cfgIf},
		"a build script":        {with("build.rs", "fn main() {}\n"), cfgIf},
		"a cargo configuration": {with(".cargo/config.toml", "[build]\n"), cfgIf},
		"a toolchain file":      {with("rust-toolchain.toml", "[toolchain]\n"), cfgIf},
		"a [[bin]] section":     {with("Cargo.toml", appCrateToml+"\n[[bin]]\nname = \"x\"\n"), cfgIf},
		"a version not locked":  {ok, []lib{{Name: "cfg-if", Version: "1.0.5", Lang: "Rust", Inside: "none"}}},
	} {
		if err := checkCargoSource(mkRow(c.files, c.libs)); err == nil {
			t.Errorf("%s: accepted", what)
		}
	}
	r := mkRow(ok, cfgIf)
	work := t.TempDir()
	tc := &toolchain{Rustc: "rustc", Cargo: "cargo", Work: work}
	for _, col := range []string{"rust", "rust-ovf"} {
		c, _ := columnByID(col)
		cmd, err := tc.command(r, c, filepath.Join(work, col+".exe"))
		if err != nil {
			t.Fatal(err)
		}
		line := strings.Join(cmd.Args, " ")
		for _, want := range []string{"build --release --offline --locked --target x86_64-pc-windows-msvc", `profile.release.lto="fat"`, "profile.release.codegen-units=1", `profile.release.panic="abort"`, "profile.release.opt-level=3"} {
			if !strings.Contains(line, want) {
				t.Errorf("%s: no %q in %s", col, want, line)
			}
		}
		wantOvf := map[string]string{"rust": "false", "rust-ovf": "true"}[col]
		if !strings.Contains(line, "profile.release.overflow-checks="+wantOvf) {
			t.Errorf("%s: overflow checks not %s: %s", col, wantOvf, line)
		}
		if v, _ := envValue(cmd.Env, "CARGO_ENCODED_RUSTFLAGS"); v != "-Ctarget-cpu=x86-64-v2\x1f-Ctarget-feature=+crt-static" {
			t.Errorf("%s: rustflags %q", col, v)
		}
		if v, _ := envValue(cmd.Env, "CARGO_TARGET_DIR"); !strings.HasPrefix(v, work) || !strings.HasSuffix(v, col) {
			t.Errorf("%s: target dir %q", col, v)
		}
		if !slices.Contains(cmd.Unset, "RUSTFLAGS") || !slices.Contains(cmd.Unset, "RUSTC_WRAPPER") {
			t.Errorf("%s: inherited flags not removed: %v", col, cmd.Unset)
		}
		if !strings.HasSuffix(cmd.Product, filepath.Join("x86_64-pc-windows-msvc", "release", "probe.exe")) {
			t.Errorf("%s: product %q", col, cmd.Product)
		}
	}
	// The inherited variables are gone from the build's environment.
	env := buildEnv([]string{"RUSTFLAGS=-Copt-level=0", "PATH=x", "rustc_wrapper=sccache"}, cargoEnvCleared)
	if !slices.Equal(env, []string{"PATH=x"}) && runtime.GOOS == "windows" {
		t.Fatalf("buildEnv %q", env)
	}
}

// TestAppCargoBuild builds a crate with one registry dependency through
// buildOne, offline, and runs it: cargo accepts the command line and the
// product reaches the column's executable.
func TestAppCargoBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a crate with cargo")
	}
	if runtime.GOOS != "windows" {
		t.Skip("the app table's cargo builds target x86_64-pc-windows-msvc")
	}
	cargo, err1 := exec.LookPath("cargo")
	rustc, err2 := exec.LookPath("rustc")
	if err1 != nil || err2 != nil {
		t.Skip("no cargo or rustc on PATH")
	}
	d := t.TempDir()
	appCrate(t, filepath.Join(d, "rs"), map[string]string{"Cargo.toml": appCrateToml, "Cargo.lock": appCrateLock, "src/main.rs": appCrateMain})
	// The build is --offline --locked: without cfg-if 1.0.0 in the user's
	// registry (a fresh or emptied CARGO_HOME) it cannot run here.
	fetch := exec.Command(cargo, "fetch", "--offline", "--locked", "--manifest-path", filepath.Join(d, "rs", "Cargo.toml"))
	if out, err := fetch.CombinedOutput(); err != nil {
		t.Skipf("cargo fetch --offline (the registry lacks cfg-if 1.0.0?): %v\n%s", err, out)
	}
	r := &row{Name: "probe", Table: "app", Dir: d, Sources: map[string]string{"rust": "rs"}, Kinds: map[string]string{"rust": "eco"},
		Libs: map[string][]lib{"rust": {{Name: "cfg-if", Version: "1.0.0", Lang: "Rust", Inside: "none"}}}}
	if err := checkCargoSource(r); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	tc := &toolchain{Rustc: rustc, Cargo: cargo, Work: work}
	c, _ := columnByID("rust-ovf")
	b := &built{Row: r, Col: c, Exe: exeName(work, "verify", r, c)}
	if b.Cmd, err1 = tc.command(r, c, b.Exe); err1 != nil {
		t.Fatal(err1)
	}
	t.Setenv("RUSTFLAGS", "-Cthis-flag-does-not-exist")
	// cargo ignores RUSTFLAGS while CARGO_ENCODED_RUSTFLAGS is set, which
	// tools/bench always sets, so these are the poisons that fail the
	// build unless buildEnv removes them.
	t.Setenv("RUSTC_WRAPPER", filepath.Join(d, "no-such-wrapper.exe"))
	t.Setenv("CARGO_BUILD_RUSTC", filepath.Join(d, "no-such-rustc.exe"))
	t.Setenv("CARGO_PROFILE_RELEASE_STRIP", "this-is-not-a-strip-value")
	if out, err := buildOne(b, func(s string) { t.Log(s) }); err != nil {
		t.Fatalf("cargo build: %v\n%s", err, out)
	}
	got, _, err := runOnce(b.Exe, nil)
	if err != nil || got != "windows 55" {
		t.Fatalf("the built crate printed %q (%v)", got, err)
	}
	// The stamp holds: a second build is skipped.
	if out, err := buildOne(b, func(s string) { t.Fatalf("rebuilt: %s", s) }); err != nil {
		t.Fatalf("second build: %v %s", err, out)
	}
}

func TestAppVendorList(t *testing.T) {
	root := appRepo(t, map[string]string{
		"evidence/vendor/lib/a.c":            "int a;\n",
		"evidence/vendor/lib/sub/b.c":        "int b;\n",
		"evidence/vendor/lib/sub/c.c":        "int c;\n",
		"evidence/vendor/lib/sub/x.S":        "\n",
		"evidence/vendor/lib/sub/h.h":        "\n",
		"evidence/vendor/lib/include/i.h":    "\n",
		"tests/bench/app/z/z.c":              "// z\n",
		"tests/bench/app/z/mk/pkg.mk":        appPkg,
		"tests/bench/app/z/mk/z.mk":          "Main() {}\n",
		"tests/bench/idiomatic/q/mk/pkg.mk":  appPkg,
		"tests/bench/idiomatic/q/q.c":        "// q\n",
		"tests/bench/idiomatic/q/bench.json": `{"table": "idiomatic", "sources": {"c": "q.c", "mk": "mk"}, "vendor": {"dir": "vendor/lib", "files": ["a.c"], "c_flags": []}}`,
	})
	rowDir := filepath.Join(root, "tests", "bench", "app", "z")
	v := &vendorSpec{Dir: "vendor/lib", Files: []string{"a.c", "sub/*.c", "sub/x.S", "sub/h.h"}, Include: []string{"include"}, For: []string{"c"}}
	if err := resolveVendor(v, rowDir); err != nil {
		t.Fatal(err)
	}
	if want := []string{"a.c", "sub/b.c", "sub/c.c", "sub/x.S", "sub/h.h"}; !slices.Equal(v.files, want) {
		t.Fatalf("files %q, want %q", v.files, want)
	}
	r := &row{Name: "z", Table: "app", Dir: rowDir, Sources: map[string]string{"c": "z.c", "mk": "mk"}, Vendor: v}
	args := strings.Join(vendorCArgs(r), " ")
	for _, want := range []string{"-I" + v.Path, "-I" + filepath.Join(v.Path, "include"), filepath.Join(v.Path, "sub", "c.c"), filepath.Join(v.Path, "sub", "x.S")} {
		if !strings.Contains(args, want) {
			t.Errorf("C line has no %s: %s", want, args)
		}
	}
	if strings.Contains(args, "h.h") {
		t.Errorf("a header on the C line: %s", args)
	}
	if vendorFor(r, "mk") || vendorFiles(r, "mk") != nil || len(vendorFiles(r, "c")) != 5 {
		t.Fatalf("for [c]: mk %v %v, c %v", vendorFor(r, "mk"), vendorFiles(r, "mk"), vendorFiles(r, "c"))
	}
	if err := checkVendorFlags(r); err != nil {
		t.Fatalf("a C-only vendor checked against the Mk package: %v", err)
	}
	tc := &toolchain{Mk: "mk.exe", Work: t.TempDir()}
	cmd, err := tc.command(r, columns[0], "o.exe")
	if err != nil || cmd.Args[len(cmd.Args)-1] != filepath.Join(rowDir, "mk") {
		t.Fatalf("the Mk package of a C-only vendor was copied: %v %v", cmd.Args, err)
	}
	// The existing default: both sides.
	q, err := loadRow(filepath.Join(root, "tests", "bench", "idiomatic", "q", "bench.json"), "")
	if err != nil {
		t.Fatal(err)
	}
	if !vendorFor(q, "mk") || !vendorFor(q, "mk-wrap") || !vendorFor(q, "c") || vendorFor(q, "rust") {
		t.Fatal("a vendor with no for is not the Mk and C sides'")
	}
	for _, bad := range []*vendorSpec{
		{Dir: "vendor/lib", Files: []string{"../lib/a.c"}},
		{Dir: "vendor/lib", Files: []string{`sub\b.c`}},
		{Dir: "vendor/lib", Files: []string{"*/b.c"}},
		{Dir: "vendor/lib", Files: []string{"sub/*.zz"}},
		{Dir: "vendor/lib", Files: []string{"a.c"}, For: []string{"rust"}},
		{Dir: "vendor/lib", Files: []string{"a.c"}, Include: []string{"../x"}},
	} {
		if err := resolveVendor(bad, rowDir); err == nil {
			t.Errorf("vendor %+v accepted", *bad)
		}
	}
}

// treeListing is every file of a generated tree with its SHA-256, in path
// order.
func treeListing(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		rel, _ := filepath.Rel(dir, p)
		s := sha256.Sum256(b)
		out = append(out, filepath.ToSlash(rel)+" "+hex.EncodeToString(s[:]))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAppGenKinds(t *testing.T) {
	dir := t.TempDir()
	spec := dataSpec{Kind: "tree", File: "tree", Bytes: 1 << 20, Seed: 11}
	p, err := genData(filepath.Join(dir, "a"), spec)
	if err != nil {
		t.Fatal(err)
	}
	q, err := genData(filepath.Join(dir, "b"), spec)
	if err != nil {
		t.Fatal(err)
	}
	la, lb := treeListing(t, p), treeListing(t, q)
	if !slices.Equal(la, lb) {
		t.Fatal("two trees of one spec differ")
	}
	var hidden, binary, crlf, upper, deep int
	var total int64
	for _, l := range la {
		path, _, _ := strings.Cut(l, " ")
		b, _ := os.ReadFile(filepath.Join(p, filepath.FromSlash(path)))
		total += int64(len(b))
		base := filepath.Base(path)
		if strings.HasPrefix(base, ".") || strings.Contains(path, "/.") {
			hidden++
		}
		if bytes.IndexByte(b, 0) >= 0 {
			binary++
		}
		if bytes.Contains(b, []byte("\r\n")) {
			crlf++
		}
		if base[0] >= 'A' && base[0] <= 'Z' {
			upper++
		}
		if ext := filepath.Ext(base); slices.Contains([]string{".go", ".mk", ".c", ".h", ".rs", ".zig", ".md"}, ext) {
			t.Fatalf("%s: a generated file a repository tool would read as source", path)
		}
		if strings.Count(path, "/") >= 2 {
			deep++
		}
	}
	if hidden == 0 || binary == 0 || crlf == 0 || upper == 0 || deep == 0 || total < spec.Bytes || len(la) < 50 {
		t.Fatalf("tree of %d files, %d bytes: hidden %d, binary %d, CR LF %d, capitalised %d, nested %d", len(la), total, hidden, binary, crlf, upper, deep)
	}
	if b, err := os.ReadFile(p + ".spec"); err != nil || string(b) != "tree 1048576 11\n" {
		t.Fatalf("tree stamp %q %v", b, err)
	}
	// A changed spec regenerates the whole directory: no file of the old
	// tree survives.
	spec.Seed = 12
	if _, err := genData(filepath.Join(dir, "a"), spec); err != nil {
		t.Fatal(err)
	}
	if lc := treeListing(t, p); slices.Equal(lc, la) || len(slices.DeleteFunc(slices.Clone(lc), func(s string) bool { return !slices.Contains(la, s) })) > len(la)/2 {
		t.Fatal("the regenerated tree kept the old one's files")
	}

	jp, err := genData(dir, dataSpec{Kind: "jsonl", File: "e.jsonl", Bytes: 256 << 10, Seed: 3})
	if err != nil {
		t.Fatal(err)
	}
	jb, _ := os.ReadFile(jp)
	if i := bytes.IndexFunc(jb, func(r rune) bool { return r > 127 || r == '<' || r == '>' || r == '&' }); i >= 0 {
		t.Fatalf("jsonl holds <, > or & or a non-ASCII byte at %d: %q", i, jb[max(0, i-40):min(len(jb), i+40)])
	}
	sc := bufio.NewScanner(bytes.NewReader(jb))
	lines, escapes := 0, 0
	for sc.Scan() {
		var ev struct {
			ID, Status, Bytes, Ms, Delta int64
			User, Event, Msg             string
			Ok                           bool
			Tags                         []string
		}
		dec := json.NewDecoder(bytes.NewReader(sc.Bytes()))
		dec.UseNumber()
		var raw map[string]any
		if err := json.Unmarshal(sc.Bytes(), &raw); err != nil {
			t.Fatalf("line %d: %v", lines+1, err)
		}
		for k, v := range raw {
			if f, ok := v.(float64); ok && f != float64(int64(f)) {
				t.Fatalf("line %d: %s is not an integer", lines+1, k)
			}
		}
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil || ev.ID != int64(lines+1) || ev.User == "" || ev.Event == "" {
			t.Fatalf("line %d: %+v %v", lines+1, ev, err)
		}
		if bytes.Contains(sc.Bytes(), []byte(`\`)) {
			escapes++
		}
		lines++
	}
	if lines < 100 || escapes == 0 {
		t.Fatalf("jsonl: %d lines, %d with escapes", lines, escapes)
	}

	mp, err := genData(dir, dataSpec{Kind: "mixed", File: "m.bin", Bytes: 1<<20 + 123, Seed: 5})
	if err != nil {
		t.Fatal(err)
	}
	mb, _ := os.ReadFile(mp)
	if len(mb) != 1<<20+123 {
		t.Fatalf("mixed: %d bytes", len(mb))
	}
	zp, err := genData(dir, dataSpec{Kind: "zlib", File: "m.bin.z", Bytes: 1<<20 + 123, Seed: 5, Of: "mixed"})
	if err != nil {
		t.Fatal(err)
	}
	zb, _ := os.ReadFile(zp)
	zr, err := zlib.NewReader(bytes.NewReader(zb))
	if err != nil {
		t.Fatal(err)
	}
	dec, err := io.ReadAll(zr)
	if err != nil || !bytes.Equal(dec, mb) {
		t.Fatalf("zlib of mixed does not decode to mixed (%v)", err)
	}
	if len(zb) >= len(mb) || len(zb) < len(mb)/20 {
		t.Fatalf("zlib of mixed: %d bytes from %d; mixed should hold both compressible and random blocks", len(zb), len(mb))
	}
	if b, _ := os.ReadFile(zp + ".spec"); string(b) != "zlib 1048699 5 of=mixed\n" {
		t.Fatalf("zlib stamp %q", b)
	}
	// The stamps of the existing kinds are unchanged by the of field.
	wp, err := genData(dir, dataSpec{Kind: "words", File: "w.txt", Bytes: 1000, Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(wp + ".spec"); string(b) != "words 1000 1\n" {
		t.Fatalf("words stamp %q", b)
	}
	if _, err := genData(dir, dataSpec{Kind: "zlib", File: "x.z", Bytes: 10, Of: "tree"}); err == nil {
		t.Fatal("zlib of a tree accepted")
	}
	if _, err := genData(dir, dataSpec{Kind: "words", File: "a/b.txt", Bytes: 10}); err == nil {
		t.Fatal("a data file path accepted")
	}
}

func TestRenderAppRow(t *testing.T) {
	r := &row{Name: "zlib-c", Table: "app", Kinds: map[string]string{"mk": "std", "c": "eco", "rust": "eco", "zig": "std", "go": "std"}}
	cs := []cell{}
	add := func(col string, med float64, peak uint64) {
		c, _ := columnByID(col)
		ts := []float64{med * 0.99, med, med * 1.01, med, med * 1.02, med * 0.98, med, med, med, med}
		x := cell{Row: "zlib-c", Table: "app", Col: col, Class: appClass(r, c), Times: ts, Summary: summarize(ts), OutBytes: 1234}
		if peak > 0 {
			x.Peak = &memPeak{RSS: peak, Commit: peak / 2}
		}
		cs = append(cs, x)
	}
	add("mk", 3.3, 50<<20)
	add("c", 1.0, 10<<20)
	add("rust", 1.1, 0)
	add("rust-ovf", 1.2, 0)
	add("zig-safe", 2.0, 0)
	add("go", 1.5, 20<<20)
	res := &results{
		Runs:    []facts{{Commit: "abc", Versions: map[string]string{}}},
		Columns: columns, Sweeps: 3, Options: hfOptions{Warmup: 3, Runs: 10},
		Rows: []rowInfo{{ID: "zlib-c", Table: "app", Algorithm: "Compress a file.", Args: []string{"c", "{data}", "{out}"}, Checksum: "x",
			Kinds: r.Kinds, Env: map[string]string{"MK_CARRIERS": "1"},
			Libs:    map[string][]lib{"c": {{Name: "zlib", Version: "1.3.1", Lang: "C", Inside: "C"}}, "rust": {{Name: "flate2", Version: "1.1.1", Lang: "Rust", Inside: "miniz_oxide, pure Rust"}}},
			Package: &pkgFact{Path: "Dogfoods/pack", Commit: "0123456789abcdef0123", Dirty: true}, Digest: true,
			Output: &outputSpec{File: "o.z", Check: "zlib"}}},
		Cells: cs,
	}
	md := renderResults(res, "2026-11-15")
	for _, want := range []string{
		`## Table "app"`, "Applications: each row is a real program",
		"| mk | std | checked |", "| c | eco | eco (not classed) |", "| rust-ovf | eco | eco (not classed) |", "| go | std | bounds |",
		"50.0 MiB (commit 25.0 MiB)", "| 1234 |",
		"- `c` is `eco`: zlib 1.3.1 (C; inside: C).", "flate2 1.1.1 (Rust; inside: miniz_oxide, pure Rust)",
		"The Mk side is the package `Dogfoods/pack`, used unchanged, last changed by commit 0123456789abcdef, with uncommitted changes in it.",
		"`MK_CARRIERS=1`", "Go's compress/zlib decodes it",
		"Defect line: `mk` against the best std cell, `go`: 2.20",
		"**over 2.0: a std defect for the std",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("RESULTS.md has no %q", want)
		}
	}
	// The best std cell is Go's (1.5), not C's eco cell (1.0); the kind
	// ratio of rust-ovf is against rust (1.1), the best eco cell C (1.0).
	if strings.Contains(md, "Median of mk / c") || strings.Contains(md, "Against 1.10") {
		t.Error("an app row fed the same-algorithm metric")
	}
	// A language's label is printed beside each of its cells, and "-"
	// beside the others'.
	res.Rows[0].Labels = map[string]string{"zig": "software AES"}
	md = renderResults(res, "2026-11-15")
	if !strings.Contains(md, "| software AES |\n") || !strings.Contains(md, " Label |\n") {
		t.Error("no label printed")
	}
	for _, line := range strings.Split(md, "\n") {
		switch {
		case strings.HasPrefix(line, "| Column | Kind |") && !strings.Contains(line, "| Label |"):
			t.Errorf("no Label column: %s", line)
		case strings.HasPrefix(line, "| zig-safe | std |") && !strings.HasSuffix(line, "| software AES |"):
			t.Errorf("zig-safe's cell has no label: %s", line)
		case strings.HasPrefix(line, "| go | std |") && !strings.HasSuffix(line, "| - |"):
			t.Errorf("go's cell has a label: %s", line)
		}
	}
	res.Rows[0].Labels = nil
	// A Linux reading at or under its spawner's floor reaches the page as a
	// bound, and the row says how its Linux peaks were read; the
	// Windows page above has neither.
	if strings.Contains(md, "at most") || strings.Contains(md, "Peak memory on Linux") {
		t.Error("a Windows row printed a Linux peak's text")
	}
	for i := range cs {
		switch cs[i].Col {
		case "rust":
			cs[i].Peak = &memPeak{Maxrss: 3 << 20, Floor: 4 << 20, Spawner: "peakspawn"}
		case "go":
			cs[i].Peak = &memPeak{RSS: 20 << 20, Maxrss: 20 << 20, Floor: 4 << 20, Spawner: "peakspawn"}
		}
	}
	md = renderResults(res, "2026-11-15")
	for _, want := range []string{"| at most 3.0 MiB |", "| 20.0 MiB |", "Peak memory on Linux is the program's `ru_maxrss`", "(`peakspawn`, a minimal Go program)"} {
		if !strings.Contains(md, want) {
			t.Errorf("RESULTS.md with Linux peaks has no %q", want)
		}
	}
	// An Mk-only row (Tier A) has no std peer: said so, no verdict.
	res.Cells = cs[:1]
	md = renderResults(res, "2026-11-15")
	if !strings.Contains(md, "`mk` has no std cell of C, Rust, Zig, Go or C# to be judged against") {
		t.Error("an Mk-only row has no defect-line note")
	}
}
