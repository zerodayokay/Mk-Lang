// Why: a fetched package must be the same bytes on every machine and every
// build; pkg.sum, beside the root manifest, records the hashes (not the
// choices) that prove it.
//
// What: pkgSum and sumLine (parsePkgSum, parseSumLine, get, check, add,
// keep, sorted, text), checkSumURL, readPkgSum, writePkgSum, mergePkgSum
// and writeFileAtomic, and writeSameFile for a file many builds write with
// the same bytes.
//
// How: one line per package version in the build list, test dependencies
// included:
//
//	<source> v<version> <commit, 40 hex> tree-sha256:<64 hex>
//
// one line per version whose manifest the version walk read:
//
//	<source> v<version>/pkg.mk <commit> sha256:<64 hex of pkg.mk>
//
// and one per url package, `<url> - - tree-sha256:<hex>`. Lines are sorted
// by source, then version (semantic versioning's order, a version's tree
// line before its /pkg.mk line), and the file is written atomically: a
// temporary file in the same directory, renamed over pkg.sum. Credentials
// never appear: a source is host/path, and a url line with user
// information is refused (fetch.CheckURL alone admits one).
//
// The parse is strict: a malformed line is an error naming its line, so a
// hand edit that breaks the file is caught rather than silently dropped.

package driver

import (
	"bytes"
	"cmp"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"mk/internal/fetch"
)

// sumFile is pkg.sum's name beside the root manifest.
const sumFile = "pkg.sum"

// sumLine is one line of pkg.sum.
type sumLine struct {
	// Source is a source's host/path, or a url package's url.
	Source string
	// Version is X.Y.Z[-pre] with no "v"; "" for a url package.
	Version string
	// Manifest marks a /pkg.mk line (the hash is of pkg.mk's bytes).
	Manifest bool
	// Commit is the tag's commit, 40 hex digits; "" for a url package.
	Commit string
	// Hash is "tree-sha256:<hex>", or "sha256:<hex>" on a /pkg.mk line.
	Hash string
}

// String is the line as pkg.sum writes it, without its newline.
func (l sumLine) String() string {
	if l.Version == "" {
		return l.Source + " - - " + l.Hash
	}
	v := "v" + l.Version
	if l.Manifest {
		v += "/pkg.mk"
	}
	return l.Source + " " + v + " " + l.Commit + " " + l.Hash
}

// key is the line's identity: a source, a version and its kind.
func (l sumLine) key() string {
	k := l.Source + " " + l.Version
	if l.Manifest {
		k += "/pkg.mk"
	}
	return k
}

// pkgSum is a parsed pkg.sum.
type pkgSum struct {
	lines map[string]sumLine
	// used are the keys a walk read, checked or added: the lines its build
	// needs (mk remove keeps only those, pkgcmd.go).
	used map[string]bool
}

func newPkgSum() *pkgSum { return &pkgSum{lines: map[string]sumLine{}, used: map[string]bool{}} }

// use marks key as needed by the build.
func (s *pkgSum) use(key string) {
	if s.used == nil {
		s.used = map[string]bool{}
	}
	s.used[key] = true
}

// parsePkgSum reads pkg.sum's text. Empty lines are skipped; any other
// malformed line, or two lines for one key with different hashes, is an
// error naming its line.
func parsePkgSum(text string) (*pkgSum, error) {
	s := newPkgSum()
	for i, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		l, err := parseSumLine(raw)
		if err != nil {
			return nil, fmt.Errorf("pkg.sum:%d: %v", i+1, err)
		}
		if old, ok := s.lines[l.key()]; ok && old != l {
			return nil, fmt.Errorf("pkg.sum:%d: a second line for %s with another hash or commit", i+1, l.key())
		}
		s.lines[l.key()] = l
	}
	return s, nil
}

