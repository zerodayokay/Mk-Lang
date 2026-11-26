// Why: a release is only trustworthy if its archives are reproducible and its
// signature checks catch tampering.
// What: tests of build, sums, sign and verify: two builds give identical bytes,
// the signing round-trips, and bad releases are refused.
// How: builds from a stand-in mk (goBuild) and signs with the repository's test
// key; a tampered archive, a wrong key and a truncated signature must fail.

package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"mk/internal/release"
)

func testKey(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "internal", "release", "testdata", "test.key"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func testKeys(t *testing.T) []release.Key {
	priv, err := release.ParsePrivate(testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	return []release.Key{release.PublicOf(priv)}
}

// goBuild builds a trivial Go program as a stand-in mk whose `version`
// prints an mk 0.3.0 tested on LLVM 21.
func goBuild(t *testing.T, dir string, trimpath bool) string {
	return goBuildMk(t, dir, trimpath, fakeVersionText("0.3.0", 21))
}

func fakeVersionText(version string, tested int) string {
	return fmt.Sprintf("mk %s commit 0123456789ab\nmk build 0000000000000000 (go)\nllvm tested %d (supported 21 or newer)\ntoolchain: none\n", version, tested)
}

// goBuildMk builds a stand-in mk that prints versionText for `mk version`.
func goBuildMk(t *testing.T, dir string, trimpath bool, versionText string) string {
	t.Helper()
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "go.mod"), []byte("module fakemk\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "main.go"), []byte("package main\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n\nfunc main() {\n\tif len(os.Args) > 1 && os.Args[1] == \"version\" {\n\t\tfmt.Print("+strconv.Quote(versionText)+")\n\t\treturn\n\t}\n\tprintln(\"fake mk\")\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "mk")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	args := []string{"build", "-o", out}
	if trimpath {
		args = append(args, "-trimpath")
	}
	cmd := exec.Command(goTool(t), append(args, ".")...)
	cmd.Dir = src
	cmd.Env = append(os.Environ(), "GOFLAGS=", "CGO_ENABLED=0", "GOWORK=off")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, b)
	}
	return out
}

func goTool(t *testing.T) string {
	p := filepath.Join(runtime.GOROOT(), "bin", "go")
	if runtime.GOOS == "windows" {
		p += ".exe"
	}
	if _, err := os.Stat(p); err != nil {
		t.Skipf("SKIP: no go tool at %s", p)
	}
	return p
}

// fakeLLVM is an LLVM release directory with fake binaries and headers.
func fakeLLVM(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	exe := ""
	if runtime.GOOS == "windows" {
		exe = ".exe"
	}
	files := map[string]string{
		"bin/clang" + exe:                   "fake clang",
		"bin/lld" + exe:                     "fake lld",
		"bin/llvm-lib" + exe:                "fake llvm-lib",
		"bin/clang-tidy" + exe:              "not shipped",
		"lib/clang/21/include/stddef.h":     "#define NULL 0\n",
		"lib/clang/21/include/sub/stdint.h": "typedef int int32_t;\n",
		"LICENSE.TXT":                       "LLVM licence placeholder for tests\n",
	}
	for n, body := range files {
		p := filepath.Join(d, filepath.FromSlash(n))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func stubClang(t *testing.T) {
	old := clangVersion
	clangVersion = func(string) (string, error) { return "clang version 21.1.8\nTarget: x86_64-pc-windows-msvc\n", nil }
	t.Cleanup(func() { clangVersion = old })
}

func testJob(t *testing.T) buildJob {
	t.Helper()
	stubClang(t)
	dir := t.TempDir()
	lic := filepath.Join(dir, "LICENSE")
	os.WriteFile(lic, []byte("mk licence placeholder for tests\n"), 0o644)
	changelog := filepath.Join(dir, "CHANGELOG.md")
	os.WriteFile(changelog, []byte("# Changelog\n"), 0o644)
	doc := filepath.Join(dir, "doc")
	os.MkdirAll(doc, 0o755)
	os.WriteFile(filepath.Join(doc, "reference.md"), []byte("# Reference\n"), 0o644)
	return buildJob{MK: goBuild(t, dir, true), LLVM: fakeLLVM(t), Version: "0.3.0", License: lic, Changelog: changelog, Doc: doc, Commit: "0123456789ab"}
}

// TestBuildTwiceSameBytes: two builds of the same inputs into two
// directories give byte-identical archives, holding the release layout.
func TestBuildTwiceSameBytes(t *testing.T) {
	j := testJob(t)
	var outs [2]string
	for i := range outs {
		j.Out = t.TempDir()
		p, _, err := buildArchive(j)
		if err != nil {
			t.Fatal(err)
		}
		outs[i] = p
		if i == 0 {
			// Touch the inputs between the runs: times never reach the bytes.
			os.WriteFile(j.License, []byte("mk licence placeholder for tests\n"), 0o644)
		}
	}
	a, _ := os.ReadFile(outs[0])
	b, _ := os.ReadFile(outs[1])
	if !bytes.Equal(a, b) {
		t.Fatal("two builds differ")
	}
	host := release.HostOf(runtime.GOOS, runtime.GOARCH)
	if filepath.Base(outs[0]) != release.ArchiveName("0.3.0", host) {
		t.Errorf("archive name %s", filepath.Base(outs[0]))
	}
	dest := filepath.Join(t.TempDir(), "x")
	if err := release.Extract(outs[0], dest, release.TopDir("0.3.0", host), release.Limits{}, nil); err != nil {
		t.Fatal(err)
	}
	var got []string
	filepath.WalkDir(dest, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			r, _ := filepath.Rel(dest, p)
			got = append(got, filepath.ToSlash(r))
		}
		return nil
	})
	exe := ""
	if host == release.HostWindows {
		exe = ".exe"
	}
	want := []string{"CHANGELOG.md", "LICENSE", "LICENSE-LLVM.txt", "RELEASE.txt", "bin/mk" + exe, "doc/reference.md",
		"llvm/bin/clang" + exe, "llvm/bin/lld" + exe, "llvm/lib/clang/21/include/stddef.h", "llvm/lib/clang/21/include/sub/stdint.h"}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("archive holds\n%v\nwant\n%v", got, want)
	}
	notes, _ := os.ReadFile(filepath.Join(dest, "RELEASE.txt"))
	for _, w := range []string{"mk 0.3.0\n", "commit 0123456789ab\n", "llvm 21.1.8\n", "  llvm/bin/clang" + exe + "\n", "  llvm/lib/clang/21/include/stddef.h\n"} {
		if !strings.Contains(string(notes), w) {
			t.Errorf("RELEASE.txt lacks %q:\n%s", w, notes)
		}
	}
}

