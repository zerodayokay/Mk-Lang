// Why: a system view must be usable with no SDK on the machine, so its
// archive ships inside mk; a source build without the generated data must
// still compile and say how to make it.
//
// What: Embedded (a view's archive bytes for a target), EmbeddedManifest
// (the generated views.txt of a target) and ArchiveName.
//
// How: data/ is embedded whole; it always holds README.txt, so the embed
// pattern matches even when no archive was generated, and archives are
// found by name at run time: data/<target>/<view>.mksv.

package sysview

import (
	"embed"
	"path"
)

//go:embed data
var data embed.FS

// ArchiveName is the file name of view name's archive.
func ArchiveName(name string) string { return name + ".mksv" }

// ManifestName is the file name of a target's manifest of views.
const ManifestName = "views.txt"

// Embedded is the archive of view name for target that this mk carries, or
// false when it carries none.
func Embedded(target, name string) ([]byte, bool) {
	b, err := data.ReadFile(path.Join("data", target, ArchiveName(name)))
	return b, err == nil
}

// EmbeddedManifest is the manifest of target's views this mk carries, ""
// when none.
func EmbeddedManifest(target string) string {
	b, err := data.ReadFile(path.Join("data", target, ManifestName))
	if err != nil {
		return ""
	}
	return string(b)
}
