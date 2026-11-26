// Why: import c.<name> must bind real system headers, not only test samples,
// and at a speed a build can pay.
// What: TestCImport_HeaderCorpus and TestCImport_WindowsHSteps, measurements
// run only when MK_CIMPORT_CORPUS=1 or MK_CIMPORT_WINDOWS_SYSROOT is set.
// How: logs each header's share of declarations bound without hand-written
// glue, against zig translate-c's share when zig is on PATH, and windows.h's
// step times; neither share nor time is asserted.

package cimport

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// corpusHeaders is the header corpus: the trees are Zig's mingw-w64 and musl
// headers and the sqlite amalgamation's sqlite3.h.
var corpusHeaders = []struct{ target, header string }{
	{"x86_64-windows", "windows.h"}, {"x86_64-windows", "winsock2.h"},
	{"x86_64-linux", "sqlite3.h"},
	{"x86_64-linux", "stdio.h"}, {"x86_64-linux", "stdlib.h"}, {"x86_64-linux", "string.h"},
	{"x86_64-linux", "pthread.h"}, {"x86_64-linux", "unistd.h"}, {"x86_64-linux", "signal.h"},
	{"x86_64-linux", "time.h"}, {"x86_64-linux", "math.h"}, {"x86_64-linux", "errno.h"},
	{"x86_64-linux", "fcntl.h"}, {"x86_64-linux", "dirent.h"}, {"x86_64-linux", "poll.h"},
	{"x86_64-linux", "netdb.h"}, {"x86_64-linux", "sys/socket.h"}, {"x86_64-linux", "sys/stat.h"},
	{"x86_64-linux", "sys/mman.h"}, {"x86_64-linux", "sys/epoll.h"}, {"x86_64-linux", "netinet/in.h"},
	{"x86_64-linux", "arpa/inet.h"}, {"x86_64-linux", "termios.h"}, {"x86_64-linux", "setjmp.h"},
	{"x86_64-linux", "locale.h"}, {"x86_64-linux", "wchar.h"}, {"x86_64-linux", "dlfcn.h"},
	{"x86_64-linux", "regex.h"}, {"x86_64-linux", "linux/io_uring.h"}, {"x86_64-linux", "linux/input.h"},
}

// TestCImport_HeaderCorpus (measurement, MK_CIMPORT_CORPUS=1): imports each
// corpus header alone and logs the share of its declarations bound with
// no glue written by hand, and, with zig on PATH, zig translate-c's share
// over the same header: its top-level pub declarations not demoted to
// opaque and not a @compileError, over all of them, the target's
// predefined macros left out. The two denominators are each tool's own
// count, so the comparison is per header, not per declaration.
func TestCImport_HeaderCorpus(t *testing.T) {
	if os.Getenv("MK_CIMPORT_CORPUS") != "1" {
		t.Skip("MK_CIMPORT_CORPUS=1 runs the header corpus measurement")
	}
	tl, rd := clangFor(t)
	zig, zerr := exec.LookPath("zig")
	if zerr != nil {
		t.Fatal("the corpus trees are Zig's libc headers: zig is not on PATH")
	}
	zp, _ := filepath.EvalSymlinks(zig)
	libc := filepath.Join(filepath.Dir(zp), "lib", "libc", "include")
	sysroots := map[string][]string{
		"x86_64-windows": {filepath.Join(libc, "any-windows-any")},
		"x86_64-linux":   {filepath.Join(libc, "x86_64-linux-musl"), filepath.Join(libc, "generic-musl"), filepath.Join(libc, "any-linux-any")},
	}
	sqliteDir := filepath.Join(evidence(), "vendor", "sqlite")
	zigTriple := map[string]string{"x86_64-windows": "x86_64-windows-gnu", "x86_64-linux": "x86_64-linux-musl"}
	base := map[string]map[string]bool{}
	for target, triple := range zigTriple {
		empty := filepath.Join(t.TempDir(), "empty.h")
		_ = os.WriteFile(empty, nil, 0o644)
		out, _ := exec.Command(zig, "translate-c", "-lc", "-target", triple, empty).Output()
		base[target] = zigNames(out)
	}
	for _, c := range corpusHeaders {
		inc := []string{t.TempDir()}
		if c.header == "sqlite3.h" {
			inc = []string{sqliteDir}
		}
		start := time.Now()
		res, err := Translate(context.Background(), tl, rd, Request{Target: c.target, Triple: ParseTriple(c.target),
			Imports: []Import{{Name: "h", Headers: []string{c.header}}}, Include: inc, Sysroot: sysroots[c.target],
			WorkDir: filepath.Join(t.TempDir(), "w")})
		took := time.Since(start)
		if err != nil {
			t.Errorf("%s %s: %v", c.target, c.header, err)
			continue
		}
		st := res.Stats[0]
		line := ""
		if st.Decls > 0 {
			line = c.target + " " + c.header + ": mk " + strconv.Itoa(st.Bound()) + "/" + strconv.Itoa(st.Decls) + " = " + pct(st.Bound(), st.Decls) + " in " + took.Round(time.Millisecond).String()
		}
		// translate-c over the same header, through a file that includes it.
		src := filepath.Join(t.TempDir(), "h.h")
		_ = os.WriteFile(src, []byte("#include <"+c.header+">\n"), 0o644)
		args := []string{"translate-c", "-lc", "-target", zigTriple[c.target]}
		if c.header == "sqlite3.h" {
			args = append(args, "-I", sqliteDir)
		}
		out, zerr := exec.Command(zig, append(args, src)...).Output()
		if zerr != nil {
			line += "; translate-c failed: " + zerr.Error()
		} else {
			ok, all := zigShare(out, base[c.target])
			line += "; translate-c " + strconv.Itoa(ok) + "/" + strconv.Itoa(all) + " = " + pct(ok, all)
		}
		t.Logf("%s; mk skipped %v", line, st.Skipped)
	}
}

