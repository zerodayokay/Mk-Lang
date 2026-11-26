// Why: a package's C imports, its C sources and its shim must see one set
// of system headers, read for one triple, and that set depends on what the
// target's sysroot holds (a Microsoft SDK is read for the MSVC triple).
//
// What: cSystem, the system header setup of a target's C, and cRequest,
// the translation request of a package's C imports.
//
// How: the sysroot comes from MK_SYSROOT_<target> (sysrootFor);
// internal/cimport reads its flavour and gives the triple, and for a
// Windows SDK mk's own headers for the MSVC toolset's names, which come
// first. With no sysroot, or one that is no Microsoft SDK, the request is
// the one mk always built.

package driver

import (
	"os"
	"path/filepath"

	"mk/internal/cimport"
	"mk/internal/diag"
)

// cSystem is the system header setup of target's C: its sysroot's
// directories and the triple its C imports' headers are read for. mk's
// headers for a Windows SDK go under the cache's msvch directory (the
// OS's temporary directory with no cache), named by their content, so the
// path stays the same from build to build.
func (s *Session) cSystem(target string, man *Manifest) (cimport.System, *diag.Diagnostic) {
	dirs, d := s.sysrootOf(target, man) // sdkimport.go
	if d != nil {
		return cimport.System{}, d
	}
	base := ""
	if cimport.SysrootFlavour(target, dirs) == cimport.FlavourMicrosoft {
		base = filepath.Join(os.TempDir(), "mk-msvch")
		if st := s.w.openStore(s.cacheDir()); st != nil {
			if d, err := st.Dir("msvch"); err == nil {
				base = d
			}
		}
	}
	sys, err := cimport.SystemFor(target, dirs, base)
	if err != nil {
		return sys, diag.Toolchain("fs", "write", err.Error(), "cannot write mk's headers for the Windows SDK")
	}
	return sys, nil
}

// cRequest is the translation request of the C imports of man (the
// package at osRoot) for target, without its work directory.
func (s *Session) cRequest(imports []cimport.Import, man *Manifest, osRoot, target string) (cimport.Request, *diag.Diagnostic) {
	sys, d := s.cSystem(target, man)
	return cimport.Request{Target: target, Triple: sys.Triple, Imports: imports,
		Include: cIncludeDirs(man, osRoot), Sysroot: sys.Dirs}, d
}
