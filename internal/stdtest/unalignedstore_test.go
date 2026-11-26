// Why: raw.WriteUnaligned is the store that byte-addressed views (a
// DataView, a wire format, a packed record) need: a store wider than a byte
// at an address only known to be byte aligned must be an align 1 store, and
// must write exactly its bytes.
// What: TestUnalignedStores and the program unalignedStoreProgram.
// How: builds the program for the host at dev and release, requires the
// store of each Store function to be align 1 in codegen's IR, and runs it:
// a u16, u32 and u64 stored at every offset 0 to 7 of a buffer reads back,
// little-endian, with every other byte untouched, natively and in the
// compile-time evaluator (a const of the same check), and so does a u32
// stored through raw.WriteUnaligned named as a fn value.

package stdtest

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"mk/internal/driver"
	"mk/internal/types"
)

// unalignedStoreProgram exits with 0 when every store round-trips, else
// with 1 + 16 * width + the failing offset (200 + the offset for the store
// through a fn value), twice over when the compile-time run fails too.
const unalignedStoreProgram = `import std.raw

export Store16(b: mut [16]u8, at: int, x: u16) {
    unsafe "the caller keeps at + 2 <= 16" {
        raw.WriteUnaligned(raw.PtrCast[u8, u16](raw.Offset(raw.DataMut(mut b[..]), at)), x)
    }
}

export Store32(b: mut [16]u8, at: int, x: u32) {
    unsafe "the caller keeps at + 4 <= 16" {
        raw.WriteUnaligned(raw.PtrCast[u8, u32](raw.Offset(raw.DataMut(mut b[..]), at)), x)
    }
}

export Store64(b: mut [16]u8, at: int, x: u64) {
    unsafe "the caller keeps at + 8 <= 16" {
        raw.WriteUnaligned(raw.PtrCast[u8, u64](raw.Offset(raw.DataMut(mut b[..]), at)), x)
    }
}

Load64(b: &[16]u8, at: int): u64 {
    unsafe "the caller keeps at + 8 <= 16" {
        return raw.ReadUnaligned(raw.PtrCast[u8, u64](raw.Offset(raw.Data(b[..]), at)))
    }
}

Check(n: int, x: u64): int {
    for at in 0..8 {
        var b: [16]u8 = [0xAA; 16]
        if n == 2 {
            Store16(mut b, at, Trunc[u16](x))
        } else if n == 4 {
            Store32(mut b, at, Trunc[u32](x))
        } else {
            Store64(mut b, at, x)
        }
        for i in 0..16 {
            want: u8 = if i >= at && i < at + n { Trunc[u8](x >> (8 * (i - at))) } else { 0xAA }
            if b[i] != want {
                return 1 + 16 * n + at
            }
        }
        mask := if n == 8 { ~u64(0) } else { (u64(1) << (8 * n)) - 1 }
        if Load64(&b, at) & mask != x & mask {
            return 1 + 16 * n + at
        }
    }
    return 0
}

// ViaValue stores through raw.WriteUnaligned named as a fn value.
ViaValue(): int {
    for at in 0..8 {
        var b: [16]u8 = [0xAA; 16]
        unsafe "at + 4 <= 16" {
            g: fn(p: *u32, x: u32) = raw.WriteUnaligned[u32]
            g(raw.PtrCast[u8, u32](raw.Offset(raw.DataMut(mut b[..]), at)), 0x0102_0304)
        }
        if b[at] != 4 || b[at + 3] != 1 || b[at + 4] != 0xAA || (at > 0 && b[at - 1] != 0xAA) {
            return 200 + at
        }
    }
    return 0
}

All(): int {
    return Check(2, 0x0102) + Check(4, 0x0102_0304) + Check(8, 0x0102_0304_0506_0708) + ViaValue()
}

const AtCompileTime: int = All()

export Main(sys: &System): int {
    return All() + AtCompileTime
}
`

const unalignedStorePkg = `const Package = pkg.Package(
    name: "ustore",
    bins: [pkg.Bin(name: "ustore", root: ".")],
    unsafe_modules: ["."],
)
`

// alignedStore matches a store of an integer of the width in group 1 and
// its alignment in group 2.
var alignedStore = regexp.MustCompile(`^\s*store i(16|32|64) %[\w.]+, ptr %[\w.]+, align (\d+)`)

// fnBody returns the lines of the definition of fn in ll, or nil.
func fnBody(ll, fn string) []string {
	var body []string
	in := false
	for _, l := range strings.Split(ll, "\n") {
		if m := defineLine.FindStringSubmatch(l); m != nil {
			in = strings.Trim(m[1], `"`) == fn
			continue
		}
		if in && l == "}" {
			return body
		}
		if in {
			body = append(body, l)
		}
	}
	return body
}

// TestUnalignedStores: at dev and release, Store16, Store32 and Store64's
// one store of their width is align 1, and the program exits 0.
func TestUnalignedStores(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "ustore")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{"main.mk": unalignedStoreProgram, "pkg.mk": unalignedStorePkg} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		var mu sync.Mutex
		var lls []string
		cfg := driver.Config{Root: dir, StdDir: stdRoot(t), NoCache: true, Target: driver.HostTarget(), Profile: prof,
			TestHooks: driver.TestHooks{OnLL: func(_, ll string) {
				mu.Lock()
				lls = append(lls, ll)
				mu.Unlock()
			}}}
		s, err := driver.NewSession(cfg)
		if err != nil {
			t.Fatal(err)
		}
		exe := filepath.Join(t.TempDir(), "ustore.exe")
		r, paths, err := s.Build(exe)
		if err != nil {
			if strings.Contains(err.Error(), "LLVM") {
				t.Skipf("needs the LLVM toolchain (the gate fails on this skip): %v", err)
			}
			t.Fatal(err)
		}
		if len(r.Report.Diags) > 0 || len(paths) != 1 {
			t.Fatalf("profile %v: build:\n%s", prof, r.Report.Text(r.Sources))
		}
		for fn, width := range map[string]string{"mk.main.Store16": "16", "mk.main.Store32": "32", "mk.main.Store64": "64"} {
			var body []string
			for _, ll := range lls {
				if body = fnBody(ll, fn); body != nil {
					break
				}
			}
			if body == nil {
				t.Errorf("profile %v: %s not defined in the IR", prof, fn)
				continue
			}
			// The parameter spills are stores of the same width at the
			// type's alignment; the raw store is the one through the
			// computed pointer, the last of its width.
			last := ""
			for _, l := range body {
				if m := alignedStore.FindStringSubmatch(l); m != nil && m[1] == width {
					last = m[2]
				}
			}
			if last != "1" {
				t.Errorf("profile %v: %s's last i%s store is align %q, want 1:\n%s", prof, fn, width, last, strings.Join(body, "\n"))
			}
		}
		out, err := exec.Command(paths[0]).CombinedOutput()
		if code := 0; err != nil {
			ee, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatal(err)
			}
			code = ee.ExitCode()
			t.Errorf("profile %v: exit %d (1 + 16 * width + offset of the first bad store, doubled when the const fails too)\n%s", prof, code, out)
		}
	}
}
