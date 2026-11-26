// Why: mk writes some C headers itself (std.libc's, for C sources; the
// MSVC toolset names the Windows SDK needs, for C imports), and Clang must
// find them at a path that names their content, so a cache key holding the
// path changes exactly when a header does, and two builds never see a
// half-written file.
//
// What: WriteHeaderDir.
//
// How: the directory is named by a hash of every header's name and text
// under a caller's domain; each file is written beside itself and renamed
// into place, and left alone when it already holds the text.

package cimport

import (
	"fmt"
	"os"
	"path/filepath"

	"mk/internal/source"
)

// WriteHeaderDir writes the headers names (each files[n]) into the
// directory under base named by their content in domain, and returns that
// directory. A header already there with the same text is not rewritten.
func WriteHeaderDir(base, domain string, names []string, files map[string]string) (string, error) {
	h := source.NewHasher(domain)
	for _, n := range names {
		h.String(n)
		h.String(files[n])
	}
	sum := h.Sum()
	dir := filepath.Join(base, fmt.Sprintf("%x", sum[:8]))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for _, n := range names {
		p := filepath.Join(dir, n)
		if old, err := os.ReadFile(p); err == nil && string(old) == files[n] {
			continue
		}
		// A temporary and a rename, so a concurrent build never reads a
		// half-written header.
		tmp := p + fmt.Sprintf(".%d.tmp", os.Getpid())
		if err := os.WriteFile(tmp, []byte(files[n]), 0o644); err != nil {
			return "", err
		}
		if err := os.Rename(tmp, p); err != nil {
			os.Remove(tmp)
			if old, rerr := os.ReadFile(p); rerr != nil || string(old) != files[n] {
				return "", err
			}
		}
	}
	return dir, nil
}
