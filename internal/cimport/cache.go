// Why: running Clang over a large header set on every build is slow, so a
// translation is cached, and the cache must notice any header change,
// including a new header that would now shadow one found before.
//
// What: Key (Clang identity, target, defines, sysroot hash, ordered search
// paths), Load and Save, Cand with Cands and PutCand (the candidate lists
// of this cache and of internal/driver's C objects), Negatives, and
// DepHasher, which hashes the -MD dependency files through a stat memo and
// answers negative lookups. The existence lookups of a unit's text are
// hasinclude.go's.
//
// How: the C object's shape. Key names a mutable list (KindManifest) of at
// most MaxCands candidates, newest first: each the dependency files with
// their content hashes and the negative lookups (Negatives), none of which
// existed when it was saved. A candidate holds while every hash matches and
// every negative is absent; the first that holds names the immutable entry
// (KindCImport) keyed by Key and its dependencies' hashes, which holds the
// bindings and the shim. So a header edit adds a candidate, and the old
// content stays a hit too.

package cimport

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"mk/internal/cache"
	"mk/internal/source"
)

// formatVersion changes whenever the mapping changes what it writes or what
// a candidate's negatives cover.
const formatVersion = "mk.cimport.v6"

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

// entry is what the store holds for a key and one candidate.
type entry struct {
	Modules []string
	Shim    string
	Stats   []Stats
}

// Cand is one candidate of a C cache's list: the files the unit read
// (-MD), with their content hashes, and its negative lookups (Negatives).
type Cand struct {
	Deps []Dep
	Neg  []string `json:",omitempty"`
}

// Dep is one file a unit read and its content hash.
type Dep struct {
	Path string
	Hash source.Hash
}

// MaxCands bounds a list's candidates; the oldest go.
const MaxCands = 4

// Cands are the candidates stored under the KindManifest key lk.
func Cands(st *cache.Store, lk source.Hash) []Cand {
	raw, ok, _ := st.Get(cache.KindManifest, lk)
	if !ok {
		return nil
	}
	var cs []Cand
	if json.Unmarshal(raw, &cs) != nil {
		return nil
	}
	return cs
}

// PutCand puts c first among lk's candidates, dropping an older one with
// the same dependencies (its negatives are older) and any beyond MaxCands.
func PutCand(st *cache.Store, lk source.Hash, c Cand) {
	cs := []Cand{c}
	for _, o := range Cands(st, lk) {
		if len(cs) < MaxCands && !slices.Equal(o.Deps, c.Deps) {
			cs = append(cs, o)
		}
	}
	if data, err := json.Marshal(cs); err == nil {
		_ = st.Put(cache.KindManifest, lk, data)
	}
}

