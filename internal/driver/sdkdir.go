// Why: the system view generator and a C import with sdk: true both read
// a Windows SDK, which installs one Include\<version> directory per
// version beside the others; which one is read must follow a fixed rule.
//
// What: windowsSDKInclude, the Include\<version> directory of a Windows SDK
// root, and its version; windowsSDKDirs, its um, shared and ucrt
// directories; versionLess.
//
// How: a directory holding um\windows.h is taken as it is (its name is the
// version); otherwise the newest version directory under root\Include that
// holds um\windows.h, versions compared number by number.

package driver

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// windowsSDKInclude is the Include\<version> directory of the Windows SDK
// at root (the Windows Kits\10 directory, or an Include\<version> directory
// itself) and its version: the newest that holds um\windows.h.
func windowsSDKInclude(root string) (dir, version string, err error) {
	if isRegular(filepath.Join(root, "um", "windows.h")) {
		return root, filepath.Base(root), nil
	}
	inc := filepath.Join(root, "Include")
	ents, err := os.ReadDir(inc)
	if err != nil {
		return "", "", fmt.Errorf("%s holds no Include directory of a Windows SDK", root)
	}
	for _, e := range ents {
		v := e.Name()
		if !e.IsDir() || !isRegular(filepath.Join(inc, v, "um", "windows.h")) {
			continue
		}
		if version == "" || versionLess(version, v) {
			version = v
		}
	}
	if version == "" {
		return "", "", fmt.Errorf("%s holds no SDK version with um\\windows.h", inc)
	}
	return filepath.Join(inc, version), version, nil
}

// windowsSDKDirs are the system header directories of an SDK's
// Include\<version> directory, in search order.
func windowsSDKDirs(include string) []string {
	return []string{filepath.Join(include, "um"), filepath.Join(include, "shared"), filepath.Join(include, "ucrt")}
}

// versionLess compares dotted versions number by number; a part that is no
// number compares as text after every number.
func versionLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		x, ex := strconv.Atoi(pa[i])
		y, ey := strconv.Atoi(pb[i])
		switch {
		case ex == nil && ey == nil && x != y:
			return x < y
		case (ex == nil) != (ey == nil):
			return ex == nil
		case ex != nil && pa[i] != pb[i]:
			return pa[i] < pb[i]
		}
	}
	return len(pa) < len(pb)
}
