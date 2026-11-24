// Why: an aligned 8- or 16-byte load at an address only known to be byte
// aligned is undefined behaviour, and std's text and hash-table paths load
// 8 and 16 bytes.
// What: TestUnalignedGroupLoads and its plant variant, the IR half;
// package std's TestStdColl_NoAlignedWideByteLoads scans the sources.
// How: builds a probe at release for x86_64-windows and requires every 8-byte
// load in str.From and Words.Next, and the Swiss table's 16-byte group load
// (GroupAt16), to be align 1 in codegen's IR.

package stdtest

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"mk/internal/driver"
	"mk/internal/types"
)

const std15Probe = `Count(b: []u8): int {
    s := str.From(b) else {
        return -1
    }
    var it := s.Words()
    var n := 0
    for {
        _ = it.Next() else {
            return n
        }
        n += 1
    }
}

export Main(sys: &System): int {
    text := "ab cd\u{E9}f ghijklmnop q rstu vwxyz0123456789"
    var m := Map[int, int].New(sys.arena)
    for i in 0..40 {
        m.Set(i, i) else {
            return 1
        }
    }
    var hits := 0
    for i in 0..80 {
        if m.Get(&i).IsSome() {
            hits += 1
        }
    }
    return Count(text.AsBytes()) + hits - 47
}
`

// std15IR builds the probe over the std at root at release and returns
// codegen's IR of every pack.
func std15IR(t *testing.T, root string) map[string]string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.mk"), []byte(std15Probe), 0o644); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	lls := map[string]string{}
	cfg := driver.Config{Root: filepath.Join(dir, "main.mk"), StdDir: root, NoCache: true, Target: types.X86_64Windows, Profile: types.Release,
		TestHooks: driver.TestHooks{OnLL: func(name, ll string) {
			mu.Lock()
			lls[name] = ll
			mu.Unlock()
		}}}
	s, err := driver.NewSession(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := s.Build(filepath.Join(dir, "probe.exe"))
	if err != nil {
		if strings.Contains(err.Error(), "LLVM") {
			t.Skipf("needs the LLVM toolchain (the gate fails on this skip): %v", err)
		}
		t.Fatal(err)
	}
	if len(r.Report.Diags) > 0 {
		t.Fatalf("probe build:\n%s", r.Report.Text(r.Sources))
	}
	return lls
}

var (
	defineLine = regexp.MustCompile(`^define [^@]*@("[^"]+"|[\w.$:]+)\(`)
	assignLine = regexp.MustCompile(`^\s*(%[\w.]+) = (.*)$`)
	storePtr   = regexp.MustCompile(`^\s*store ptr (%[\w.]+), ptr (%[\w.]+)`)
	loadPtr    = regexp.MustCompile(`^load ptr, ptr (%[\w.]+)`)
	byteGEP    = regexp.MustCompile(`^getelementptr (?:inbounds )?i8, ptr (%[\w.]+), i64 (%[\w.]+)`)
	mulBy      = regexp.MustCompile(`^mul i64 %[\w.]+, (\d+)`)
	loadI64    = regexp.MustCompile(`^load (?:i64|<16 x i8>), ptr (%[\w.]+), align (\d+)`)
)

// wordLoad is one 8- or 16-byte load through a byte pointer.
type wordLoad struct {
	fn    string
	align string
}

