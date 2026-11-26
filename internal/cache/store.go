// Why: incremental builds reuse earlier work only if a store on disk can be
// shared by many processes at once and never hands back a torn, stale or
// colliding entry as a hit.
//
// What: Store (Open, Get, Put, Dir, TmpDir, Mark, Account), the entry kinds
// (Kind, KindManifest and the rest), Options, DefaultBudget and the timing
// constants (TouchInterval, Grace, GCInterval, LockStale).
//
// How: entries live under <root>/v1/<kind>/<hh>/ named by a 128-bit prefix of
// their key; a write goes to tmp/ and is renamed into place, and every read
// verifies the frame and the full key. Every kind but KindManifest is
// content-addressed and never rewritten. A failed write is dropped and
// logged: it costs a later miss, never a wrong hit.

// Package cache is the content-addressed store on disk: named entries of
// opaque bytes, written atomically by rename, verified on every read, marked on
// use, evicted least recently used under a size budget, with a lock that
// serializes garbage collection between processes. It never interprets an
// entry: it sees a kind, a source.Hash key and bytes.
package cache

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"

	"mk/internal/source"
)

// Kind names a family of entries; it is a directory name, lowercase ASCII,
// at most 8 bytes. The cache never interprets it.
type Kind string

const (
	KindParse    Kind = "parse"    // per-file parse summary (iface.ParseSummary); the driver writes none (a module's summaries are one KindManifest entry, its parse index)
	KindManifest Kind = "manifest" // every candidate list: module, function, evaluation, generator, C bindings, C object (the only mutable kind)
	KindIface    Kind = "iface"    // module interface
	KindFunc     Kind = "func"     // function check result
	KindEval     Kind = "eval"     // evaluation record
	KindGen      Kind = "gen"      // generator output
	KindObj      Kind = "obj"      // object file (bitcode in release)
	KindPos      Kind = "pos"      // positions object (codegen's positions pack), when codegen keeps one
	KindLib      Kind = "lib"      // generated import library
	KindLayout   Kind = "layout"   // clang data layout and triple, per clang version
	KindProfile  Kind = "prof"     // merged .mkprof copies (by content)
	KindFetch    Kind = "fetch"    // a url dependency's archive, keyed by its SHA-256 (internal/fetch)
	KindCImport  Kind = "cimport"  // a package's C import bindings per target, keyed by the request and its headers' hashes (internal/cimport)
	KindCObj     Kind = "cobj"     // an object compiled from a c_sources, asm_sources or shim file, keyed by command line and content
)

// Directory and file names under v1 that are not entry kinds.
const (
	tmpDir    = "tmp"
	hintsDir  = "hints"
	lockFile  = "gc.lock"
	stampFile = "gc.stamp"
	ltoDir    = "thinlto"
)

// BinDir is the Dir of the executables the driver links by content (mk run,
// mk test, a corpus unit's run), one per <link key>/<name>. Like every Dir it
// counts toward the budget and GC evicts its files least recently used first;
// a running executable that cannot be deleted is skipped. It is no entry
// kind.
const BinDir = "bin"

// Options configures a Store. The zero Options is valid and means the
// defaults.
type Options struct {
	Budget   int64            // bytes; 0 means DefaultBudget
	Now      func() time.Time // injectable clock for tests; nil means time.Now
	ReadOnly bool             // Get only: Put, PutFile and Save are no-ops returning nil, no mark is written, Open creates nothing
	// Logf receives the writes the store dropped (a manifest rename that lost
	// to an open handle on Windows three times, a Put that could not land):
	// the driver passes its --timings log. Nil discards them. The Store takes
	// no lock, so Logf is called from many goroutines at once and must be
	// safe for concurrent use.
	Logf func(format string, args ...any)
}

// DefaultBudget is the eviction cap, 2 GiB: a chosen default, not a
// measured need.
const DefaultBudget = 2 << 30

const (
	TouchInterval = time.Hour        // Get re-marks an entry only if its mark is older
	Grace         = 2 * time.Hour    // GC never evicts an entry marked more recently
	GCInterval    = time.Hour        // MaybeGC walks at most this often
	LockStale     = 10 * time.Minute // a gc.lock whose mtime is older is broken
)

// renameAttempts bounds the renames of one write: the first try and three
// retries.
const renameAttempts = 4

