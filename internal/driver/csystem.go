// Why: a package's C imports, its C sources and its shim must see one set
// of system headers, read for one triple, and that set depends on what the
// target's sysroot holds (a Microsoft SDK is read for the MSVC triple).
//
// What: cSystem, the system header setup of a target's C, and cRequest,
// the translation request of a package's C imports.
//
// How: the sysroot comes from MK_SYSROOT_<target> (sysrootFor);
// internal/cimport reads its flavour and gives the triple. With no sysroot,
// or one that is no Microsoft SDK, the request is the one mk always built.

package driver

import (
	"os"

	"mk/internal/cimport"
)

// cSystem is the system header setup of target's C: its sysroot's
// directories and the triple its C imports' headers are read for.
func (s *Session) cSystem(target string) cimport.System {
	return cimport.SystemFor(target, sysrootFor(target, os.Getenv))
}

// cRequest is the translation request of the C imports of man (the
// package at osRoot) for target, without its work directory.
func (s *Session) cRequest(imports []cimport.Import, man *Manifest, osRoot, target string) cimport.Request {
	sys := s.cSystem(target)
	return cimport.Request{Target: target, Triple: sys.Triple, Imports: imports,
		Include: cIncludeDirs(man, osRoot), Sysroot: sys.Dirs}
}
