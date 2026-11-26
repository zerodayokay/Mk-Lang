// Why: a heap container records its allocator at New; a call that allocates or
// frees with another must panic, a call that does not must not check.
// What: the TestStdColl_ owner and bench tests: wrong allocator
// panics, no compare without allocation, no walk in HeapMap.Free.
// How: programs print each table layout so no pass is vacuous; a
// 200,000-operation churn predicts from the shape which inserts allocate.

package std

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// collOwnerCore is written to std/core/tests_collowner.mk in the copy.
const collOwnerCore = `// Fixture of the std-coll owner-check tests (COLL-R2, COLL-R4): a
// HeapMap's table shape and the control byte of the slot an insert would
// take, for user code.

export CollOwnerShape(m: &HeapMap[int, int]): (cap: int, used: int, growth: int) {
    return (cap: m.t.cap, used: m.t.used, growth: m.t.growth)
}

// CollOwnerFreeCtrl is the control byte of the slot an insert of k takes
// (TableLookup's free slot): 255 empty, 128 deleted; -1 when k is present
// or the capacity is 0.
export CollOwnerFreeCtrl(m: &HeapMap[int, int], k: int): int {
    r := TableLookup(&m.t, HashOf(&k), &k)
    if r.at >= 0 || r.free < 0 {
        return -1
    }
    return int(CtrlAt(m.t.ctrl, r.free))
}
`

// collOwnerModule is written to std/collowner/collowner.mk in the copy.
const collOwnerModule = `// Fixture of the std-coll owner-check tests: std.core's calls for user code.

import std.core

export Shape(m: &HeapMap[int, int]): (cap: int, used: int, growth: int) {
    return core.CollOwnerShape(m)
}

export FreeCtrl(m: &HeapMap[int, int], k: int): int {
    return core.CollOwnerFreeCtrl(m, k)
}
`