// Store is one cache root. Safe for concurrent use by many goroutines and by
// many processes on the same root. Every method that touches the disk returns
// a Go error only for an I/O failure the caller must report; a missing,
// torn or corrupt entry is a miss, never an error.
type Store struct {
	root string // <root>/v1
	opts Options

	put     atomic.Int64 // bytes this process wrote into the store
	dropped atomic.Int64 // writes given up (after renameAttempts, or a sidecar rolled back)
	walks   atomic.Int64 // full walks (Stats, GC); read by tests
	reads   atomic.Int64 // FileHash reads of file bytes; read by tests

	// r5 counts the sidecar removals that keep a sidecar from outliving its
	// file; read by tests and the multi-process test's report.
	r5 struct {
		rollbacks atomic.Int64 // PutFile removed its sidecar: the file was gone
		orphans   atomic.Int64 // FilePath or PutFile's standing check removed a sidecar whose file was missing
		late      atomic.Int64 // GC or Clean removed a sidecar after its file that the walk had not seen
		stuck     atomic.Int64 // a removal that failed on a sharing error after every retry
	}

	// hooks are called between the steps of PutFile, GC and Clean, so this
	// package's tests can force an interleaving of processes.
	// Nil outside tests; set before the Store is shared.
	hooks struct {
		afterObj  func() // PutFile: the file is in place, its sidecar not yet written
		afterSum  func() // PutFile: the sidecar is in place, the file not yet checked again
		afterWalk func() // GC, Clean: the walk is done, nothing removed yet
		afterSide func() // GC, Clean: a pair's sidecar removed, its file not yet
	}
}

// hook calls f if a test set it.
func hook(f func()) {
	if f != nil {
		f()
	}
}

