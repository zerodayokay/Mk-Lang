// Why: mk check --all-targets must verify every pkg.Select target OS compiles
// cleanly and exports matching APIs, flows, error sets, and type modifiers
// while permitting private internals and constant values to vary per OS.
//
// What: TestAllTargetsSelect_MatchAllowedDiffs,
// TestAllTargetsSelect_MismatchAndErrorCases, and
// TestAllTargetsSelect_MissingTargetSelect.
//
// How: builds temporary packages with Windows and Linux pkg.Select modules
// and checks them through frontCheck under AllTargets: true (plus host
// target pinning to x86_64-windows so behavior is identical on Windows and
// Linux runners).

package driver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mk/internal/diag"
	"mk/internal/types"
)

func selectTestPkg(winSrc, linSrc string, extraMan map[string]string) map[string]string {
	m := map[string]string{
		"name": `"seltest"`,
		"bins": `[pkg.Bin(name: "seltest", root: "src", link_script: "", cpu: "", subsystem: "console")]`,
		"targets": `[` +
			`pkg.Select(os: "windows", modules: [pkg.Module(name: "os.native", dir: "os/windows")]), ` +
			`pkg.Select(os: "linux", modules: [pkg.Module(name: "os.native", dir: "os/linux")])` +
			`]`,
	}
	for k, v := range extraMan {
		m[k] = v
	}
	return map[string]string{
		"pkg.mk": mkmf(m),
		"src/main.mk": `import os.native

export Main(sys: &System) {
    _ = native.Flag
}
`,
		"os/windows/native.mk": winSrc,
		"os/linux/native.mk":   linSrc,
	}
}

func mkmf(fields map[string]string) string {
	return manifest(fields)
}

// TestAllTargetsSelect_MatchAllowedDiffs verifies that matching exported APIs
// pass --all-targets even when private helpers, private struct fields, struct
// sizes, and exported const values differ per OS, and without --all-targets a
// broken unselected target is not checked on the host.
func TestAllTargetsSelect_MatchAllowedDiffs(t *testing.T) {
	win := `export const Flag: u32 = 1

export error Failed

export plain view struct Handle {
    export tag: u32
    win_hi: u64
    win_lo: u64
}

export plain view struct Holder {
    buf: str
}

export MakeHandle(id: int): Handle {
    return Helper(id)
}

Helper(id: int): Handle {
    return Handle(tag: Trunc[u32](id), win_hi: 10, win_lo: 20)
}

export BorrowStr(h: &Holder): Failed!str from h {
    return h.buf
}
`
	lin := `export const Flag: u32 = 2

export error Failed

export plain view struct Handle {
    export tag: u32
    lin_fd: i32
}

export plain view struct Holder {
    buf: str
}

export MakeHandle(id: int): Handle {
    return LinuxOnlyHelper(id)
}

LinuxOnlyHelper(id: int): Handle {
    return Handle(tag: Trunc[u32](id), lin_fd: 3)
}

export BorrowStr(h: &Holder): Failed!str from h {
    return h.buf
}
`
	dir := writeTree(t, selectTestPkg(win, lin, nil))
	cfg := Config{Root: dir, Target: types.X86_64Windows, CacheDir: t.TempDir(), AllTargets: true, LazyStd: true}
	r := frontCheck(t, cfg)
	if len(r.Report.Diags) != 0 {
		t.Fatalf("matching pkg.Select with allowed diffs failed:\n%s", r.Report.Text(r.Sources))
	}
}

