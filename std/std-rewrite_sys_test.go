// Why: a database needs file modes, truncation, file ids, wall time, sleep,
// byte-range locks and the heap's usable size from std/sys and std/rt.
// What: tests of OpenFile mode 2, Truncate, FileId, WallNow, SleepNanos,
// LockRange, UnlockRange and rt.HeapUsable, and TestSTDRW_SysEntriesLinux.
// How: a fixture in std.time reaches std.sys and std.rt (user code cannot)
// and prints one line per check for Go to judge; Linux only with MK_LINUX=1.

package std

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const sysProbeTime = `// Test fixture (std/std-rewrite_sys_test.go): std.sys and std.rt entries.
import std.core
import std.rt
import std.sys

SysOk(r: sys.OsResult): bool {
    return r.err == core.SysErr.None
}

SysOpen(p: str, mode: u32): uint {
    r := sys.OpenFile(p, mode, false)
    Assert(SysOk(r))
    return Trunc[uint](r.value)
}

export SysProbe(sys0: &System): WriteError|ParseError!void {
    var out := sys0.Stdout()
    p := sys0.args[1]
    q := sys0.args[2]
    gowall := try sys0.args[3].ParseInt()
    // Mode 2 creates a missing file, then opens it again without truncating.
    h := SysOpen(p, 2)
    var abc: [3]u8 = [0x61, 0x62, 0x63]
    Assert(SysOk(sys.WriteSync(h, abc[..], 0)))
    _ = sys.Close(h)
    h2 := SysOpen(p, 2)
    size := sys.FileSize(h2).value
    try Print(mut out, "mode2 {size}\n", (size))
    // Truncate shrinks, then extends with zeros.
    Assert(SysOk(sys.Truncate(h2, 1)))
    s1 := sys.FileSize(h2).value
    Assert(SysOk(sys.Truncate(h2, 10)))
    s2 := sys.FileSize(h2).value
    var back: [10]u8 = [9; 10]
    got := sys.ReadSync(h2, mut back[..], 0).value
    var zeros := 0
    for i in 1..10 {
        if back[i] == 0 {
            zeros += 1
        }
    }
    neg := sys.Truncate(h2, -1).err == core.SysErr.None
    try Print(mut out, "truncate {s1} {s2} {got} {zeros} {neg}\n", (s1, s2, got, zeros, neg))
    // FileId: one file through two handles is one id; another file is not.
    h3 := SysOpen(q, 0)
    hq := SysOpen(q, 2)
    var a: [2]u64 = [0; 2]
    var b: [2]u64 = [0; 2]
    var c: [2]u64 = [0; 2]
    Assert(SysOk(sys.FileId(h2, mut a)))
    Assert(SysOk(sys.FileId(h3, mut b)))
    Assert(SysOk(sys.FileId(hq, mut c)))
    same := b[0] == c[0] && b[1] == c[1]
    other := a[0] != b[0] || a[1] != b[1]
    try Print(mut out, "fileid {same} {other}\n", (same, other))
    // Byte-range locks between two handles of one file (each its own open
    // description, as two processes have).
    x := SysOpen(p, 2)
    y := SysOpen(p, 2)
    l1 := sys.LockRange(x, 100, 10, true, false).value
    l2 := sys.LockRange(y, 100, 10, true, false).value
    l3 := sys.LockRange(y, 105, 1, false, false).value
    l4 := sys.LockRange(y, 200, 10, true, false).value
    Assert(SysOk(sys.UnlockRange(x, 100, 10)))
    l5 := sys.LockRange(y, 100, 10, true, false).value
    l6 := sys.LockRange(x, 300, 4, false, false).value
    l7 := sys.LockRange(y, 300, 4, false, false).value
    l8 := sys.LockRange(x, 300, 4, true, false).value
    Assert(SysOk(sys.UnlockRange(y, 100, 10)))
    Assert(SysOk(sys.UnlockRange(y, 200, 10)))
    Assert(SysOk(sys.UnlockRange(y, 300, 4)))
    Assert(SysOk(sys.UnlockRange(x, 300, 4)))
    // A whole-file lock (n 0) excludes every range; Close drops it.
    l9 := sys.LockRange(x, 0, 0, true, false).value
    l10 := sys.LockRange(y, 1 << 40, 1, false, false).value
    _ = sys.Close(x)
    l11 := sys.LockRange(y, 1 << 40, 1, false, false).value
    try Print(mut out, "lock {l1} {l2} {l3} {l4} {l5} {l6} {l7} {l8} {l9} {l10} {l11}\n", (l1, l2, l3, l4, l5, l6, l7, l8, l9, l10, l11))
    _ = sys.Close(y)
    // RW-2: a handle locking bytes it already locks. Linux converts and
    // merges in place; Windows judges the new lock against the handle's
    // own, so Go expects per target. Never with wait true here:
    // on Windows that waits on itself forever.
    hu := SysOpen(p, 2)
    hv := SysOpen(p, 2)
    r1a := sys.LockRange(hu, 0, 1, false, false).value
    r1b := sys.LockRange(hu, 0, 1, true, false).value
    _ = sys.UnlockRange(hu, 0, 1)
    _ = sys.UnlockRange(hu, 0, 1)
    r2a := sys.LockRange(hu, 10, 1, false, false).value
    r2b := sys.LockRange(hu, 10, 1, false, false).value
    _ = sys.UnlockRange(hu, 10, 1)
    r2c := sys.LockRange(hv, 10, 1, true, false).value
    _ = sys.UnlockRange(hv, 10, 1)
    _ = sys.UnlockRange(hu, 10, 1)
    // The rule both targets agree on: unlock, then lock with the new kind.
    r3a := sys.LockRange(hu, 20, 1, false, false).value
    Assert(SysOk(sys.UnlockRange(hu, 20, 1)))
    r3b := sys.LockRange(hu, 20, 1, true, false).value
    r3c := sys.LockRange(hv, 20, 1, false, false).value
    Assert(SysOk(sys.UnlockRange(hu, 20, 1)))
    try Print(mut out, "rw2 {r1a} {r1b} {r2a} {r2b} {r2c} {r3a} {r3b} {r3c}\n", (r1a, r1b, r2a, r2b, r2c, r3a, r3b, r3c))
    // RW-1: a negative size or offset is Other with os 0 and takes no lock
    // on either target, so hv's whole-file lock after it is free.
    n1 := sys.LockRange(hu, 0, -1, true, false)
    n2 := sys.UnlockRange(hu, 0, -1)
    n3 := sys.LockRange(hu, -1, 1, true, false)
    neg1 := n1.err == core.SysErr.Other && n1.os == 0
    neg2 := n2.err == core.SysErr.Other && n2.os == 0
    neg3 := n3.err == core.SysErr.Other && n3.os == 0
    free := sys.LockRange(hv, 0, 0, true, false).value
    _ = sys.UnlockRange(hv, 0, 0)
    try Print(mut out, "rw1 {neg1} {neg2} {neg3} {free}\n", (neg1, neg2, neg3, free))
    _ = sys.Close(hu)
    _ = sys.Close(hv)
    _ = sys.Close(h2)
    _ = sys.Close(h3)
    _ = sys.Close(hq)
    // WallNow is Unix time, near Go's; SleepNanos sleeps at least as asked.
    wall := sys.WallNow()
    var near := wall - gowall
    if near < 0 {
        near = -near
    }
    near60 := near < 60_000_000_000
    // RW-11: slept is judged only under MK_TIMING (Go's timingRun); a
    // plain run checks only that each SleepNanos returns.
    t0 := sys.Now()
    sys.SleepNanos(20_000_000)
    slept := sys.Now() - t0 >= 20_000_000
    sys.SleepNanos(0)
    sys.SleepNanos(-5)
    try Print(mut out, "clock {near60} {slept}\n", (near60, slept))
    // HeapUsable: at least the request, the bound HeapResize grows to in
    // place, and a class size for a small block.
    for n in [1, 16, 17, 100, 8192, 8193, 70000, 2000000] {
        m := rt.HeapAlloc(n, 8)
        u := rt.HeapUsable(m, 8)
        fits := rt.HeapResize(m, n, u, 8)
        over := rt.HeapResize(m, u, u + 1, 8)
        grown := rt.HeapUsable(m, 8)
        rt.HeapFree(m, grown, 8)
        try Print(mut out, "usable {n} {u} {fits} {over} {grown}\n", (n, u, fits, over, grown))
    }
    zero := rt.HeapUsable(rt.HeapAlloc(0, 8), 8)
    try Print(mut out, "usable0 {zero}\n", (zero))
}
`

