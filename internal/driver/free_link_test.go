// Why: a freestanding (kernel) program must link with none of the hosted
// runtime, and a program's own panic hook must win over the default.
// What: TestFreestandingLinks and TestFreestandingSortStableInPlace, over the
// programs freeLayer0, freeStrongHook and freeSortStableInPlace.
// How: mk build --target=x86_64-freestanding of a layer-0 program links with
// ld.lld into a static ELF executable entered at MkEntry; the test reads its
// symbols, and the support pack's weak MkPanicHook gives way to a program's
// own. Nothing here runs the executable, so no runner (and no WSL) is involved.

package driver

import (
	"debug/elf"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mk/internal/types"
)

const freeLayer0 = `Add(a: int, b: int): int {
    return a + b
}

export extern MkEntry() {
    var x := Add(1, 2)
    for {
        x = Add(x, 1)
    }
}
`

const freeStrongHook = `import std.free

export extern MkPanicHook(info: &free.PanicInfo): never {
    for {
    }
}

`

// TestFreestandingLinks: dev and release, with and without the
// program's MkPanicHook: the build links (ld.lld), the result is an x86-64
// ET_EXEC whose entry is MkEntry, it names no hosted entry, task, system
// stack or std.rt/std.task symbol, a trap's hook is std.free's (dev, where
// nothing is inlined), and MkPanicHook is the support pack's weak default
// only when the program defines none.
func TestFreestandingLinks(t *testing.T) {
	t.Parallel()
	for _, strong := range []bool{false, true} {
		for _, prof := range []types.Profile{types.Dev, types.Release} {
			name := "weak/" + prof.String()
			src := freeLayer0
			if strong {
				name = "strong/" + prof.String()
				src = freeStrongHook + freeLayer0
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				dir := t.TempDir()
				file := filepath.Join(dir, "main.mk")
				if err := os.WriteFile(file, []byte(src), 0o644); err != nil {
					t.Fatal(err)
				}
				exe := buildProgram(t, file, types.X86_64Freestanding, prof, t.TempDir())
				f, err := elf.Open(exe)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				if f.Type != elf.ET_EXEC || f.Machine != elf.EM_X86_64 || f.Class != elf.ELFCLASS64 {
					t.Errorf("type %v machine %v class %v, want ET_EXEC EM_X86_64 ELFCLASS64", f.Type, f.Machine, f.Class)
				}
				for _, p := range f.Progs {
					if p.Type == elf.PT_INTERP || p.Type == elf.PT_DYNAMIC {
						t.Errorf("program header %v in a freestanding executable", p.Type)
					}
				}
				syms, err := f.Symbols()
				if err != nil {
					t.Fatal(err)
				}
				byName := map[string]elf.Symbol{}
				for _, s := range syms {
					byName[s.Name] = s
					for _, bad := range []string{"_start", "mk_start", "mk_switch", "mk_task_start", "mk_clone",
						"mk_sigreturn", "mk_call_system", "mk.support.InitContext", "mk.std.rt.", "mk.std.task."} {
						if s.Name == bad || (strings.HasSuffix(bad, ".") && strings.HasPrefix(s.Name, bad)) {
							t.Errorf("hosted symbol %s in the freestanding executable", s.Name)
						}
					}
				}
				entry, ok := byName["MkEntry"]
				if !ok || entry.Value != f.Entry {
					t.Errorf("entry %#x, MkEntry %#x (found %t)", f.Entry, entry.Value, ok)
				}
				if prof == types.Dev {
					if _, ok := byName["mk.std.free.PanicAt"]; !ok {
						t.Error("no mk.std.free.PanicAt: the overflow check of Add does not reach std.free's hook")
					}
				}
				hook, ok := byName["MkPanicHook"]
				weak := ok && elf.ST_BIND(hook.Info) == elf.STB_WEAK
				switch {
				case !strong && !weak:
					t.Errorf("MkPanicHook is not the weak default (found %t, bind %v)", ok, elf.ST_BIND(hook.Info))
				case strong && weak:
					t.Error("the program's MkPanicHook did not replace the weak default")
				case strong && prof == types.Dev && (!ok || elf.ST_BIND(hook.Info) != elf.STB_GLOBAL):
					t.Errorf("dev: the program's MkPanicHook is not a global definition (found %t)", ok)
				}
			})
		}
	}
}

const freeSortStableInPlace = `struct Empty {}

align(64) struct CacheLine {
    k: int
    id: int
    pad: [6]u64
}

export extern MkEntry() {
    var xs: [64]int = [0; 64]
    for i in 0..64 {
        xs[i] = 64 - i
    }
    xs.SortStableInPlace(fn(a: &int, b: &int): bool { return *a < *b })
    var zs: [64]Empty = [Empty(); 64]
    zs.SortStableInPlace(fn(a: &Empty, b: &Empty): bool { return false })
    var cs: [64]CacheLine = [CacheLine(k: 0, id: 0, pad: [0; 6]); 64]
    for i in 0..64 {
        cs[i] = CacheLine(k: (64 - i) % 7, id: i, pad: [0; 6])
    }
    cs.SortStableInPlace(fn(a: &CacheLine, b: &CacheLine): bool { return a.k < b.k })
    for {
    }
}
`

// TestFreestandingSortStableInPlace: a freestanding program that calls
// Slice[T].SortStableInPlace on ints, zero-size elements and align(64)
// elements links cleanly at dev and release with no hosted runtime and no
// allocator or driftsort symbols.
func TestFreestandingSortStableInPlace(t *testing.T) {
	t.Parallel()
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		t.Run(prof.String(), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			file := filepath.Join(dir, "main.mk")
			if err := os.WriteFile(file, []byte(freeSortStableInPlace), 0o644); err != nil {
				t.Fatal(err)
			}
			exe := buildProgram(t, file, types.X86_64Freestanding, prof, t.TempDir())
			f, err := elf.Open(exe)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if f.Type != elf.ET_EXEC || f.Machine != elf.EM_X86_64 || f.Class != elf.ELFCLASS64 {
				t.Errorf("type %v machine %v class %v, want ET_EXEC EM_X86_64 ELFCLASS64", f.Type, f.Machine, f.Class)
			}
			syms, err := f.Symbols()
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range syms {
				for _, bad := range []string{"_start", "mk_start", "mk.std.rt.", "mk.std.task.",
					"AllocBytes", "FreeBytes", "GrowSlots", "DriftSort", "DriftStable"} {
					if s.Name == bad || strings.Contains(s.Name, bad) {
						t.Errorf("unexpected symbol %s in freestanding SortStableInPlace executable", s.Name)
					}
				}
			}
		})
	}
}