// Open opens or creates the store under root (".mk-cache" in the package root
// by default, or MK_CACHE; the driver chooses). It creates root/v1 and its
// tmp directory, unless ReadOnly; it never deletes anything.
func Open(root string, opts Options) (*Store, error) {
	if opts.Budget <= 0 {
		opts.Budget = DefaultBudget
	}
	s := &Store{root: filepath.Join(root, "v1"), opts: opts}
	if !opts.ReadOnly {
		if err := os.MkdirAll(filepath.Join(s.root, tmpDir), 0o755); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Root returns root/v1, the directory every entry lives under.
func (s *Store) Root() string { return s.root }

// DirPath is the path Dir(name) has in the store under root, computed
// without opening it: for a caller that has no Store (one that failed to
// open) but must name the same place.
func DirPath(root, name string) string { return filepath.Join(root, "v1", name) }

// TmpDir is v1/tmp, where writes in progress are made: the store's own
// temporary files, and the work directories of the processes using the store.
// GC and Clean remove a file or a directory there whose newest file is older
// than Grace (a crashed writer); anything younger is left to its writer.
func (s *Store) TmpDir() string { return filepath.Join(s.root, tmpDir) }

// Account counts n bytes the caller placed in a Dir by itself (an
// executable renamed into BinDir) toward MaybeGC's estimate, as Put and
// PutFile count theirs, so a run of commands that each add one executable
// still collects once the store's estimate passes the budget.
func (s *Store) Account(n int64) {
	if n > 0 && !s.opts.ReadOnly {
		s.put.Add(n)
	}
}

// Mark records a use of the file p in a Dir (an executable found under
// BinDir and run again), as a Get hit marks an entry: its mtime is set to
// now when older than TouchInterval, so GC evicts it last. Best effort.
func (s *Store) Mark(p string) {
	fi, err := os.Stat(p)
	if err != nil {
		return
	}
	s.mark(p, fi.ModTime())
}

// Budget returns the eviction cap in bytes; the driver derives lld's ThinLTO
// cache policy from it (cache_size_bytes one quarter of it).
func (s *Store) Budget() int64 { return s.opts.Budget }

// Mutable reports whether entries of kind are rewritten in place
// (KindManifest only). Put, PutFile and GC use it.
func Mutable(kind Kind) bool { return kind == KindManifest }

// Dropped is the number of writes this Store gave up (each was passed to
// Options.Logf); a dropped write costs a later miss, never a wrong hit.
func (s *Store) Dropped() int64 { return s.dropped.Load() }

func (s *Store) now() time.Time {
	if s.opts.Now != nil {
		return s.opts.Now()
	}
	return time.Now()
}

// Now is the store's clock (Options.Now, or time.Now). The driver reads it
// before the os.ReadDir whose DirEntry.Info it passes to Hints.Record, as
// that call's statAt.
func (s *Store) Now() time.Time { return s.now() }

func (s *Store) logf(format string, args ...any) {
	if s.opts.Logf != nil {
		s.opts.Logf(format, args...)
	}
}

// validName is a kind or Dir name: 1 to 8 bytes of [a-z0-9], starting with a
// letter, so it can never be gc.lock, gc.stamp or a sidecar.
func validName(n string) bool {
	if len(n) == 0 || len(n) > 8 || n[0] < 'a' || n[0] > 'z' {
		return false
	}
	for i := 0; i < len(n); i++ {
		c := n[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// checkKind raises an ICE for a kind that is not a valid directory name or is
// one of the store's own directories: a caller's bug, never user input.
func checkKind(kind Kind) {
	k := string(kind)
	if !validName(k) || k == tmpDir || k == hintsDir || k == ltoDir || k == BinDir {
		source.ICEf(source.Span{}, "cache: invalid kind %q", k)
	}
}

// entryPath is v1/<kind>/<hh>/<32 hex>: the name is key.Trunc128.
func (s *Store) entryPath(kind Kind, key source.Hash) string {
	name := key.Trunc128().String()
	return filepath.Join(s.root, string(kind), name[:2], name)
}

// Get returns the payload of entry (kind, key) and true, or false on a miss.
// The full 32-byte key is checked against the entry header (a 128-bit name
// collision is a miss, and the colliding entry stays). An entry that fails
// verification is removed (best effort) and is a miss. Get marks the entry
// used. A missing file and Windows' transient sharing errors
// are a silent miss; any other I/O error is a miss returned with the error.
func (s *Store) Get(kind Kind, key source.Hash) ([]byte, bool, error) {
	checkKind(kind)
	p := s.entryPath(kind, key)
	b, mtime, err := readFile(p)
	if err != nil {
		if isMiss(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	payload, r := decodeFrame(b, kind, key)
	switch r {
	case frameCorrupt:
		if !s.opts.ReadOnly {
			os.Remove(p)
		}
		return nil, false, nil
	case frameOtherKey:
		return nil, false, nil
	}
	s.mark(p, mtime)
	return payload, true, nil
}

// Put stores payload under (kind, key) atomically: write to tmp, then rename.
// Every kind but KindManifest is content-addressed: if an entry with that
// name already exists and verifies, Put succeeds without writing, and marks
// the entry used as a Get hit does. KindManifest is mutable: Put always
// writes and renames over the existing entry, so the last rename wins. A
// rename that fails on a sharing error (Windows: another handle holds the old
// entry open) is retried up to 3 times, then dropped and logged through
// Options.Logf; a dropped write returns nil. The payload is not retained.
func (s *Store) Put(kind Kind, key source.Hash, payload []byte) error {
	checkKind(kind)
	if s.opts.ReadOnly {
		return nil
	}
	dst := s.entryPath(kind, key)
	var stands func() bool
	if !Mutable(kind) {
		stands = func() bool { _, ok := s.verifies(dst, kind, key); return ok }
		if mtime, ok := s.verifies(dst, kind, key); ok {
			// The driver re-produced this entry and is about to name it from a
			// new manifest candidate: mark it as a Get hit would, or the next
			// GC evicts it first and the next build misses again.
			if !mtime.IsZero() {
				s.mark(dst, mtime)
			}
			return nil
		}
	}
	data := encodeFrame(kind, key, payload)
	tmp, err := s.writeTmp(data)
	if err != nil {
		return err
	}
	wrote, err := s.place(tmp, dst, func() (string, error) { return s.writeTmp(data) }, stands)
	if err != nil {
		return s.drop(dst, err)
	}
	if wrote {
		s.put.Add(int64(len(data)))
	}
	return nil
}

// verifies reports whether a framed entry at p stands: it verifies for key,
// or it is a verified frame for a colliding key (kept, so the new key misses).
// mtime is the entry's mark when it verifies for key, and zero for a
// colliding key's entry, which is not this key's to mark.
func (s *Store) verifies(p string, kind Kind, key source.Hash) (mtime time.Time, ok bool) {
	b, m, err := readFile(p)
	if err != nil {
		return time.Time{}, false
	}
	_, r := decodeFrame(b, kind, key)
	switch r {
	case frameOK:
		return m, true
	case frameOtherKey:
		return time.Time{}, true
	}
	return time.Time{}, false
}

// mark sets an entry's mtime to now when its mark is older than
// TouchInterval: at most one Chtimes per entry per hour. atime
// is never used; Windows disables it by default.
func (s *Store) mark(p string, mtime time.Time) {
	if s.opts.ReadOnly {
		return
	}
	now := s.now()
	if now.Sub(mtime) > TouchInterval {
		os.Chtimes(p, now, now) // best effort: a lost mark only ages the entry
	}
}

// readFile reads p whole through one handle and returns its mtime from the
// same handle, so a hit costs one open (no memory mapping).
func readFile(p string) ([]byte, time.Time, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, time.Time{}, err
	}
	b := make([]byte, fi.Size())
	if _, err := io.ReadFull(f, b); err != nil {
		if err == io.ErrUnexpectedEOF || err == io.EOF {
			return b[:0], fi.ModTime(), nil // shorter than its stat: corrupt
		}
		return nil, time.Time{}, err
	}
	return b, fi.ModTime(), nil
}

// isMiss reports whether a read error means "no entry": the file is absent,
// or (Windows) it is being deleted or renamed over by another process.
func isMiss(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || isTransient(err)
}

// tmpPath returns a fresh name in tmp/: <pid>-<16 random hex>. The name is
// never stored anywhere, so its randomness reaches no output.
func (s *Store) tmpPath() string {
	var r [8]byte
	rand.Read(r[:])
	return filepath.Join(s.root, tmpDir, strconv.Itoa(os.Getpid())+"-"+hex.EncodeToString(r[:]))
}

// writeTmp writes data to a new file in tmp/ and returns its path. A missing
// tmp directory (removed by hand) is recreated.
func (s *Store) writeTmp(data []byte) (string, error) {
	for attempt := 0; ; attempt++ {
		p := s.tmpPath()
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			if attempt == 0 && errors.Is(err, fs.ErrNotExist) {
				if err := os.MkdirAll(filepath.Join(s.root, tmpDir), 0o755); err != nil {
					return "", err
				}
				continue
			}
			return "", err
		}
		_, werr := f.Write(data)
		cerr := f.Close()
		if werr == nil {
			werr = cerr
		}
		if werr != nil {
			os.Remove(p)
			return "", werr
		}
		return p, nil
	}
}

// droppedError is a write given up after renameAttempts.
type droppedError struct {
	dst string
	err error
}

func (e *droppedError) Error() string {
	return fmt.Sprintf("cache: write of %s dropped: %v", e.dst, e.err)
}

// place renames the finished file tmp to dst and reports whether it wrote.
// On a failed rename it recovers what it can, in this order: if dst's
// directory is missing (new, or removed by Clean) it is created; if tmp itself
// vanished (a GC whose clock runs ahead removed it) it is rewritten by
// rewrite, when there is one; if stands reports that dst already holds a
// verified entry, the write is not needed; a sharing error is retried with a
// short backoff. Anything else is returned. tmp never outlives the call.
func (s *Store) place(tmp, dst string, rewrite func() (string, error), stands func() bool) (bool, error) {
	var last error
	for attempt := 0; attempt < renameAttempts; attempt++ {
		if attempt > 0 && last != nil && isTransient(last) {
			time.Sleep(time.Millisecond << (2 * (attempt - 1))) // 1, 4, 16 ms
		}
		if tmp == "" {
			if rewrite == nil {
				break // the file to place is gone (a raw file has no second copy)
			}
			var err error
			if tmp, err = rewrite(); err != nil {
				return false, err
			}
		}
		err := os.Rename(tmp, dst)
		if err == nil {
			return true, nil
		}
		last = err
		if errors.Is(err, fs.ErrNotExist) {
			if _, serr := os.Lstat(tmp); serr != nil {
				tmp = "" // tmp is gone
				continue
			}
			if merr := os.MkdirAll(filepath.Dir(dst), 0o755); merr != nil {
				os.Remove(tmp)
				return false, merr
			}
			continue
		}
		if stands != nil && stands() {
			os.Remove(tmp)
			return false, nil
		}
		if !isTransient(err) {
			os.Remove(tmp)
			return false, err
		}
	}
	if tmp != "" {
		os.Remove(tmp)
	}
	return false, &droppedError{dst: dst, err: last}
}

// drop turns a write given up after retries into a logged nil (a lost entry
// costs a later miss, never a wrong hit) and returns any other error.
func (s *Store) drop(dst string, err error) error {
	var d *droppedError
	if errors.As(err, &d) {
		s.dropped.Add(1)
		s.logf("%v", d)
		return nil
	}
	return err
}

// Dir returns root/v1/<name>, created (unless ReadOnly), for tools that keep
// their own cache in it: "thinlto" for lld. Its size counts toward the
// budget, and GC evicts its files like entries. name is a valid kind name
// that is no entry kind of this package.
func (s *Store) Dir(name string) (string, error) {
	if !validName(name) || name == tmpDir || name == hintsDir {
		source.ICEf(source.Span{}, "cache: invalid directory name %q", name)
	}
	switch Kind(name) {
	case KindParse, KindManifest, KindIface, KindFunc, KindEval, KindGen, KindObj, KindPos, KindLib, KindLayout, KindProfile, KindFetch,
		KindCImport, KindCObj:
		source.ICEf(source.Span{}, "cache: directory name %q is an entry kind", name)
	}
	p := filepath.Join(s.root, name)
	if s.opts.ReadOnly {
		return p, nil
	}
	return p, os.MkdirAll(p, 0o755)
}