// Holds reports whether c still describes the files: every dependency has
// its hash and no negative lookup finds a file.
func (d *DepHasher) Holds(c Cand) bool {
	for _, dep := range c.Deps {
		if h, ok := d.Hash(dep.Path); !ok || h != dep.Hash {
			return false
		}
	}
	for _, n := range c.Neg {
		if d.Exists(n) {
			return false
		}
	}
	return true
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
// ordered search path dirs, with roots the files of the unit that are not
// in deps and whose directory may gain a header (a C source; not the
// translation's unit or the shim, which mk writes to a fresh work
// directory): the paths that would shadow a dependency, or answer an
// existence lookup, and are absent now. A C object's candidate
// (internal/driver) keeps them as a translation's does. False when the
// unit reads in a way this does not model: its result is then not cached
// (a miss, never a stale hit).
//
// A dependency P has a name under each directory L holding it, rel(L, P),
// one of which is the name an #include spelled. Through search directory
// dirs[k], each dirs[j]/name with j < k comes first. A quoted include
// first searches the directory of its includer, and on the msvc target
// the directories of the whole include stack (Clang's MSVC compatibility),
// and every includer is a root or a dependency: so each such directory B
// gives B/name, whatever L was, and one rule covers both targets.
//
// A name with ".." is not a name under any directory once the path is
// clean, so deps are the paths as -MD wrote them, which keep the spelling
// (dir/../x.h): the part before the first ".." is the directory it was
// found through or one below it, and the rest of the name follows; or the
// path begins with a search directory as that is spelled (a sysroot given
// as a/../sys). A spelling that no search or includer directory begins is
// not modelled.
//
// __has_include and its kin (probes) leave no trace in -MD when the file
// is absent, so every file of the unit is read for them, with texts, what
// the compile reads that no file of deps or roots holds (the unit mk
// writes, its -D definitions): each name is a negative under every
// directory its lookup could search (a found one is a dependency, which a
// later lookup in a header may not shadow unnoticed: its directory is
// searched too), and an absolute name is its own.
func Negatives(deps, dirs, roots []string, texts ...string) ([]string, bool) {
	var out []string
	// A path the unit read is no negative: #include_next reads a header
	// under a later directory after the one under an earlier (Clang's
	// stdint.h, then the sysroot's), and its hash covers it.
	seen := map[string]bool{}
	for _, d := range deps {
		seen[filepath.Clean(d)] = true
	}
	var bases []string
	hasBase := map[string]bool{}
	for _, f := range append(slices.Clone(roots), deps...) {
		if b := filepath.Dir(filepath.Clean(f)); !hasBase[b] {
			hasBase[b] = true
			bases = append(bases, b)
		}
	}
	add := func(dir, name string) {
		p := filepath.Clean(filepath.Join(dir, name))
		if seen[p] {
			return
		}
		seen[p] = true
		// A path that exists now was not picked by the unit (an #include
		// with <> does not search beside its includer, or one in an
		// inactive #if): it shadows nothing, and keeping it would make the
		// entry miss on every build.
		if _, err := os.Stat(p); err != nil {
			out = append(out, p)
		}
	}
	// through adds name's negatives for a file found through dirs[k] (k
	// -1: beside an includer): each earlier directory and each includer's.
	through := func(k int, name string) {
		if k > 0 {
			for _, earlier := range dirs[:k] {
				add(earlier, name)
			}
		}
		for _, b := range bases {
			add(b, name)
		}
	}
	for _, d := range deps {
		ad := filepath.Clean(d)
		// -MD does not say which directory found a file. Where directories
		// nest (include_dirs "inc" and "inc/sub"), inc/sub/y.h is
		// "sub/y.h" through inc or "y.h" through inc/sub, so every
		// directory holding it counts, each with its own name for it.
		for k, dir := range dirs {
			if rel, ok := nameUnder(dir, ad); ok {
				through(k, rel)
			}
		}
		for _, l := range bases {
			if rel, ok := nameUnder(l, ad); ok {
				through(-1, rel)
			}
		}
		if prefix, rest, ok := dotDot(d); ok {
			found := false
			for k, dir := range dirs {
				// A directory spelled with ".." itself (a sysroot given as
				// a/../sys) begins the path as spelled.
				raw := strings.TrimRight(filepath.FromSlash(dir), string(filepath.Separator))
				if name, ok := strings.CutPrefix(filepath.FromSlash(d), raw+string(filepath.Separator)); ok && name != "" {
					through(k, name)
					found = true
				}
				if rel, ok := dirName(dir, prefix); ok {
					through(k, rel+rest)
					found = true
				}
			}
			for _, l := range bases {
				if rel, ok := dirName(l, prefix); ok {
					through(-1, rel+rest)
					found = true
				}
			}
			if !found {
				return nil, false
			}
		}
	}
	texts = slices.Clone(texts)
	for _, f := range append(slices.Clone(roots), deps...) {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, false
		}
		texts = append(texts, string(b))
	}
	ps, ok := probes(texts)
	if !ok {
		return nil, false
	}
	for _, p := range ps {
		// Every directory, the includers' too, whatever the form: an _next
		// lookup searches after its file's directory, an angled one skips
		// the includers, so this is more than each searches, which only
		// adds a miss when such a file appears.
		switch {
		case filepath.IsAbs(p.name):
			add("", p.name) // the path itself, wherever the lookup is
		case filepath.VolumeName(p.name) != "" || os.IsPathSeparator(p.name[0]):
			// A rooted (\x.h) or drive-relative (C:x.h) Windows name: not
			// under any directory, and not one path either.
			return nil, false
		default:
			through(len(dirs), p.name)
		}
	}
	return out, true
}

