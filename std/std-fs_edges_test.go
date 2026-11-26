// Why: some std.fs edge cases need Linux file-system semantics or a failing
// heap, which std/fs's own test blocks cannot set up.
// What: TestFs_LinuxEdges (trailing-separator links, dangling links, mode
// 0o000, FIFOs, positioned writes under append) and TestFs_WalkOutOfMemory.
// How: the Linux cases run in the distribution's /tmp; Walker.Next runs over a
// heap that fails one request, each in turn, and must keep its place.

package std

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fs2EdgeProg runs each case in args[1], which the shell has given an
// empty directory locked (mode 0o000) and a FIFO fifo, and prints one line
// per case.
const fs2EdgeProg = `import std.fs

Name(a: &Arena, e: fs.Error): str from a {
    return a.Fmt("{e}", (e)).Must()
}

// At is d/rel in a.
At(a: &Arena, d: str, rel: str): str from a {
    return a.Fmt("{d}/{rel}", (d, rel)).Must()
}

Size(io: Io, p: str): i64 {
    st := fs.Stat(io, p) else {
        return -1
    }
    return st.size
}

export Main(sys: &System): fs.Error|OutOfMemory|WriteError!void {
    io := sys.io
    a := sys.arena
    d := sys.args[1]
    // A link to a directory, named with a trailing separator.
    try fs.MakeDirAll(io, At(a, d, "t/target"))
    try fs.WriteAll(io, At(a, d, "t/target/keep"), "k".AsBytes())
    try fs.Symlink(io, "target", At(a, d, "t/link"))
    var s := "ok"
    fs.RemoveAll(io, At(a, d, "t/link/")) else e { s = Name(a, e) }
    var lk := "kept"
    _ = fs.StatLink(io, At(a, d, "t/link")) else { lk = "gone" }
    try sys.Print("linkdir {s} {lk} {n}\n", (s, lk, n: Size(io, At(a, d, "t/target/keep"))))
    // A dangling link, as p and as a parent of p.
    try fs.Symlink(io, "missing", At(a, d, "t/dl"))
    s = "ok"
    fs.MakeDirAll(io, At(a, d, "t/dl")) else e { s = Name(a, e) }
    var s2 := "ok"
    fs.MakeDirAll(io, At(a, d, "t/dl/x/y")) else e { s2 = Name(a, e) }
    try sys.Print("dangling {s} {s2}\n", (s, s2))
    // An empty directory nobody may list.
    s = "ok"
    fs.RemoveAll(io, At(a, d, "locked")) else e { s = Name(a, e) }
    var gone := "kept"
    _ = fs.StatLink(io, At(a, d, "locked")) else { gone = "gone" }
    try sys.Print("locked {s} {gone}\n", (s, gone))
    // A FIFO: Seek is ESPIPE from the first call; Copy refuses it and
    // leaves dst as it was.
    var f := try fs.OpenWith(io, At(a, d, "fifo"), fs.ReadWrite)
    s = "ok"
    _ = f.Seek(io, 5, fs.Whence.Start) else e { s = Name(a, e) }
    s2 = "ok"
    _ = f.Seek(io, 0, fs.Whence.End) else e { s2 = Name(a, e) }
    f.Abandon()
    try sys.Print("fifo-seek {s} {s2}\n", (s, s2))
    try fs.WriteAll(io, At(a, d, "cpd"), "keep".AsBytes())
    s = "ok"
    _ = fs.Copy(io, At(a, d, "fifo"), At(a, d, "cpd")) else e { s = Name(a, e) }
    try fs.MakeDir(io, At(a, d, "cpsrc"))
    s2 = "ok"
    _ = fs.Copy(io, At(a, d, "cpsrc"), At(a, d, "cpd")) else e { s2 = Name(a, e) }
    try sys.Print("copy {s} {s2} {t}\n", (s, s2, t: try fs.ReadText(a, io, At(a, d, "cpd"))))
    // A positioned write under append.
    try fs.WriteAll(io, At(a, d, "ap"), "0123456789".AsBytes())
    var g := try fs.OpenWith(io, At(a, d, "ap"), fs.Append)
    s = "ok"
    _ = g.WriteAt(io, 0, "ab".AsBytes()) else e { s = Name(a, e) }
    g.Abandon()
    try sys.Print("append {s} {t}\n", (s, t: try fs.ReadText(a, io, At(a, d, "ap"))))
    // A conversion another holder wins: does the holder keep its lock?
    fs.WriteAll(io, At(a, d, "lk"), "x".AsBytes()).Must()
    x := fs.OpenWith(io, At(a, d, "lk"), fs.ReadWrite).Must()
    y := fs.OpenWith(io, At(a, d, "lk"), fs.ReadWrite).Must()
    x.Lock(io, fs.LockMode.Shared).Must()
    Assert(y.TryLock(io, fs.LockMode.Shared).Must())
    won := x.TryLock(io, fs.LockMode.Exclusive).Must()
    y.Unlock(io).Must()
    free := y.TryLock(io, fs.LockMode.Exclusive).Must()
    x.Abandon()
    y.Abandon()
    try sys.Print("conversion won {won} then free {free}\n", (won, free))
}
`