// TestBuildMacOS: an mk built for darwin/arm64 or darwin/amd64 gives that
// host's .tar.gz with the macOS layout: bin/mk, llvm/bin/clang and
// llvm/bin/lld executable, clang's headers, no .exe and no llvm-lib. The
// stand-in mk cannot run here, so its `mk version` is replaced.
func TestBuildMacOS(t *testing.T) {
	oldV := mkVersion
	mkVersion = func(string) (string, error) { return fakeVersionText("0.3.0", 21), nil }
	t.Cleanup(func() { mkVersion = oldV })
	j := testJob(t)
	llvm := t.TempDir()
	for n, body := range map[string]string{"bin/clang": "fake clang", "bin/lld": "fake lld", "bin/ld64.lld": "not shipped",
		"lib/clang/21/include/stddef.h": "#define NULL 0\n", "LICENSE.TXT": "LLVM licence placeholder for tests\n"} {
		p := filepath.Join(llvm, filepath.FromSlash(n))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o755)
	}
	j.LLVM = llvm
	for _, c := range []struct{ goarch, host string }{{"arm64", release.HostMacOSARM64}, {"amd64", release.HostMacOS}} {
		dir := t.TempDir()
		src := filepath.Join(dir, "src")
		os.MkdirAll(src, 0o755)
		os.WriteFile(filepath.Join(src, "go.mod"), []byte("module fakemk\n\ngo 1.22\n"), 0o644)
		os.WriteFile(filepath.Join(src, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644)
		j.MK = filepath.Join(dir, "mk")
		cmd := exec.Command(goTool(t), "build", "-trimpath", "-o", j.MK, ".")
		cmd.Dir = src
		cmd.Env = append(os.Environ(), "GOFLAGS=", "CGO_ENABLED=0", "GOWORK=off", "GOOS=darwin", "GOARCH="+c.goarch)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go build for darwin/%s: %v\n%s", c.goarch, err, b)
		}
		j.Out = t.TempDir()
		p, _, err := buildArchive(j)
		if err != nil {
			t.Fatalf("darwin/%s: %v", c.goarch, err)
		}
		if filepath.Base(p) != "mk-0.3.0-"+c.host+".tar.gz" {
			t.Errorf("darwin/%s: archive %s", c.goarch, filepath.Base(p))
		}
		dest := filepath.Join(t.TempDir(), "x")
		if err := release.Extract(p, dest, release.TopDir("0.3.0", c.host), release.Limits{}, nil); err != nil {
			t.Fatal(err)
		}
		var got []string
		filepath.WalkDir(dest, func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				r, _ := filepath.Rel(dest, p)
				got = append(got, filepath.ToSlash(r))
			}
			return nil
		})
		want := []string{"CHANGELOG.md", "LICENSE", "LICENSE-LLVM.txt", "RELEASE.txt", "bin/mk", "doc/reference.md",
			"llvm/bin/clang", "llvm/bin/lld", "llvm/lib/clang/21/include/stddef.h"}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("darwin/%s: archive holds\n%v\nwant\n%v", c.goarch, got, want)
		}
		if runtime.GOOS != "windows" {
			for _, x := range []string{"bin/mk", "llvm/bin/clang", "llvm/bin/lld"} {
				if fi, err := os.Stat(filepath.Join(dest, x)); err != nil || fi.Mode().Perm()&0o111 == 0 {
					t.Errorf("darwin/%s: %s is not executable: %v", c.goarch, x, err)
				}
			}
		}
		notes, _ := os.ReadFile(filepath.Join(dest, "RELEASE.txt"))
		if !strings.Contains(string(notes), "host "+c.host+"\n") {
			t.Errorf("darwin/%s: RELEASE.txt names no host %s:\n%s", c.goarch, c.host, notes)
		}
	}
}

