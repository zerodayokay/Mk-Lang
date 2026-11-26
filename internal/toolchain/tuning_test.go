// Why: the Tuning switch (tuning.go) passes optimizer experiments to LLVM,
// and must change nothing when it is off.
// What: TestZeroTuningArgs, TestParseTuning,
// TestTuningReachesEveryLTOStep and TestUnrollReachesPackCompile.
// How: the zero Tuning leaves every command line as it was, each option
// reaches every LTO step, and the parser refuses what it does not know
// (irce among them).

package toolchain

import (
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// TestZeroTuningArgs: with the zero Tuning, the compile, link and
// ThinLTO backend command lines are the untuned ones, written out
// here literally (the driver's helloLinkGolden pins the link lines of a
// real build too).
func TestZeroTuningArgs(t *testing.T) {
	c, err := compileArgs(CompileJob{Triple: "x86_64-pc-windows-msvc", Input: hostAbs("w", "p.ll"), Output: hostAbs("w", "p.o"), Opt: O3, LTO: true, CPU: "x86-64-v2"})
	if err != nil {
		t.Fatal(err)
	}
	wantC := []string{"--target=x86_64-pc-windows-msvc19.33.0", "-x", "ir", "-c", hostAbs("w", "p.ll"), "-o", hostAbs("w", "p.o"), "-O3", "-flto=thin", "-march=x86-64-v2",
		"-ffunction-sections", "-fdata-sections", "-fno-addrsig", "-fdebug-compilation-dir=.", "-mno-incremental-linker-compatible", "-Xclang", "-gno-codeview-command-line"}
	if !slices.Equal(c, wantC) {
		t.Errorf("compile:\n got %q\nwant %q", c, wantC)
	}
	b := thinBackendArgs("m0000.o", "m0000.o.thinlto.bc", "m0000.native.o", Tuning{})
	wantB := []string{"--target=x86_64-pc-windows-msvc19.33.0", "-O3", "-x", "ir", "m0000.o", "-fthinlto-index=m0000.o.thinlto.bc",
		"-c", "-o", "m0000.native.o", "-Wno-override-module", "-ffunction-sections", "-fdata-sections", "-fno-addrsig",
		"-fdebug-compilation-dir=.", "-mno-incremental-linker-compatible", "-Xclang", "-gno-codeview-command-line",
		"-falign-loops=32", "-mllvm", "-unroll-threshold=400"}
	if !slices.Equal(b, wantB) {
		t.Errorf("backend:\n got %q\nwant %q", b, wantB)
	}
	coff, err := linkArgs(LinkJob{Format: COFF, Output: hostAbs("o", "a.exe"), Objects: []string{hostAbs("o", "p.o")}, Release: true, LTO: true,
		LTOCacheDir: hostAbs("c", "thinlto"), LTOCacheSize: 1024, Jobs: 4})
	if err != nil {
		t.Fatal(err)
	}
	wantCOFF := []string{"/nologo", "/nodefaultlib", "/entry:mk_start", "/subsystem:console", "/osversion:10.0", "/machine:x64", "/Brepro",
		"/out:" + hostAbs("o", "a.exe"), hostAbs("o", "p.o"), "/mllvm:-align-loops=32", "/mllvm:-unroll-threshold=400", "/opt:ref", "/opt:icf",
		"/opt:lldlto=3", "/lldltocache:" + hostAbs("c", "thinlto"), "/lldltocachepolicy:cache_size_bytes=1024:prune_after=48h", "/opt:lldltojobs=4"}
	if !slices.Equal(coff, wantCOFF) {
		t.Errorf("COFF link:\n got %q\nwant %q", coff, wantCOFF)
	}
	elf, err := linkArgs(LinkJob{Format: ELF, Output: hostAbs("o", "a"), Objects: []string{hostAbs("o", "p.o")}, Release: true, LTO: true,
		LTOCacheDir: hostAbs("c", "thinlto"), LTOCacheSize: 1024, Jobs: 4})
	if err != nil {
		t.Fatal(err)
	}
	wantELF := []string{"-static", "-nostdlib", "-e", "_start", "--build-id=none", "-z", "noexecstack", "-o", hostAbs("o", "a"), hostAbs("o", "p.o"),
		"-mllvm", "-align-loops=32", "-mllvm", "-unroll-threshold=400", "--gc-sections", "--strip-all", "--lto-O3",
		"--thinlto-cache-dir=" + hostAbs("c", "thinlto"), "--thinlto-cache-policy=cache_size_bytes=1024:prune_after=48h", "--thinlto-jobs=4"}
	if !slices.Equal(elf, wantELF) {
		t.Errorf("ELF link:\n got %q\nwant %q", elf, wantELF)
	}
	if !thinRoute(LinkJob{Format: COFF, LTO: true}) {
		t.Error("a default COFF LTO link does not go through thinLink")
	}
	if experiment != "" {
		t.Errorf("this test binary was built with an experiment: %q", experiment)
	}
	if tu, err := Baked(); err != nil || !tu.IsZero() {
		t.Errorf("Baked() of a default binary: %v, %v", tu, err)
	}
}

// hostAbs is an absolute path on this host: under C:\ on Windows, under /
// elsewhere.
func hostAbs(dir, name string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(`C:\`, dir, name)
	}
	return filepath.Join("/", dir, name)
}

// TestParseTuning: names, values and the canonical String form.
func TestParseTuning(t *testing.T) {
	for _, c := range []struct {
		in, canon string
		want      Tuning
	}{
		{"", "", Tuning{}},
		{"unroll=300", "unroll=300", Tuning{Unroll: 300}},
		{"unroll=llvm", "unroll=llvm", Tuning{Unroll: UnrollLLVM}},
		{"fatlto,noslp,unroll=600", "unroll=600,noslp,fatlto", Tuning{Unroll: 600, NoSLP: true, FatLTO: true}},
		{"interleave=4,extravec,novec", "novec,extravec,interleave=4", Tuning{Interleave: 4, ExtraVec: true, NoLoopVec: true}},
	} {
		got, err := ParseTuning(c.in)
		if err != nil || got != c.want || got.String() != c.canon {
			t.Errorf("ParseTuning(%q) = %+v, %v; String %q, want %+v and %q", c.in, got, err, got.String(), c.want, c.canon)
		}
		if back, err := ParseTuning(got.String()); err != nil || back != got {
			t.Errorf("ParseTuning(%q.String()) = %+v, %v", c.in, back, err)
		}
	}
	for _, c := range []struct{ in, msg string }{
		{"irce", "the experiment cannot run"},
		{"unroll", "takes a number"},
		{"unroll=0", "takes a number"},
		{"unroll=x", "takes a number"},
		{"interleave=17", "takes a number"},
		{"novec=1", "takes no value"},
		{"novec,novec", "given twice"},
		{"fatlto,", "unknown name"},
		{"thinlto", "unknown name"},
	} {
		if _, err := ParseTuning(c.in); err == nil || !strings.Contains(err.Error(), c.msg) {
			t.Errorf("ParseTuning(%q): error %v, want one containing %q", c.in, err, c.msg)
		}
	}
}

// TestTuningReachesEveryLTOStep: each option is in the ThinLTO
// backend's line (COFF, thinLink) and in both linkers' in-process LTO
// lines; unroll=llvm passes no threshold; a non-zero Tuning drops lld's
// ThinLTO cache, whose key does not hash -mllvm options, and fatlto also
// the backend job count and the thinLink route, and makes the pack
// compile -flto=full.
func TestTuningReachesEveryLTOStep(t *testing.T) {
	tu := Tuning{Unroll: 600, NoLoopVec: true, NoSLP: true, ExtraVec: true, Interleave: 2}
	want := []string{"-unroll-threshold=600", "-vectorize-loops=false", "-vectorize-slp=false", "-extra-vectorizer-passes", "-force-vector-interleave=2"}
	b := thinBackendArgs("m.o", "m.bc", "m.n.o", tu)
	var mllvm []string
	for i := 0; i+1 < len(b); i++ {
		if b[i] == "-mllvm" {
			mllvm = append(mllvm, b[i+1])
		}
	}
	if !slices.Equal(mllvm, want) {
		t.Errorf("backend -mllvm options %q, want %q", mllvm, want)
	}
	job := func(f Format, tu Tuning) LinkJob {
		out, obj, dir := hostAbs("o", "a"), hostAbs("o", "p.o"), hostAbs("c", "thinlto")
		return LinkJob{Format: f, Output: out, Objects: []string{obj}, Release: true, LTO: true, LTOCacheDir: dir, LTOCacheSize: 1024, Jobs: 4, Tune: tu}
	}
	for _, f := range []Format{COFF, ELF} {
		args, err := linkArgs(job(f, tu))
		if err != nil {
			t.Fatal(err)
		}
		line := strings.Join(args, " ")
		for _, o := range append([]string{ltoLoopAlign}, want...) {
			if !strings.Contains(line, o) {
				t.Errorf("format %d: link line lacks %s: %s", f, o, line)
			}
		}
		if strings.Contains(line, "-unroll-threshold=400") || strings.Contains(line, "cache") {
			t.Errorf("format %d: the default threshold or an lld ThinLTO cache flag is left: %s", f, line)
		}
		if !strings.Contains(line, "jobs=4") {
			t.Errorf("format %d: a ThinLTO experiment lost its backend jobs: %s", f, line)
		}
		args, _ = linkArgs(job(f, Tuning{Unroll: UnrollLLVM}))
		if line := strings.Join(args, " "); strings.Contains(line, "unroll-threshold") || !strings.Contains(line, "align-loops=32") {
			t.Errorf("format %d: unroll=llvm: %s", f, line)
		}
		args, _ = linkArgs(job(f, Tuning{FatLTO: true}))
		if line := strings.Join(args, " "); strings.Contains(line, "jobs") || strings.Contains(line, "cache") || !strings.Contains(line, "-unroll-threshold=400") {
			t.Errorf("format %d: fatlto: %s", f, line)
		}
	}
	b = thinBackendArgs("m.o", "m.bc", "m.n.o", Tuning{Unroll: UnrollLLVM})
	if slices.Contains(b, "-mllvm") || b[len(b)-1] != "-falign-loops=32" {
		t.Errorf("backend, unroll=llvm: %q", b)
	}
	if thinRoute(LinkJob{Format: COFF, LTO: true, Tune: Tuning{FatLTO: true}}) || !thinRoute(LinkJob{Format: COFF, LTO: true, Tune: tu}) {
		t.Error("thinRoute: fatlto must link in process, a ThinLTO experiment through thinLink")
	}
	c, err := compileArgs(CompileJob{Triple: "x86_64-unknown-linux-gnu", Input: hostAbs("w", "p.ll"), Output: hostAbs("w", "p.o"), Opt: O3, LTO: true, Tune: Tuning{FatLTO: true, NoSLP: true}})
	if err != nil || !slices.Contains(c, "-flto=full") || slices.Contains(c, "-flto=thin") ||
		!slices.Equal(c[len(c)-4:], []string{"-mllvm", "-unroll-threshold=400", "-mllvm", "-vectorize-slp=false"}) {
		t.Errorf("fatlto compile: %q, %v", c, err)
	}
	c, _ = compileArgs(CompileJob{Triple: "x86_64-unknown-linux-gnu", Input: hostAbs("w", "p.ll"), Output: hostAbs("w", "p.o"), Opt: O3, Tune: Tuning{FatLTO: true}})
	if slices.ContainsFunc(c, func(a string) bool { return strings.HasPrefix(a, "-flto") }) {
		t.Errorf("fatlto without LTO (the support pack) must stay native: %q", c)
	}
	for _, j := range []CompileJob{{LTO: true, Tune: Tuning{NoSLP: true}}, {Tune: Tuning{FatLTO: true}},
		{LTO: true, Tune: Tuning{Unroll: UnrollLLVM, NoLoopVec: true, ExtraVec: true, Interleave: 4}}, {Tune: Tuning{Unroll: 600}}} {
		if k := j.CompileKey(); k != "" {
			t.Errorf("CompileKey of %+v is %q; only fatlto or an unroll threshold changes an LTO object", j, k)
		}
	}
	if k := (CompileJob{LTO: true, Tune: Tuning{FatLTO: true, Unroll: 600}}).CompileKey(); k != "flto=full unroll=600,fatlto" {
		t.Errorf("fatlto CompileKey %q", k)
	}
	if k := (CompileJob{LTO: true, Tune: Tuning{Unroll: 600, NoSLP: true}}).CompileKey(); k != "flto=thin -unroll-threshold=600" {
		t.Errorf("unroll=600 ThinLTO CompileKey %q", k)
	}
}

// TestUnrollReachesPackCompile: ThinLTO's pre-link pipeline runs the
// full unroller, so unroll=<N> is on every release pack compile's line, and
// nothing else of a Tune is; unroll=llvm and the zero Tuning pass no
// threshold there (LLVM's 300); the support pack (no LTO) never takes one.
func TestUnrollReachesPackCompile(t *testing.T) {
	job := func(lto bool, tu Tuning) []string {
		t.Helper()
		c, err := compileArgs(CompileJob{Triple: "x86_64-pc-windows-msvc", Input: hostAbs("w", "p.ll"), Output: hostAbs("w", "p.o"), Opt: O3, LTO: lto, CPU: "x86-64-v2", Tune: tu})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	plain := job(true, Tuning{})
	c := job(true, Tuning{Unroll: 600, NoLoopVec: true, NoSLP: true, ExtraVec: true, Interleave: 2})
	if want := append(append([]string(nil), plain...), "-mllvm", "-unroll-threshold=600"); !slices.Equal(c, want) {
		t.Errorf("unroll=600 pack compile:\n got %q\nwant %q", c, want)
	}
	for _, tu := range []Tuning{{Unroll: UnrollLLVM}, {NoLoopVec: true}, {NoSLP: true, ExtraVec: true, Interleave: 4}} {
		if c := job(true, tu); !slices.Equal(c, plain) {
			t.Errorf("%s: the pack compile changed: %q", tu, c)
		}
	}
	if c := job(false, Tuning{Unroll: 600}); slices.Contains(c, "-mllvm") {
		t.Errorf("the support pack's compile took the threshold: %q", c)
	}
}