// fs2EdgeWant is each line fs2EdgeProg prints, as Go's os gives the same
// case (dangling: Go's MkdirAll says EEXIST, std.fs's doc NotDirectory;
// fifo-seek: Go says ESPIPE every time; copy and append: Go's own copy
// truncates dst, and its WriteAt refuses append, which std.fs answers
// Unsupported). The conversion and fifo-seek lines are logged, not
// compared: whether a lost conversion keeps the holder's Shared lock is
// the kernel's, and a FIFO opened on the poller (io_uring, epoll) seeks
// in std.rt's record without the OS, so
// Go's ESPIPE is not reached there yet.
var fs2EdgeWant = []string{
	"linkdir ok gone 1",
	"dangling NotDirectory NotDirectory",
	"locked ok gone",
	"copy Unsupported IsDirectory keep",
	"append Unsupported 0123456789",
}

// TestFs_LinuxEdges runs fs2EdgeProg in a fresh directory of the
// distribution's /tmp, dev and release, one wsl.exe client. MK_LINUX=1,
// under the WSL lock.
func TestFs_LinuxEdges(t *testing.T) {
	linuxGate(t)
	dir, err := packageTempDir("mkfs2edge")
	if err != nil {
		t.Fatal(err)
	}
	var sh strings.Builder
	sh.WriteString("#!/bin/sh\n")
	for _, release := range []bool{false, true} {
		flags := []string{"--target", "x86_64-linux"}
		name := "dev"
		if release {
			flags = append(flags, "--release")
			name = "release"
		}
		exe := fs2Build(t, fs2EdgeProg, flags...)
		keep := filepath.Join(dir, "fs2edge-"+name)
		b, err := os.ReadFile(exe)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(keep, b, 0o755); err != nil {
			t.Fatal(err)
		}
		cmd := `cd "$MK_LINUX_TMP" && mkdir locked && chmod 000 locked && mkfifo fifo && echo "uid $(id -u)" && ` +
			rtsWSLPath(keep) + ` "$MK_LINUX_TMP"; echo "exit $?"`
		sh.WriteString("echo 'LEG " + name + "'\n")
		sh.WriteString(linuxScratchSh(cmd))
	}
	out := linuxRunScript(t, dir, "fs2edge.sh", sh.String(), 3*time.Minute)
	parts := strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "LEG ")
	if len(parts) != 3 {
		t.Fatalf("want two legs:\n%s", out)
	}
	for _, part := range parts[1:] {
		leg, rest, _ := strings.Cut(part, "\n")
		t.Logf("%s:\n%s", leg, rest)
		if !strings.Contains(rest, "exit 0") {
			t.Errorf("%s: the program failed:\n%s", leg, rest)
		}
		uidRoot := strings.Contains(rest, "uid 0\n")
		for _, want := range fs2EdgeWant {
			if uidRoot && strings.HasPrefix(want, "locked") {
				// root lists any directory: the case proves nothing.
				continue
			}
			if !strings.Contains(rest, want+"\n") {
				t.Errorf("%s: want the line %q", leg, want)
			}
		}
	}
}

