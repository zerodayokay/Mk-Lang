// Why: direct -cc1 invocation probed via `clang -###` must produce objects
// byte-identical to the clang driver across -O0, -O3 -flto=thin, and COFF
// ThinLTO backends, persist cleanly through CC1Get/CC1Put, and fall back to
// the driver on unexpected -### output.
//
// What: TestCC1MatchesDriver, TestCC1TemplatePersistence, and
// TestParseCC1ProbeFallback.

package toolchain

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"mk/internal/source"
)

func TestCC1MatchesDriver(t *testing.T) {
	tl := realTools(t)
	const triple = "x86_64-pc-windows-msvc"
	cases := []struct {
		name string
		opt  OptLevel
		lto  bool
	}{
		{"O0", O0, false},
		{"O3-thinlto", O3, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dirA := filepath.Join(root, "drv")
			dirB := filepath.Join(root, "cc1-longer-dir")
			for _, d := range []string{dirA, dirB} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			src := []byte("source_filename = \"m.ll\"\n" + reproLL)
			inA, outA := filepath.Join(dirA, "m.ll"), filepath.Join(dirA, "m.o")
			inB, outB := filepath.Join(dirB, "m.ll"), filepath.Join(dirB, "m.o")
			if err := os.WriteFile(inA, src, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(inB, src, 0o644); err != nil {
				t.Fatal(err)
			}
			jobA := CompileJob{Triple: triple, Input: inA, Output: outA, Opt: tc.opt, LTO: tc.lto, CPU: "x86-64-v2"}
			drvArgs, err := compileArgs(jobA)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := run(context.Background(), NameClang, tl.Clang, drvArgs, dirA); err != nil {
				t.Fatalf("driver compile: %v", err)
			}
			jobB := CompileJob{Triple: triple, Input: inB, Output: outB, Opt: tc.opt, LTO: tc.lto, CPU: "x86-64-v2"}
			cc1Args := tl.compileCC1Args(context.Background(), jobB, drvArgs)
			if len(cc1Args) == 0 || cc1Args[0] != "-cc1" {
				t.Fatalf("expected -cc1 args, got %q", cc1Args)
			}
			if err := tl.Compile(context.Background(), jobB); err != nil {
				t.Fatalf("cc1 compile: %v", err)
			}
			if !sameBytes(t, outA, outB) {
				t.Errorf("%s: -cc1 object differs from driver object", tc.name)
			}
		})
	}
}

func TestCC1TemplatePersistence(t *testing.T) {
	base := realTools(t)
	store := map[source.Hash][]byte{}
	var puts, hits atomic.Int32
	mkTools := func() *Tools {
		cp := *base
		cp.cc1 = newCC1Cache(
			func(k source.Hash) ([]byte, bool) {
				b, ok := store[k]
				if ok {
					hits.Add(1)
				}
				return b, ok
			},
			func(k source.Hash, b []byte) {
				puts.Add(1)
				store[k] = append([]byte(nil), b...)
			},
		)
		return &cp
	}
	job := CompileJob{Triple: "x86_64-pc-windows-msvc", Opt: O0, CPU: "x86-64-v2"}
	t1 := mkTools()
	t1.WarmCC1(context.Background(), job)
	if puts.Load() != 1 || hits.Load() != 0 {
		t.Fatalf("cold warm: puts=%d hits=%d, want 1, 0", puts.Load(), hits.Load())
	}
	t2 := mkTools()
	t2.WarmCC1(context.Background(), job)
	if puts.Load() != 1 || hits.Load() != 1 {
		t.Fatalf("cached warm: puts=%d hits=%d, want 1, 1", puts.Load(), hits.Load())
	}
}

func TestParseCC1ProbeFallback(t *testing.T) {
	if tmpl := parseCC1Probe("no cc1 line here\n", "/a/in.ll", "/a/out.o", ""); tmpl != nil {
		t.Errorf("expected nil for empty probe, got %+v", tmpl)
	}
	two := "\"clang\" \"-cc1\" \"-o\" \"/a/out.o\" \"/a/in.ll\"\n\"clang\" \"-cc1\" \"-o\" \"/a/out.o\" \"/a/in.ll\"\n"
	if tmpl := parseCC1Probe(two, "/a/in.ll", "/a/out.o", ""); tmpl != nil {
		t.Errorf("expected nil for multi-cc1 probe, got %+v", tmpl)
	}
	leaked := "\"clang\" \"-cc1\" \"-D__MK_CC1_EXTRA__\" \"-o\" \"/a/out.o\" \"/a/in.ll\"\n"
	if tmpl := parseCC1Probe(leaked, "/a/in.ll", "/a/out.o", ""); tmpl != nil {
		t.Errorf("expected nil for unexpected sentinel, got %+v", tmpl)
	}
}
