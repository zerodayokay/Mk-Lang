// Why: the whole-build fingerprint fast path skips the front-end and native
// pipeline on unchanged warm builds; it is only correct if ANY change to
// sources, directory layout, manifest, flags, toolchain, build ID, cache
// contents, or output file invalidates cleanly.
//
// What: TestFastBuild_WarmHitAndInvalidation and TestFastBuild_OutputRepairAndVerify.

package driver

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"mk/internal/cache"
	"mk/internal/source"
	"mk/internal/types"
)

func TestFastBuild_WarmHitAndInvalidation(t *testing.T) {
	tools := needTools(t)
	dir := writeTree(t, incPkg("x + 1"))
	cacheDir := filepath.Join(t.TempDir(), "cache")
	outDir := t.TempDir()
	sfx := exeSuffix(HostTarget())
	outPath := filepath.Join(outDir, "app"+sfx)

	cfg := Config{Root: dir, Target: HostTarget(), Tools: tools, CacheDir: cacheDir, Timings: &bytes.Buffer{}}

	buildOne := func(c Config, out string) (Result, []byte) {
		t.Helper()
		s, err := NewSession(c)
		if err != nil {
			t.Fatal(err)
		}
		r, paths, err := s.Build(out)
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Report.Diags) > 0 || len(paths) != 1 {
			t.Fatalf("build failed: %d paths, diags=%s", len(paths), r.Report.Text(r.Sources))
		}
		b, err := os.ReadFile(paths[0])
		if err != nil {
			t.Fatal(err)
		}
		return r, b
	}
	isFastHit := func(r Result) bool {
		return len(r.Timings) == 1 && r.Timings[0].Stage == "cache"
	}

	// 1. Cold build populates the fast-build record.
	cold, coldExe := buildOne(cfg, outPath)
	if isFastHit(cold) {
		t.Fatal("cold build unexpectedly hit fast-build path")
	}

	// 2. Immediate warm rebuild to same path hits fast-build path (even within racy 2s window).
	warm, warmExe := buildOne(cfg, outPath)
	if !isFastHit(warm) {
		t.Fatalf("warm build did not hit fast-build path; timings=%v", warm.Timings)
	}
	if !bytes.Equal(warmExe, coldExe) {
		t.Fatal("warm fast-build output differs from cold build")
	}
	if !slices.Equal(warm.Trace.PacksFromCache, cold.Trace.PacksCompiled) {
		t.Errorf("warm PacksFromCache=%v, want %v", warm.Trace.PacksFromCache, cold.Trace.PacksCompiled)
	}

	// 3. Warm rebuild to a DIFFERENT output path still hits fast-build path and copies exe.
	out2 := filepath.Join(outDir, "copy"+sfx)
	warm2, warm2Exe := buildOne(cfg, out2)
	if !isFastHit(warm2) {
		t.Fatalf("warm build to second output path missed fast-build; timings=%v", warm2.Timings)
	}
	if !bytes.Equal(warm2Exe, coldExe) {
		t.Fatal("second output path differs from cold build")
	}

	// 4. Same-length rapid edit (within milliseconds) MUST invalidate fast-build!
	edited := incPkg("x + 2")
	if err := os.WriteFile(filepath.Join(dir, "lib", "lib.mk"), []byte(edited["lib/lib.mk"]), 0o644); err != nil {
		t.Fatal(err)
	}
	editRes, editExe := buildOne(cfg, outPath)
	if isFastHit(editRes) {
		t.Fatal("file content edit wrongly hit fast-build path")
	}
	if bytes.Equal(editExe, coldExe) {
		t.Fatal("edited build produced stale bytes matching pre-edit exe")
	}

	// 5. Warm rebuild after edit hits fast-build again with the new bytes.
	warmAfterEdit, warmAfterEditExe := buildOne(cfg, outPath)
	if !isFastHit(warmAfterEdit) {
		t.Fatalf("warm rebuild after edit missed fast-build; timings=%v", warmAfterEdit.Timings)
	}
	if !bytes.Equal(warmAfterEditExe, editExe) {
		t.Fatal("warm rebuild after edit returned wrong bytes")
	}

	// 6. Adding an extra .mk file with a syntax error MUST invalidate fast-build!
	badFile := filepath.Join(dir, "lib", "extra.mk")
	if err := os.WriteFile(badFile, []byte("this is not valid mk syntax @@@\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := NewSession(cfg)
	if err != nil {
		t.Fatal(err)
	}
	badRes, _, err := s.Build(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(badRes.Report.Diags) == 0 {
		t.Fatal("added broken .mk file was missed by fast-build")
	}
	if err := os.Remove(badFile); err != nil {
		t.Fatal(err)
	}

	// 7. Adding a new subdirectory with a broken .mk file MUST invalidate fast-build!
	newSub := filepath.Join(dir, "newmod")
	if err := os.MkdirAll(newSub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newSub, "bad.mk"), []byte("bad syntax @@@\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err = NewSession(cfg)
	if err != nil {
		t.Fatal(err)
	}
	badSubRes, _, err := s.Build(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(badSubRes.Report.Diags) == 0 {
		t.Fatal("added subdirectory with broken .mk file was missed by fast-build")
	}
	if err := os.RemoveAll(newSub); err != nil {
		t.Fatal(err)
	}

	// 8. Editing pkg.mk MUST invalidate fast-build!
	pkgPath := filepath.Join(dir, "pkg.mk")
	origPkg, err := os.ReadFile(pkgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pkgPath, append(slices.Clone(origPkg), []byte("\n// comment edit\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	pkgEditRes, _ := buildOne(cfg, outPath)
	if isFastHit(pkgEditRes) {
		t.Fatal("pkg.mk edit wrongly hit fast-build path")
	}

	// 9. Changing BuildID or Profile MUST invalidate fast-build!
	otherID := cfg
	otherID.BuildID = source.HashString("test", "other-compiler-v2")
	idRes, _ := buildOne(otherID, outPath)
	if isFastHit(idRes) {
		t.Fatal("different BuildID wrongly hit fast-build path")
	}

	relCfg := cfg
	relCfg.Profile = types.Release
	relRes, _ := buildOne(relCfg, filepath.Join(outDir, "rel"+sfx))
	if isFastHit(relRes) {
		t.Fatal("release profile wrongly hit dev fast-build entry")
	}
	relWarm, _ := buildOne(relCfg, filepath.Join(outDir, "rel"+sfx))
	if !isFastHit(relWarm) {
		t.Fatalf("release warm rebuild missed fast-build; timings=%v", relWarm.Timings)
	}
}

func TestFastBuild_OutputRepairAndVerify(t *testing.T) {
	tools := needTools(t)
	dir := writeTree(t, incPkg("x + 1"))
	cacheDir := filepath.Join(t.TempDir(), "cache")
	outPath := filepath.Join(t.TempDir(), "app"+exeSuffix(HostTarget()))
	cfg := Config{Root: dir, Target: HostTarget(), Tools: tools, CacheDir: cacheDir, Timings: &bytes.Buffer{}}

	run := func(c Config) (Result, []byte) {
		t.Helper()
		s, err := NewSession(c)
		if err != nil {
			t.Fatal(err)
		}
		r, paths, err := s.Build(outPath)
		if err != nil || len(r.Report.Diags) > 0 || len(paths) != 1 {
			t.Fatalf("build: err=%v paths=%d diags=%s", err, len(paths), r.Report.Text(r.Sources))
		}
		b, err := os.ReadFile(paths[0])
		if err != nil {
			t.Fatal(err)
		}
		return r, b
	}

	_, coldExe := run(cfg)

	// Corrupt the output file in place: fast-build must detect hash mismatch and restore exact bytes.
	if err := os.WriteFile(outPath, append(slices.Clone(coldExe), []byte("corrupt")...), 0o755); err != nil {
		t.Fatal(err)
	}
	r, restored := run(cfg)
	if len(r.Timings) != 1 || r.Timings[0].Stage != "cache" {
		t.Fatalf("corrupt-output restore missed fast-build; timings=%v", r.Timings)
	}
	if !bytes.Equal(restored, coldExe) {
		t.Fatal("fast-build did not restore exact bytes over modified output")
	}

	// Delete the output file: fast-build must recreate it from <cache>/bin.
	if err := os.Remove(outPath); err != nil {
		t.Fatal(err)
	}
	r, recreated := run(cfg)
	if len(r.Timings) != 1 || r.Timings[0].Stage != "cache" {
		t.Fatalf("deleted-output recreate missed fast-build; timings=%v", r.Timings)
	}
	if !bytes.Equal(recreated, coldExe) {
		t.Fatal("fast-build did not recreate exact bytes after output deletion")
	}

	// MK_CACHE_VERIFY=1 MUST bypass fast-build and run full build/link.
	verCfg := cfg
	verCfg.CacheVerify = true
	verRes, _ := run(verCfg)
	if len(verRes.Timings) == 1 && verRes.Timings[0].Stage == "cache" {
		t.Fatal("MK_CACHE_VERIFY=1 wrongly took fast-build path")
	}

	// Deleting <cache>/v1/bin MUST cause fast-build to miss and relink.
	if err := os.RemoveAll(cache.DirPath(cacheDir, cache.BinDir)); err != nil {
		t.Fatal(err)
	}
	relinkRes, relinkExe := run(cfg)
	if len(relinkRes.Timings) == 1 && relinkRes.Timings[0].Stage == "cache" {
		t.Fatal("fast-build hit after <cache>/v1/bin was deleted")
	}
	if !bytes.Equal(relinkExe, coldExe) {
		t.Fatal("relinked bytes differ from cold build")
	}
}