// TestBuildRefuses: an mk built without -trimpath, a missing licence and a
// bad version are refused before anything is written.
func TestBuildRefuses(t *testing.T) {
	j := testJob(t)
	j.Out = t.TempDir()
	bad := j
	bad.MK = goBuild(t, t.TempDir(), false)
	if _, _, err := buildArchive(bad); err == nil || !strings.Contains(err.Error(), "-trimpath") {
		t.Errorf("no -trimpath: %v", err)
	}
	bad = j
	bad.License = filepath.Join(t.TempDir(), "absent")
	if _, _, err := buildArchive(bad); err == nil {
		t.Error("a missing licence was accepted")
	}
	bad = j
	bad.Version = "0.3"
	if _, _, err := buildArchive(bad); err == nil {
		t.Error("a bad version was accepted")
	}
	if des, _ := os.ReadDir(j.Out); len(des) != 0 {
		t.Errorf("a refused build wrote %v", des)
	}
}

// releaseDir is a directory with two archives and their SHA256SUMS,
// signed by the test key.
func releaseDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, h := range []string{release.HostWindows, release.HostLinux} {
		os.WriteFile(filepath.Join(dir, release.ArchiveName("0.3.0", h)), []byte("archive for "+h), 0o644)
	}
	os.WriteFile(filepath.Join(dir, "notes.md"), []byte("not an archive"), 0o644)
	if n, err := writeSums(dir); err != nil || n != 2 {
		t.Fatalf("sums: %d %v", n, err)
	}
	priv, _ := release.ParsePrivate(testKey(t))
	if _, err := signDir(priv, dir, true); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSumsSignVerify(t *testing.T) {
	dir := releaseDir(t)
	k, n, err := verifyDir(dir, testKeys(t))
	if err != nil || n != 2 || k.ID != release.TestKeyID {
		t.Fatalf("round trip: %v %d %v", k, n, err)
	}
	sums, _ := os.ReadFile(filepath.Join(dir, release.SumsFile))
	if strings.Contains(string(sums), "notes.md") || strings.Count(string(sums), "\n") != 2 {
		t.Errorf("SHA256SUMS:\n%s", sums)
	}
	// A tampered archive.
	tampered := releaseDir(t)
	os.WriteFile(filepath.Join(tampered, release.ArchiveName("0.3.0", release.HostLinux)), []byte("evil"), 0o644)
	if _, _, err := verifyDir(tampered, testKeys(t)); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Errorf("tampered archive: %v", err)
	}
	// An archive not listed.
	extra := releaseDir(t)
	os.WriteFile(filepath.Join(extra, release.ArchiveName("0.3.1", release.HostLinux)), []byte("x"), 0o644)
	if _, _, err := verifyDir(extra, testKeys(t)); err == nil {
		t.Error("an unlisted archive was accepted")
	}
	// A wrong key.
	_, other, _ := release.GenerateKey()
	if _, _, err := verifyDir(dir, []release.Key{other}); !errors.Is(err, release.ErrUnknownKey) {
		t.Errorf("wrong key: %v", err)
	}
	// A truncated signature.
	trunc := releaseDir(t)
	sig, _ := os.ReadFile(filepath.Join(trunc, release.SigFile))
	os.WriteFile(filepath.Join(trunc, release.SigFile), sig[:len(sig)/2], 0o644)
	if _, _, err := verifyDir(trunc, testKeys(t)); !errors.Is(err, release.ErrBadSignature) {
		t.Errorf("truncated signature: %v", err)
	}
	// A tampered SHA256SUMS.
	ts := releaseDir(t)
	s, _ := os.ReadFile(filepath.Join(ts, release.SumsFile))
	s[0] ^= 1
	os.WriteFile(filepath.Join(ts, release.SumsFile), s, 0o644)
	if _, _, err := verifyDir(ts, testKeys(t)); err == nil {
		t.Error("a tampered SHA256SUMS verified")
	}
	// The command: the built-in keys never trust the test key, so the
	// test release fails, closed.
	var out, errOut bytes.Buffer
	if st := run([]string{"verify", "-dir", dir}, &out, &errOut); st != 1 {
		t.Errorf("verify with the built-in keys: status %d, %s", st, errOut.String())
	}
}

