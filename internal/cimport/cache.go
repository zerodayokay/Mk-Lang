// Why: running Clang over a large header set on every build is slow, so a
// translation is cached, and the cache must notice any header change,
// including a new header that would now shadow one found before.
//
// What: Key (Clang identity, target, defines, sysroot hash, ordered search
// paths), the cached entry with Load and Save, Negatives, and DepHasher,
// which hashes the -MD dependency files through a stat memo and answers
// negative lookups.
//
// How: an entry holds the bindings, the shim, the dependency files with
// their content hashes, and the negative lookups: for each dependency under
// search directory k, the same relative path under every directory before
// k, none of which existed when the entry was saved. A hit needs every hash
// to match and every negative to be absent.

package cimport

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mk/internal/cache"
	"mk/internal/source"
)

// formatVersion changes whenever the mapping changes what it writes.
const formatVersion = "mk.cimport.v3"

// Key is the cache key of r translated by the Clang of identity: every
// input but the header contents, which the entry checks itself.
func Key(identity source.Hash, resourceDir string, r Request) source.Hash {
	h := source.NewHasher(formatVersion)
	h.Hash(identity)
	h.String(r.Target)
	h.String(r.Triple)
	h.String(resourceDir)
	h.Uvarint(uint64(len(r.Imports)))
	for _, im := range r.Imports {
		h.String(im.Name)
		for _, l := range [][]string{im.Headers, im.Defines, im.Macros} {
			h.Uvarint(uint64(len(l)))
			for _, s := range l {
				h.String(s)
			}
		}
	}
	for _, l := range [][]string{r.Include, r.Sysroot} {
		h.Uvarint(uint64(len(l)))
		for _, s := range l {
			h.String(s)
		}
	}
	return h.Sum()
}

// entry is what the store holds for a key.
type entry struct {
	Modules []string
	Shim    string
	Stats   []Stats
	Deps    []depHash
	Neg     []string
}

type depHash struct {
	Path string
	Hash source.Hash
}

// fileHash is the content hash of a dependency file.
func fileHash(p string) (source.Hash, bool) {
	b, err := os.ReadFile(p)
	if err != nil {
		return source.Hash{}, false
	}
	return source.HashBytes(depDomain, b), true
}

const depDomain = "mk.cimport.dep.v1"

// DepHasher hashes the files a C translation or a C object read (-MD) for
// a cache check, by content, through a stat memo (cache.Hints, the
// store's size and mtime hint, never part of a key): a file whose size,
// mtime and change identity are those recorded, and not racy, is not read
// again. So a warm check of a package importing windows.h stats its
// headers and reads none. One goroutine uses a DepHasher.
type DepHasher struct {
	st     *cache.Store
	hints  *cache.Hints
	exists map[string]bool // Exists's answers
	reads  int             // files read and hashed, for tests
}

// NewDepHasher is a DepHasher over st's memo; with a nil st it reads every
// file.
func NewDepHasher(st *cache.Store) *DepHasher {
	d := &DepHasher{st: st}
	if st != nil {
		// One memo for every package: dependency paths are absolute.
		d.hints = st.LoadHints(filepath.Join(st.Root(), "cimport-deps"))
	}
	return d
}

// Hash is the content hash of the file at p (absolute), false when it
// cannot be read.
func (d *DepHasher) Hash(p string) (source.Hash, bool) {
	var fi os.FileInfo
	var statAt time.Time
	if d.hints != nil {
		statAt = d.st.Now() // before the stat: the racy rule's anchor
		var err error
		if fi, err = os.Stat(p); err != nil {
			return source.Hash{}, false
		}
		if h, ok := d.hints.Lookup(p, fi); ok {
			return h, true
		}
	}
	h, ok := fileHash(p)
	if !ok {
		return source.Hash{}, false
	}
	d.reads++
	if d.hints != nil {
		d.hints.Record(p, fi, statAt, h)
	}
	return h, true
}