// parseSumLine reads one line.
func parseSumLine(raw string) (sumLine, error) {
	f := strings.Split(raw, " ")
	if len(f) != 4 {
		return sumLine{}, fmt.Errorf("a line is four fields separated by one space: source, version, commit, hash")
	}
	l := sumLine{Source: f[0], Commit: f[2], Hash: f[3]}
	if f[1] == "-" {
		if f[2] != "-" {
			return l, fmt.Errorf("a url line's commit is -")
		}
		if err := fetch.CheckURL(l.Source); err != nil {
			return l, err
		}
		if err := checkSumURL(l.Source); err != nil {
			return l, err
		}
		l.Commit = ""
		if _, ok := fetch.ParseTreeHash(l.Hash); !ok {
			return l, fmt.Errorf("hash %q is not tree-sha256:<64 hex digits>", l.Hash)
		}
		return l, nil
	}
	if err := checkSource(l.Source); err != nil {
		return l, err
	}
	v, ok := strings.CutPrefix(f[1], "v")
	if !ok {
		return l, fmt.Errorf("version %q does not start with v", f[1])
	}
	v, l.Manifest = strings.CutSuffix(v, "/pkg.mk")
	if _, ok := parseSemver(v); !ok {
		return l, fmt.Errorf("version %q is not vX.Y.Z", f[1])
	}
	l.Version = v
	if !isHex(l.Commit, 40) {
		return l, fmt.Errorf("commit %q is not 40 hex digits", l.Commit)
	}
	if l.Manifest {
		if _, ok := fetch.ParseHash(l.Hash); !ok {
			return l, fmt.Errorf("hash %q is not sha256:<64 hex digits>", l.Hash)
		}
	} else if _, ok := fetch.ParseTreeHash(l.Hash); !ok {
		return l, fmt.Errorf("hash %q is not tree-sha256:<64 hex digits>", l.Hash)
	}
	return l, nil
}

// checkSumURL refuses a url pkg.sum may not hold: one with user
// information, a query or a fragment. pkg.sum is committed, so no
// credential may reach it; a dependency that needs a token names it in
// netrc.
func checkSumURL(raw string) error {
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return err
	case u.User != nil:
		return fmt.Errorf("a url line carries no user information")
	case u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#"):
		return fmt.Errorf("a url line carries no query or fragment; a token goes in netrc")
	}
	return nil
}