// TestSignRefusesTestKey: sign never signs with the test key.
func TestSignRefusesTestKey(t *testing.T) {
	dir := releaseDir(t)
	os.Remove(filepath.Join(dir, release.SigFile))
	keyFile := filepath.Join(t.TempDir(), "test.key")
	os.WriteFile(keyFile, testKey(t), 0o600)
	var out, errOut bytes.Buffer
	if st := run([]string{"sign", "-key", keyFile, "-dir", dir}, &out, &errOut); st != 2 || !strings.Contains(errOut.String(), "test key") {
		t.Errorf("sign with the test key: status %d, %s", st, errOut.String())
	}
	if _, err := os.Stat(filepath.Join(dir, release.SigFile)); err == nil {
		t.Error("a signature was written")
	}
}

// TestKeygen: keygen writes a key that signs and verifies against the
// printed public key, refuses an existing file, and refuses a path inside
// a git work tree, whose .git is a directory or a linked work tree's file.
// The work trees are the test's own, so it holds in a source export too.
// The key is a throwaway in the test's temporary directory, never a
// release key.
func TestKeygen(t *testing.T) {
	out := filepath.Join(t.TempDir(), "throwaway.key")
	var stdout, stderr bytes.Buffer
	if st := run([]string{"keygen", "-out", out}, &stdout, &stderr); st != 0 {
		t.Fatalf("keygen: %d %s", st, stderr.String())
	}
	data, _ := os.ReadFile(out)
	priv, err := release.ParsePrivate(data)
	if err != nil {
		t.Fatal(err)
	}
	pub := release.PublicOf(priv)
	if !strings.Contains(stdout.String(), `{ID: "`+pub.ID+`", Public: "`+pub.Public+`"},`) {
		t.Errorf("keygen printed:\n%s", stdout.String())
	}
	if st := run([]string{"keygen", "-out", out}, &stdout, &stderr); st != 2 {
		t.Error("keygen overwrote a key file")
	}
	// A clone's .git is a directory; a linked work tree's is a file.
	for _, linked := range []bool{false, true} {
		repo := filepath.Join(t.TempDir(), "repository")
		nested := filepath.Join(repo, "nested")
		if err := os.MkdirAll(nested, 0o755); err != nil {
			t.Fatal(err)
		}
		marker := filepath.Join(repo, ".git")
		if linked {
			err = os.WriteFile(marker, []byte("gitdir: "+filepath.Join(t.TempDir(), "worktrees", "repository")+"\n"), 0o644)
		} else {
			err = os.Mkdir(marker, 0o755)
		}
		if err != nil {
			t.Fatal(err)
		}
		inRepo := filepath.Join(nested, "never-written.key")
		stderr.Reset()
		if st := run([]string{"keygen", "-out", inRepo}, &stdout, &stderr); st != 2 ||
			!strings.Contains(stderr.String(), "lies in the git work tree "+repo+";") {
			t.Errorf("keygen inside a repository (linked %v): exit %d, want a refusal naming %s: %s", linked, st, repo, stderr.String())
		}
		if _, err := os.Stat(inRepo); !os.IsNotExist(err) {
			t.Errorf("repository key should be absent (linked %v): %v", linked, err)
		}
	}
}

// TestBuildChecksMkVersion: build runs `<mk>
// version` and refuses an mk whose version is not -version, or whose
// tested LLVM major is not clang's.
func TestBuildChecksMkVersion(t *testing.T) {
	j := testJob(t)
	j.Out = t.TempDir()
	for _, c := range []struct {
		name, text, want string
	}{
		{"older mk", fakeVersionText("0.2.0", 21), "0.2.0"},
		{"other LLVM", fakeVersionText("0.3.0", 20), "tested"},
		{"no version", "hello\n", "version"},
	} {
		bad := j
		bad.MK = goBuildMk(t, t.TempDir(), true, c.text)
		if _, _, err := buildArchive(bad); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if des, _ := os.ReadDir(j.Out); len(des) != 0 {
		t.Errorf("a refused build wrote %v", des)
	}
	if _, _, err := buildArchive(j); err != nil {
		t.Errorf("a matching mk: %v", err)
	}
}
