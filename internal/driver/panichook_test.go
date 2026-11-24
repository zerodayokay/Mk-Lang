// Why: a hosted program or library may define MkPanicHook to see a panic before
// the runtime reports it, and the hook's call order and arguments are part of
// the runtime's contract.
// What: TestPanicHook_Hosted, TestPanicHook_SuspendInCallback and
// TestPanicHook_LibAfterStop, with their sample programs and C hosts.
// How: builds Windows programs and a library whose hook writes the PanicInfo
// through Win32, runs them in dev and release, and matches stderr and the exit
// status 101.

package driver

import (
	"context"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"mk/internal/types"
)

// hookProgram defines the hosted MkPanicHook: it writes the
// PanicInfo it receives to stderr, through Win32 (the package's own
// unsafe module), then returns.
const hookProgram = `import std.runtime
import std.raw

extern GetStdHandle(n: u32): *u8
extern WriteFile(h: *u8, buf: *u8, n: u32, done: *u32, ov: *u8): i32

Say(s: str) {
    var done: u32 = 0
    unsafe "a console write of s's bytes" {
        _ = WriteFile(GetStdHandle(0xFFFFFFF4), raw.StrData(s), Trunc[u32](s.Len()), raw.Of(&done), raw.Null[u8]())
    }
}

export extern MkPanicHook(info: &runtime.PanicInfo) {
    var buf: [32]u8 = [0; 32]
    Say("hook ")
    Say(info.message)
    Say(" ")
    Say(info.file)
    if info.code == 2 && info.line == 30 && info.column == 9 {
        Say(" code 2 at 30:9")
    }
    Say("\n")
    _ = buf
}

export Main(sys: &System): void {
    xs := [1, 2, 3]
    i := sys.args.Len() + 5
    _ = xs[i]
}
`

// TestPanicHook_Hosted: a hosted program's MkPanicHook is called once,
// before the report, with the panic's code, message and site; then the
// report is written and the process exits 101. A program without one gets
// the plain report (the support pack's weak default returns at once): every
// other panic test.
func TestPanicHook_Hosted(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("SKIP: the hook writes through Win32; linux.sh runs the Linux leg")
	}
	tools := needTools(t)
	root := writeTree(t, map[string]string{
		"pkg.mk": manifest(map[string]string{"name": `"hook"`,
			"bins":           `[pkg.Bin(name: "hook", root: ".", link_script: "", cpu: "", subsystem: "", kind: "", abi: "")]`,
			"unsafe_modules": `["."]`}),
		"main.mk": hookProgram,
	})
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		s, err := NewSession(Config{Root: root, Target: types.X86_64Windows, Profile: prof, Tools: tools, CacheDir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		r, paths, err := s.Build(filepath.Join(t.TempDir(), "hook.exe"))
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Report.Diags) > 0 || len(paths) != 1 {
			t.Fatalf("build: %d paths\n%s", len(paths), r.Report.Text(r.Sources))
		}
		_, errOut, code := runExe(t, types.X86_64Windows, paths[0], "")
		want := regexp.MustCompile(`^hook bounds main\.mk code 2 at 30:9\npanic: bounds at main\.mk:30:9\n$`)
		if code != 101 || !want.MatchString(dropBacktrace(errOut)) || strings.Count(errOut, "hook ") != 1 {
			t.Errorf("%v: exit %d, stderr %q", prof, code, errOut)
		}
	}
}

const suspendHookC = `#include <stdint.h>
extern void SuspendCb(uintptr_t arg);
void call_cb(uintptr_t arg) {
    SuspendCb(arg);
}
`

const suspendHookProgram = `import std.raw
import std.runtime
import std.time

extern GetStdHandle(n: u32): *u8
extern WriteFile(h: *u8, buf: *u8, n: u32, done: *u32, ov: *u8): i32
extern call_cb(arg: uint)

Say(s: str) {
    var done: u32 = 0
    unsafe "a console write of s's bytes" {
        _ = WriteFile(GetStdHandle(0xFFFFFFF4), raw.StrData(s), Trunc[u32](s.Len()), raw.Of(&done), raw.Null[u8]())
    }
}

export extern MkPanicHook(info: &runtime.PanicInfo) {
    Say("hook ")
    Say(info.message)
    if info.code == 16 {
        Say(" code 16")
    }
    Say("\n")
}

export extern SuspendCb(io_addr: uint) {
    unsafe "io_addr points to io in Main while call_cb runs" {
        io := raw.Read(raw.FromAddr[Io](io_addr))
        _ = io.Sleep(time.Millis(1)) else {}
    }
}

export Main(sys: &System): void {
    var io := sys.io
    unsafe "io outlives call_cb, which runs SuspendCb on the carrier system stack" {
        call_cb(raw.Addr(raw.Of(&io)))
    }
}
`