const sysProbeMain = `import std.time

export Main(sys: &System): WriteError|ParseError!void {
    try time.SysProbe(sys)
}
`

// sysProbeWant are the lines the probe must print, but for the usable
// lines' sizes, which sysUsableOK judges. rw2 differs by target: Linux
// converts and merges a handle's own locks, Windows judges a
// new lock against them. The clock line's last word, SleepNanos(20 ms)
// measured against sys.Now, is a timing assertion: judged only
// under MK_TIMING=1 (tools/gate), else either word passes.
func sysProbeWant(linux bool) []string {
	rw2 := "rw2 1 0 1 1 0 1 1 0"
	if linux {
		rw2 = "rw2 1 1 1 1 1 1 1 0"
	}
	clock := "clock true true"
	if !timingRun() {
		clock = "clock true *"
	}
	return []string{
		"mode2 3",
		"truncate 1 10 10 9 false",
		"fileid true true",
		"lock 1 0 0 1 1 1 1 0 1 0 1",
		rw2,
		"rw1 true true true 1",
		clock,
	}
}

// sysUsableOK checks the class size or span bound and the huge block's
// committed capacity. A failed extra commit leaves the capacity unchanged.
func sysUsableOK(line string, n int) bool {
	f := strings.Fields(line)
	if len(f) != 6 || f[0] != "usable" || f[1] != strconv.Itoa(n) || f[3] != "true" {
		return false
	}
	u, err := strconv.Atoi(f[2])
	grown, growErr := strconv.Atoi(f[5])
	if err != nil || growErr != nil || u < n {
		return false
	}
	small := map[int]int{1: 16, 16: 16, 17: 32, 100: 112, 8192: 8192}
	if want, ok := small[n]; ok && u != want {
		return false
	}
	if n > 1<<20 && f[4] == "true" {
		return grown > u
	}
	return f[4] == "false" && grown == u
}

