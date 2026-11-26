// Why: a warm build with nothing changed should not pay for clang or the
// link, and toolchain discovery should not start clang on every command.
// What: tests that mk build reuses the link its key names (with the PDB,
// and never with --no-cache or MK_CACHE_VERIFY), and that versionLine and
// discovery memoize clang --version per file.
// How: builds small programs cold and warm through reuseBuild, compares the
// bytes and whether the link ran, and runs versionLine over fake clang files
// whose size and mtime change.

package driver

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"syscall"
	"testing"
	"time"

	"mk/internal/cache"
	"mk/internal/source"
	"mk/internal/toolchain"
	"mk/internal/types"
)

// reuseBuild is one mk build into out; it returns the result, the
// executable's bytes and whether the link ran (incBuild's OnLink saw the
// objects of one).
func reuseBuild(t *testing.T, cfg Config, out string) (Result, []byte, bool) {
	t.Helper()
	r, exe, objs := incBuild(t, cfg, out)
	if (len(objs) > 0) != r.Trace.Linked {
		t.Errorf("the link hook saw %d objects, Trace.Linked %v", len(objs), r.Trace.Linked)
	}
	return r, exe, r.Trace.Linked
}

// binFiles lists the regular files under the cache's bin directory.
func binFiles(t *testing.T, cacheDir string) []string {
	t.Helper()
	var files []string
	filepath.WalkDir(cache.DirPath(cacheDir, cache.BinDir), func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			files = append(files, p)
		}
		return nil
	})
	return files
}

// TestBuildReusesLink: a warm mk build with nothing changed, into
// another path, runs neither clang nor the link: it copies the executable
// its link key names, with the bytes of the cold link, and the trace says
// every pack came from the cache. The copy is the build's own file: an
// edit of it does not reach the next build. A body edit misses the key
// and links, with the bytes of a cold build of the edited source. Dev on
// both targets (the Linux build is a cross build) and release on Windows.
func TestBuildReusesLink(t *testing.T) {
	tools := needTools(t)
	for _, c := range []struct {
		target types.Target
		prof   types.Profile
	}{{types.X86_64Windows, types.Dev}, {types.X86_64Linux, types.Dev}, {types.X86_64Windows, types.Release}} {
		t.Run(c.target.Name+"/"+c.prof.String(), func(t *testing.T) {
			dir := writeTree(t, incPkg("x + 1"))
			cacheDir := filepath.Join(t.TempDir(), "cache")
			out := t.TempDir()
			cfg := Config{Root: dir, Target: c.target, Profile: c.prof, Tools: tools, CacheDir: cacheDir}
			sfx := exeSuffix(c.target)
			cold, coldExe, linked := reuseBuild(t, cfg, filepath.Join(out, "cold"+sfx))
			if !linked || len(cold.Trace.PacksCompiled) == 0 {
				t.Fatalf("cold build: linked %v, compiled %v", linked, cold.Trace.PacksCompiled)
			}
			stored := binFiles(t, cacheDir)
			if len(stored) != 1 {
				t.Fatalf("cold build stored %v under bin, want one executable", stored)
			}
			warmOut := filepath.Join(out, "warm"+sfx)
			warm, warmExe, linked := reuseBuild(t, cfg, warmOut)
			if linked || len(warm.Trace.PacksCompiled) != 0 {
				t.Errorf("warm build: linked %v, compiled %v; want neither", linked, warm.Trace.PacksCompiled)
			}
			if !slices.Equal(warm.Trace.PacksFromCache, cold.Trace.PacksCompiled) {
				t.Errorf("warm from cache %v, want the cold build's %v", warm.Trace.PacksFromCache, cold.Trace.PacksCompiled)
			}
			if !bytes.Equal(coldExe, warmExe) {
				t.Error("the reused executable differs from the cold link")
			}
			a, err1 := os.Stat(warmOut)
			b, err2 := os.Stat(stored[0])
			if err1 != nil || err2 != nil || os.SameFile(a, b) {
				t.Fatalf("the build's executable is the store's file (%v, %v)", err1, err2)
			}
			if runtime.GOOS != "windows" && a.Mode().Perm()&0o111 == 0 {
				t.Errorf("the reused executable has mode %v, not executable", a.Mode())
			}
			// An edit in place of the build's executable stays there.
			if err := os.WriteFile(warmOut, append(warmExe, "tail"...), 0o755); err != nil {
				t.Fatal(err)
			}
			_, againExe, linked := reuseBuild(t, cfg, filepath.Join(out, "again"+sfx))
			if linked || !bytes.Equal(againExe, coldExe) {
				t.Errorf("after editing the copy: linked %v, bytes equal %v; want a reuse of the stored bytes", linked, bytes.Equal(againExe, coldExe))
			}

			edited := incPkg("x + 2")
			if err := os.WriteFile(filepath.Join(dir, "lib", "lib.mk"), []byte(edited["lib/lib.mk"]), 0o644); err != nil {
				t.Fatal(err)
			}
			edit, editExe, linked := reuseBuild(t, cfg, warmOut)
			if !linked || len(edit.Trace.PacksCompiled) != 1 {
				t.Errorf("edit build: linked %v, compiled %v; want a link and one pack", linked, edit.Trace.PacksCompiled)
			}
			fresh := Config{Root: writeTree(t, edited), Target: c.target, Profile: c.prof, Tools: tools, CacheDir: filepath.Join(t.TempDir(), "c2")}
			_, freshExe, _ := reuseBuild(t, fresh, filepath.Join(out, "fresh"+sfx))
			if !bytes.Equal(editExe, freshExe) {
				t.Error("the edit's link differs from a cold build of the edited source")
			}
			if got := len(binFiles(t, cacheDir)); got != 2 {
				t.Errorf("%d executables under bin after the edit, want 2", got)
			}
		})
	}
}