const collOwnerProgram = `import std.collowner

// Report prints a HeapMap's shape and, for key k, the control byte of the
// slot its insert would take.
Report(out: mut Writer, tag: str, m: &HeapMap[int, int], k: int): WriteError!void {
    s := collowner.Shape(m)
    c := collowner.FreeCtrl(m, k)
    cap := s.cap
    used := s.used
    growth := s.growth
    try Print(out, "{tag} {cap} {used} {growth} {c}\n", (tag, cap, used, growth, c))
    try out.Flush()
}

// Full32 fills the growth allowance of a capacity-32 map with ordinary
// consecutive keys. At capacity 16 an erase cannot leave a tombstone.
Full32(h: &Alloc): OutOfMemory!HeapMap[int, int] {
    var m := HeapMap[int, int].New(h)
    for i in 0..28 {
        m.Set(h, i, i) else {
            m.Free(h)
            fail OutOfMemory
        }
    }
    return m
}

// Churn drives HeapMap inserts and removes with a seeded sequence. Before
// each insert of an absent key it predicts from the shape alone (4.8 rules
// 4 and 6, written out here, not std's TablePlaceKind) whether the insert
// allocates: at capacity 0, or when the slot it takes is empty, growth is
// 0 and more than cap·7/16 slots are used. Inserts predicted not to
// allocate, present keys included, get the wrong allocator w, which panics
// if std compares (4.9); the others get h. After each insert the capacity
// must have changed exactly when growth was predicted. The first half is
// random over 56 keys in phases of 500 operations, 9 in 10 inserts then 1
// in 10, and the map starts again every 3,000 operations, so capacity 0
// and early growth recur. The second half is a sliding window of 28 keys
// (insert key i, remove key i - 28) at capacity 64: there removes leave
// deleted bytes until growth is 0 with 28 used, cap·7/16, which is rule
// 6's in-place rehash.
Churn(out: mut Writer, h: &Alloc, w: &Alloc, ops: int, seed: u64): WriteError!void {
    var m := HeapMap[int, int].New(h)
    var s := seed
    var claim_empty := 0
    var claim_deleted := 0
    var present := 0
    var rehash := 0
    var grow := 0
    half := ops / 2
    for op in 0..ops {
        if op < half && op % 3000 == 2999 {
            m.Free(h)
            m = HeapMap[int, int].New(h)
        }
        if op == half {
            m.Free(h)
            m = HeapMap[int, int].New(h)
        }
        s = s *% 6364136223846793005 +% 1442695040888963407
        r := Trunc[int](s >> 33)
        var k := r % 56
        var inserts := 1
        if (op / 500) % 2 == 0 {
            inserts = 9
        }
        var insert := (r / 56) % 10 < inserts
        if op >= half {
            i := (op - half) / 2
            insert = (op - half) % 2 == 0
            k = 1000 + i
            if !insert {
                k = 1000 + i - 28
            }
        }
        if insert {
            before := collowner.Shape(&m)
            c := collowner.FreeCtrl(&m, k)
            var allocates := false
            if m.Get(&k).IsSome() {
                present += 1
            } else if before.cap == 0 {
                allocates = true
            } else if c == 255 && before.growth == 0 && before.used > before.cap * 7 / 16 {
                allocates = true
            } else if c == 255 && before.growth == 0 {
                rehash += 1
            } else if c == 255 {
                claim_empty += 1
            } else {
                claim_deleted += 1
            }
            var failed := false
            if allocates {
                grow += 1
                m.Set(h, k, op) else {
                    failed = true
                }
            } else {
                m.Set(w, k, op) else {
                    failed = true
                }
            }
            after := collowner.Shape(&m)
            if failed || (after.cap != before.cap) != allocates {
                bc := before.cap
                ac := after.cap
                m.Free(h)
                try Print(out, "mismatch op {op} key {k} cap {bc} to {ac}\n", (op, k, bc, ac))
                return
            }
        } else {
            _ = m.Remove(&k)
        }
    }
    m.Free(h)
    try Print(out, "churn empty {claim_empty} deleted {claim_deleted} present {present} rehash {rehash} grow {grow}\n", (claim_empty, claim_deleted, present, rehash, grow))
}

// Room makes every call that 4.9 says compares nothing, with the wrong
// allocator w: pushes and inserts into spare capacity, a HeapMap insert
// with growth left, Ensure and Set of present keys, a HeapText push that
// fits, an empty push, and the empty TextMap key in and out.
Room(out: mut Writer, h: &Alloc, w: &Alloc): WriteError|OutOfMemory!void {
    var l := HeapList[int].New(h)
    defer l.Free(h)
    try l.Push(h, 1)
    try l.Push(w, 2)
    try l.Insert(w, 0, 3)
    var t := HeapText.New(h)
    defer t.Free(h)
    try t.Push(h, "a")
    try t.Push(w, "bcd")
    try t.Push(w, "")
    var m := HeapMap[int, int].New(h)
    defer m.Free(h)
    try m.Set(h, 1, 1)
    try Report(out, "pre", &m, 2)
    try m.Set(w, 2, 2)
    _ = try m.Ensure(w, 3, 3)
    _ = try m.Ensure(w, 1, 5)
    try m.Set(w, 1, 4)
    try Report(out, "post", &m, 4)
    var tm := TextMap[int].New(h)
    defer tm.Free(h)
    _ = try tm.Ensure(h, "k", 1)
    _ = try tm.Ensure(w, "", 2)
    _ = try tm.Ensure(w, "k", 3)
    _ = tm.Remove(w, "")
    n := l.Len() + t.Str().Len() + m.Len() + tm.Len()
    try Print(out, "sizes {n}\n", (n))
}

export Main(sys: &System) {
    var out := sys.Stdout()
    h := sys.heap.Alloc()
    w := sys.arena.Alloc()
    which := sys.args[1]
    Print(mut out, "before\n", ()) else {
        return
    }
    out.Flush() else {
        return
    }
    if which == "HeapMap.Set" {
        // Capacity 0: the first insert allocates.
        var m := HeapMap[int, int].New(h)
        m.Set(w, 1, 1) else {
        }
        m.Free(h)
    } else if which == "HeapMap.Ensure" {
        var m := HeapMap[int, int].New(h)
        _ = m.Ensure(w, 1, 1) else {
        }
        m.Free(h)
    } else if which == "HeapMap.Set.grow" {
        // Capacity 32, growth 0, 28 used: an insert into an empty slot
        // doubles the table.
        var m := Full32(h) else {
            return
        }
        k := 1000
        Report(mut out, "pre", &m, k) else {
        }
        m.Set(w, k, 1) else {
        }
        m.Free(h)
    } else if which == "HeapMap.Ensure.grow" {
        var m := Full32(h) else {
            return
        }
        k := 1000
        Report(mut out, "pre", &m, k) else {
        }
        _ = m.Ensure(w, k, 1) else {
        }
        m.Free(h)
    } else if which == "HeapMap.Free" {
        var m := HeapMap[int, int].New(h)
        m.Set(h, 1, 1) else {
        }
        m.Free(w)
    } else if which == "HeapMap.Free.empty" {
        var m := HeapMap[int, int].New(h)
        m.Free(w)
    } else if which == "TextMap.Ensure" {
        var m := TextMap[int].New(h)
        _ = m.Ensure(w, "k", 1) else {
        }
        m.Free(h)
    } else if which == "TextMap.Ensure.empty" {
        // The empty key at capacity 0: no copy, but the table allocates.
        var m := TextMap[int].New(h)
        _ = m.Ensure(w, "", 1) else {
        }
        m.Free(h)
    } else if which == "TextMap.Remove" {
        var m := TextMap[int].New(h)
        _ = m.Ensure(h, "k", 1) else {
        }
        _ = m.Remove(w, "k")
        m.Free(h)
    } else if which == "TextMap.Free" {
        var m := TextMap[int].New(h)
        _ = m.Ensure(h, "k", 1) else {
        }
        m.Free(w)
    } else if which == "HeapText.Push" {
        // Past capacity: 4 bytes fill the first buffer, the fifth grows.
        var t := HeapText.New(h)
        t.Push(h, "abcd") else {
        }
        t.Push(w, "e") else {
        }
        t.Free(h)
    } else if which == "HeapText.Free" {
        var t := HeapText.New(h)
        t.Push(h, "abc") else {
        }
        t.Free(w)
    } else if which == "HeapList.Push" {
        var l := HeapList[int].New(h)
        l.Push(w, 1) else {
        }
        l.Free(h)
    } else if which == "HeapList.Insert" {
        var l := HeapList[int].New(h)
        l.Insert(w, 0, 1) else {
        }
        l.Free(h)
    } else if which == "HeapList.Free" {
        var l := HeapList[int].New(h)
        l.Push(h, 1) else {
        }
        l.Free(w)
    } else if which == "room" {
        Room(mut out, h, w) else {
            return
        }
    } else if which == "tombstone" {
        // Remove and reinsert an ordinary key whose slot stays deleted.
        var m := Full32(h) else {
            return
        }
        var k := -1
        for i in 0..28 {
            _ = m.Remove(&i)
            if collowner.Shape(&m).growth == 0 && collowner.FreeCtrl(&m, i) == 128 {
                k = i
                break
            }
            m.Set(h, i, i) else {
                m.Free(h)
                return
            }
        }
        defer m.Free(h)
        Report(mut out, "pre", &m, k) else {
            return
        }
        if k < 0 {
            return
        }
        m.Set(w, k, 1) else {
            return
        }
        Report(mut out, "post", &m, k) else {
            return
        }
    } else if which == "rehash" {
        // A sliding window of 28 ordinary keys at capacity 64 leaves
        // tombstones. Stop at the first new key that needs an empty slot
        // with no growth left. At 28 live slots it must rehash in place.
        var m := HeapMap[int, int].New(h)
        defer m.Free(h)
        for k in 1000..51000 {
            s := collowner.Shape(&m)
            if s.cap == 64 && s.used == 28 && s.growth == 0 && collowner.FreeCtrl(&m, k) == 255 {
                Report(mut out, "pre", &m, k) else {
                    return
                }
                _ = m.Ensure(w, k, 1) else {
                    return
                }
                Report(mut out, "post", &m, k) else {
                    return
                }
                break
            }
            m.Set(h, k, k) else {
                return
            }
            if k >= 1028 {
                old := k - 28
                _ = m.Remove(&old)
            }
        }
    } else if which == "churn" {
        Churn(mut out, h, w, 200000, 12345) else {
            return
        }
    }
    Print(mut out, "after\n", ()) else {
        return
    }
}
`