// TestPanicHook_SuspendInCallback: suspending inside a callback called from an
// extern C frame passes PanicSuspendInCallback (16) to MkPanicHook.
func TestPanicHook_SuspendInCallback(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("SKIP: the hook writes through Win32")
	}
	tools := needTools(t)
	root := writeTree(t, map[string]string{
		"pkg.mk": manifest(map[string]string{
			"name":           `"hook16"`,
			"bins":           `[pkg.Bin(name: "hook16", root: ".", link_script: "", cpu: "", subsystem: "", kind: "", abi: "")]`,
			"c_sources":      `[pkg.CSource(path: "cb.c", flags: [])]`,
			"unsafe_modules": `["."]`,
		}),
		"cb.c":    suspendHookC,
		"main.mk": suspendHookProgram,
	})
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		s, err := NewSession(Config{Root: root, Target: types.X86_64Windows, Profile: prof, Tools: tools, CacheDir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		r, paths, err := s.Build(filepath.Join(t.TempDir(), "hook16.exe"))
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Report.Diags) > 0 || len(paths) != 1 {
			t.Fatalf("build: %d paths\n%s", len(paths), r.Report.Text(r.Sources))
		}
		_, errOut, code := runExe(t, types.X86_64Windows, paths[0], "")
		want := regexp.MustCompile(`^hook suspend in callback code 16\npanic: suspend in callback\n$`)
		if code != 101 || !want.MatchString(dropBacktrace(errOut)) || strings.Count(errOut, "hook ") != 1 {
			t.Errorf("%v: exit %d, stderr %q", prof, code, errOut)
		}
	}
}

const libAfterStopProgram = `import std.raw
import std.runtime

extern GetStdHandle(n: u32): *u8
extern WriteFile(h: *u8, buf: *u8, n: u32, done: *u32, ov: *u8): i32

Say(s: str) {
    var done: u32 = 0
    unsafe "a console write of s's bytes" {
        _ = WriteFile(GetStdHandle(0xFFFFFFF4), raw.StrData(s), Trunc[u32](s.Len()), raw.Of(&done), raw.Null[u8]())
    }
}

export extern MkPanicHook(info: &runtime.PanicInfo) {
    Say("hook ")
    Say(info.message)
    if info.code == 16 {
        Say(" code 16")
    }
    Say("\n")
}

export extern Ping(): i32 {
    return 1
}
`

const libAfterStopC = `#include "libhook16.h"
__declspec(dllimport) void __stdcall ExitProcess(unsigned code);
void start(void) {
    if (libhook16_mk_start(0) != 0) ExitProcess(10);
    if (Ping() != 1) ExitProcess(11);
    if (libhook16_mk_stop() != 0) ExitProcess(12);
    Ping();
    ExitProcess(0);
}
`

// TestPanicHook_LibAfterStop: a call into a library after its stop passes
// PanicSuspendInCallback (16) and the formatted post-stop message to
// MkPanicHook in dev and release.
func TestPanicHook_LibAfterStop(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("SKIP: the hook writes through Win32")
	}
	tools := needTools(t)
	root := writeTree(t, map[string]string{
		"pkg.mk": manifest(map[string]string{
			"name":           `"libhook16"`,
			"bins":           `[` + libBin("libhook16", ".", "dynamic", "") + `]`,
			"unsafe_modules": `["."]`,
		}),
		"lib.mk": libAfterStopProgram,
	})
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		t.Run(prof.String(), func(t *testing.T) {
			k32 := filepath.Join(t.TempDir(), "k32.lib")
			if err := tools.ImportLib(context.Background(), "kernel32.dll", []string{"ExitProcess"}, "x64", k32); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			buildLib(t, root, types.X86_64Windows, prof, dir)
			exe := clangHost(t, tools, dir, "afterstop", libAfterStopC, filepath.Join(dir, "libhook16.lib"), k32)
			_, errOut, code := runExe(t, types.X86_64Windows, exe, "")
			want := regexp.MustCompile(`^hook suspend in callback: a call after the library's stop code 16\npanic: suspend in callback: a call after the library's stop\n$`)
			if code != 101 || !want.MatchString(dropBacktrace(errOut)) || strings.Count(errOut, "hook ") != 1 {
				t.Errorf("%v: exit %d, stderr %q", prof, code, errOut)
			}
		})
	}
}