// TestAllTargetsSelect_MismatchAndErrorCases tests every E0213 mismatch mode:
// missing export, int vs i64, flow mismatch, error set mismatch, plain view vs
// view, and type/borrow error on the unselected target.
func TestAllTargetsSelect_MismatchAndErrorCases(t *testing.T) {
	baseWin := `export const Flag: u32 = 1

export error Bad
export error Alt

export plain view struct Handle {
    idx: u32
}

export plain view struct Holder {
    buf: str
}

export TakeInt(n: int): int {
    return n
}

export ReadBuf(h: &Holder): str from h {
    return h.buf
}

export Fallible(): Bad!int {
    return 1
}

export ExtraOnly(): int {
    return 1
}
`
	cases := []struct {
		name     string
		lin      string
		wantSub  string
		wantCode diag.Code
	}{
		{
			name:     "missing_export_on_other_os",
			lin:      strings.ReplaceAll(baseWin, "\nexport ExtraOnly(): int {\n    return 1\n}\n", "\n"),
			wantSub:  "ExtraOnly",
			wantCode: diag.E0213,
		},
		{
			name:     "int_vs_i64_mismatch",
			lin:      strings.ReplaceAll(baseWin, "export TakeInt(n: int): int {", "export TakeInt(n: i64): i64 {"),
			wantSub:  "TakeInt",
			wantCode: diag.E0213,
		},
		{
			name:     "flow_mismatch",
			lin:      strings.ReplaceAll(baseWin, "export ReadBuf(h: &Holder): str from h {\n    return h.buf\n}", "export ReadBuf(h: &Holder): str {\n    return \"lit\"\n}"),
			wantSub:  "ReadBuf",
			wantCode: diag.E0213,
		},
		{
			name:     "error_set_mismatch",
			lin:      strings.ReplaceAll(baseWin, "export Fallible(): Bad!int {", "export Fallible(): Bad|Alt!int {"),
			wantSub:  "Fallible",
			wantCode: diag.E0213,
		},
		{
			name:     "plain_view_vs_view_mismatch",
			lin:      strings.ReplaceAll(baseWin, "export plain view struct Handle {", "export view struct Handle {"),
			wantSub:  "Handle",
			wantCode: diag.E0213,
		},
		{
			name:     "type_error_on_unselected_target",
			lin:      baseWin + "\nBrokenLinux(): int {\n    return \"nope\"\n}\n",
			wantSub:  "x86_64-linux",
			wantCode: diag.E0213,
		},
		{
			name: "borrow_error_on_unselected_target",
			lin: baseWin + `
DangleLinux(): &i32 {
    x := 7
    return &x
}
`,
			wantSub:  "x86_64-linux",
			wantCode: diag.E0213,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeTree(t, selectTestPkg(baseWin, tc.lin, nil))
			// Without --all-targets, host check passes.
			hostCfg := Config{Root: dir, Target: types.X86_64Windows, CacheDir: t.TempDir(), AllTargets: false, LazyStd: true}
			if hr := frontCheck(t, hostCfg); len(hr.Report.Diags) != 0 {
				t.Fatalf("host check without --all-targets failed unexpectedly:\n%s", hr.Report.Text(hr.Sources))
			}
			// With --all-targets, E0213 fires.
			allCfg := Config{Root: dir, Target: types.X86_64Windows, CacheDir: t.TempDir(), AllTargets: true, LazyStd: true}
			r := frontCheck(t, allCfg)
			if len(r.Report.Diags) == 0 || r.Report.Diags[0].Diag.Code != tc.wantCode {
				t.Fatalf("want %s, got:\n%s", tc.wantCode, r.Report.Text(r.Sources))
			}
			text := r.Report.Text(r.Sources)
			if !strings.Contains(text, tc.wantSub) {
				t.Errorf("E0213 text missing %q:\n%s", tc.wantSub, text)
			}
		})
	}
}

// TestAllTargetsSelect_MissingTargetSelect checks that a declared target OS
// without a pkg.Select entry in targets fails with E0213 under --all-targets.
func TestAllTargetsSelect_MissingTargetSelect(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"pkg.mk": manifest(map[string]string{
			"name":        `"missel"`,
			"bins":        `[pkg.Bin(name: "missel", root: "src", link_script: "", cpu: "", subsystem: "console")]`,
			"targets":     `[pkg.Select(os: "windows", modules: [pkg.Module(name: "os.native", dir: "os/windows")])]`,
			"asm_sources": `[pkg.AsmSource(path: "boot.s", target: "x86_64-linux")]`,
		}),
		"boot.s": ".text\n",
		"src/main.mk": `import os.native

export Main(sys: &System) {
    _ = native.Flag
}
`,
		"os/windows/native.mk": "export const Flag: u32 = 1\n",
	})
	_ = os.MkdirAll(filepath.Join(dir, "os", "linux"), 0o755)
	cfg := Config{Root: dir, Target: types.X86_64Windows, CacheDir: t.TempDir(), AllTargets: true, LazyStd: true}
	r := frontCheck(t, cfg)
	if len(r.Report.Diags) == 0 || r.Report.Diags[0].Diag.Code != diag.E0213 {
		t.Fatalf("want E0213 for missing linux pkg.Select, got:\n%s", r.Report.Text(r.Sources))
	}
	if text := r.Report.Text(r.Sources); !strings.Contains(text, "linux") || !strings.Contains(text, "missing-target-select") {
		t.Errorf("unexpected missing-target-select message:\n%s", text)
	}
}

