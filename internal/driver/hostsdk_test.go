// Why: which system headers a C import with sdk: true reads must follow
// one order on every machine, and CI's runners have no SDK.
// What: TestHostSDK_Order and TestSDKText.
// How: hostSDKFor over fake hosts: a fake Windows Kits tree as the
// registry's root and as the default directory, a fake /usr/include, the
// evidence directory and nothing.

package driver

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestHostSDK_Order(t *testing.T) {
	kits := fakeKits(t)
	inc := filepath.Join(kits, "Include", "10.0.1.0")
	usr := writeTree(t, map[string]string{"stdio.h": "", "x86_64-linux-gnu/bits.h": ""})
	ev := writeTree(t, map[string]string{"c-import/sysroots/x86_64-linux/x.h": "", "c-import/sysroots/x86_64-windows/x.h": ""})
	pf := t.TempDir()
	env := func(vars map[string]string, goos string, roots ...string) hostEnv {
		return hostEnv{getenv: func(k string) string { return vars[k] }, goos: goos, kitsRoots: func() []string { return roots }, usrDir: usr}
	}
	for _, c := range []struct {
		name, target string
		e            hostEnv
		dirs         []string
		source       string
	}{
		{"variable first", "x86_64-windows", env(map[string]string{"MK_SYSROOT_x86_64-windows": pf, "MK_EVIDENCE": ev}, "windows", kits), []string{pf}, "MK_SYSROOT_x86_64-windows"},
		{"registry", "x86_64-windows", env(map[string]string{"MK_EVIDENCE": ev}, "windows", kits),
			[]string{filepath.Join(inc, "um"), filepath.Join(inc, "shared"), filepath.Join(inc, "ucrt")}, "the Windows SDK 10.0.1.0 at " + kits + " (the registry's KitsRoot10)"},
		{"default directory", "x86_64-windows", env(map[string]string{"ProgramFiles(x86)": filepath.Dir(filepath.Dir(kits))}, "windows", t.TempDir()), nil, ""},
		{"no Windows SDK off Windows", "x86_64-windows", env(map[string]string{"MK_EVIDENCE": ev}, "linux", kits), []string{filepath.Join(ev, "c-import", "sysroots", "x86_64-windows")}, "$MK_EVIDENCE/c-import/sysroots/x86_64-windows"},
		{"linux host", "x86_64-linux", env(map[string]string{"MK_EVIDENCE": ev}, "linux"), []string{filepath.Join(usr, "x86_64-linux-gnu"), usr}, "the host's " + usr + " and " + filepath.Join(usr, "x86_64-linux-gnu")},
		{"no multiarch directory", "aarch64-linux", env(nil, "linux"), nil, ""},
		{"linux target on windows", "x86_64-linux", env(map[string]string{"MK_EVIDENCE": ev}, "windows", kits), []string{filepath.Join(ev, "c-import", "sysroots", "x86_64-linux")}, "$MK_EVIDENCE/c-import/sysroots/x86_64-linux"},
		{"none", "x86_64-windows", env(nil, "windows"), nil, ""},
	} {
		h := hostSDKFor(c.target, c.e)
		if c.name == "default directory" {
			// The fake tree is Include\10.0.1.0 under kits; a default
			// directory named Windows Kits\10 under ProgramFiles(x86) is
			// not it, so nothing is found.
			if h.Source != "" {
				t.Errorf("%s: %+v", c.name, h)
			}
			continue
		}
		if strings.Join(h.Dirs, "|") != strings.Join(c.dirs, "|") || h.Source != c.source {
			t.Errorf("%s: got %q from %q\nwant %q from %q", c.name, h.Dirs, h.Source, c.dirs, c.source)
		}
	}
	// The default directory, when the registry names nothing.
	def := writeTree(t, map[string]string{
		"Windows Kits/10/Include/10.0.5.0/um/windows.h": "", "Windows Kits/10/Include/10.0.5.0/um/winnt.h": "",
		"Windows Kits/10/Include/10.0.40.0/um/windows.h": "", "Windows Kits/10/Include/10.0.40.0/um/winnt.h": ""})
	h := hostSDKFor("x86_64-windows", env(map[string]string{"ProgramFiles(x86)": def}, "windows"))
	if !strings.Contains(h.Source, "the Windows SDK 10.0.40.0 at ") || !strings.HasSuffix(h.Source, "(the default directory)") ||
		h.Dirs[0] != filepath.Join(def, "Windows Kits", "10", "Include", "10.0.40.0", "um") {
		t.Errorf("default directory: %+v", h)
	}
}

func TestSDKText(t *testing.T) {
	kits := fakeKits(t)
	e := hostEnv{getenv: func(string) string { return "" }, goos: "windows", kitsRoots: func() []string { return []string{kits} }}
	text := sdkText(false, e)
	for _, want := range []string{"system views (import c.<view>, no pkg.CImport needed):\n", "c.vulkan", "  x86_64-windows\n",
		"    without sdk: true none: only Clang's and the package's headers\n",
		"    with sdk: true    " + filepath.Join(kits, "Include", "10.0.1.0", "um"), "(from the Windows SDK 10.0.1.0 at " + kits + " (the registry's KitsRoot10); microsoft flavour, triple x86_64-pc-windows-msvc)"} {
		if !strings.Contains(text, want) {
			t.Errorf("mk sdk lacks %q:\n%s", want, text)
		}
	}
}
