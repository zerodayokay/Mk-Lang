// Why: the install scripts run on users' machines with network input, so they
// must install and refuse bad releases without touching more than their
// root.
// What: tests of tools/install/install.ps1 (Windows) and install.sh (Linux
// and macOS): install, newest version, signature, tampering, hardening and
// PATH handling; install.sh's Perl Ed25519 check; TestInstallKeys, the
// scripts' keys against keys.go's.
// How: each script runs against a local httptest release server into the
// test's temporary directory, PATH changes off and every home redirected;
// install.ps1's registry write is never run (Join-MkPath is tested instead).
// The releases are signed with the repository's test key, which the
// scripts take through MK_INSTALL_TEST_KEY because the server is loopback.

package main

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"mk/internal/release"
)

type scriptServer struct {
	mu        sync.Mutex
	files     map[string][]byte
	redirects map[string]string // URL path -> Location
	requests  []string
}

func (s *scriptServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, r.URL.Path)
	b, ok := s.files[r.URL.Path]
	loc, redir := s.redirects[r.URL.Path]
	s.mu.Unlock()
	if redir {
		http.Redirect(w, r, loc, http.StatusFound)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Write(b)
}

func (s *scriptServer) redirect(p, loc string) {
	s.mu.Lock()
	if s.redirects == nil {
		s.redirects = map[string]string{}
	}
	s.redirects[p] = loc
	s.mu.Unlock()
}

func (s *scriptServer) requested() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

func (s *scriptServer) set(p string, b []byte) {
	s.mu.Lock()
	s.files[p] = b
	s.mu.Unlock()
}

// publish serves one release of host's archive under download/v<version>
// and, when latest, under latest/download, with SHA256SUMS signed by the
// test key.
func (s *scriptServer) publish(t *testing.T, version, host string, latest bool) []byte {
	t.Helper()
	exe := "mk"
	if host == release.HostWindows {
		exe = "mk.exe"
	}
	var b bytes.Buffer
	if err := release.WriteArchive(&b, release.TopDir(version, host), []release.Entry{
		{Name: "bin/" + exe, Data: []byte("mk " + version), Exec: true},
		{Name: "llvm/bin/clang", Data: []byte("clang"), Exec: true},
	}, host == release.HostWindows); err != nil {
		t.Fatal(err)
	}
	s.publishArchive(t, version, host, latest, b.Bytes())
	return b.Bytes()
}

func (s *scriptServer) publishArchive(t *testing.T, version, host string, latest bool, archive []byte) {
	t.Helper()
	name := release.ArchiveName(version, host)
	h := sha256.Sum256(archive)
	// Every other host's line, at a newer version, and a pre-release line,
	// which the scripts skip or rank below the release.
	list := release.Sums{name: hex.EncodeToString(h[:]), release.ArchiveName(version+"-rc1", host): strings.Repeat("1", 64)}
	for _, other := range release.Hosts {
		if other != host {
			list[release.ArchiveName("9.9.9", other)] = strings.Repeat("0", 64)
		}
	}
	sums := list.Format()
	priv, err := release.ParsePrivate(testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	sig := release.Sign(priv, sums)
	for _, d := range []string{"/download/v" + version + "/", "/latest/download/"} {
		if d == "/latest/download/" && !latest {
			continue
		}
		s.set(d+name, archive)
		s.set(d+release.SumsFile, sums)
		s.set(d+release.SigFile, sig)
	}
}

// testKeyEnv makes the scripts trust the test key, which they take only
// with a loopback release address.
const testKeyEnv = "MK_INSTALL_TEST_KEY=" + release.TestKeyID + " " + release.TestKeyPublic

// withoutTestKey is env with the test key removed: the scripts then trust
// only their own keys.
func withoutTestKey(env []string) []string {
	return slices.DeleteFunc(append([]string(nil), env...), func(kv string) bool { return kv == testKeyEnv })
}

// signatureLegs runs the refusals every script shares, against s's newest
// release, each leaving the install as it was: SHA256SUMS changed after
// signing, a signature by a key the script does not trust, a missing
// signature, and (with the test key removed from env) no trusted key at
// all. run runs the script with env; check reports what it changed.
func signatureLegs(t *testing.T, s *scriptServer, env []string, run func(env []string) (string, error), check func(leg string)) {
	t.Helper()
	sums := s.files["/latest/download/"+release.SumsFile]
	sig := s.files["/latest/download/"+release.SigFile]
	defer func() {
		s.set("/latest/download/"+release.SumsFile, sums)
		s.set("/latest/download/"+release.SigFile, sig)
	}()
	other, _, err := release.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	otherPriv, err := release.ParsePrivate(other)
	if err != nil {
		t.Fatal(err)
	}
	legs := []struct {
		name      string
		sums, sig []byte
		env       []string
		want      string
	}{
		{"changed list", append(append([]byte(nil), sums...), []byte(strings.Repeat("2", 64)+"  mk-0.0.1-x86_64-linux.tar.gz\n")...), sig, env, "signature does not match"},
		{"untrusted key", sums, release.Sign(otherPriv, sums), env, "does not trust"},
		{"truncated signature", sums, sig[:40], env, "SHA256SUMS.sig is not"},
		{"no trusted key", sums, sig, withoutTestKey(env), withoutTestKeyRefusal()},
	}
	for _, l := range legs {
		s.set("/latest/download/"+release.SumsFile, l.sums)
		s.set("/latest/download/"+release.SigFile, l.sig)
		out, err := run(l.env)
		if err == nil || !strings.Contains(out, l.want) {
			t.Errorf("%s: %v, want %q in\n%s", l.name, err, l.want, out)
		}
		check(l.name)
	}
	s.mu.Lock()
	delete(s.files, "/latest/download/"+release.SigFile)
	s.mu.Unlock()
	if out, err := run(env); err == nil {
		t.Errorf("no signature file: installed\n%s", out)
	}
	check("no signature file")
}

// TestInstallKeys: the release keys written into install.ps1 and
// install.sh are exactly the ones keys.go lists, so a script checks a
// release with the keys mk itself trusts.
func TestInstallKeys(t *testing.T) {
	var want []string
	for _, k := range release.Trusted() {
		want = append(want, k.ID+" "+k.Public)
	}
	line := regexp.MustCompile(`^[0-9a-f]{16} [A-Za-z0-9+/]{43}=$`)
	for _, c := range []struct{ file, list string }{
		{"install.ps1", `(?s)\n\$MkTrustedKeys = @\(\n(.*?)\)\n`},
		{"install.sh", `(?s)\ntrusted_keys='\n(.*?)'\n`},
	} {
		b, err := os.ReadFile(scriptPath(t, c.file))
		if err != nil {
			t.Fatal(err)
		}
		m := regexp.MustCompile(c.list).FindStringSubmatch(string(b))
		if m == nil {
			t.Fatalf("%s has no key list (%s)", c.file, c.list)
		}
		var got []string
		for _, l := range strings.Split(m[1], "\n") {
			l = strings.TrimSpace(l)
			if c.file == "install.ps1" {
				l = strings.Trim(strings.TrimSuffix(l, ","), "'")
			}
			if l == "" || strings.HasPrefix(l, "#") {
				continue
			}
			if !line.MatchString(l) {
				t.Errorf("%s: %q is not \"<key id> <base64 public key>\"", c.file, l)
			}
			got = append(got, l)
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s trusts %q; internal/release/keys.go lists %q", c.file, got, want)
		}
	}
}

// scratchEnv is the child's environment: the host's, without MK_* and
// with every per-user location redirected into dir.
func scratchEnv(dir string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(k) {
		case "LOCALAPPDATA", "USERPROFILE", "APPDATA", "HOME", "XDG_DATA_HOME":
			continue
		}
		if strings.HasPrefix(strings.ToUpper(k), "MK_") {
			continue
		}
		env = append(env, kv)
	}
	for _, k := range []string{"LOCALAPPDATA", "USERPROFILE", "APPDATA", "HOME"} {
		p := filepath.Join(dir, "profile", strings.ToLower(k))
		os.MkdirAll(p, 0o755)
		env = append(env, k+"="+p)
	}
	// Windows PowerShell's own files stay out of the fake profile where the
	// variables can move them.
	cache := filepath.Join(dir, "pscache")
	os.MkdirAll(cache, 0o755)
	env = append(env, "PSModuleAnalysisCachePath="+filepath.Join(cache, "ModuleAnalysisCache"), "POWERSHELL_TELEMETRY_OPTOUT=1", "POWERSHELL_UPDATECHECK=Off")
	// A script that ignored -NoModifyPath / --no-modify-path still leaves
	// the registry and ~/.profile alone.
	return append(env, "MK_NO_MODIFY_PATH=1", testKeyEnv)
}