// dotDot splits a path as -MD wrote it at its first ".." element: the
// clean directory before it and the rest from it, with a leading separator
// ("/a/inc/../x.h" is "/a/inc" and "/../x.h"); false without one.
func dotDot(p string) (string, string, bool) {
	p = filepath.FromSlash(p)
	sep := string(filepath.Separator)
	parts := strings.Split(p, sep)
	for i, e := range parts {
		if e == ".." && i > 0 {
			return filepath.Clean(strings.Join(parts[:i], sep) + sep), sep + strings.Join(parts[i:], sep), true
		}
	}
	return "", "", false
}

// dirName is the spelling's start under dir of a path whose part before
// its first ".." is the clean directory prefix: "" when prefix is dir, or
// its name under dir; false when dir does not hold it.
func dirName(dir, prefix string) (string, bool) {
	if filepath.Clean(dir) == prefix {
		return "", true
	}
	if rel, ok := nameUnder(dir, prefix); ok {
		return rel, true
	}
	return "", false
}

// nameUnder is p's name under dir, false when p is not below it.
func nameUnder(dir, p string) (string, bool) {
	rel, err := filepath.Rel(filepath.Clean(dir), p)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

// candsKey is the KindManifest key of key's candidates.
func candsKey(key source.Hash) source.Hash {
	h := source.NewHasher("mk.cimport.cands.v1")
	h.Hash(key)
	return h.Sum()
}

// entryKey is the KindCImport key of key's translation with c's files:
// the bindings are a function of the request and the headers' contents.
func entryKey(key source.Hash, c Cand) source.Hash {
	h := source.NewHasher("mk.cimport.entry.v1")
	h.Hash(key)
	h.Uvarint(uint64(len(c.Deps)))
	for _, d := range c.Deps {
		h.String(d.Path)
		h.Hash(d.Hash)
	}
	return h.Sum()
}

// Load is the stored result of key, from the first of its candidates that
// still holds.
func Load(st *cache.Store, key source.Hash) (*Result, bool) {
	if st == nil {
		return nil, false
	}
	dh := NewDepHasher(st)
	defer dh.Save()
	for _, c := range Cands(st, candsKey(key)) {
		if !dh.Holds(c) {
			continue
		}
		data, ok, err := st.Get(cache.KindCImport, entryKey(key, c))
		var e entry
		if err != nil || !ok || json.Unmarshal(data, &e) != nil {
			continue // collected, or unreadable: an older candidate may hold
		}
		res := &Result{Modules: e.Modules, Shim: e.Shim, Stats: e.Stats}
		for _, d := range c.Deps {
			res.Deps = append(res.Deps, d.Path)
		}
		return res, true
	}
	return nil, false
}

// Save stores res under key: the entry, then the candidate naming it, so a
// list never names an entry not yet written. A file that cannot be read is
// no entry (the next run translates again).
func Save(st *cache.Store, key source.Hash, res *Result) {
	if st == nil {
		return
	}
	neg, ok := Negatives(res.Deps, res.SearchDirs, nil, res.Unit)
	if !ok {
		return // it read in a way a candidate cannot describe
	}
	c := Cand{Neg: neg}
	dh := NewDepHasher(st)
	defer dh.Save()
	for _, d := range res.Deps {
		h, ok := dh.Hash(d)
		if !ok {
			return
		}
		c.Deps = append(c.Deps, Dep{Path: d, Hash: h})
	}
	data, err := json.Marshal(entry{Modules: res.Modules, Shim: res.Shim, Stats: res.Stats})
	if err != nil {
		return
	}
	if st.Put(cache.KindCImport, entryKey(key, c), data) == nil {
		PutCand(st, candsKey(key), c)
	}
}