// isHex reports whether s is n lower-case hex digits.
func isHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, c := range []byte(s) {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// get is the line for key's parts, if any.
func (s *pkgSum) get(source, version string, manifest bool) (sumLine, bool) {
	k := sumLine{Source: source, Version: version, Manifest: manifest}.key()
	l, ok := s.lines[k]
	if ok {
		s.use(k)
	}
	return l, ok
}

// check compares a fetched version with its line: "" when there is none
// or it agrees, else the E0202 reason, `tag-moved` when the tag names
// another commit than the line records, `sum-mismatch` when the commit
// agrees and the hash does not (and for a url line, which has no commit).
func (s *pkgSum) check(l sumLine) (reason string, want sumLine) {
	old, ok := s.lines[l.key()]
	s.use(l.key())
	switch {
	case !ok:
		return "", sumLine{}
	case old.Commit != l.Commit:
		return "tag-moved", old
	case old.Hash != l.Hash:
		return fetch.ReasonSumMismatch, old
	}
	return "", old
}

// add records l; it reports whether the file changed. A line that
// disagrees with the recorded one is never replaced here: check it first.
// A url pkg.sum may not hold (checkSumURL) is never recorded: the caller
// refuses its Dep first (E0202 invalid).
func (s *pkgSum) add(l sumLine) bool {
	s.use(l.key())
	if _, ok := s.lines[l.key()]; ok {
		return false
	}
	if l.Version == "" && checkSumURL(l.Source) != nil {
		return false
	}
	s.lines[l.key()] = l
	return true
}

// keep drops every line keep refuses (mk remove, prune of what is no longer
// in the build); it reports whether the file changed.
func (s *pkgSum) keep(keep func(sumLine) bool) bool {
	changed := false
	for k, l := range s.lines {
		if !keep(l) {
			delete(s.lines, k)
			changed = true
		}
	}
	return changed
}

// sorted is the lines in pkg.sum's order: by source, then version (url
// lines have none), a version's tree line before its /pkg.mk line.
func (s *pkgSum) sorted() []sumLine {
	out := make([]sumLine, 0, len(s.lines))
	for _, l := range s.lines {
		out = append(out, l)
	}
	slices.SortFunc(out, func(a, b sumLine) int {
		if c := strings.Compare(a.Source, b.Source); c != 0 {
			return c
		}
		va, oka := parseSemver(a.Version)
		vb, okb := parseSemver(b.Version)
		if oka && okb {
			if c := compareSemver(va, vb); c != 0 {
				return c
			}
		} else if c := strings.Compare(a.Version, b.Version); c != 0 {
			return c
		}
		return cmp.Compare(b2i(a.Manifest), b2i(b.Manifest))
	})
	return out
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// text is the file's content.
func (s *pkgSum) text() string {
	var b strings.Builder
	for _, l := range s.sorted() {
		b.WriteString(l.String())
		b.WriteByte('\n')
	}
	return b.String()
}

// readPkgSum reads pkg.sum in dir: an empty file when there is none.
func readPkgSum(dir string) (*pkgSum, error) {
	b, err := os.ReadFile(filepath.Join(dir, sumFile))
	if os.IsNotExist(err) {
		return newPkgSum(), nil
	}
	if err != nil {
		return nil, err
	}
	return parsePkgSum(string(b))
}

// writePkgSum writes s as dir's pkg.sum atomically: a temporary file in
// dir, renamed over pkg.sum, so a reader sees the old file or the new one.
// An empty s with no file on disk writes nothing (a build with no source
// or url package keeps no pkg.sum).
func writePkgSum(dir string, s *pkgSum) error {
	final := filepath.Join(dir, sumFile)
	if len(s.lines) == 0 {
		if _, err := os.Stat(final); os.IsNotExist(err) {
			return nil
		}
	}
	return writeFileAtomic(final, []byte(s.text()))
}

// writeFileAtomic writes data to a temporary file beside path and renames
// it over path.
func writeFileAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// writeSameFile makes the file p hold data unless it already does, and
// reports whether this call wrote it. It is for a file that every writer
// writes with the same bytes, such as a header in a directory named by
// its content, when builds in this process and in others may write it at
// once: each writes a temporary of its own (mode 0644, as os.WriteFile
// makes a file) and renames it over p, so a reader never sees half the
// file. On Windows the rename fails while another writer or a reader has
// p open, so a failed rename is retried, briefly, until it succeeds or p
// holds data, whoever wrote it.
func writeSameFile(p string, data []byte) (bool, error) {
	if old, err := os.ReadFile(p); err == nil && bytes.Equal(old, data) {
		return false, nil
	}
	f, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+".tmp-*")
	if err != nil {
		return false, err
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
		return false, err
	}
	for try := 1; ; try++ {
		if err = os.Rename(tmp, p); err == nil {
			return true, nil
		}
		if old, rerr := os.ReadFile(p); rerr == nil && bytes.Equal(old, data) {
			err = nil // another writer's copy is in place
		}
		if err == nil || try == sameFileTries {
			os.Remove(tmp)
			return false, err
		}
		time.Sleep(time.Duration(try) * time.Millisecond)
	}
}

// sameFileTries bounds writeSameFile's renames: their pauses add up to
// about a second.
const sameFileTries = 45

// mergePkgSum writes s's lines into dir's pkg.sum under a lock file, adding
// every line the file holds now that s lacks: a build writes only what it
// adds, so two concurrent builds lose no line, as Go merges go.sum. A line
// of the file that disagrees with s's for one key keeps s's: this build
// checked its own. The lock is pkg.sum.lock, created exclusively and removed
// after; one older than a minute is a crashed writer's and is taken over.
func mergePkgSum(dir string, s *pkgSum) error {
	lock := filepath.Join(dir, sumFile+".lock")
	deadline := time.Now().Add(30 * time.Second)
	for {
		f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			f.Close()
			break
		}
		if fi, serr := os.Stat(lock); serr == nil && time.Since(fi.ModTime()) > time.Minute {
			_ = os.Remove(lock)
			continue
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s is held by another build: %w", lock, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	defer os.Remove(lock)
	if now, err := readPkgSum(dir); err == nil {
		for k, l := range now.lines {
			if _, ok := s.lines[k]; !ok {
				s.lines[k] = l
			}
		}
	}
	return writePkgSum(dir, s)
}