// TestDebugReuseCopiesPDB: a --debug COFF build stores the PDB with
// the executable; a hit copies both beside the output, with the stored
// bytes. A stored executable whose PDB is gone is not a hit: the build
// links again and stores both.
func TestDebugReuseCopiesPDB(t *testing.T) {
	tools := needTools(t)
	dir := writeTree(t, incPkg("x + 1"))
	cacheDir := filepath.Join(t.TempDir(), "cache")
	cfg := Config{Root: dir, Target: types.X86_64Windows, Debug: true, Tools: tools, CacheDir: cacheDir}
	coldDir, warmDir := t.TempDir(), t.TempDir()
	_, coldExe, linked := reuseBuild(t, cfg, filepath.Join(coldDir, "app.exe"))
	if !linked {
		t.Fatal("cold build did not link")
	}
	var pdbs []string
	for _, f := range binFiles(t, cacheDir) {
		if filepath.Ext(f) == ".pdb" {
			pdbs = append(pdbs, f)
		}
	}
	if len(pdbs) != 1 || filepath.Base(pdbs[0]) != "app.pdb" {
		t.Fatalf("stored PDBs %v, want one app.pdb", pdbs)
	}
	_, warmExe, linked := reuseBuild(t, cfg, filepath.Join(warmDir, "app.exe"))
	if linked || !bytes.Equal(warmExe, coldExe) {
		t.Fatalf("warm --debug build: linked %v, same bytes %v", linked, bytes.Equal(warmExe, coldExe))
	}
	stored, err1 := os.ReadFile(pdbs[0])
	placed, err2 := os.ReadFile(filepath.Join(warmDir, "app.pdb"))
	if err1 != nil || err2 != nil || !bytes.Equal(stored, placed) {
		t.Fatalf("the PDB beside the reused executable: %v, %v, equal %v", err1, err2, bytes.Equal(stored, placed))
	}
	if err := os.Remove(pdbs[0]); err != nil {
		t.Fatal(err)
	}
	_, _, linked = reuseBuild(t, cfg, filepath.Join(t.TempDir(), "app.exe"))
	if !linked {
		t.Error("a stored executable without its PDB was reused")
	}
	if _, err := os.Stat(pdbs[0]); err != nil {
		t.Errorf("the relink did not store the PDB again: %v", err)
	}
}

// TestNoReuseWithoutCacheOrUnderVerify: --no-cache links every time
// and writes nothing under bin; MK_CACHE_VERIFY links every time over a
// cache that holds the link.
func TestNoReuseWithoutCacheOrUnderVerify(t *testing.T) {
	tools := needTools(t)
	dir := writeTree(t, incPkg("x + 1"))
	cacheDir := filepath.Join(t.TempDir(), "cache")
	out := t.TempDir()
	for i := range 2 {
		_, _, linked := reuseBuild(t, Config{Root: dir, Tools: tools, CacheDir: cacheDir, NoCache: true}, filepath.Join(out, "nc.exe"))
		if !linked {
			t.Errorf("--no-cache build %d did not link", i)
		}
	}
	if got := binFiles(t, cacheDir); len(got) != 0 {
		t.Errorf("--no-cache wrote %v under bin", got)
	}
	cfg := Config{Root: dir, Tools: tools, CacheDir: cacheDir}
	reuseBuild(t, cfg, filepath.Join(out, "a.exe"))
	verify := cfg
	verify.CacheVerify = true
	r, _, linked := reuseBuild(t, verify, filepath.Join(out, "v.exe"))
	if !linked || len(r.Report.Diags) != 0 {
		t.Errorf("MK_CACHE_VERIFY build: linked %v, %v; want a link and no diagnostic", linked, codes(r))
	}
}

