// Why: the lazy-binding object is hand-written assembly every lazily bound
// call goes through: its layout is std's resolver's contract, and its stub
// must keep the arguments and unwind.
// What: TestLazyGolden, TestLazyUnwind and TestLazyRefusals.
// How: EmitLazy of two libraries against testdata/lazy.x86_64-windows.ll.golden;
// the object clang makes of it read with llvm-readobj (unwind entries and
// symbols).

package codegen

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mk/internal/types"
)

// lazyLibs are the two libraries of the lazy tests: a user link's flags
// (0xA00) with two names, and std's System32 flags (0x800) with one.
var lazyLibs = []LazyLib{{File: "mklazy.dll", Flags: 0xA00, Names: []string{"MkAdd", "MkSub"}},
	{File: "crypt32.dll", Flags: 0x800, Names: []string{"CertOpenStore"}}}

func lazyLL(t *testing.T) string {
	t.Helper()
	var b bytes.Buffer
	if err := EmitLazy(lazyLibs, Config{Target: types.X86_64Windows}, &b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestLazyGolden: the object's text, emitted twice the same.
func TestLazyGolden(t *testing.T) {
	got := lazyLL(t)
	if again := lazyLL(t); again != got {
		t.Fatal("two emissions of the lazy object differ")
	}
	path := filepath.Join("testdata", "lazy."+types.X86_64Windows.Name+".ll.golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if string(want) != got {
		t.Errorf("the lazy object differs from %s (run with -update and read the diff)", path)
	}
}

// TestLazyUnwind: clang assembles the object; mk_lazy_stub has an unwind
// entry that records its push of rbp and rbp as its frame register, so a
// walk from MkLazyResolve (a panic's backtrace, a fault) steps through it
// to the caller; every name gets its thunk S and its slot __imp_S.
func TestLazyUnwind(t *testing.T) {
	obj := clangC(t, types.X86_64Windows, t.TempDir(), "lazy", lazyLL(t), "-O2")
	unw := runTool(t, filepath.Join(needLLVM(t), "llvm-readobj"), "--unwind", obj)
	i := strings.Index(unw, "StartAddress: "+symLazyStub)
	if i < 0 {
		t.Fatalf("mk_lazy_stub has no unwind entry:\n%s", unw)
	}
	entry := unw[i:]
	if j := strings.Index(entry[1:], "StartAddress: "); j >= 0 {
		entry = entry[:j+1]
	}
	for _, want := range []string{"PUSH_NONVOL reg=RBP", "SET_FPREG", "FrameRegister: RBP"} {
		if !strings.Contains(entry, want) {
			t.Errorf("mk_lazy_stub's unwind entry lacks %s:\n%s", want, entry)
		}
	}
	syms := runTool(t, filepath.Join(needLLVM(t), "llvm-nm"), obj)
	for _, l := range lazyLibs {
		for _, n := range l.Names {
			for _, s := range []string{" T " + n, " D " + LazySlot(n)} {
				if !strings.Contains(syms, s+"\n") && !strings.Contains(syms, s+"\r\n") {
					t.Errorf("no %q among the object's symbols:\n%s", s, syms)
				}
			}
		}
	}
	if !strings.Contains(syms, " U "+symLazyResolve) {
		t.Errorf("the stub does not call %s:\n%s", symLazyResolve, syms)
	}
}

// TestLazyRefusals: a name that is not a C identifier, and any target but
// x86_64-windows, are internal errors, never assembly.
func TestLazyRefusals(t *testing.T) {
	for _, n := range []string{"", "1x", "a.b", "a b", "?f@@YAXXZ"} {
		if LazySymOK(n) {
			t.Errorf("LazySymOK(%q)", n)
		}
	}
	for _, n := range []string{"MkAdd", "_x", "a$b", "D3D12CreateDevice"} {
		if !LazySymOK(n) {
			t.Errorf("!LazySymOK(%q)", n)
		}
	}
	var b bytes.Buffer
	if err := EmitLazy([]LazyLib{{File: "x.dll", Names: []string{"a.b"}}}, Config{Target: types.X86_64Windows}, &b); err == nil {
		t.Error("EmitLazy took a name with a dot")
	}
	if err := EmitLazy(lazyLibs, Config{Target: types.X86_64Linux}, &b); err == nil {
		t.Error("EmitLazy emitted for x86_64-linux")
	}
}
