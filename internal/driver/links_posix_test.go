// Why: automatic glibc linkage is a per-bin decision based on reached
// declarations and must respect explicit package links.
// What: synthetic POSIX view link, precedence and diagnostic tests.
// How: serve immutable archives through the normal view hook and inspect
// the linked ELF's interpreter, dependencies and versioned symbol imports.

package driver

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"mk/internal/mono"
	"mk/internal/sysview"
	"mk/internal/types"
)

const posixCall = `import c.posix

export Main(system: &System): int {
    _ = system
    unsafe "getuid has no parameters" {
        return int(posix.getuid())
    }
}
`

func fakePosix(t *testing.T) TestHooks {
	t.Helper()
	archives := map[string][]byte{}
	for target, spec := range libcSpecs {
		items := []sysview.Item{
			{Name: "UserId", Kind: "type", Defs: []string{"UserId"}, Text: "export type UserId = u32"},
			{Name: "getuid", Kind: "func", Defs: []string{"getuid"}, Text: "// C: unsigned int getuid(void)\nexport extern getuid(): UserId",
				Deps: []sysview.Ref{{Item: 0}}, Link: sysview.Link{Lib: libcSoname, Version: spec.base}},
			{Name: "posixUnavailable", Kind: "func", Defs: []string{"posixUnavailable"}, Text: "// C: int posixUnavailable(void)\nexport extern posixUnavailable(): i32"},
		}
		var err error
		archives[target], err = sysview.Write(sysview.Meta{Name: "posix", Target: target}, items, sysview.DefaultOptions)
		if err != nil {
			t.Fatal(err)
		}
	}
	return TestHooks{SysViews: func(target, name string) ([]byte, bool) {
		archive, ok := archives[target]
		return archive, name == "posix" && ok
	}}
}

func TestPOSIXAutoLinkReachability(t *testing.T) {
	hooks := fakePosix(t)
	for _, target := range []types.Target{types.X86_64Linux, types.Aarch64Linux} {
		spec := libcSpecs[target.Name]
		for _, prof := range []types.Profile{types.Dev, types.Release} {
			t.Run(target.Name+"/"+prof.String(), func(t *testing.T) {
				r, exe := ffiBuild(t, writeTree(t, sysPkg(posixCall, nil)), target, prof, hooks)
				if exe == "" {
					t.Fatalf("POSIX call did not link:\n%s", r.Report.Text(r.Sources))
				}
				interp, libs := elfDyn(t, exe)
				if interp != spec.interp || !slices.Equal(libs, []string{libcSoname}) {
					t.Fatalf("ELF interpreter %q, libraries %v", interp, libs)
				}
				if version, lib := importVersion(t, exe, "getuid"); version != spec.base || lib != libcSoname {
					t.Fatalf("getuid imports from %q with version %q", lib, version)
				}
			})
		}
	}
	uses := map[string]string{
		"type-only": "import c.posix\n\nexport Main(system: &System): int {\n    _ = system\n    n: posix.UserId = 0\n    return int(n)\n}\n",
		"dead-call": strings.Replace(posixCall, "export Main", "Unused", 1) + "\nexport Main(system: &System): int {\n    _ = system\n    return 0\n}\n",
	}
	for _, name := range []string{"type-only", "dead-call"} {
		t.Run(name, func(t *testing.T) {
			r, exe := ffiBuild(t, writeTree(t, sysPkg(uses[name], nil)), types.X86_64Linux, types.Dev, hooks)
			if exe == "" {
				t.Fatalf("build failed:\n%s", r.Report.Text(r.Sources))
			}
			if interp, libs := elfDyn(t, exe); interp != "" || len(libs) != 0 {
				t.Fatalf("unused POSIX symbol selected interpreter %q, libraries %v", interp, libs)
			}
		})
	}
	t.Run("guarded-on-Windows", func(t *testing.T) {
		main := strings.Replace(posixCall, "unsafe ", "if Target.os == Os.Linux {\n        unsafe ", 1)
		main = strings.Replace(main, "\n    }\n}", "\n        }\n    }\n    return 0\n}", 1)
		r, exe := ffiBuild(t, writeTree(t, sysPkg(main, nil)), types.X86_64Windows, types.Dev, hooks)
		if exe == "" {
			t.Fatalf("guarded build failed:\n%s", r.Report.Text(r.Sources))
		}
		if dlls := importedDLLs(t, exe); slices.Contains(dlls, libcSoname) {
			t.Fatalf("PE imports libc: %v", dlls)
		}
	})
}

