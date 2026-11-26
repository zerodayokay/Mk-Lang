// Why: std.websocket's benchmark rows must time the library's real code and
// obey the source rules of the table they are filed in.
// What: TestStdWs_BenchMaskIsTheLibrarys and TestStdWs_BenchRowsMeetTheirTable.
// How: compares the row's copy of Mask with the library's text, and reads
// each row's bench.json and sources.

// The ws-mask bench row (tests/bench-lanes/std-ws/mask) times a copy of
// std/websocket/frame.mk's Mask, since Mask is not a public name and a
// program cannot call it. This test fails when the copy and the library's
// function differ, from the line that opens the function to its closing
// brace, so the row always times the library's code. A second test checks
// that each of std.websocket's rows is filed in the table its columns meet.

package std

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// wsMaskBody is the text of the function Mask in the Mk file at path,
// from the line that starts "Mask(" to the first line that is "}".
func wsMaskBody(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	for i, l := range lines {
		if !strings.HasPrefix(l, "Mask(") {
			continue
		}
		for j := i; j < len(lines); j++ {
			if lines[j] == "}" {
				return strings.Join(lines[i:j+1], "\n")
			}
		}
	}
	t.Fatalf("%s: no function Mask", path)
	return ""
}

func TestStdWs_BenchMaskIsTheLibrarys(t *testing.T) {
	lib := wsMaskBody(t, filepath.Join("websocket", "frame.mk"))
	row := wsMaskBody(t, filepath.Join("..", "tests", "bench-lanes", "std-ws", "mask", "mk", "mask.mk"))
	if lib != row {
		t.Fatalf("the bench row's Mask differs from std/websocket/frame.mk's:\n--- library\n%s\n--- row\n%s", lib, row)
	}
}

// wsRow is the part of a bench row's bench.json these tests read.
type wsRow struct {
	Name     string            `json:"name"`
	Table    string            `json:"table"`
	Sources  map[string]string `json:"sources"`
	Skip     map[string]string `json:"skip"`
	Variants []struct {
		Name     string   `json:"name"`
		Cpu      string   `json:"cpu"`
		Baseline []string `json:"baseline"`
		Bytes    float64  `json:"bytes"`
	} `json:"variants"`
	Notes []string `json:"notes"`
}

// wsLoadRow reads tests/bench-lanes/std-ws/<dir>/bench.json and the text
// of each source file it names (a directory source: its .mk files).
func wsLoadRow(t *testing.T, dir string) (wsRow, map[string]string) {
	t.Helper()
	base := filepath.Join("..", "tests", "bench-lanes", "std-ws", dir)
	b, err := os.ReadFile(filepath.Join(base, "bench.json"))
	if err != nil {
		t.Fatal(err)
	}
	var r wsRow
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	texts := map[string]string{}
	for lang, src := range r.Sources {
		p := filepath.Join(base, filepath.FromSlash(src))
		st, err := os.Stat(p)
		if err != nil {
			t.Fatalf("%s %s: %v", r.Name, lang, err)
		}
		if !st.IsDir() {
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			texts[lang] = string(b)
			continue
		}
		ms, _ := filepath.Glob(filepath.Join(p, "*.mk"))
		for _, m := range ms {
			b, err := os.ReadFile(m)
			if err != nil {
				t.Fatal(err)
			}
			texts[lang] += string(b)
		}
	}
	return r, texts
}

// wsVectorLayer matches a source's use of its language's SIMD layer: an
// intrinsics header, std::arch, @Vector, std.simd.
var wsVectorLayer = regexp.MustCompile(`#include\s*<\w*intrin\.h>|std::arch|core::arch|@Vector|import std\.simd`)

// wsTuning matches what the benchmark rules forbid outside the SIMD layer
// in every table (tools/bench's sameBans): unchecked indexing,
// fast-math, hints, forced inlining, unsafe.
var wsTuning = regexp.MustCompile(`get_unchecked|-ffast-math|@setRuntimeSafety|@setFloatMode|\brestrict\b|__restrict|#\s*pragma|__builtin_|__attribute__|__declspec|\binline\b|__forceinline|\bunsafe\b|std::hint|target_feature|#\[(inline|cold)|@branchHint|@prefetch|always_inline|//go:`)

// TestStdWs_BenchRowsMeetTheirTable: each of std.websocket's bench rows is
// filed in the table its columns meet
// (tests/bench/README.md). ws-mask is a SIMD row: one fixed target per
// variant (x86-64-v2), L1, L3 and DRAM sizes with a baseline and a byte
// count, every column in its SIMD layer but Go's, which is labelled
// scalar, and every buffer 64-byte aligned or labelled as the exception.
// ws-extend is an idiomatic row with no C cell (C has no std growable
// list). ws-frames is a same-algorithm row whose columns use no vector
// layer.
func TestStdWs_BenchRowsMeetTheirTable(t *testing.T) {
	mask, mt := wsLoadRow(t, "mask")
	if mask.Table != "simd" || len(mask.Variants) != 3 {
		t.Errorf("ws-mask: table %q with %d variants, want simd with l1v2, l3v2 and dramv2", mask.Table, len(mask.Variants))
	}
	for _, v := range mask.Variants {
		if v.Cpu != "x86-64-v2" || len(v.Baseline) != 2 || v.Baseline[1] != "0" || v.Bytes <= 0 {
			t.Errorf("ws-mask %s: cpu %q, baseline %v, bytes %v", v.Name, v.Cpu, v.Baseline, v.Bytes)
		}
	}
	for _, lang := range []string{"c", "rust", "zig", "go"} {
		if !strings.Contains(mt[lang], "buffer not 64-byte aligned") {
			t.Errorf("ws-mask %s: no 64-byte alignment refusal", lang)
		}
	}
	for _, lang := range []string{"mk", "c", "rust", "zig"} {
		if !wsVectorLayer.MatchString(mt[lang]) {
			t.Errorf("ws-mask %s: not in its language's SIMD layer", lang)
		}
	}
	if !strings.Contains(strings.Join(mask.Notes, " "), "Go: scalar, labelled as such") || !strings.Contains(mt["mk"], "labelled alignment exception") {
		t.Error("ws-mask: the Go cell is not labelled scalar, or the Mk cell's alignment exception is not labelled")
	}
	ext, et := wsLoadRow(t, "extend")
	if ext.Table != "idiomatic" || et["c"] != "" || !strings.HasPrefix(ext.Skip["c"], "no std growable list") {
		t.Errorf("ws-extend: table %q, C source %v, C skip %q; want idiomatic with C skipped", ext.Table, et["c"] != "", ext.Skip["c"])
	}
	for lang, text := range et {
		if wsVectorLayer.MatchString(text) || wsTuning.MatchString(text) {
			t.Errorf("ws-extend %s: uses a vector layer or a form 4.1 forbids", lang)
		}
	}
	frames, ft := wsLoadRow(t, "frames")
	if frames.Table != "same" || len(ft) != 5 {
		t.Errorf("ws-frames: table %q with %d sources, want same with mk, c, rust, zig and go", frames.Table, len(ft))
	}
	for lang, text := range ft {
		if wsVectorLayer.MatchString(text) || wsTuning.MatchString(text) {
			t.Errorf("ws-frames %s: uses a vector layer or a form the same-algorithm table forbids", lang)
		}
	}
}
