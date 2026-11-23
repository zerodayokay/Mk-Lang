// Why: preserve_nonecc tail-dispatch table cloning must work end to end
// through the full Mk pipeline (front end, mono, codegen, ThinLTO, linker,
// native execution, debug info and incremental pack caching).
//
// What: TestBecome_PreserveNoneThreadedInterpreter (8-parameter threaded
// bytecode interpreter running 200,000+ become dispatches with constant
// stack, outside direct and table entry, direct become to a helper handler,
// cold C-ABI call, re-entrant table call, IR checks, dev/release/--debug
// execution) and TestBecome_PreserveNoneCrossPackCache (cross-pack
// invalidation verified under MK_CACHE_VERIFY).
//
// How: builds real Mk programs via NewSession, inspects emitted release IR
// via TestHooks.OnLL, runs the linked executables natively, and checks warm
// incremental builds against cold verification builds via pkVerify.

package driver

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"mk/internal/types"
)

const pnoneInterpreterSrc = `// Eight-parameter threaded bytecode interpreter exercising preserve_nonecc
// tail-dispatch tables, direct become closure, cold C-ABI helpers, outside
// C-ABI entry (direct and via table), re-entrant table calls and constant
// stack depth across 200,000+ dispatches.

Low(lo: u64, sp: u64): u64 {
    if sp < lo {
        return sp
    }
    return lo
}

Top(top: u64, lo: u64, steps: int): u64 {
    if top == 0 && steps <= 199000 {
        return lo
    }
    return top
}

SlowFinish(acc: int, r0: int, lo: u64, top: u64): int {
    if lo != top {
        return -1
    }
    return acc + r0
}

OpHalt(steps: int, acc: int, r0: int, r1: int, r2: int, r3: int, lo: u64, top: u64): int {
    return SlowFinish(acc + r1 + r2 + r3, r0, lo, top)
}

OpAdd(steps: int, acc: int, r0: int, r1: int, r2: int, r3: int, lo: u64, top: u64): int {
    var sp: u64 = 0
    unsafe "reads the stack pointer" {
        asm("mov {sp}, rsp", out(reg) sp, options(nomem, nostack, preserves_flags))
    }
    nlo := Low(lo, sp)
    ntop := Top(top, nlo, steps)
    var bonus: int = 0
    if steps == 100000 {
        // Re-entrant regular call through the C-ABI table inside a preserve_nonecc handler.
        bonus = Handlers[0](0, 5, 1, 2, 3, 4, 7, 7)
    }
    if steps == 0 {
        become Handlers[0](0, acc + bonus, r0, r1, r2, r3, nlo, ntop)
    }
    next := (steps % 3) + 1
    become Handlers[next](steps - 1, acc + r0 + bonus, r1, r2, r3, r0, nlo, ntop)
}

OpRotate(steps: int, acc: int, r0: int, r1: int, r2: int, r3: int, lo: u64, top: u64): int {
    var sp: u64 = 0
    unsafe "reads the stack pointer" {
        asm("mov {sp}, rsp", out(reg) sp, options(nomem, nostack, preserves_flags))
    }
    nlo := Low(lo, sp)
    ntop := Top(top, nlo, steps)
    if steps == 0 {
        become Handlers[0](0, acc, r0, r1, r2, r3, nlo, ntop)
    }
    next := (steps % 3) + 1
    become Handlers[next](steps - 1, acc + r1, r2, r3, r0, r1, nlo, ntop)
}

OpJumpDirect(steps: int, acc: int, r0: int, r1: int, r2: int, r3: int, lo: u64, top: u64): int {
    if steps == 0 {
        become OpHalt(0, acc, r0, r1, r2, r3, lo, top)
    }
    become OpMerge(steps - 1, acc + r2, r3, r0, r1, r2, lo, top)
}

// OpMerge is not in Handlers; direct become from OpJumpDirect pulls it into
// the preserve_nonecc set so musttail calling conventions match.
OpMerge(steps: int, acc: int, r0: int, r1: int, r2: int, r3: int, lo: u64, top: u64): int {
    var sp: u64 = 0
    unsafe "reads the stack pointer" {
        asm("mov {sp}, rsp", out(reg) sp, options(nomem, nostack, preserves_flags))
    }
    nlo := Low(lo, sp)
    ntop := Top(top, nlo, steps)
    if steps == 0 {
        become Handlers[0](0, acc, r0, r1, r2, r3, nlo, ntop)
    }
    become Handlers[1](steps - 1, acc + r3, r0, r1, r2, r3, nlo, ntop)
}

const Handlers: [4]fn(int, int, int, int, int, int, u64, u64): int = [OpHalt, OpAdd, OpRotate, OpJumpDirect]

EnterTable(op: int, steps: int): int {
    max: u64 = 18446744073709551615
    return Handlers[op](steps, 0, 1, 2, 3, 4, max, 0)
}

EnterDirect(steps: int): int {
    max: u64 = 18446744073709551615
    return OpAdd(steps, 0, 1, 2, 3, 4, max, 0)
}

export Main(sys: &System): WriteError!void {
    a := EnterTable(1, 200000)
    b := EnterDirect(200000)
    try sys.Print("table {a} direct {b}\n", (a: a, b: b))
}
`