var zigDecl = regexp.MustCompile(`^pub (?:extern )?(?:inline )?(?:fn|const|var) ([A-Za-z_][A-Za-z0-9_]*)`)

// zigNames are the top-level pub names of a translate-c output.
func zigNames(out []byte) map[string]bool {
	names := map[string]bool{}
	for _, l := range bytes.Split(out, []byte("\n")) {
		if m := zigDecl.FindSubmatch(l); m != nil {
			names[string(m[1])] = true
		}
	}
	return names
}

// zigShare counts translate-c's top-level pub declarations outside base,
// and those of them neither an opaque demotion nor a @compileError.
func zigShare(out []byte, base map[string]bool) (ok, all int) {
	for _, l := range bytes.Split(out, []byte("\n")) {
		m := zigDecl.FindSubmatch(l)
		if m == nil || base[string(m[1])] {
			continue
		}
		all++
		s := string(l)
		if strings.Contains(s, "= opaque {}") || strings.Contains(s, "@compileError(") {
			continue
		}
		ok++
	}
	return ok, all
}

func evidence() string {
	if e := os.Getenv("MK_EVIDENCE"); e != "" {
		return e
	}
	wd, _ := os.Getwd()
	return filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(wd))), "Mk-evidence")
}

func pct(a, b int) string {
	if b == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.1f%%", 100*float64(a)/float64(b))
}

// TestCImport_WindowsHSteps (measurement, MK_CIMPORT_WINDOWS_SYSROOT names
// mingw-w64's include directory, or lists a Windows SDK's include
// directories): translates windows.h five times, clean
// each time, and logs each step's wall time against a 2 s budget.
// It asserts no time: timings on a shared machine are not stable.
func TestCImport_WindowsHSteps(t *testing.T) {
	sys := os.Getenv("MK_CIMPORT_WINDOWS_SYSROOT")
	if sys == "" {
		t.Skip("MK_CIMPORT_WINDOWS_SYSROOT is not set")
	}
	tl, rd := clangFor(t)
	// A list, as MK_SYSROOT_<target> is: a Windows SDK's um, shared and
	// ucrt directories are read for the MSVC triple, with mk's headers.
	st, err := SystemFor("x86_64-windows", filepath.SplitList(sys), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		res, err := Translate(context.Background(), tl, rd, Request{Target: "x86_64-windows", Triple: st.Triple,
			Imports: []Import{{Name: "win", Headers: []string{"windows.h"}}}, Include: []string{t.TempDir()}, Sysroot: st.Dirs,
			WorkDir: filepath.Join(t.TempDir(), "w")})
		if err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		for _, s := range res.Steps {
			fmt.Fprintf(&b, " %s %v", s.Name, s.Took.Round(time.Millisecond))
		}
		st := res.Stats[0]
		t.Logf("windows.h run %d:%s; bound %d/%d = %s", i+1, b.String(), st.Bound(), st.Decls, pct(st.Bound(), st.Decls))
	}
}