// TestAllTargetsSelect_CrossArchClearsCPU checks that forwarding an aarch64 CPU
// flag under --all-targets does not poison an x86_64 pkg.Select target.
func TestAllTargetsSelect_CrossArchClearsCPU(t *testing.T) {
	win := "export const Flag: u32 = 1\n"
	mac := "export const Flag: u32 = 2\n"
	dir := writeTree(t, map[string]string{
		"pkg.mk": manifest(map[string]string{
			"name": `"crossarch"`,
			"bins": `[pkg.Bin(name: "crossarch", root: "src", link_script: "", cpu: "", subsystem: "console")]`,
			"targets": `[` +
				`pkg.Select(os: "windows", modules: [pkg.Module(name: "os.native", dir: "os/windows")]), ` +
				`pkg.Select(os: "macos", modules: [pkg.Module(name: "os.native", dir: "os/macos")])` +
				`]`,
		}),
		"src/main.mk": `import os.native

export Main(sys: &System) {
    _ = native.Flag
}
`,
		"os/windows/native.mk": win,
		"os/macos/native.mk":   mac,
	})
	cfg := Config{Root: dir, Target: types.Aarch64Macos, CPU: "apple-m1", CacheDir: t.TempDir(), AllTargets: true, LazyStd: true}
	r := frontCheck(t, cfg)
	if len(r.Report.Diags) != 0 {
		t.Fatalf("cross-arch --all-targets with host CPU failed:\n%s", r.Report.Text(r.Sources))
	}
}

// TestAllTargetsSelect_NonHostBaselineSeedsFirstTarget checks that when
// targets omits the host OS, --all-targets still compares selected targets
// against one another rather than against empty host facts.
func TestAllTargetsSelect_NonHostBaselineSeedsFirstTarget(t *testing.T) {
	makePkg := func(macSrc string) string {
		return writeTree(t, map[string]string{
			"pkg.mk": manifest(map[string]string{
				"name": `"nonhost"`,
				"bins": `[pkg.Bin(name: "nonhost", root: "src", link_script: "", cpu: "", subsystem: "console")]`,
				"targets": `[` +
					`pkg.Select(os: "linux", modules: [pkg.Module(name: "os.native", dir: "os/linux")]), ` +
					`pkg.Select(os: "macos", modules: [pkg.Module(name: "os.native", dir: "os/macos")])` +
					`]`,
			}),
			"src/main.mk": `export Main(sys: &System) {
    _ = sys
}
`,
			"os/linux/native.mk": "export Val(): i32 {\n    return 1\n}\n",
			"os/macos/native.mk": macSrc,
		})
	}

	t.Run("matching non-host targets produce no bogus missing-on-host API diff", func(t *testing.T) {
		r := frontCheck(t, Config{Root: makePkg("export Val(): i32 {\n    return 2\n}\n"), Target: types.X86_64Windows, CacheDir: t.TempDir(), AllTargets: true, LazyStd: true})
		text := r.Report.Text(r.Sources)
		if strings.Contains(text, "selected module os.native differs") {
			t.Fatalf("unexpected bogus selected-module diff against host:\n%s", text)
		}
		if !strings.Contains(text, "missing-target-select") {
			t.Fatalf("expected missing-target-select for omitted host OS, got:\n%s", text)
		}
	})

	t.Run("differing non-host targets report diff between selected targets", func(t *testing.T) {
		r := frontCheck(t, Config{Root: makePkg("export Val(): i64 {\n    return 2\n}\n"), Target: types.X86_64Windows, CacheDir: t.TempDir(), AllTargets: true, LazyStd: true})
		text := r.Report.Text(r.Sources)
		if !strings.Contains(text, "x86_64-linux") || !strings.Contains(text, "x86_64-macos") || !strings.Contains(text, "Val") {
			t.Fatalf("expected diff between x86_64-linux and x86_64-macos for Val, got:\n%s", text)
		}
	})
}