const pnoneManifest = `const Package = pkg.Package(
    name: "pnone",
    bins: [pkg.Bin(name: "interp", root: ".", link_script: "", cpu: "", subsystem: "console")],
    deps: [],
    c_sources: [],
    c_imports: [],
    asm_sources: [],
    targets: [],
    unsafe_modules: ["."],
    links: [],
)
`

func TestBecome_PreserveNoneThreadedInterpreter(t *testing.T) {
	tools := needTools(t)
	dir := writeTree(t, map[string]string{
		"pkg.mk":  pnoneManifest,
		"main.mk": pnoneInterpreterSrc,
	})
	target := HostTarget()
	if target.Arch != types.ArchX64 {
		t.Skip("inline rsp probe uses x86_64 syntax")
	}

	var releaseOut string
	for _, tc := range []struct {
		name      string
		profile   types.Profile
		debug     bool
		wantPnone bool
	}{
		{"release", types.Release, false, true},
		{"dev", types.Dev, false, false},
		{"release-debug", types.Release, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var ll strings.Builder
			hooks := TestHooks{OnLL: func(name, text string) {
				mu.Lock()
				ll.WriteString(text)
				mu.Unlock()
			}}
			s, err := NewSession(Config{
				Root: dir, Target: target, Profile: tc.profile, Debug: tc.debug,
				Tools: tools, CacheDir: t.TempDir(), TestHooks: hooks,
			})
			if err != nil {
				t.Fatal(err)
			}
			exe := filepath.Join(t.TempDir(), "interp"+exeSuffix(target))
			r, paths, err := s.Build(exe)
			if err != nil || len(r.Report.Diags) > 0 || len(paths) != 1 {
				t.Fatalf("build: %v\n%s", err, r.Report.Text(r.Sources))
			}
			ir := ll.String()
			if tc.wantPnone {
				for _, want := range []string{
					`preserve_nonecc noundef i64 @mk.main.OpAdd$pnone(`,
					`preserve_nonecc noundef i64 @mk.main.OpRotate$pnone(`,
					`preserve_nonecc noundef i64 @mk.main.OpJumpDirect$pnone(`,
					`preserve_nonecc noundef i64 @mk.main.OpHalt$pnone(`,
					`preserve_nonecc noundef i64 @mk.main.OpMerge$pnone(`,
					`@mk.main.Handlers$pnone = private unnamed_addr constant [4 x ptr]`,
					`musttail call preserve_nonecc noundef i64 %`,
					`musttail call preserve_nonecc noundef i64 @mk.main.OpMerge$pnone(`,
					`call preserve_nonecc noundef i64 @mk.main.OpAdd$pnone(`,
				} {
					if !strings.Contains(ir, want) {
						t.Errorf("release IR lacks %q", want)
					}
				}
				if strings.Contains(ir, "mk.main.SlowFinish$pnone") {
					t.Error("cold helper SlowFinish should not be cloned to $pnone")
				}
			} else {
				if strings.Contains(ir, "preserve_nonecc") || strings.Contains(ir, "$pnone") {
					t.Errorf("%s: emitted preserve_nonecc unexpectedly", tc.name)
				}
			}
			out, errOut, code := runExe(t, target, paths[0], "")
			if code != 0 {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errOut)
			}
			// Both table entry and direct entry start identically and must report identical non-negative results.
			if !strings.HasPrefix(out, "table ") || strings.Contains(out, "-1") {
				t.Fatalf("unexpected output (stack grew or wrong result): %q", out)
			}
			fields := strings.Fields(out)
			if len(fields) != 4 || fields[1] != fields[3] {
				t.Fatalf("table entry (%s) != direct entry (%s) in %q", fields[1], fields[3], out)
			}
			if tc.name == "release" {
				releaseOut = out
			} else if releaseOut != "" && out != releaseOut {
				t.Errorf("%s output %q != release output %q", tc.name, out, releaseOut)
			}
		})
	}
}

