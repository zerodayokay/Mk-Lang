// Why: a package's own C import whose headers include the platform's
// (a third-party library including <windows.h>) must say it wants the
// host's SDK, and which SDK it then reads must follow a fixed order that
// mk sdk can show.
//
// What: hostSDK and hostSDKFor, the system header directories a C import
// with sdk: true reads for a target, and where they come from.
//
// How: the first rule that gives directories: MK_SYSROOT_<target>; the
// host's SDK (for x86_64-windows on a Windows host, the newest version of
// the Windows SDK under the registry's KitsRoot10, then under
// %ProgramFiles(x86)%\Windows Kits\10; for a Linux target on a Linux host,
// the target's multiarch directory and /usr/include);
// $MK_EVIDENCE/c-import/sysroots/<target>; else none. The registry is read
// only on a Windows host (hostsdk_windows.go).

package driver

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// hostSDK is the system header setup of a C import with sdk: true.
type hostSDK struct {
	Dirs   []string // in search order; none when Source is ""
	Source string   // where they come from, as mk sdk prints it
}

// hostEnv is what hostSDKFor reads of the machine, so a test can give it
// a fake one.
type hostEnv struct {
	getenv    func(string) string
	goos      string
	kitsRoots func() []string
	usrDir    string // /usr/include on a real host
}

// realHostEnv is this machine.
func realHostEnv() hostEnv {
	return hostEnv{getenv: os.Getenv, goos: runtime.GOOS, kitsRoots: windowsKitsRoots, usrDir: "/usr/include"}
}

// multiarch is a Linux target's Debian multiarch directory name.
var multiarch = map[string]string{"x86_64-linux": "x86_64-linux-gnu", "aarch64-linux": "aarch64-linux-gnu"}

// hostSDKFor is the setup sdk: true gives target on host e.
func hostSDKFor(target string, e hostEnv) hostSDK {
	for _, v := range []string{"MK_SYSROOT_" + target, "MK_SYSROOT_" + strings.ReplaceAll(target, "-", "_")} {
		if e.getenv(v) != "" {
			return hostSDK{Dirs: sysrootFor(target, e.getenv), Source: v}
		}
	}
	switch {
	case target == "x86_64-windows" && e.goos == "windows":
		roots := append([]string(nil), e.kitsRoots()...)
		n := len(roots)
		if pf := e.getenv("ProgramFiles(x86)"); pf != "" {
			roots = append(roots, filepath.Join(pf, "Windows Kits", "10"))
		}
		for i, r := range roots {
			inc, v, err := windowsSDKInclude(r)
			if err != nil {
				continue
			}
			how := "the registry's KitsRoot10"
			if i >= n {
				how = "the default directory"
			}
			return hostSDK{Dirs: windowsSDKDirs(inc), Source: "the Windows SDK " + v + " at " + r + " (" + how + ")"}
		}
	case multiarch[target] != "" && e.goos == "linux" && e.usrDir != "":
		ma := filepath.Join(e.usrDir, multiarch[target])
		if isDir(ma) && isDir(e.usrDir) {
			return hostSDK{Dirs: []string{ma, e.usrDir}, Source: "the host's " + e.usrDir + " and " + ma}
		}
	}
	if ev := evidenceDir(e.getenv); ev != "" {
		d := filepath.Join(ev, "c-import", "sysroots", target)
		if isDir(d) {
			return hostSDK{Dirs: sysrootFor(target, func(k string) string {
				if strings.HasPrefix(k, "MK_SYSROOT_") {
					return ""
				}
				return e.getenv(k)
			}), Source: "$MK_EVIDENCE/c-import/sysroots/" + target}
		}
	}
	return hostSDK{}
}

// isDir reports a directory at p.
func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
