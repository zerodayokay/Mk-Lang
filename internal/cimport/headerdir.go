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
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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
		if err := writeSameHeader(filepath.Join(dir, n), []byte(files[n])); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// writeSameHeader makes p hold data using a unique temporary file and
// retried rename so concurrent builds never observe a half-written header
// or collide on a shared temporary name.
func writeSameHeader(p string, data []byte) error {
	if old, err := os.ReadFile(p); err == nil && bytes.Equal(old, data) {
		return nil
	}
	f, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	err = f.Chmod(0o644)
	if err == nil {
		_, err = f.Write(data)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	for try := 1; ; try++ {
		if err = os.Rename(tmp, p); err == nil {
			removeStaleHeaderTemps(p)
			return nil
		}
		if old, rerr := os.ReadFile(p); rerr == nil && bytes.Equal(old, data) {
			err = nil
		}
		if err == nil || try == 45 {
			os.Remove(tmp)
			return err
		}
		time.Sleep(time.Duration(try) * time.Millisecond)
	}
}

func removeStaleHeaderTemps(p string) {
	dir, prefix := filepath.Dir(p), "."+filepath.Base(p)+".tmp-"
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	old := time.Now().Add(-10 * time.Minute)
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), prefix) || !e.Type().IsRegular() {
			continue
		}
		if fi, err := e.Info(); err == nil && fi.ModTime().Before(old) {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
