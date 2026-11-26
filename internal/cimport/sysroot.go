// Why: the Windows headers come in two flavours that need different Clang
// triples: mingw-w64's parse under its own, the Microsoft SDK's only under
// the MSVC one. A package that names no Microsoft SDK must be read exactly
// as before, so the choice follows what the sysroot holds, never what else
// is installed.
//
// What: Flavour, SysrootFlavour, HeaderTriple, MSVCVersion, TripleFlags
// and System, the system header directories, the header triple and its
// fixed flags of one target's C imports.
//
// How: the sysroot's directories, in search order, are looked at for a few
// file names: a _mingw.h anywhere is mingw-w64; else the first directory
// holding windows.h is the Microsoft SDK when winnt.h is beside it. Only the
// x86_64-windows target looks; any other target, no sysroot, or no
// windows.h keeps ParseTriple's triple. A Microsoft SDK also gets mk's
// headers for the four MSVC toolset headers it includes (msvch.go), first
// among the system directories, and a fixed Visual C++ version on every
// Clang command line, so Clang never takes one from an installed Visual
// Studio.

package cimport

import "path/filepath"

// Flavour is the kind of Windows header set a sysroot holds.
type Flavour uint8

const (
	// FlavourNone: no Windows header set is recognised (no sysroot, no
	// windows.h, or a target other than x86_64-windows).
	FlavourNone Flavour = iota
	// FlavourMinGW: mingw-w64's headers (a _mingw.h on the path).
	FlavourMinGW
	// FlavourMicrosoft: the Windows SDK's headers (windows.h beside
	// winnt.h, and no _mingw.h).
	FlavourMicrosoft
)

func (f Flavour) String() string {
	switch f {
	case FlavourMinGW:
		return "mingw-w64"
	case FlavourMicrosoft:
		return "microsoft"
	}
	return "none"
}

// SysrootFlavour is the flavour of target's sysroot dirs (in search
// order): for x86_64-windows, FlavourMinGW when any directory holds a
// _mingw.h; else FlavourMicrosoft when the first directory holding a
// windows.h also holds a winnt.h; else FlavourNone. It stats at most three
// names per directory and reads no file.
func SysrootFlavour(target string, dirs []string) Flavour {
	if target != "x86_64-windows" || len(dirs) == 0 {
		return FlavourNone
	}
	for _, d := range dirs {
		if isFile(filepath.Join(d, "_mingw.h")) {
			return FlavourMinGW
		}
	}
	for _, d := range dirs {
		if isFile(filepath.Join(d, "windows.h")) {
			if isFile(filepath.Join(d, "winnt.h")) {
				return FlavourMicrosoft
			}
			return FlavourNone
		}
	}
	return FlavourNone
}

// HeaderTriple is the Clang triple target's headers are read for under
// flavour f: the MSVC triple for the Microsoft SDK, whose headers parse
// under no other; ParseTriple's for every other case, so a build that names
// no Microsoft SDK reads its headers as it always did.
func HeaderTriple(target string, f Flavour) string {
	if target == "x86_64-windows" && f == FlavourMicrosoft {
		return "x86_64-pc-windows-msvc"
	}
	return ParseTriple(target)
}

// MSVCVersion is the Visual C++ version Clang compiles for under the MSVC
// triple: the one Clang assumes when it finds no Visual Studio. Without the
// flag Clang takes _MSC_VER from an installed Visual Studio (found through
// the environment, the Visual Studio setup API or the registry), and the
// SDK's headers branch on it: from 1950 the ucrt's inline functions are no
// longer static, so a binding would follow what else is installed.
const MSVCVersion = "19.33"

// TripleFlags are the Clang flags that fix what triple would otherwise
// take from the host: for the MSVC triple, the Visual C++ version
// (MSVCVersion); none for any other triple, whose command lines stay as
// they were. The C imports' runs, the shim and the C sources of a package
// whose headers are read for the MSVC triple all take them.
func TripleFlags(triple string) []string {
	if triple == "x86_64-pc-windows-msvc" {
		return []string{"-fms-compatibility-version=" + MSVCVersion}
	}
	return nil
}

// System is the system header setup of one target's C imports, its C
// sources and its shim.
type System struct {
	Dirs    []string // the system directories, after Clang's own, in search order
	Triple  string   // the triple the C imports' headers are read for
	Flavour Flavour
}

// SystemFor is the system header setup of target whose sysroot
// directories are sysroot (as MK_SYSROOT_<target> lists them). For a
// Microsoft SDK, mk's own headers for the MSVC toolset's names
// (MicrosoftHeaders) are written under base, in a directory named by their
// content (WriteHeaderDir), and that directory comes first: before the
// SDK's, and before any toolset directory the list names, so the bindings'
// names never depend on what else is installed. Its path, and so the
// headers' content, is part of every cache key that holds the sysroot.
// Any other sysroot is returned as it is, and nothing is written.
func SystemFor(target string, sysroot []string, base string) (System, error) {
	f := SysrootFlavour(target, sysroot)
	sys := System{Dirs: sysroot, Triple: HeaderTriple(target, f), Flavour: f}
	if f != FlavourMicrosoft {
		return sys, nil
	}
	dir, err := WriteHeaderDir(base, "mk.cimport.msvch.v1", MicrosoftHeaderNames, MicrosoftHeaders())
	if err != nil {
		return System{}, err
	}
	sys.Dirs = append([]string{dir}, sysroot...)
	return sys, nil
}
