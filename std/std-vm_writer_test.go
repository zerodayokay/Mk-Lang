// Why: std.vm's CodeWriter enforces W^X and exclusive write windows at compile
// time through linearity, mut borrowing, !Send, and unsafe-only gating.
// What: TestVm_CodeWriterDiscipline (compile-time rejection of leaked writers,
// overlapping Entry/Edit/Commit/Release, escaping Bytes slices, cross-task
// send, and outside-unsafe calls, plus cross-target check of valid usage).
// How: writes small packages in temp dirs and runs `mk check` against a copy
// of std, verifying exact error codes and clean cross-target checking.

package std

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const vmWriterPkg = `const Package = pkg.Package(
    name: "vmw",
    bins: [pkg.Bin(name: "vmw", root: "app", link_script: "", cpu: "", subsystem: "console")],
    deps: [],
    c_sources: [],
    c_imports: [],
    asm_sources: [],
    targets: [],
    unsafe_modules: ["app"],
    links: [],
)
`

func TestVm_CodeWriterDiscipline(t *testing.T) {
	root, stdDir := missStd(t)
	cases := []struct {
		name string
		src  string
		want string // expected diagnostic code, or "" for clean check
	}{
		{
			name: "valid_defer_done",
			src: `import std.vm

Emit(c: mut vm.CodeRegion): vm.Error!void {
    unsafe "write inside Edit window" {
        var w := try c.Edit()
        defer w.Done()
        w.Bytes()[0] = 0xC3
        w.Flush(0, 1)
    }
}

export Main(sys: &System): vm.Error!void {
    unsafe "make, edit, query and release" {
        var c := try vm.CodeRegion.New(vm.PageSize())
        onerr c.Release()
        try Emit(mut c)
        _ = c.Entry(0)
        c.Release()
    }
}
`,
			want: "",
		},
		{
			name: "forget_done_is_linear_error",
			src: `import std.vm

export Main(sys: &System): vm.Error!void {
    unsafe "forget Done on linear CodeWriter" {
        var c := try vm.CodeRegion.New(vm.PageSize())
        onerr c.Release()
        var w := try c.Edit()
        w.Flush(0, 1)
        c.Release()
    }
}
`,
			want: "E0402",
		},
		{
			name: "entry_while_editing_rejected",
			src: `import std.vm

export Main(sys: &System): vm.Error!void {
    unsafe "Entry while CodeWriter borrows c mutably" {
        var c := try vm.CodeRegion.New(vm.PageSize())
        onerr c.Release()
        var w := try c.Edit()
        _ = c.Entry(0)
        w.Done()
        c.Release()
    }
}
`,
			want: "E0504",
		},
		{
			name: "double_edit_rejected",
			src: `import std.vm

export Main(sys: &System): vm.Error!void {
    unsafe "second Edit while first CodeWriter is live" {
        var c := try vm.CodeRegion.New(vm.PageSize())
        onerr c.Release()
        var w1 := try c.Edit()
        var w2 := try c.Edit()
        w2.Done()
        w1.Done()
        c.Release()
    }
}
`,
			want: "E0501",
		},
		{
			name: "commit_while_editing_rejected",
			src: `import std.vm

export Main(sys: &System): vm.Error!void {
    unsafe "Commit while CodeWriter is live" {
        var c := try vm.CodeRegion.Reserve(2 * vm.PageSize())
        onerr c.Release()
        var w := try c.Edit()
        try c.Commit(0, vm.PageSize())
        w.Done()
        c.Release()
    }
}
`,
			want: "E0501",
		},
		{
			name: "release_while_editing_rejected",
			src: `import std.vm

export Main(sys: &System): vm.Error!void {
    unsafe "Release while CodeWriter is live" {
        var c := try vm.CodeRegion.New(vm.PageSize())
        onerr c.Release()
        var w := try c.Edit()
        c.Release()
        w.Done()
    }
}
`,
			want: "E0503",
		},
		{
			name: "bytes_after_done_rejected",
			src: `import std.vm

export Main(sys: &System): vm.Error!void {
    unsafe "use Bytes slice after Done" {
        var c := try vm.CodeRegion.New(vm.PageSize())
        onerr c.Release()
        var w := try c.Edit()
        var b := w.Bytes()
        w.Done()
        b[0] = 0xC3
        c.Release()
    }
}
`,
			want: "E0503",
		},
		{
			name: "send_codewriter_rejected",
			src: `import std.vm

Take(w: vm.CodeWriter) {
    unsafe "close writer" {
        w.Done()
    }
}

export Main(sys: &System): vm.Error!void {
    unsafe "attempt to spawn with CodeWriter" {
        var c := try vm.CodeRegion.New(vm.PageSize())
        onerr c.Release()
        var w := try c.Edit()
        scope s := sys.io.Scope() {
            s.go Take(w)
        }
        c.Release()
    }
}
`,
			want: "E1001",
		},
		{
			name: "writer_methods_need_unsafe",
			src: `import std.vm

BadEdit(c: mut vm.CodeRegion): vm.Error!void {
    var w := try c.Edit()
    w.Bytes()[0] = 0
    w.Flush(0, 1)
    w.Done()
}

export Main(sys: &System) {
}
`,
			want: "E1101",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(root, tc.name)
			if err := os.MkdirAll(filepath.Join(dir, "app"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "pkg.mk"), []byte(vmWriterPkg), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "app", "main.mk"), []byte(tc.src), 0o644); err != nil {
				t.Fatal(err)
			}
			out, status := mkCmd(t, dir, stdDir, "check", ".")
			if tc.want == "" {
				if status != 0 {
					t.Fatalf("mk check: status %d\n%s", status, out)
				}
				for _, target := range []string{"x86_64-linux", "aarch64-linux", "aarch64-macos", "x86_64-macos", "x86_64-nutshell"} {
					tout, tst := mkCmd(t, dir, stdDir, "check", "--target", target, ".")
					if tst != 0 {
						t.Errorf("mk check --target %s: status %d\n%s", target, tst, tout)
					}
				}
				return
			}
			if status == 0 || !strings.Contains(out, tc.want) {
				t.Errorf("mk check: status %d, want %s\n%s", status, tc.want, out)
			}
		})
	}
}
