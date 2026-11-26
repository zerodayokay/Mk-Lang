// Why: rt.ObjectWait wraps Windows kernel handles (events, semaphores) via a
// registered wait on the IOCP poller; verifying foreign-thread wakeups,
// cancel-reuse, semaphore credit conservation under cancel races, and timeouts
// requires real Win32 kernel objects.
//
// What: TestRt_ObjectWait, running the four Windows kernel-handle scenarios
// against std/rt/objwait.mk and std/sys/windows/poll_iocp.mk.
//
// How: copies std/ with SetEvent and ResetEvent added to kernel32.def and a
// test file std/rt/tests_objwait_win.mk that exercises ObjectWait under real
// scopes, foreign threads, events and semaphores via `mk test --std std.rt`.

//go:build windows

package std

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const objwaitWinTests = `// Windows kernel-handle tests for rt.ObjectWait (std/rt-objwait_windows_test.go).

import std.core
import std.raw
import std.sys

extern CreateEventW(sa: *u8, manual: i32, initial: i32, name: *u16): uint
extern SetEvent(h: uint): i32
extern ResetEvent(h: uint): i32
extern CreateSemaphoreW(sa: *u8, initial: i32, max: i32, name: *u16): uint
extern ReleaseSemaphore(h: uint, n: i32, previous: *i32): i32
extern WaitForSingleObject(h: uint, ms: u32): u32
extern CloseHandle(h: uint): i32

struct OwThreadArg {
    ev: uint
    delay_ms: i64
}

OwForeignSetEntry(p: *u8) {
    unsafe "p is OwThreadArg from caller, alive until ThreadJoin" {
        arg := raw.PtrCast[u8, OwThreadArg](p)
        sys.SleepNanos((*arg).delay_ms * 1_000_000)
        _ = SetEvent((*arg).ev)
    }
}

OwWaitTask(io: Io, ev: uint, done: &raw.Atomic[u32], out_err: &raw.Atomic[u32], out_fired: &raw.Atomic[u32]) {
    r := ObjectWait(io, ev, -1)
    out_err.Store(u32(r.err))
    out_fired.Store(if r.fired_after_cancel { 1 } else { 0 })
    done.Store(1)
}

OwSemWaitTask(io: Io, sem: uint, acquired: &raw.Atomic[u32], fired_cancel: &raw.Atomic[u32], other_err: &raw.Atomic[u32]) {
    r := ObjectWait(io, sem, -1)
    if r.err == core.SysErr.None && !r.timed_out {
        _ = acquired.Add(1)
    } else if r.err == core.SysErr.Canceled {
        if r.fired_after_cancel {
            _ = fired_cancel.Add(1)
        }
    } else {
        _ = other_err.Add(1)
    }
}

test "RT-ObjectWait-Win foreign thread sets event" (s: &System) {
    unsafe "kernel32 event and ThreadStartJoin" {
        ev := CreateEventW(raw.Null[u8](), 0, 0, raw.Null[u16]())
        Assert(ev != 0)
        var arg := OwThreadArg(ev: ev, delay_ms: 15)
        tr := sys.ThreadStartJoin(OwForeignSetEntry, raw.PtrCast[OwThreadArg, u8](raw.Of(&arg)), 65536)
        Assert(tr.err == core.SysErr.None)
        r := ObjectWait(s.io, ev, 5_000_000_000)
        Assert(r.err == core.SysErr.None && !r.timed_out && !r.fired_after_cancel)
        _ = sys.ThreadJoin(tr.value)
        _ = CloseHandle(ev)
    }
}

test "RT-ObjectWait-Win cancel leaves auto-reset event usable" (s: &System) {
    unsafe "kernel32 auto-reset event" {
        ev := CreateEventW(raw.Null[u8](), 0, 0, raw.Null[u16]())
        Assert(ev != 0)
        done := raw.Atomic[u32].New(0)
        out_err := raw.Atomic[u32].New(99)
        out_fired := raw.Atomic[u32].New(99)
        scope sc := s.io.Scope() {
            sc.go OwWaitTask(s.io, ev, &done, &out_err, &out_fired)
            s.io.Sleep(core.DurationOf(10_000_000)) else {}
            sc.Cancel()
        }
        Assert(done.Load() == 1)
        Assert(out_err.Load() == u32(core.SysErr.Canceled) && out_fired.Load() == 0)
        Assert(WaitForSingleObject(ev, 0) == 258)
        Assert(SetEvent(ev) != 0)
        r := ObjectWait(s.io, ev, 1_000_000_000)
        Assert(r.err == core.SysErr.None && !r.timed_out && !r.fired_after_cancel)
        _ = CloseHandle(ev)
    }
}

test "RT-ObjectWait-Win timeout returns cleanly without consuming signal" (s: &System) {
    unsafe "kernel32 semaphore" {
        sem := CreateSemaphoreW(raw.Null[u8](), 0, 16, raw.Null[u16]())
        Assert(sem != 0)
        r0 := ObjectWait(s.io, sem, 0)
        Assert(r0.err == core.SysErr.None && r0.timed_out && !r0.fired_after_cancel)
        r1 := ObjectWait(s.io, sem, 20_000_000)
        Assert(r1.err == core.SysErr.None && r1.timed_out && !r1.fired_after_cancel)
        Assert(ReleaseSemaphore(sem, 1, raw.Null[i32]()) != 0)
        r2 := ObjectWait(s.io, sem, 1_000_000_000)
        Assert(r2.err == core.SysErr.None && !r2.timed_out && !r2.fired_after_cancel)
        Assert(WaitForSingleObject(sem, 0) == 258)
        _ = CloseHandle(sem)
    }
}

test "RT-ObjectWait-Win semaphore released at moment of cancel reported once" (s: &System) {
    unsafe "kernel32 semaphore cancel race" {
        sem := CreateSemaphoreW(raw.Null[u8](), 0, 1000, raw.Null[u16]())
        Assert(sem != 0)
        acquired := raw.Atomic[u32].New(0)
        fired_cancel := raw.Atomic[u32].New(0)
        other_err := raw.Atomic[u32].New(0)
        rounds := 200
        for round in 0..rounds {
            scope sc := s.io.Scope() {
                sc.go OwSemWaitTask(s.io, sem, &acquired, &fired_cancel, &other_err)
                if round % 4 == 0 {
                    s.io.Sleep(core.DurationOf(1_000_000)) else {}
                } else if round % 4 == 1 {
                    s.io.Yield() else {}
                }
                if round % 2 == 0 {
                    _ = ReleaseSemaphore(sem, 1, raw.Null[i32]())
                    sc.Cancel()
                } else {
                    sc.Cancel()
                    _ = ReleaseSemaphore(sem, 1, raw.Null[i32]())
                }
            }
        }
        Assert(other_err.Load() == 0)
        var remaining: u32 = 0
        for WaitForSingleObject(sem, 0) == 0 {
            remaining += 1
        }
        total := acquired.Load() + fired_cancel.Load() + remaining
        Assert(total == Trunc[u32](rounds))
        _ = CloseHandle(sem)
    }
}
`

func TestRt_ObjectWait(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	def, err := os.ReadFile(filepath.Join("sys", "windows", "kernel32.def"))
	if err != nil {
		t.Fatal(err)
	}
	fixtures := map[string]string{
		"sys/windows/kernel32.def": strings.TrimRight(string(def), "\r\n") + "\nResetEvent\nSetEvent\n",
		"rt/tests_objwait_win.mk":  objwaitWinTests,
	}
	stdDir := copyStd(t, root, fixtures)
	out, status := mkCmd(t, root, stdDir, "test", "--std", "std.rt", "--run", "RT-ObjectWait-Win")
	if status != 0 {
		t.Fatalf("mk test std.rt RT-ObjectWait-Win failed (status %d):\n%s", status, out)
	}
	t.Logf("\n%s", out)
}