func powershell(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("SKIP: install.ps1 runs on Windows")
	}
	p, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skipf("SKIP: no powershell.exe: %v", err)
	}
	return p
}

func scriptPath(t *testing.T, name string) string {
	p, err := filepath.Abs(filepath.Join("..", "install", name))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestInstallPs1(t *testing.T) {
	ps := powershell(t)
	s := &scriptServer{files: map[string][]byte{}}
	srv := httptest.NewServer(s)
	defer srv.Close()
	s.publish(t, "0.3.0", release.HostWindows, false)
	s.publish(t, "0.4.0", release.HostWindows, true)
	dir := t.TempDir()
	root := filepath.Join(dir, "mkroot")
	run := func(extra ...string) (string, error) {
		args := append([]string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", scriptPath(t, "install.ps1"),
			"-Root", root, "-Releases", srv.URL, "-NoModifyPath"}, extra...)
		cmd := exec.Command(ps, args...)
		cmd.Env = scratchEnv(dir)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	in := release.Install{Root: root}
	out, err := run("-Version", "0.3.0")
	if err != nil {
		t.Fatalf("install 0.3.0: %v\n%s", err, out)
	}
	if cur, _ := in.Current(); cur != "0.3.0" {
		t.Fatalf("current %q after installing 0.3.0:\n%s", cur, out)
	}
	if b, err := os.ReadFile(filepath.Join(root, "current", "bin", "mk.exe")); err != nil || string(b) != "mk 0.3.0" {
		t.Errorf("through current: %q %v", b, err)
	}
	if !strings.Contains(out, "PATH was not changed") || !strings.Contains(out, filepath.Join(root, `current\bin`)) {
		t.Errorf("no PATH line:\n%s", out)
	}
	// The newest (0.4.0, not its rc1, not another host's 9.9.9).
	if out, err = run(); err != nil {
		t.Fatalf("install newest: %v\n%s", err, out)
	}
	if cur, _ := in.Current(); cur != "0.4.0" {
		t.Errorf("current %q after the newest:\n%s", cur, out)
	}
	// Back to 0.3.0: already unpacked, current moves.
	if out, err = run("-Version", "0.3.0"); err != nil || !strings.Contains(out, "already") {
		t.Errorf("reinstall: %v\n%s", err, out)
	}
	if vs, _ := in.Versions(); len(vs) != 2 {
		t.Errorf("versions %v", vs)
	}
	// A tampered archive is refused and nothing changes.
	name := release.ArchiveName("0.5.0", release.HostWindows)
	good := s.publish(t, "0.5.0", release.HostWindows, true)
	bad := append([]byte(nil), good...)
	bad[len(bad)/2] ^= 0xff
	s.set("/download/v0.5.0/"+name, bad)
	if out, err = run(); err == nil || !strings.Contains(out, "SHA-256") {
		t.Errorf("tampered archive: %v\n%s", err, out)
	}
	if cur, _ := in.Current(); cur != "0.3.0" {
		t.Errorf("a refused install moved current to %q", cur)
	}
	if vs, _ := in.Versions(); len(vs) != 2 {
		t.Errorf("a refused install left %v", vs)
	}
	des, _ := os.ReadDir(in.Toolchains())
	for _, de := range des {
		if strings.HasPrefix(de.Name(), ".") {
			t.Errorf("left %s", de.Name())
		}
	}
	// A bad or missing signature is refused before the archive is fetched.
	s.set("/download/v0.5.0/"+name, good)
	runEnv := func(env []string) (string, error) {
		cmd := exec.Command(ps, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", scriptPath(t, "install.ps1"),
			"-Root", root, "-Releases", srv.URL, "-NoModifyPath")
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	signatureLegs(t, s, scratchEnv(dir), runEnv, func(leg string) {
		if cur, _ := in.Current(); cur != "0.3.0" {
			t.Errorf("%s moved current to %q", leg, cur)
		}
		if vs, _ := in.Versions(); len(vs) != 2 {
			t.Errorf("%s left %v", leg, vs)
		}
	})
	// Plain http to a host that is not loopback is refused.
	cmd := exec.Command(ps, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", scriptPath(t, "install.ps1"),
		"-Root", root, "-Releases", "http://example.invalid/releases", "-NoModifyPath")
	cmd.Env = scratchEnv(dir)
	if b, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(b), "not https") {
		t.Errorf("http elsewhere: %v\n%s", err, b)
	}
	// Nothing reached the redirected profile directories but what Windows
	// PowerShell itself writes.
	for _, k := range []string{"localappdata", "userprofile", "appdata", "home"} {
		if got := profileWrites(filepath.Join(dir, "profile", k), k); len(got) != 0 {
			t.Errorf("the script wrote into %s: %v", k, got)
		}
	}
}

// powershellOwn are the files Windows PowerShell 5.1 and its .NET runtime
// write for themselves, under the local and roaming application data
// folders: the startup profile data and module analysis cache, and the
// CLR's usage log.
// Where the user's shell folders are stored as %USERPROFILE%\AppData\...
// (the hosted windows-2025 image), that folder is the fake USERPROFILE's,
// whatever LOCALAPPDATA says. install.ps1 writes only under -Root.
var powershellOwn = []string{`Microsoft\Windows\PowerShell\`, `Microsoft\CLR_v4.0\UsageLogs\`}

// profileWrites lists the files and directories under the fake profile
// directory p (k is its variable, lower case) that are neither in a
// powershellOwn directory nor on the way to one.
func profileWrites(p, k string) []string {
	var own []string
	for _, o := range powershellOwn {
		switch k {
		case "localappdata":
			own = append(own, o)
		case "userprofile":
			own = append(own, `AppData\Local\`+o, `AppData\Roaming\`+o)
		}
	}
	var out []string
	filepath.WalkDir(p, func(path string, d os.DirEntry, err error) error {
		if err != nil || path == p {
			return nil
		}
		rel, _ := filepath.Rel(p, path)
		for _, o := range own {
			if strings.HasPrefix(rel, o) || d.IsDir() && strings.HasPrefix(o, rel+`\`) {
				return nil
			}
		}
		out = append(out, rel)
		if d.IsDir() {
			return filepath.SkipDir
		}
		return nil
	})
	return out
}

// TestInstallPs1PathString: the user PATH value install.ps1 would write,
// from its Join-MkPath through the hidden -JoinPathOnly switch (no
// install, no registry).
func TestInstallPs1PathString(t *testing.T) {
	ps := powershell(t)
	sp := scriptPath(t, "install.ps1")
	script := `& '` + sp + `' -JoinPathOnly 'C:\a;C:\b','C:\m\current\bin'
& '` + sp + `' -JoinPathOnly 'C:\a;C:\M\Current\Bin\;C:\b','C:\m\current\bin'
& '` + sp + `' -JoinPathOnly '','C:\m\current\bin'
`
	cmd := exec.Command(ps, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	cmd.Env = scratchEnv(t.TempDir())
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	want := "C:\\m\\current\\bin;C:\\a;C:\\b\r\nC:\\a;C:\\M\\Current\\Bin\\;C:\\b\r\nC:\\m\\current\\bin\r\n"
	if string(b) != want {
		t.Errorf("Join-MkPath:\n%q\nwant\n%q", b, want)
	}
}

// TestInstallPs1Extraction covers a release with a deep ZIP entry and a
// correctly signed payload that cannot be extracted. The latter reaches
// extraction only after both verification steps.
func TestInstallPs1Extraction(t *testing.T) {
	ps := powershell(t)
	s := &scriptServer{files: map[string][]byte{}}
	srv := httptest.NewServer(s)
	defer srv.Close()
	entry := "llvm/include/" + strings.Repeat("components/", 12) + "header.h"
	var archive bytes.Buffer
	if err := release.WriteArchive(&archive, release.TopDir("0.3.0", release.HostWindows), []release.Entry{
		{Name: "bin/mk.exe", Data: []byte("mk"), Exec: true},
		{Name: entry, Data: []byte("header")},
	}, true); err != nil {
		t.Fatal(err)
	}
	s.publishArchive(t, "0.3.0", release.HostWindows, false, archive.Bytes())
	s.publishArchive(t, "0.4.0", release.HostWindows, false, []byte("not a ZIP file"))
	var badName bytes.Buffer
	zw := zip.NewWriter(&badName)
	if _, err := zw.Create(release.TopDir("0.5.0", release.HostWindows) + "/header."); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	s.publishArchive(t, "0.5.0", release.HostWindows, false, badName.Bytes())

	dir := t.TempDir()
	in := release.Install{Root: filepath.Join(dir, "mkroot")}
	run := func(version string) (string, error) {
		cmd := exec.Command(ps, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", scriptPath(t, "install.ps1"),
			"-Releases", srv.URL, "-Root", in.Root, "-NoModifyPath", "-Version", version)
		cmd.Env = scratchEnv(dir)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	t.Run("deep entry", func(t *testing.T) {
		if out, err := run("0.3.0"); err != nil || !strings.Contains(out, "current is mk 0.3.0") {
			t.Fatalf("long extraction path: %v\n%s", err, out)
		}
		if b, err := os.ReadFile(filepath.Join(in.CurrentLink(), filepath.FromSlash(entry))); err != nil || string(b) != "header" {
			t.Fatalf("installed header: %q %v", b, err)
		}
	})
	for _, tt := range []struct{ name, version, want string }{
		{"invalid ZIP", "0.4.0", "OpenRead"},
		{"ambiguous filename", "0.5.0", "unsafe archive entry"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before, _ := in.Current()
			out, err := run(tt.version)
			if _, ok := err.(*exec.ExitError); !ok || !strings.Contains(out, "SHA256SUMS is signed") ||
				!strings.Contains(out, tt.want) {
				t.Errorf("signed ZIP: want a failing extraction process with %q, got %v\n%s", tt.want, err, out)
			}
			if cur, _ := in.Current(); cur != before {
				t.Errorf("failed extraction changed current from %q to %q", before, cur)
			}
			installed, err := os.ReadDir(filepath.Join(in.Root, "toolchains"))
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range installed {
				if d.Name() != before {
					t.Errorf("failed extraction left %q", d.Name())
				}
			}
		})
	}
}

// zipOf is a ZIP of the named entries, in order, with no checks of its
// own: a name ending in "/" with data is a directory entry holding data.
func zipOf(t *testing.T, entries [][2]string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	var renames [][2]string
	for _, e := range entries {
		name := e[0]
		// archive/zip writes no data under a directory name: write it under
		// a name of the same length, then rename it in the bytes.
		if strings.HasSuffix(name, "/") && e[1] != "" {
			name = name[:len(name)-1] + "\x01"
			renames = append(renames, [2]string{name, e[0]})
		}
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	out := b.Bytes()
	for _, r := range renames {
		out = bytes.ReplaceAll(out, []byte(r[0]), []byte(r[1]))
	}
	return out
}

// TestInstallPs1EntryNames: install.ps1 writes each verified entry through
// an extended path, which Windows does not normalize, so it refuses every
// name that would leave the staging tree or land under another name: a .
// or .. component, an empty one, a rooted or drive name, a backslash, a
// stream, a device name and a trailing space. A second entry for one file
// (by case) and a directory entry with data fail too. Every archive first
// holds a deep entry, so each failed install removes a staging tree longer
// than MAX_PATH, and leaves current and the toolchains as they were.
// PowerShell 7, where it is installed, runs the deep install and the first
// refusal.
func TestInstallPs1EntryNames(t *testing.T) {
	ps := powershell(t)
	s := &scriptServer{files: map[string][]byte{}}
	srv := httptest.NewServer(s)
	defer srv.Close()
	deep := "llvm/include/" + strings.Repeat("components/", 12) + "header.h"
	top := release.TopDir("0.5.0", release.HostWindows)
	s.publishArchive(t, "0.5.0", release.HostWindows, false, zipOf(t, [][2]string{{top + "/bin/mk.exe", "mk"}, {top + "/" + deep, "header"}}))
	// {top} is the archive's top directory.
	cases := []struct{ name, entry, want string }{
		{"dot-dot in the tree", "{top}/../../escaped", "unsafe archive entry"},
		{"dot-dot first", "../escaped", "unsafe archive entry"},
		{"dot", "{top}/./escaped", "unsafe archive entry"},
		{"empty component", "{top}//escaped", "unsafe archive entry"},
		{"rooted", "/escaped", "unsafe archive entry"},
		{"drive", "C:/escaped", "unsafe archive entry"},
		{"backslash", `{top}/bin\..\..\escaped`, "unsafe archive entry"},
		{"stream", "{top}/bin/mk.exe:escaped", "unsafe archive entry"},
		{"device", "{top}/lib/con.txt", "unsafe archive entry"},
		{"trailing space", "{top}/lib /escaped", "unsafe archive entry"},
		{"one file by case", "{top}/BIN/MK.EXE", "already exists"},
		{"directory with data", "{top}/lib/", "contains data"},
	}
	for i, c := range cases {
		v := fmt.Sprintf("0.6.%d", i)
		top := release.TopDir(v, release.HostWindows)
		s.publishArchive(t, v, release.HostWindows, false, zipOf(t, [][2]string{
			{top + "/bin/mk.exe", "mk"}, {top + "/" + deep, "header"}, {strings.ReplaceAll(c.entry, "{top}", top), "x"}}))
	}
	shells := []string{ps}
	if p, err := exec.LookPath("pwsh.exe"); err == nil {
		shells = append(shells, p)
	}
	for _, shell := range shells {
		t.Run(filepath.Base(shell), func(t *testing.T) {
			dir := t.TempDir()
			in := release.Install{Root: filepath.Join(dir, "mkroot")}
			run := func(version string) (string, error) {
				cmd := exec.Command(shell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", scriptPath(t, "install.ps1"),
					"-Releases", srv.URL, "-Root", in.Root, "-NoModifyPath", "-Version", version)
				cmd.Env = scratchEnv(dir)
				out, err := cmd.CombinedOutput()
				return string(out), err
			}
			if out, err := run("0.5.0"); err != nil || !strings.Contains(out, "current is mk 0.5.0") {
				t.Fatalf("deep entry: %v\n%s", err, out)
			}
			if b, err := os.ReadFile(filepath.Join(in.CurrentLink(), filepath.FromSlash(deep))); err != nil || string(b) != "header" {
				t.Fatalf("installed header: %q %v", b, err)
			}
			for i, c := range cases {
				if shell != ps && i > 0 {
					break
				}
				t.Run(c.name, func(t *testing.T) {
					out, err := run(fmt.Sprintf("0.6.%d", i))
					if _, ok := err.(*exec.ExitError); !ok || !strings.Contains(out, "SHA256SUMS is signed") || !strings.Contains(out, c.want) {
						t.Errorf("want a failing install with %q, got %v\n%s", c.want, err, out)
					}
					if cur, _ := in.Current(); cur != "0.5.0" {
						t.Errorf("a refused install moved current to %q", cur)
					}
					if vs, err := in.Versions(); err != nil || !slices.Equal(vs, []string{"0.5.0"}) {
						t.Errorf("a refused install left the toolchains %v (%v)", vs, err)
					}
					if des, _ := os.ReadDir(in.Toolchains()); len(des) != 1 {
						t.Errorf("a refused install left %d entries in the toolchains", len(des))
					}
					filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
						if err == nil && strings.HasPrefix(d.Name(), "escaped") {
							t.Errorf("a refused install wrote %s", p)
						}
						return nil
					})
				})
			}
		})
	}
}

// TestInstallPs1Hardening: a version that
// is not a version token is refused before any request; a redirect to
// plain http elsewhere is refused; a redirect on the loopback server is
// followed; a relative -Root is made absolute; and `irm | iex` (the script
// text run in the caller's session) leaves no preference, strict mode,
// function or variable behind.
func TestInstallPs1Hardening(t *testing.T) {
	ps := powershell(t)
	s := &scriptServer{files: map[string][]byte{}}
	srv := httptest.NewServer(s)
	defer srv.Close()
	s.publish(t, "0.3.0", release.HostWindows, false)
	s.publish(t, "0.4.0", release.HostWindows, false)
	dir := t.TempDir()
	root := filepath.Join(dir, "mkroot")
	run := func(cwd string, extra ...string) (string, error) {
		args := append([]string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", scriptPath(t, "install.ps1"),
			"-Releases", srv.URL, "-NoModifyPath"}, extra...)
		cmd := exec.Command(ps, args...)
		cmd.Env = scratchEnv(dir)
		cmd.Dir = cwd
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	for _, v := range []string{"../evil", "0.3.0/../../x", "0.3", "0.3.0-rc1/x"} {
		n := len(s.requested())
		out, err := run(dir, "-Root", root, "-Version", v)
		if err == nil || !strings.Contains(out, "not a version") {
			t.Errorf("-Version %q: %v\n%s", v, err, out)
		}
		if got := s.requested()[n:]; len(got) != 0 {
			t.Errorf("-Version %q requested %v", v, got)
		}
	}
	// GitHub's newest address redirects; the script follows on loopback.
	s.redirect("/latest/download/"+release.SumsFile, "/download/v0.4.0/"+release.SumsFile)
	s.redirect("/latest/download/"+release.SigFile, "/download/v0.4.0/"+release.SigFile)
	if out, err := run(dir, "-Root", root); err != nil || !strings.Contains(out, "current is mk 0.4.0") {
		t.Fatalf("a loopback redirect: %v\n%s", err, out)
	}
	// A redirect to plain http elsewhere is refused, and nothing changes.
	s.redirect("/download/v0.3.0/"+release.ArchiveName("0.3.0", release.HostWindows), "http://example.invalid/x.zip")
	out, err := run(dir, "-Root", root, "-Version", "0.3.0")
	if err == nil || !strings.Contains(out, "not https") {
		t.Errorf("a redirect to http: %v\n%s", err, out)
	}
	in := release.Install{Root: root}
	if cur, _ := in.Current(); cur != "0.4.0" {
		t.Errorf("current %q after a refused redirect", cur)
	}
	// A relative -Root, from another working directory.
	cwd := filepath.Join(dir, "cwd")
	os.MkdirAll(cwd, 0o755)
	if out, err := run(cwd, "-Root", "rel", "-Version", "0.4.0"); err != nil {
		t.Fatalf("relative root: %v\n%s", err, out)
	}
	rel := release.Install{Root: filepath.Join(cwd, "rel")}
	tgt, err := os.Readlink(rel.CurrentLink())
	if err != nil || !filepath.IsAbs(tgt) {
		t.Errorf("relative root's current: %q %v", tgt, err)
	}
	if b, err := os.ReadFile(filepath.Join(rel.CurrentLink(), "bin", "mk.exe")); err != nil || string(b) != "mk 0.4.0" {
		t.Errorf("through the relative root's current: %q %v", b, err)
	}
	// irm | iex: the text run in the caller's session.
	script := `$env:MK_HOME = '` + filepath.Join(dir, "iexroot") + `'
$env:MK_RELEASES = '` + srv.URL + `'
$env:MK_VERSION = '0.3.0'
Get-Content -Raw -LiteralPath '` + scriptPath(t, "install.ps1") + `' | Invoke-Expression
"EAP=$ErrorActionPreference"
"FN=$([bool](Get-Command Join-MkPath -ErrorAction SilentlyContinue))"
"VAR=$(Test-Path variable:MkHost)"
"PARAM=$(Test-Path variable:Releases)"
try { $undefinedVariable | Out-Null; "STRICT=off" } catch { "STRICT=on" }
`
	s.redirect("/download/v0.3.0/"+release.ArchiveName("0.3.0", release.HostWindows), "/download/v0.3.0/"+release.ArchiveName("0.3.0", release.HostWindows)+"x")
	s.set("/download/v0.3.0/"+release.ArchiveName("0.3.0", release.HostWindows)+"x", s.files["/download/v0.3.0/"+release.ArchiveName("0.3.0", release.HostWindows)])
	cmd := exec.Command(ps, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	cmd.Env = scratchEnv(dir)
	b, err := cmd.CombinedOutput()
	o := string(b)
	if err != nil || !strings.Contains(o, "current is mk 0.3.0") {
		t.Fatalf("irm | iex: %v\n%s", err, o)
	}
	for _, want := range []string{"EAP=Continue", "FN=False", "VAR=False", "PARAM=False", "STRICT=off"} {
		if !strings.Contains(o, want) {
			t.Errorf("irm | iex leaked: want %s in\n%s", want, o)
		}
	}
}

// shHost is the host install.sh installs for in a test: on Linux and macOS
// this machine's, which the script finds with uname itself. Elsewhere the
// test is skipped unless MK_INSTALL_SH_ANYWHERE=1 (a developer's check under
// Git's sh on Windows with native symbolic links, never in place of the
// Linux and macOS runs), and then it is x86_64 Linux, named to the script
// by MK_INSTALL_HOST (shEnv).
func shHost(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		h := release.HostOf(runtime.GOOS, runtime.GOARCH)
		// Releases carry no Intel Mac archive yet: install.sh refuses there
		// (TestInstallShHardening's Intel leg).
		if h == "" || h == release.HostMacOS {
			t.Skipf("SKIP: releases carry no archive for %s/%s", runtime.GOOS, runtime.GOARCH)
		}
		return h
	}
	if os.Getenv("MK_INSTALL_SH_ANYWHERE") != "1" {
		t.Skip("SKIP: install.sh runs on Linux and macOS (MK_LINUX=1, or the macOS runners)")
	}
	return release.HostLinux
}

// shEnv is env for install.sh, with MK_INSTALL_HOST where uname names no
// host the script installs (Windows).
func shEnv(env []string, host string) []string {
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		return env
	}
	return append(env, "MK_INSTALL_HOST="+host)
}

// TestInstallSh runs install.sh on Linux and macOS: the install, the newest,
// a tampered archive, --no-modify-path against ~/.profile (HOME is
// redirected) and ~/.zprofile on macOS, the Perl check of the signature
// when openssl cannot check Ed25519, and the signature refusals.
func TestInstallSh(t *testing.T) {
	host := shHost(t)
	s := &scriptServer{files: map[string][]byte{}}
	srv := httptest.NewServer(s)
	defer srv.Close()
	s.publish(t, "0.3.0", host, false)
	s.publish(t, "0.4.0", host, true)
	dir := t.TempDir()
	// A root with a space.
	root := filepath.Join(dir, "mk root")
	// The ~/.profile leg needs PATH modification on: HOME is the redirected
	// one in dir, so only the test's own .profile is written.
	env := shEnv(slices.DeleteFunc(scratchEnv(dir), func(kv string) bool { return kv == "MK_NO_MODIFY_PATH=1" }), host)
	home := filepath.Join(dir, "profile", "home")
	run := func(extra ...string) (string, error) {
		cmd := exec.Command("sh", append([]string{scriptPath(t, "install.sh"), "--root", root, "--releases", srv.URL}, extra...)...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	in := release.Install{Root: root}
	out, err := run("--version", "0.3.0", "--no-modify-path")
	if err != nil {
		t.Fatalf("install 0.3.0: %v\n%s", err, out)
	}
	if cur, _ := in.Current(); cur != "0.3.0" {
		t.Fatalf("current %q:\n%s", cur, out)
	}
	fi, err := os.Stat(filepath.Join(root, "current", "bin", "mk"))
	if err != nil || runtime.GOOS != "windows" && fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("bin/mk: %v %v", fi, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".profile")); err == nil {
		t.Error("--no-modify-path wrote ~/.profile")
	}
	// A backslash in root (a Windows path under Git's sh) is doubled.
	if !strings.Contains(out, `export PATH="`+strings.ReplaceAll(root, `\`, `\\`)+`/current/bin:$PATH"`) {
		t.Errorf("no PATH line:\n%s", out)
	}
	if out, err = run(); err != nil {
		t.Fatalf("newest: %v\n%s", err, out)
	}
	if cur, _ := in.Current(); cur != "0.4.0" {
		t.Errorf("current %q after the newest:\n%s", cur, out)
	}
	profiles := []string{".profile"}
	if runtime.GOOS == "darwin" {
		profiles = append(profiles, ".zprofile")
	}
	var profs [][]byte
	for _, p := range profiles {
		prof, err := os.ReadFile(filepath.Join(home, p))
		if err != nil || strings.Count(string(prof), "export PATH=") != 1 {
			t.Errorf("~/%s: %q %v", p, prof, err)
		}
		profs = append(profs, prof)
	}
	if out, err = run("--version", "0.3.0"); err != nil {
		t.Fatalf("reinstall: %v\n%s", err, out)
	}
	for i, p := range profiles {
		if prof2, _ := os.ReadFile(filepath.Join(home, p)); !bytes.Equal(profs[i], prof2) {
			t.Errorf("~/%s got the line twice", p)
		}
	}
	shPerlLegs(t, s, env, filepath.Join(dir, "fakebin"), func(env []string) (string, error) {
		cmd := exec.Command("sh", scriptPath(t, "install.sh"), "--root", root, "--releases", srv.URL, "--no-modify-path", "--version", "0.3.0")
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	})
	if cur, _ := in.Current(); cur != "0.3.0" {
		t.Errorf("current %q after the Perl legs:\n%s", cur, out)
	}
	name := release.ArchiveName("0.5.0", host)
	good := s.publish(t, "0.5.0", host, true)
	bad := append([]byte(nil), good...)
	bad[len(bad)/2] ^= 0xff
	s.set("/download/v0.5.0/"+name, bad)
	if out, err = run("--no-modify-path"); err == nil || !strings.Contains(out, "SHA-256") {
		t.Errorf("tampered archive: %v\n%s", err, out)
	}
	if cur, _ := in.Current(); cur != "0.3.0" {
		t.Errorf("a refused install moved current to %q", cur)
	}
	if vs, _ := in.Versions(); len(vs) != 2 {
		t.Errorf("a refused install left %v", vs)
	}
	// A bad or missing signature is refused before the archive is fetched.
	s.set("/download/v0.5.0/"+name, good)
	runEnv := func(env []string) (string, error) {
		cmd := exec.Command("sh", scriptPath(t, "install.sh"), "--root", root, "--releases", srv.URL, "--no-modify-path")
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	signatureLegs(t, s, env, runEnv, func(leg string) {
		if cur, _ := in.Current(); cur != "0.3.0" {
			t.Errorf("%s moved current to %q", leg, cur)
		}
		if vs, _ := in.Versions(); len(vs) != 2 {
			t.Errorf("%s left %v", leg, vs)
		}
	})
}

// shPerlLegs runs install.sh (run, which installs 0.3.0) with an openssl
// that fails every command first on PATH, as macOS's LibreSSL fails
// Ed25519: the script checks the signature with Perl, accepts the good one
// and refuses SHA256SUMS changed after signing. With perl failing too, it
// refuses to install anything. dir is for the stand-in programs.
func shPerlLegs(t *testing.T, s *scriptServer, env []string, dir string, run func(env []string) (string, error)) {
	t.Helper()
	withPath := func(bin string) []string {
		out := append([]string(nil), env...)
		for i, kv := range out {
			if k, v, _ := strings.Cut(kv, "="); strings.EqualFold(k, "PATH") {
				out[i] = k + "=" + bin + string(os.PathListSeparator) + v
			}
		}
		return out
	}
	fake := func(bin string, names ...string) {
		os.MkdirAll(bin, 0o755)
		for _, n := range names {
			if err := os.WriteFile(filepath.Join(bin, n), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	noSSL := filepath.Join(dir, "no-openssl")
	fake(noSSL, "openssl")
	// Git's sh.exe on Windows puts its own directories first on PATH.
	probe := exec.Command("sh", "-c", "openssl version")
	probe.Env = withPath(noSSL)
	if probe.Run() == nil {
		t.Log("NOT RUN: the Perl legs (this sh puts another openssl before the stand-in)")
		return
	}
	if exec.Command("perl", "-MMath::BigInt", "-MDigest::SHA", "-MMIME::Base64", "-e", "1").Run() == nil {
		if out, err := run(withPath(noSSL)); err != nil || !strings.Contains(out, "checked with Perl") {
			t.Errorf("openssl without Ed25519: %v\n%s", err, out)
		}
		p := "/download/v0.3.0/" + release.SumsFile
		sums := s.files[p]
		s.set(p, append(append([]byte(nil), sums...), []byte(strings.Repeat("2", 64)+"  mk-0.0.1-x86_64-linux.tar.gz\n")...))
		if out, err := run(withPath(noSSL)); err == nil || !strings.Contains(out, "signature does not match") {
			t.Errorf("a changed list, checked with Perl: %v\n%s", err, out)
		}
		s.set(p, sums)
	} else {
		t.Log("NOT RUN: the Perl check of the signature (no perl with Math::BigInt, Digest::SHA and MIME::Base64)")
	}
	neither := filepath.Join(dir, "neither")
	fake(neither, "openssl", "perl")
	if out, err := run(withPath(neither)); err == nil || !strings.Contains(out, "nothing here can check Ed25519") {
		t.Errorf("neither openssl nor perl: %v\n%s", err, out)
	}
}

// TestInstallShEd25519: install.sh's Perl Ed25519 check (ed25519_pl) gives
// Go's answer for Go's signatures: yes for a good one; no for a changed
// message, a changed R or S, S plus the group order (the same point, a
// non-canonical S) and another key.
func TestInstallShEd25519(t *testing.T) {
	if exec.Command("perl", "-MMath::BigInt", "-MDigest::SHA", "-MMIME::Base64", "-e", "1").Run() != nil {
		t.Skip("SKIP: no perl with Math::BigInt, Digest::SHA and MIME::Base64")
	}
	b, err := os.ReadFile(scriptPath(t, "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)\ned25519_pl='\n(.*?)'\n`).FindSubmatch(b)
	if m == nil {
		t.Fatal("install.sh has no ed25519_pl")
	}
	prog := string(m[1])
	dir := t.TempDir()
	check := func(pub ed25519.PublicKey, msg, sig []byte) bool {
		f := filepath.Join(dir, "msg")
		os.WriteFile(f, msg, 0o644)
		enc := base64.StdEncoding.EncodeToString
		err := exec.Command("perl", "-e", prog, enc(pub), enc(sig), f).Run()
		if err != nil {
			if _, ok := err.(*exec.ExitError); !ok {
				t.Fatal(err)
			}
		}
		return err == nil
	}
	order, _ := new(big.Int).SetString("7237005577332262213973186563042994240857116359379907606001950938285454250989", 10)
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	for i := range 3 {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		msg := make([]byte, 10+i*300)
		rand.Read(msg)
		sig := ed25519.Sign(priv, msg)
		if !check(pub, msg, sig) {
			t.Errorf("%d: a good signature refused", i)
		}
		flip := func(at int) []byte { c := slices.Clone(sig); c[at] ^= 1 << (i % 8); return c }
		bad := map[string][]byte{"R changed": flip(i), "S changed": flip(40 + i), "S plus the order": plusOrder(sig, order)}
		for what, sg := range bad {
			if ed25519.Verify(pub, msg, sg) {
				t.Fatalf("%d: Go accepts %s", i, what)
			}
			if check(pub, msg, sg) {
				t.Errorf("%d: %s accepted", i, what)
			}
		}
		if check(pub, append(slices.Clone(msg), 'x'), sig) {
			t.Errorf("%d: a changed message accepted", i)
		}
		if check(other, msg, sig) {
			t.Errorf("%d: another key accepted", i)
		}
	}
}