// TestBecome_PreserveNoneCrossPackCache verifies that when module app holds a
// private dispatch table referencing exported handlers in module lib, escaping
// the table in app invalidates lib's cached pack key in release mode (even
// though lib's source is unchanged) and passes MK_CACHE_VERIFY byte-for-byte.
func TestBecome_PreserveNoneCrossPackCache(t *testing.T) {
	tools := needTools(t)
	base := pkFiles()
	base["lib/lib.mk"] += "\nexport Op0(n: int, a: int, b: int, c: int, d: int, e: int, f: int, g: int): int {\n" +
		"    if n == 0 {\n" +
		"        return a + b + c + d + e + f + g\n" +
		"    }\n" +
		"    become Op0(n - 1, a + 1, b, c, d, e, f, g)\n" +
		"}\n"
	app := base["app/app.mk"]
	app = strings.Replace(app, "import shape\n", "import shape\n\n"+
		"Op1(n: int, a: int, b: int, c: int, d: int, e: int, f: int, g: int): int {\n"+
		"    if n == 0 {\n"+
		"        return a + b + c + d + e + f + g\n"+
		"    }\n"+
		"    become Handlers[n & 1](n - 1, a + 1, b, c, d, e, f, g)\n"+
		"}\n\n"+
		"const Handlers: [2]fn(int, int, int, int, int, int, int, int): int = [lib.Op0, Op1]\n\n"+
		"Touch(t: &[2]fn(int, int, int, int, int, int, int, int): int): int {\n"+
		"    return t.Len()\n"+
		"}\n\n"+
		"RunInterp(): int {\n"+
		"    // HOOK\n"+
		"    return Op1(50, 0, 1, 2, 3, 4, 5, 6) - 65\n"+
		"}\n", 1)
	app = strings.Replace(app, "lib.Counter - 26", "lib.Counter + RunInterp() - 26", 1)
	base["app/app.mk"] = app
	dir := writeTree(t, base)
	out := t.TempDir()
	pkWrite(t, dir, base)
	cfg := Config{Root: dir, Target: pkTarget(), Profile: types.Release, Tools: tools, CacheDir: filepath.Join(t.TempDir(), "cache")}
	cold := pkBuild(t, cfg, filepath.Join(out, "cold"+exeSuffix(pkTarget())))
	pkVerify(t, cfg, filepath.Join(out, "cold-verify"+exeSuffix(pkTarget())), cold.exe)
	// Edit app to take &Handlers, escaping the table so lib.Op0 loses its $pnone clone.
	e := pkEdit{name: "escape-table", file: "app/app.mk", old: "    // HOOK\n", new: "    _ = Touch(&Handlers)\n"}
	pkWrite(t, dir, e.apply(t, base))
	exe := filepath.Join(out, "edit"+exeSuffix(pkTarget()))
	ed := pkBuild(t, cfg, exe)
	if got := strings.Join(ed.emittedMods(), " "); !strings.Contains(got, "app") || !strings.Contains(got, "lib") {
		t.Errorf("emitted %v, want both app and lib (lib.Op0 lost its $pnone body)", got)
	}
	pkVerify(t, cfg, filepath.Join(out, "edit-verify"+exeSuffix(pkTarget())), ed.exe)
	if o, errOut, code := runExe(t, pkTarget(), exe, ""); code != 0 {
		t.Fatalf("exit %d, stdout %q, stderr %q; want 0", code, o, errOut)
	}
}