func sysProbeCheck(t *testing.T, stdout string, linux bool) {
	t.Helper()
	want := sysProbeWant(linux)
	got := strings.Split(strings.TrimSuffix(strings.ReplaceAll(stdout, "\r\n", "\n"), "\n"), "\n")
	if len(got) != len(want)+9 {
		t.Fatalf("%d lines, want %d:\n%s", len(got), len(want)+9, stdout)
	}
	for i, w := range want {
		if pre, ok := strings.CutSuffix(w, " *"); ok {
			// The last word is free: same prefix, same word count.
			if !strings.HasPrefix(got[i], pre+" ") || len(strings.Fields(got[i])) != len(strings.Fields(w)) {
				t.Errorf("line %d: got %q, want %q", i, got[i], w)
			}
			continue
		}
		if got[i] != w {
			t.Errorf("line %d: got %q, want %q", i, got[i], w)
		}
	}
	for i, n := range []int{1, 16, 17, 100, 8192, 8193, 70000, 2000000} {
		if line := got[len(want)+i]; !sysUsableOK(line, n) {
			t.Errorf("bad HeapUsable line for %d bytes: %q", n, line)
		}
	}
	if last := got[len(got)-1]; last != "usable0 0" {
		t.Errorf("got %q, want usable0 0", last)
	}
}

func TestSTDRW_SysEntries(t *testing.T) { sysEntries(t, hostIo) }

// TestSTDRW_SysEntriesLinux is the same on x86_64-linux (MK_LINUX=1, under
// the WSL lock, -p 1).
func TestSTDRW_SysEntriesLinux(t *testing.T) {
	stdioNeedWSL(t)
	sysEntries(t, linuxIo)
}

func sysEntries(t *testing.T, h ioHost) {
	exe := h.build(t, map[string]string{"time/zsysprobe.mk": sysProbeTime}, sysProbeMain)
	dir := filepath.Dir(exe)
	p, q := filepath.Join(dir, "a.db"), filepath.Join(dir, "b.db")
	if h.linux {
		p, q = "a.db", "b.db" // the batch runs in the program's directory
	}
	os.Remove(filepath.Join(dir, "a.db"))
	if err := os.WriteFile(filepath.Join(dir, "b.db"), []byte("other"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := h.runMany(t, exe, []ioCall{{args: []string{p, q, strconv.FormatInt(time.Now().UnixNano(), 10)}}})[0]
	if r.code != 0 {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	sysProbeCheck(t, r.stdout, h.linux)
}