// collOwnerRun runs exe with one mode and returns its exit status, stdout
// and stderr.
func collOwnerRun(t *testing.T, exe, mode string) (int, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), collTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, mode)
	cmd.Dir = filepath.Dir(exe)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("%s: timed out after %v", mode, collTimeout)
	}
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return code, strings.ReplaceAll(out.String(), "\r\n", "\n"), strings.ReplaceAll(errb.String(), "\r\n", "\n")
}

func collOwnerBuild(t *testing.T, flags ...string) string {
	t.Helper()
	return collBuildWith(t, map[string]string{
		"core/tests_collowner.mk": collOwnerCore,
		"collowner/collowner.mk":  collOwnerModule,
	}, collOwnerProgram, flags...)
}

// shape is one Report line: capacity, used, growth, the control byte of
// the key's free slot.
type shape struct{ cap, used, growth, ctrl int }

var shapeLine = regexp.MustCompile(`(?m)^(pre|post) (-?\d+) (-?\d+) (-?\d+) (-?\d+)$`)

func parseShapes(t *testing.T, stdout string) map[string]shape {
	t.Helper()
	m := map[string]shape{}
	for _, g := range shapeLine.FindAllStringSubmatch(stdout, -1) {
		var v [4]int
		for i := range v {
			n, err := strconv.Atoi(g[i+2])
			if err != nil {
				t.Fatal(err)
			}
			v[i] = n
		}
		m[g[1]] = shape{v[0], v[1], v[2], v[3]}
	}
	return m
}

