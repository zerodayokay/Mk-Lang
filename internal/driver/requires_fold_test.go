// Why: a requires clause that holds for every run of an instance must cost
// nothing on entry at compile time, in every profile, and a clause that does
// not hold (or that only the entry check judges) must still panic there.
// What: TestRequiresFoldedPerInstance and TestFrontFoldedRequiresEdit.
// How: consts whose fuel fits a loop of a thousand calls only when the
// instance's clause is not run again, in dev and release; entry-only clauses
// that are false, signed or overflow for the instance stay E0901 in dev (and
// unchecked in release, as every run-time contract is); and a warm
// check after an edit of a folded clause of another module equals a cold one.

package driver

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"mk/internal/types"
)

// foldLoops is a thousand calls through WithClause[16], whose clause reads its
// const generic alone, and through NoClause[16], the same function without
// it, each at fuel 40,000: the clause run on every entry needs about 61,000.
const foldLoops = `WithClause[const N: int](x: int): int
    requires N == 16 || N == 32 || N == 64 {
    return x + N
}

NoClause[const N: int](x: int): int {
    return x + N
}

LoopWith(): int {
    var s := 0
    for i in 0..1000 {
        s += WithClause[16](i)
    }
    return s
}

LoopNo(): int {
    var s := 0
    for i in 0..1000 {
        s += NoClause[16](i)
    }
    return s
}

const A: int = LoopWith() fuel 40_000

const B: int = LoopNo() fuel 40_000

export Read(): int {
    return A + B
}
`

// entryOnly is a clause over a const generic whose argument no clause can
// spell (a SizeOf of [2]U), so only the entry check judges it.
const entryOnly = `Lane[const N: int](): int
    requires N < 64 {
    return N
}

Signed[const N: int](): int
    requires N - 200 > 0 {
    return N
}

Over[const N: int](): int
    requires N * 4611686018427387904 >= 0 {
    return N
}

G[U](): int {
    return Lane[SizeOf[[2]U]()]()
}

GS[U](): int {
    return Signed[SizeOf[[2]U]()]()
}

GO[U](): int {
    return Over[SizeOf[[2]U]()]()
}
`

func TestRequiresFoldedPerInstance(t *testing.T) {
	t.Parallel()
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		t.Run("loops "+prof.String(), func(t *testing.T) {
			t.Parallel()
			res, _, _ := gen10Check(t, map[string]string{"a/a.mk": foldLoops}, prof)
			clean(t, res)
		})
	}
	for _, c := range []struct {
		name, consts, panic string
	}{
		{"true for the instance", "const L: int = G[[16]u8]()\n\nconst S: int = GS[[128]u8]()\n\nconst O: int = GO[[0]u8]()\n", ""},
		{"false", "const K: int = G[[64]u8]()\n", "contract"},
		{"false when signed", "const K: int = GS[[8]u8]()\n", "contract"},
		{"overflows", "const K: int = GO[[8]u8]()\n", "overflow"},
	} {
		for _, prof := range []types.Profile{types.Dev, types.Release} {
			t.Run(c.name+" "+prof.String(), func(t *testing.T) {
				t.Parallel()
				res, _, _ := gen10Check(t, map[string]string{"a/a.mk": entryOnly + "\n" + c.consts}, prof)
				if c.panic == "" || prof == types.Release {
					// A release build checks no entry-only clause.
					clean(t, res)
					return
				}
				text := res.Report.Text(res.Sources)
				if got := codes(res); !slices.Equal(got, []string{"E0901"}) || !strings.Contains(text, "panic: "+c.panic) {
					t.Errorf("codes %q, want E0901 with panic %s:\n%s", got, c.panic, text)
				}
			})
		}
	}
}

// TestFrontFoldedRequiresEdit: app's const runs lib.G[[16]u8](), whose call
// of Lane[32] mono folds (N < 64 holds), so the evaluation enters no
// predicate. Editing the clause to N < 16 must make a warm check evaluate
// again and report the panic a cold check reports: the clause is part of
// lib's API, which app's cache entry holds, and the served instance records
// the predicate's body as a read too.
func TestFrontFoldedRequiresEdit(t *testing.T) {
	t.Parallel()
	lib := "export Lane[const N: int](): int\n    requires N < 64 {\n    return N\n}\n\n" +
		"export G[U](): int {\n    return Lane[SizeOf[[2]U]()]()\n}\n"
	files := map[string]string{
		"pkg.mk":     manifest(map[string]string{"bins": `[pkg.Bin(name: "app", root: "app", link_script: "", cpu: "", subsystem: "console")]`}),
		"lib/lib.mk": lib,
		"app/app.mk": "import lib\n\nconst K: int = lib.G[[16]u8]()\n\nexport Main(sys: &System): int {\n    return K - 32\n}\n",
	}
	dir := writeTree(t, files)
	cfg := Config{Root: dir, CacheDir: t.TempDir(), Target: types.X86_64Windows}
	cold := frontCheck(t, cfg)
	if len(cold.Report.Diags) > 0 || len(cold.Trace.NotStored) > 0 {
		t.Fatalf("cold check: not stored %q\n%s", cold.Trace.NotStored, cold.Report.Text(cold.Sources))
	}
	if err := os.WriteFile(filepath.Join(dir, "lib", "lib.mk"), []byte(strings.Replace(lib, "N < 64", "N < 16", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	warm := frontCheck(t, cfg)
	fresh := frontCheck(t, Config{Root: dir, CacheDir: t.TempDir(), Target: cfg.Target})
	if got := codes(fresh); !slices.Equal(got, []string{"E0901"}) {
		t.Fatalf("cold check of the edit: codes %q, want E0901\n%s", got, fresh.Report.Text(fresh.Sources))
	}
	if got := codes(warm); !slices.Equal(got, []string{"E0901"}) {
		t.Errorf("warm check of the edit: codes %q, want the cold check's E0901\n%s", got, warm.Report.Text(warm.Sources))
	}
}
