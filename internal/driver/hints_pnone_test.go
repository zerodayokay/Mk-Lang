// Why: std.runtime branch/cold hints and direct-cycle preserve_nonecc must
// work end to end across parsing, lowering, codegen, LLVM optimization, linking,
// native execution, and incremental cache verification.
//
// What: TestRuntimeHintsAndDirectPnoneEndToEnd.
//
// How: builds and executes a Release Mk program using `runtime.Likely`,
// `runtime.Unlikely`, `runtime.Cold()`, and a 6-`u64`-arg mutual `become` loop,
// inspects emitted IR, and verifies cold/warm cache consistency.

package driver

import (
	"path/filepath"
	"strings"
	"testing"

	"mk/internal/types"
)

func TestRuntimeHintsAndDirectPnoneEndToEnd(t *testing.T) {
	tools := needTools(t)
	src := `import std.runtime

ColdFallback(x: u64): u64 {
    return x +% 99
}

Ping(n: u64, a: u64, b: u64, c: u64, d: u64, acc: u64): u64 {
    if runtime.Unlikely(n == 0) {
        runtime.Cold()
        return ColdFallback(acc +% a +% b +% c +% d)
    }
    become Pong(n - 1, b, c, d, a +% 1, acc +% a)
}

Pong(n: u64, a: u64, b: u64, c: u64, d: u64, acc: u64): u64 {
    if runtime.Likely(n > 0) {
        become Ping(n - 1, b, c, d, a +% 2, acc +% a)
    }
    runtime.Cold()
    return acc +% a +% b +% c +% d
}

export Main(sys: &System): int {
    _ = sys
    res := Ping(10000, 1, 2, 3, 4, 0)
    if res == 0 {
        return 1
    }
    return 0
}
`
	files := map[string]string{
		"pkg.mk": manifest(map[string]string{
			"name": `"hintspnone"`,
			"bins": `[pkg.Bin(name: "app", root: ".", link_script: "", cpu: "", subsystem: "console")]`,
		}),
		"main.mk": src,
	}
	dir := writeTree(t, files)
	cacheDir := filepath.Join(t.TempDir(), "cache")
	var irBuf strings.Builder
	cfg := Config{
		Root:     dir,
		Target:   pkTarget(),
		Profile:  types.Release,
		Tools:    tools,
		CacheDir: cacheDir,
		TestHooks: TestHooks{
			OnLL: func(name, ll string) {
				if name == "hintspnone" || strings.Contains(ll, "Ping$pnone") {
					irBuf.WriteString(ll)
				}
			},
		},
	}
	out := filepath.Join(t.TempDir(), "app"+exeSuffix(pkTarget()))
	cold := pkBuild(t, cfg, out)
	ll := irBuf.String()
	for _, want := range []string{
		"@llvm.expect.i1(",
		"branch_weights",
		"Ping$pnone",
		"Pong$pnone",
		"musttail call preserve_nonecc",
	} {
		if !strings.Contains(ll, want) {
			t.Errorf("release IR missing %q", want)
		}
	}
	if o, errOut, code := runExe(t, pkTarget(), out, ""); code != 0 {
		t.Fatalf("exit %d, stdout %q, stderr %q; want 0", code, o, errOut)
	}
	pkVerify(t, Config{Root: dir, Target: pkTarget(), Profile: types.Release, Tools: tools, CacheDir: cacheDir},
		filepath.Join(t.TempDir(), "app-verify"+exeSuffix(pkTarget())), cold.exe)
}