func TestPOSIXAutoLinkPrecedence(t *testing.T) {
	target := types.X86_64Linux
	hooks := fakePosix(t)
	build := func(t *testing.T, root string) string {
		t.Helper()
		r, exe := ffiBuild(t, root, target, types.Dev, hooks)
		if exe == "" {
			t.Fatalf("build failed:\n%s", r.Report.Text(r.Sources))
		}
		return exe
	}
	t.Run("dynamic", func(t *testing.T) {
		files := sysPkg(posixCall, map[string]string{"links": "[" + linkLit("liboverride.so.1", "dynamic", "override.def", target.Name) + "]"})
		files["override.def"] = "getuid@MY_1\n"
		exe := build(t, writeTree(t, files))
		if ver, lib := importVersion(t, exe, "getuid"); ver != "MY_1" || lib != "liboverride.so.1" {
			t.Fatalf("getuid imports from %q with version %q", lib, ver)
		}
	})
	t.Run("static", func(t *testing.T) {
		files := sysPkg(posixCall, map[string]string{"links": "[" + linkLit("override", "static", "override.a", target.Name) + "]"})
		root := writeTree(t, files)
		cArchive(t, needTools(t), filepath.Join(root, "override.a"), target, []string{"unsigned getuid(void) { return 52; }\n"})
		if interp, libs := elfDyn(t, build(t, root)); interp != "" || len(libs) != 0 {
			t.Fatalf("static override selected interpreter %q, libraries %v", interp, libs)
		}
	})
	t.Run("C-sources-use-system-allocator", func(t *testing.T) {
		main := strings.Replace(posixCall, "import c.posix", "import c.posix\n\nextern UseMalloc(): i32", 1)
		main = strings.Replace(main, "return int(posix.getuid())", "return int(posix.getuid()) + int(UseMalloc())", 1)
		files := sysPkg(main, map[string]string{"c_sources": `[pkg.CSource(path: "alloc.c", flags: [])]`})
		files["alloc.c"] = "extern void *malloc(unsigned long);\nextern void free(void *);\nint UseMalloc(void) { void *p = malloc(24); free(p); return p != 0; }\n"
		exe := build(t, writeTree(t, files))
		if _, lib := importVersion(t, exe, "malloc"); lib != libcSoname {
			t.Fatalf("C source malloc imported from %q", lib)
		}
	})
}

func TestPOSIXAutoLinkDiagnostics(t *testing.T) {
	hooks := fakePosix(t)
	bad := strings.Replace(posixCall, "getuid()", "posixUnavailable()", 1)
	safe := "import c.posix\n\nexport Main(system: &System): int {\n    _ = system\n    return int(posix.getuid())\n}\n"
	unused := "import c.posix\n\nexport Main(system: &System): int {\n    _ = system\n    return 0\n}\n"
	for _, tc := range []struct {
		name   string
		main   string
		target types.Target
		want   string
	}{
		{"Windows", posixCall, types.X86_64Windows, "getuid is a function of c.posix for Linux"},
		{"macOS", posixCall, types.Aarch64Macos, "getuid is a function of c.posix for Linux"},
		{"unbound", bad, types.X86_64Linux, "posixUnavailable, a function of c.posix, has no pinned glibc export"},
		{"unsafe", safe, types.X86_64Linux, "E1101"},
		{"unused-import", unused, types.X86_64Linux, "E0208"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, exe := ffiBuild(t, writeTree(t, sysPkg(tc.main, nil)), tc.target, types.Dev, hooks)
			if report := r.Report.Text(r.Sources); exe != "" || !strings.Contains(report, tc.want) {
				t.Fatalf("wanted %q, got:\n%s", tc.want, report)
			}
		})
	}
	t.Run("library", func(t *testing.T) {
		source := "import c.posix\n\nexport extern GetUser(): u32 {\n    unsafe \"getuid has no parameters\" {\n        return posix.getuid()\n    }\n}\n"
		bins := `[pkg.Bin(name: "p", root: "app", link_script: "", cpu: "", subsystem: "", kind: "static", abi: "")]`
		r, exe := ffiBuild(t, writeTree(t, sysPkg(source, map[string]string{"bins": bins})), types.X86_64Linux, types.Dev, hooks)
		if report := r.Report.Text(r.Sources); exe != "" || !strings.Contains(report, "system-libc mode, which only an executable takes") {
			t.Fatalf("wanted a Linux library diagnostic, got:\n%s", report)
		}
	})
}

func TestPOSIXAutoLinkSelectedModeSurvivesFolding(t *testing.T) {
	main := "export Main(system: &System) {\n    _ = system\n}\n"
	s, err := NewSession(Config{Root: writeTree(t, sysPkg(main, nil)), Target: types.X86_64Linux, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	clean(t, s.Check())
	plan, ds := s.w.linkPlanFor(&mono.Instances{}, nil, nil, posixSelected)
	if len(ds) != 0 || plan == nil || len(plan.sos) != 1 || plan.libc.interp != libcSpecs[types.X86_64Linux.Name].interp {
		t.Fatalf("libc mode without any surviving POSIX references: plan %+v, diagnostics %+v", plan, ds)
	}
}