// TestStdColl_WrongAllocatorPanics: every call that allocates or frees
// through a, given an allocator other than the one recorded at New, exits 101
// with "panic: wrong allocator: <Op>" (code 12) and prints nothing after:
// HeapMap.Set and Ensure at capacity 0 and when the table doubles,
// HeapMap.Free (empty or not), TextMap.Ensure (the key copy), TextMap.Remove
// (the key free), TextMap.Free, HeapText.Push past its capacity,
// HeapText.Free, HeapList.Push and Insert at capacity 0, HeapList.Free.
func TestStdColl_WrongAllocatorPanics(t *testing.T) {
	t.Parallel()
	for _, flags := range [][]string{nil, {"--release"}} {
		exe := collOwnerBuild(t, flags...)
		cases := []struct {
			mode, op string
			pre      *shape // the layout the mode must have reached
		}{
			{"HeapMap.Set", "HeapMap.Set", nil},
			{"HeapMap.Ensure", "HeapMap.Ensure", nil},
			{"HeapMap.Set.grow", "HeapMap.Set", &shape{32, 28, 0, 255}},
			{"HeapMap.Ensure.grow", "HeapMap.Ensure", &shape{32, 28, 0, 255}},
			{"HeapMap.Free", "HeapMap.Free", nil},
			{"HeapMap.Free.empty", "HeapMap.Free", nil},
			{"TextMap.Ensure", "TextMap.Ensure", nil},
			{"TextMap.Ensure.empty", "TextMap.Ensure", nil},
			{"TextMap.Remove", "TextMap.Remove", nil},
			{"TextMap.Free", "TextMap.Free", nil},
			{"HeapText.Push", "HeapText.Push", nil},
			{"HeapText.Free", "HeapText.Free", nil},
			{"HeapList.Push", "HeapList.Push", nil},
			{"HeapList.Insert", "HeapList.Insert", nil},
			{"HeapList.Free", "HeapList.Free", nil},
		}
		for _, c := range cases {
			t.Run(fmt.Sprintf("%s/%v", c.mode, flags), func(t *testing.T) {
				code, stdout, stderr := collOwnerRun(t, exe, c.mode)
				want := "panic: wrong allocator: " + c.op
				if code != 101 || !strings.Contains(stderr, want) || strings.Contains(stdout, "after") {
					t.Fatalf("exit %d, stdout %q, stderr %q; want 101, no \"after\" and %q", code, stdout, stderr, want)
				}
				if c.pre != nil {
					if got := parseShapes(t, stdout)["pre"]; got != *c.pre {
						t.Fatalf("layout before the call %+v, want %+v (stdout %q)", got, *c.pre, stdout)
					}
				}
			})
		}
	}
}