// fakeClang writes a file standing for clang, its mtime an hour back so a
// hint of it is not racy.
func fakeClang(t *testing.T, path, text string, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// TestVersionLineMemo: versionLine runs clang once per file: a second
// call in another store handle on the same cache returns the first output
// verbatim and runs nothing; a replaced file (another size and mtime) runs
// again; a failed run is not kept; with no store every call runs.
func TestVersionLineMemo(t *testing.T) {
	cacheDir := t.TempDir()
	clang := filepath.Join(t.TempDir(), "clang.exe")
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	fakeClang(t, clang, "clang 1", old)
	runs := 0
	line := "clang version 21.1.8\nTarget: x86_64-pc-windows-msvc\n"
	run := func(string) (string, error) { runs++; return line, nil }
	open := func() *cache.Store {
		st, err := cache.Open(cacheDir, cache.Options{})
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	for i, want := range []int{1, 1} {
		got, err := versionLine(open(), clang, run)
		if err != nil || got != line || runs != want {
			t.Fatalf("call %d: %q, %v, %d runs; want %q and %d runs", i, got, err, runs, line, want)
		}
	}
	fakeClang(t, clang, "clang 2, larger", old.Add(time.Minute))
	line = "clang version 21.1.9\n"
	if got, _ := versionLine(open(), clang, run); got != line || runs != 2 {
		t.Errorf("replaced clang: %q after %d runs, want %q after 2", got, runs, line)
	}
	if got, _ := versionLine(open(), clang, run); got != line || runs != 2 {
		t.Errorf("replaced clang, second call: %q after %d runs", got, runs)
	}
	fakeClang(t, clang, "clang 3, failing", old.Add(2*time.Minute))
	fail := func(string) (string, error) { runs++; return "", os.ErrPermission }
	for range 2 {
		if _, err := versionLine(open(), clang, fail); err == nil {
			t.Error("a failed run returned no error")
		}
	}
	if runs != 4 {
		t.Errorf("%d runs, want 4 (a failure is not memoized)", runs)
	}
	for range 2 {
		versionLine(nil, clang, run)
	}
	if runs != 6 {
		t.Errorf("%d runs, want 6 (no store, no memo)", runs)
	}

	// A clang reached through a symbolic link (Linux's /usr/bin/clang-N)
	// is memoized by the file it names.
	t.Run("symlink", func(t *testing.T) {
		const privilegeNotHeld = syscall.Errno(1314) // Windows ERROR_PRIVILEGE_NOT_HELD
		real := filepath.Join(t.TempDir(), "clang-21")
		fakeClang(t, real, "clang 4", old)
		link := filepath.Join(t.TempDir(), "clang")
		if err := os.Symlink(real, link); err != nil {
			if runtime.GOOS == "windows" && errors.Is(err, privilegeNotHeld) {
				t.Skipf("symbolic link privilege unavailable: %v", err)
			}
			t.Fatalf("symlink: %v", err)
		}
		runs = 0
		for range 2 {
			got, err := versionLine(open(), link, run)
			if err != nil || got != line {
				t.Fatalf("through a symbolic link: %q, %v; want %q", got, err, line)
			}
		}
		if runs != 1 {
			t.Errorf("through a symbolic link: %d runs, want 1", runs)
		}
	})
}

// TestDiscoveryMemoIdentity: with discovery left to the driver, a
// warm build starts no `clang --version`, and its toolchain identity, in
// every object and link key, equals a direct Discover's; --no-cache and
// MK_CACHE_VERIFY run it every time.
func TestDiscoveryMemoIdentity(t *testing.T) {
	// The builds pass --llvm, and the identity names the rule that chose the
	// directory, so the direct Discover passes the same directory as --llvm.
	direct, err := toolchain.Discover(toolchain.Options{LLVMDir: needTools(t).Dir})
	if err != nil {
		t.Fatal(err)
	}
	dir := writeTree(t, incPkg("x + 1"))
	cacheDir := filepath.Join(t.TempDir(), "cache")
	build := func(cfg Config) (int, source.Hash) {
		t.Helper()
		runs := 0
		cfg.Root, cfg.LLVMDir, cfg.CacheDir = dir, direct.Dir, cacheDir
		cfg.TestHooks.OnVersionRun = func(string) { runs++ }
		s, err := NewSession(cfg)
		if err != nil {
			t.Fatal(err)
		}
		r, _, err := s.Build(filepath.Join(t.TempDir(), "app.exe"))
		if err != nil || len(r.Report.Diags) > 0 {
			skipKnownBlockers(t, r)
			t.Fatalf("build: %v\n%s", err, r.Report.Text(r.Sources))
		}
		return runs, s.cfg.Tools.Identity()
	}
	for i, c := range []struct {
		cfg  Config
		runs int
	}{{Config{}, 1}, {Config{}, 0}, {Config{}, 0}, {Config{NoCache: true}, 1}, {Config{CacheVerify: true}, 1}} {
		runs, id := build(c.cfg)
		if runs != c.runs {
			t.Errorf("build %d: %d clang --version runs, want %d", i, runs, c.runs)
		}
		if id != direct.Identity() {
			t.Errorf("build %d: toolchain identity %v, want Discover's %v", i, id, direct.Identity())
		}
	}
}