// Exists reports whether a file is at p, a negative lookup, statting each
// path once per DepHasher: a package's C objects share most of theirs. A
// DepHasher lasts one build's step (a Load, a Save, one package's C
// objects), so a file added later is seen by the next build.
func (d *DepHasher) Exists(p string) bool {
	if e, ok := d.exists[p]; ok {
		return e
	}
	_, err := os.Stat(p)
	if d.exists == nil {
		d.exists = map[string]bool{}
	}
	d.exists[p] = err == nil
	return err == nil
}

// Save writes the memo; a failure costs a rehash next time.
func (d *DepHasher) Save() {
	if d.hints != nil {
		_ = d.hints.Save()
	}
}

// Negatives are the negative lookups of a unit that read deps through the
// ordered search path dirs: the paths that would shadow a dependency and
// are absent now. For a file under dirs[k] as rel, each dirs[j]/rel with
// j < k. A C object's cache entry (internal/driver) keeps them as a
// translation's does.
func Negatives(deps, dirs []string) []string {
	var out []string
	// A path the unit read is no negative: #include_next reads a header
	// under a later directory after the one under an earlier (Clang's
	// stdint.h, then the sysroot's), and its hash covers it.
	seen := map[string]bool{}
	for _, d := range deps {
		seen[filepath.Clean(d)] = true
	}
	for _, d := range deps {
		ad := filepath.Clean(d)
		// -MD does not say which directory found a file. Where directories
		// nest (include_dirs "inc" and "inc/sub"), inc/sub/y.h is
		// "sub/y.h" through inc or "y.h" through inc/sub, so every
		// directory holding it counts, each with its own name for it.
		for k, dir := range dirs {
			rel, err := filepath.Rel(filepath.Clean(dir), ad)
			if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			for _, earlier := range dirs[:k] {
				p := filepath.Clean(filepath.Join(earlier, rel))
				if seen[p] {
					continue
				}
				seen[p] = true
				// A path that exists now was not picked by the unit (a quoted
				// include found beside its includer first, which this does not
				// model): it shadows nothing, and keeping it would make the
				// entry miss on every build.
				if _, err := os.Stat(p); err != nil {
					out = append(out, p)
				}
			}
		}
	}
	return out
}

// Load is the stored result of key, when every dependency still has its
// hash and no negative lookup finds a file.
func Load(st *cache.Store, key source.Hash) (*Result, bool) {
	if st == nil {
		return nil, false
	}
	data, ok, err := st.Get(cache.KindCImport, key)
	if err != nil || !ok {
		return nil, false
	}
	var e entry
	if json.Unmarshal(data, &e) != nil {
		return nil, false
	}
	dh := NewDepHasher(st)
	defer dh.Save()
	for _, d := range e.Deps {
		h, ok := dh.Hash(d.Path)
		if !ok || h != d.Hash {
			return nil, false
		}
	}
	for _, n := range e.Neg {
		if _, err := os.Stat(n); err == nil {
			return nil, false
		}
	}
	res := &Result{Modules: e.Modules, Shim: e.Shim, Stats: e.Stats}
	for _, d := range e.Deps {
		res.Deps = append(res.Deps, d.Path)
	}
	return res, true
}

// Save stores res under key with its dependencies' hashes and negative
// lookups. A file that cannot be read is no entry (the next run
// translates again).
func Save(st *cache.Store, key source.Hash, res *Result) {
	if st == nil {
		return
	}
	e := entry{Modules: res.Modules, Shim: res.Shim, Stats: res.Stats, Neg: Negatives(res.Deps, res.SearchDirs)}
	dh := NewDepHasher(st)
	defer dh.Save()
	for _, d := range res.Deps {
		h, ok := dh.Hash(d)
		if !ok {
			return
		}
		e.Deps = append(e.Deps, depHash{Path: d, Hash: h})
	}
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	_ = st.Put(cache.KindCImport, key, data)
}