// byteWordLoads returns every i64 or <16 x i8> load of a std.core function in ll whose
// address is a byte pointer: the result of a getelementptr i8 by a variable
// offset that is not a multiple of a wider element (raw.Offset on a *u8,
// an index into []u8), followed through the alloca slots it is stored in
// and loaded from. Field accesses (constant offsets) and loads of locals
// are not byte pointers; so a word load at a constant byte offset of a raw
// pointer (raw.Offset(p, 8)) would not be judged either. std has none
// today: its word loads index by a variable.
func byteWordLoads(ll string) []wordLoad {
	var out []wordLoad
	var fn string
	var body []string
	flush := func() {
		defs := map[string]string{}
		for _, l := range body {
			if m := assignLine.FindStringSubmatch(l); m != nil {
				defs[m[1]] = m[2]
			}
		}
		bytePtr := map[string]bool{} // values and alloca slots
		for changed := true; changed; {
			changed = false
			mark := func(v string) {
				if !bytePtr[v] {
					bytePtr[v], changed = true, true
				}
			}
			for v, d := range defs {
				if m := byteGEP.FindStringSubmatch(d); m != nil {
					if k := mulBy.FindStringSubmatch(defs[m[2]]); k == nil || k[1] == "1" {
						mark(v)
					}
				}
				if m := loadPtr.FindStringSubmatch(d); m != nil && bytePtr[m[1]] {
					mark(v)
				}
			}
			for _, l := range body {
				if m := storePtr.FindStringSubmatch(l); m != nil && bytePtr[m[1]] {
					mark(m[2])
				}
			}
		}
		for _, d := range defs {
			if m := loadI64.FindStringSubmatch(d); m != nil && bytePtr[m[1]] {
				out = append(out, wordLoad{fn: fn, align: m[2]})
			}
		}
	}
	for _, l := range strings.Split(ll, "\n") {
		if m := defineLine.FindStringSubmatch(l); m != nil {
			fn, body = strings.Trim(m[1], `"`), nil
			continue
		}
		if fn == "" {
			continue
		}
		if l == "}" {
			if strings.HasPrefix(fn, "mk.std.core.") {
				flush()
			}
			fn = ""
			continue
		}
		body = append(body, l)
	}
	return out
}

// std15Report is the probe's byte-pointer word loads: the ones not align 1,
// and which functions had any.
func std15Report(t *testing.T, root string) (bad []string, seen map[string]int) {
	t.Helper()
	seen = map[string]int{}
	for _, ll := range std15IR(t, root) {
		for _, w := range byteWordLoads(ll) {
			seen[w.fn]++
			if w.align != "1" {
				bad = append(bad, fmt.Sprintf("%s: load i64 through a byte pointer is align %s, want 1", w.fn, w.align))
			}
		}
	}
	slices.Sort(bad)
	return bad, seen
}

// TestUnalignedGroupLoads: in the probe's release IR every 8- or 16-byte load
// through a byte pointer of a std.core function is align 1, and the group
// load of the Swiss table (GroupAt16) and the word loads of the text paths (LoadWord, which
// str.From's validation and Words.Next reach) are among them, so the walk
// sees the loads it is about.
func TestUnalignedGroupLoads(t *testing.T) {
	bad, seen := std15Report(t, stdRoot(t))
	for _, b := range bad {
		t.Errorf("%s", b)
	}
	for _, fn := range []string{"mk.std.core.GroupAt16", "mk.std.core.LoadWord"} {
		if seen[fn] == 0 {
			t.Errorf("%s: no 8- or 16-byte load through a byte pointer in the probe's IR; the walk lost it", fn)
		}
	}
	var names []string
	for fn, n := range seen {
		names = append(names, fmt.Sprintf("%s %d", fn, n))
	}
	slices.Sort(names)
	t.Logf("byte-pointer word loads: %s", strings.Join(names, ", "))
}

// TestUnalignedGroupLoadsSeesPlants: with LoadWord planted to read through
// raw.Read (an aligned load) in a std copy, the test reports it and nothing
// else. The Swiss table's group load has no plant: it loads a Vec[u8, 16],
// which is aligned as u8, so no 16-byte load through it can claim more than
// align 1 (and raw.Read of a Vec lowers to a memcpy, not a load).
func TestUnalignedGroupLoadsSeesPlants(t *testing.T) {
	root := stdRoot(t)
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(root)); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "core", "core_str.mk")
	from := "raw.ReadUnaligned(raw.PtrCast[u8, u64](raw.Offset(raw.Data(b), i)))"
	text, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	planted := strings.Replace(string(text), from, strings.Replace(from, "raw.ReadUnaligned(", "raw.Read(", 1), 1)
	if planted == string(text) {
		t.Fatalf("core/core_str.mk: %s not found", from)
	}
	if err := os.WriteFile(p, []byte(planted), 0o644); err != nil {
		t.Fatal(err)
	}
	bad, _ := std15Report(t, dir)
	if len(bad) != 1 || !strings.HasPrefix(bad[0], "mk.std.core.LoadWord:") {
		t.Errorf("want the one LoadWord plant reported; reports: %v", bad)
	}
}