// TestStdColl_NoCompareWithoutGrowth: a Push that does not grow
// does not compare, for every container: the wrong allocator passes where
// nothing is allocated or freed (pushes and inserts into spare capacity, a
// HeapMap insert with growth left, a Set of a present key, the empty
// TextMap key).
func TestStdColl_NoCompareWithoutGrowth(t *testing.T) {
	t.Parallel()
	for _, flags := range [][]string{nil, {"--release"}} {
		exe := collOwnerBuild(t, flags...)
		code, stdout, stderr := collOwnerRun(t, exe, "room")
		if code != 0 || stderr != "" || !strings.HasSuffix(stdout, "sizes 11\nafter\n") {
			t.Fatalf("%v: exit %d, stdout %q, stderr %q; want 0, \"sizes 11\" and \"after\"", flags, code, stdout, stderr)
		}
		s := parseShapes(t, stdout)
		// One key in capacity 8 (growth 8·7/8 - 1 = 6); after two new keys
		// and two calls on present ones, the same capacity and growth 4.
		// sizes: 3 list elements, "abcd", 3 map keys, the TextMap's "k".
		if s["pre"] != (shape{8, 1, 6, 255}) || s["post"].cap != 8 || s["post"].used != 3 || s["post"].growth != 4 {
			t.Fatalf("%v: shapes %+v, want pre {8 1 6 255} and post cap 8, used 3, growth 4", flags, s)
		}
	}
}

// TestStdColl_NoCompareWithoutAllocation: a HeapMap insert at growth
// 0 that reuses a deleted slot, or that rehashes in place, allocates
// nothing and so compares nothing; the churn checks the same rule over
// 200,000 seeded operations against a model of the table's placement and
// in-place rehash rules (at most cap·7/16 used).
func TestStdColl_NoCompareWithoutAllocation(t *testing.T) {
	t.Parallel()
	for _, flags := range [][]string{nil, {"--release"}} {
		exe := collOwnerBuild(t, flags...)
		t.Run(fmt.Sprintf("tombstone/%v", flags), func(t *testing.T) {
			code, stdout, stderr := collOwnerRun(t, exe, "tombstone")
			if code != 0 || stderr != "" || !strings.HasSuffix(stdout, "after\n") {
				t.Fatalf("exit %d, stdout %q, stderr %q; want 0 and \"after\"", code, stdout, stderr)
			}
			s := parseShapes(t, stdout)
			if s["pre"] != (shape{32, 27, 0, 128}) || s["post"] != (shape{32, 28, 0, -1}) {
				t.Fatalf("shapes %+v, want pre {32 27 0 128} and post {32 28 0 -1}", s)
			}
		})
		t.Run(fmt.Sprintf("rehash/%v", flags), func(t *testing.T) {
			code, stdout, stderr := collOwnerRun(t, exe, "rehash")
			if code != 0 || stderr != "" || !strings.HasSuffix(stdout, "after\n") {
				t.Fatalf("exit %d, stdout %q, stderr %q; want 0 and \"after\"", code, stdout, stderr)
			}
			s := parseShapes(t, stdout)
			// After rehash and insertion, growth is 64*7/8 - 29.
			if s["pre"] != (shape{64, 28, 0, 255}) || s["post"] != (shape{64, 29, 27, -1}) {
				t.Fatalf("shapes %+v, want pre {64 28 0 255} and post {64 29 27 -1}", s)
			}
		})
		t.Run(fmt.Sprintf("churn/%v", flags), func(t *testing.T) {
			code, stdout, stderr := collOwnerRun(t, exe, "churn")
			if code != 0 || stderr != "" || !strings.HasSuffix(stdout, "after\n") {
				t.Fatalf("exit %d, stdout %q, stderr %q; want 0 and \"after\"", code, stdout, stderr)
			}
			var empty, deleted, present, rehash, grow int
			line := stdout[strings.Index(stdout, "churn"):]
			if _, err := fmt.Sscanf(line, "churn empty %d deleted %d present %d rehash %d grow %d", &empty, &deleted, &present, &rehash, &grow); err != nil {
				t.Fatalf("%v in %q", err, stdout)
			}
			t.Logf("inserts: empty claim %d, deleted claim %d, present %d, in-place rehash %d, growth %d", empty, deleted, present, rehash, grow)
			// Every kind must occur, or the churn tested less than it says.
			if empty == 0 || deleted == 0 || present == 0 || rehash == 0 || grow == 0 {
				t.Fatalf("a kind of insert never occurred: %q", line)
			}
		})
	}
}