// plusOrder is sig with S replaced by S + order, still 32 bytes
// little-endian (S < order < 2^253, so the sum fits).
func plusOrder(sig []byte, order *big.Int) []byte {
	le := make([]byte, 32)
	for i := range 32 {
		le[i] = sig[63-i]
	}
	s := new(big.Int).SetBytes(le)
	s.Add(s, order)
	be := s.FillBytes(make([]byte, 32))
	out := slices.Clone(sig)
	for i := range 32 {
		out[32+i] = be[31-i]
	}
	return out
}

// TestInstallShHardening runs install.sh like
// TestInstallSh (Linux and macOS; MK_INSTALL_SH_ANYWHERE=1 elsewhere as a
// developer check): a version that is not a version token is refused before any
// request; a redirect to plain http elsewhere is refused (curl, and wget
// when curl is hidden); a loopback redirect is followed; a relative --root
// is made absolute; a root that a profile line cannot hold is refused
// before ~/.profile is touched.
func TestInstallShHardening(t *testing.T) {
	host := shHost(t)
	s := &scriptServer{files: map[string][]byte{}}
	srv := httptest.NewServer(s)
	defer srv.Close()
	s.publish(t, "0.3.0", host, false)
	s.publish(t, "0.4.0", host, false)
	dir := t.TempDir()
	root := filepath.Join(dir, "mkroot")
	run := func(cwd string, env []string, extra ...string) (string, error) {
		cmd := exec.Command("sh", append([]string{scriptPath(t, "install.sh"), "--releases", srv.URL}, extra...)...)
		cmd.Env = env
		cmd.Dir = cwd
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	env := shEnv(scratchEnv(dir), host)
	for _, v := range []string{"../evil", "0.3.0/../../x", "0.3", "0.3.0-rc1/x"} {
		n := len(s.requested())
		out, err := run(dir, env, "--root", root, "--version", v)
		if err == nil || !strings.Contains(out, "not a version") {
			t.Errorf("--version %q: %v\n%s", v, err, out)
		}
		if got := s.requested()[n:]; len(got) != 0 {
			t.Errorf("--version %q requested %v", v, got)
		}
	}
	// An Intel Mac (named through MK_INSTALL_HOST, which the loopback
	// address admits): refused with the way to build from source, before
	// any request or write.
	n := len(s.requested())
	if out, err := run(dir, append(slices.Clone(env), "MK_INSTALL_HOST="+release.HostMacOS), "--root", root); err == nil ||
		!strings.Contains(out, "no binary release for Intel Macs") || !strings.Contains(out, "brew install llvm@21 lld@21") {
		t.Errorf("Intel Mac: %v\n%s", err, out)
	}
	if got := s.requested()[n:]; len(got) != 0 {
		t.Errorf("Intel Mac requested %v", got)
	}
	if _, err := os.Stat(filepath.Join(root, "toolchains")); err == nil {
		t.Error("Intel Mac: the root was written")
	}
	s.redirect("/latest/download/"+release.SumsFile, "/download/v0.4.0/"+release.SumsFile)
	s.redirect("/latest/download/"+release.SigFile, "/download/v0.4.0/"+release.SigFile)
	if out, err := run(dir, env, "--root", root); err != nil || !strings.Contains(out, "current is mk 0.4.0") {
		t.Fatalf("a loopback redirect: %v\n%s", err, out)
	}
	s.redirect("/download/v0.3.0/"+release.ArchiveName("0.3.0", host), "http://example.invalid/x.tar.gz")
	if out, err := run(dir, env, "--root", root, "--version", "0.3.0"); err == nil || !strings.Contains(out, "not https") {
		t.Errorf("a redirect to http (curl): %v\n%s", err, out)
	}
	// wget alone: a PATH directory with wget but no curl.
	if wget, err := exec.LookPath("wget"); err == nil {
		bin := filepath.Join(dir, "wgetonly")
		os.MkdirAll(bin, 0o755)
		for _, tool := range []string{"sh", "grep", "sed", "awk", "sort", "tail", "cut", "mktemp", "rm", "mkdir", "mv", "ln", "gzip", "tar", "sha256sum", "shasum", "cat", "dirname", "pwd", "tr", "head", "wc", "openssl", "perl", "uname", "sysctl"} {
			if p, err := exec.LookPath(tool); err == nil {
				os.Symlink(p, filepath.Join(bin, filepath.Base(p)))
			}
		}
		os.Symlink(wget, filepath.Join(bin, "wget"))
		wenv := append(slices.DeleteFunc(append([]string(nil), env...), func(kv string) bool { return strings.HasPrefix(kv, "PATH=") }), "PATH="+bin)
		if out, err := run(dir, wenv, "--root", root, "--version", "0.3.0"); err == nil || !strings.Contains(out, "not https") {
			t.Errorf("a redirect to http (wget): %v\n%s", err, out)
		}
	} else {
		t.Log("NOT RUN: the wget leg (no wget on PATH)")
	}
	if cur, _ := (release.Install{Root: root}).Current(); cur != "0.4.0" {
		t.Errorf("current %q after refused installs", cur)
	}
	// A relative --root, from another working directory.
	cwd := filepath.Join(dir, "cwd")
	os.MkdirAll(cwd, 0o755)
	if out, err := run(cwd, env, "--root", "rel", "--version", "0.4.0"); err != nil {
		t.Fatalf("relative root: %v\n%s", err, out)
	}
	tgt, err := os.Readlink(filepath.Join(cwd, "rel", "current"))
	if err != nil || !(filepath.IsAbs(tgt) || strings.HasPrefix(tgt, "/")) {
		t.Errorf("relative root's current: %q %v", tgt, err)
	}
	// A root a profile line cannot hold, with PATH modification on.
	home := filepath.Join(dir, "profile", "home")
	penv := slices.DeleteFunc(append([]string(nil), env...), func(kv string) bool { return kv == "MK_NO_MODIFY_PATH=1" })
	if runtime.GOOS == "windows" {
		// Windows' command line cannot carry a quote to Git's sh intact.
		t.Log("NOT RUN: the profile-line roots (Linux only)")
		return
	}
	for _, bad := range []string{`a"b`, "a$b", "a`b"} {
		out, err := run(dir, penv, "--root", filepath.Join(dir, bad), "--version", "0.4.0")
		if err == nil || !strings.Contains(out, "profile line") {
			t.Errorf("root %q: %v\n%s", bad, err, out)
		}
		if _, err := os.Stat(filepath.Join(home, ".profile")); err == nil {
			t.Errorf("root %q: ~/.profile written", bad)
		}
	}
}

// withoutTestKeyRefusal is the refusal a script or mk without the test key
// gives a release signed by the test key: with no release key listed it
// trusts no key at all; once the release key is listed, it does not trust
// the test key's signature.
func withoutTestKeyRefusal() string {
	if len(release.Trusted()) == 0 {
		return "trusts no release key"
	}
	return "does not trust"
}