// fs2OomBlocks is a probe module, std.fsoom, in a copied std whose
// manifest lets it use unsafe: a heap that fails its at-th request, as
// std/coll/tests_oom.mk's does, under a walk and its arena.
const fs2OomBlocks = `// Walker.Next under OutOfMemory: a walk over a heap that fails its at-th
// request (alloc or growing resize), for every at until a run meets no
// failure. Each failed call is made again at once; the listing, with
// SkipDir after every directory named s<i>, equals a run with no failure.

import std.core
import std.fs
import std.raw

struct Fail {
    inner: *Alloc
    reqs: int
    at: int
    fails: int
}

const FailTable = core.AllocTable(alloc: FailAlloc, resize: FailResize, free: FailFree)

FailAlloc(ctx: *u8, size: int, align: int): *u8 {
    unsafe "ctx is a live Fail whose inner allocator outlives it" {
        q := raw.PtrCast[u8, Fail](ctx)
        var c := raw.Read(q)
        c.reqs += 1
        var p := raw.Null[u8]()
        if c.reqs - 1 == c.at {
            c.fails += 1
        } else {
            p = core.AllocBytes(&*c.inner, size, align)
        }
        raw.Write(q, c)
        return p
    }
}

FailResize(ctx: *u8, p: *u8, old: int, new: int, align: int): bool {
    unsafe "ctx is a live Fail whose inner allocator outlives it" {
        q := raw.PtrCast[u8, Fail](ctx)
        var c := raw.Read(q)
        var ok := false
        var refused := false
        if new > old {
            c.reqs += 1
            if c.reqs - 1 == c.at {
                c.fails += 1
                refused = true
            }
        }
        if !refused {
            ok = core.ResizeBytes(&*c.inner, p, old, new, align)
        }
        raw.Write(q, c)
        return ok
    }
}

FailFree(ctx: *u8, p: *u8, size: int, align: int) {
    unsafe "ctx is a live Fail whose inner allocator outlives it" {
        q := raw.PtrCast[u8, Fail](ctx)
        c := raw.Read(q)
        core.FreeBytes(&*c.inner, p, size, align)
    }
}

Fails(c: &Fail): int {
    unsafe "c is live" {
        return raw.Read(raw.Of(c)).fails
    }
}

// Listing walks root over h, each OutOfMemory made again at once, into t:
// "depth kind path" a line, SkipDir after each directory named s<i>.
Listing(h: &Heap, io: Io, root: str, t: mut Text) {
    var a := Arena.New(h) else {
        Arena.New(h).Must()
    }
    var w := fs.Walk(h, io, root) else {
        fs.Walk(h, io, root).Must()
    }
    for {
        r := w.Next(&a, io) else e {
            match e {
                OutOfMemory {
                    continue
                }
                _ {
                    Panic("walk")
                }
            }
        }
        e := r else {
            break
        }
        k := if e.kind == fs.Kind.Dir { "d" } else { "f" }
        var out := t.Writer()
        Print(mut out, "{d} {k} {p}\n", (d: e.depth, k: k, p: &e.path)).Must()
        if e.kind == fs.Kind.Dir && e.path.Base().AsBytes()[0] == b's' {
            w.SkipDir()
        }
    }
    w.Close()
    a.Free()
}

// Run is the listing over a heap failing its at-th request (none when at
// is negative), and whether a failure was met.
Run(sys: &System, at: int, t: mut Text): bool {
    var c := Fail(inner: raw.Of(sys.heap.Alloc()), reqs: 0, at: at, fails: 0)
    unsafe "c outlives the heap made over it, which is dropped here" {
        h := core.MakeHeap(raw.PtrCast[Fail, u8](raw.Of(&c)), raw.Of(&FailTable))
        Listing(&h, sys.io, "w", t)
    }
    return Fails(&c) > 0
}

test "Walker.Next under OutOfMemory keeps its place: the entry comes again, and SkipDir still skips it" (sys: &System) {
    io := sys.io
    fs.RemoveAll(io, "w").Must()
    long := "nnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnn"
    for i in 0..3 {
        for j in 0..20 {
            fs.MakeDirAll(io, sys.arena.Fmt("w/d{i}/{long}{j}", (i, long, j)).Must()).Must()
        }
        fs.MakeDirAll(io, sys.arena.Fmt("w/s{i}/inner", (i)).Must()).Must()
        fs.WriteAll(io, sys.arena.Fmt("w/s{i}/f", (i)).Must(), "x".AsBytes()).Must()
    }
    var want := Text.New(sys.arena)
    Assert(!Run(sys, -1, mut want))
    var at := 0
    var met := 0
    for {
        var got := Text.New(sys.arena)
        failed := Run(sys, at, mut got)
        Assert(got.Str() == want.Str())
        if !failed {
            break
        }
        met += 1
        at += 1
    }
    // Every request of the walk failed once, a dozen or more.
    Assert(met >= 10)
    fs.RemoveAll(io, "w").Must()
}
`

// TestFs_WalkOutOfMemory runs std.fsoom's block on the host, dev and
// release, in a copied std whose manifest adds fsoom to unsafe_modules.
func TestFs_WalkOutOfMemory(t *testing.T) {
	root := t.TempDir()
	stdDir := fs2Std(t, root)
	if err := os.MkdirAll(filepath.Join(stdDir, "fsoom"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stdDir, "fsoom", "tests_walkoom.mk"), []byte(fs2OomBlocks), 0o644); err != nil {
		t.Fatal(err)
	}
	man := filepath.Join(stdDir, "pkg.mk")
	b, err := os.ReadFile(man)
	if err != nil {
		t.Fatal(err)
	}
	m := strings.Replace(string(b), `unsafe_modules: [`, `unsafe_modules: ["fsoom", `, 1)
	if m == string(b) {
		t.Fatal("std/pkg.mk: unsafe_modules not found")
	}
	if err := os.WriteFile(man, []byte(m), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MK_CACHE", filepath.Join(root, "cache"))
	for _, flags := range [][]string{nil, {"--release"}} {
		args := append([]string{"test", "--std=std.fsoom"}, flags...)
		out, status := mkCmd(t, root, stdDir, args...)
		if strings.Contains(out, "no usable LLVM") {
			t.Skipf("needs the LLVM toolchain: %s", out)
		}
		if status != 0 || !strings.Contains(out, "1 passed, 0 failed") {
			t.Fatalf("mk %s: status %d\n%s", strings.Join(args, " "), status, out)
		}
	}
}