// collMkBody is the body of the Mk function whose header line starts with
// head in src: from the header to the first line that is a lone "}".
func collMkBody(t *testing.T, src, head string) string {
	t.Helper()
	i := strings.Index(src, "\n"+head)
	if i < 0 {
		t.Fatalf("no %q in the source", head)
	}
	body := src[i+1:]
	j := strings.Index(body, "\n}\n")
	if j < 0 {
		t.Fatalf("%q has no closing brace", head)
	}
	return body[:j+2]
}

// TestStdColl_FreeHasNoWalk: HeapMap.Free is its owner check and
// TableFreeArrays, O(1), with no walk of the table unless a key or value
// type needs release (deep Free; NeedsRelease is a constant per
// instance, so plain maps keep no walk: no hidden cost); TextMap.Free
// walks only while key copies are left to free. A source
// check, because no allocator call or output tells a dead walk apart; the
// timed half is TestStdColl_BenchEmptiedFree (MK_BENCH=1).
func TestStdColl_FreeHasNoWalk(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile(filepath.Join("core", "coll_heap.mk"))
	if err != nil {
		t.Fatal(err)
	}
	src := strings.ReplaceAll(string(b), "\r\n", "\n")
	// The impl line itself says "for": scan the body below it.
	hm := collMkBody(t, src, "impl[K: Hash + Eq, V] Free for HeapMap[K, V] {")
	hm = hm[strings.Index(hm, "\n")+1:]
	guard := "        if NeedsRelease[K]() || NeedsRelease[V]() {\n"
	gi := strings.Index(hm, guard)
	ge := strings.Index(hm[max(gi, 0):], "\n        }\n")
	if gi < 0 || ge < 0 {
		t.Fatalf("HeapMap.Free has no walk guarded by NeedsRelease:\n%s", hm)
	}
	unguarded := hm[:gi] + hm[gi+ge:]
	if strings.Contains(unguarded, "TableNext") || strings.Contains(unguarded, "for ") || !strings.Contains(unguarded, "TableFreeArrays(&t, a)") {
		t.Fatalf("HeapMap.Free walks the table or does not free its arrays:\n%s", hm)
	}
	tm := collMkBody(t, src, "impl[V] Free for TextMap[V] {")
	tm = tm[strings.Index(tm, "\n")+1:]
	if !strings.Contains(tm, "for left > 0 {") || strings.Count(tm, "for ") != 1 || !strings.Contains(tm, "left -= 1") {
		t.Fatalf("TextMap.Free's key walk is not bounded by the keys left:\n%s", tm)
	}
}
