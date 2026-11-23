// Why: the native half of a build (objects, import libraries, ThinLTO) is
// the slow part, so it must be cached under keys that change exactly when
// its output would.
//
// What: the build ID (storeState, openStore), the object and import-library
// keys (objKey, libKey), MK_CACHE_VERIFY's raw-file comparison (verifyFile),
// the ThinLTO directory and its budget (ltoDir), and MaybeGC once per
// command (maybeGC), over internal/cache; --no-cache turns it all off.
//
// How: an object's key starts with the build ID, the hash of the running
// compiler's own bytes, so two compilers never share an object, and adds
// the toolchain identity, the target, the options and the pack's IR, so
// equal keys mean equal objects. An import library's key is the target,
// the toolchain identity, the DLL and its name list.

package driver

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"mk/internal/cache"
	"mk/internal/diag"
	"mk/internal/iface"
	"mk/internal/source"
	"mk/internal/toolchain"
)

// storeState is a world's cache store, opened at most once, on the first
// native need. st is nil with --no-cache, when the store cannot be opened,
// or when the compiler's own bytes cannot be hashed: the cache is an
// optimization, and a build with no build ID must not share entries with
// another compiler.
type storeState struct {
	once   sync.Once
	st     *cache.Store
	idDone chan struct{}
	id     source.Hash // the build ID, ready once idDone is closed (or nil)
}

// openStore opens the store under dir once per world and computes the
// build ID: Config.BuildID when set (tests, TestInvalidation_CompilerVersion),
// else iface.BuildIDFrom over cache.Store.FileHash of the running
// executable on a guarded background goroutine so hashing the compiler binary
// overlaps StageManifest/StageLoad/StageParse on cold caches.
// dir "" means --no-cache.
func (w *world) openStore(dir string) *cache.Store {
	if dir == "" || w.cfg.NoCache {
		return nil
	}
	w.store.once.Do(func() {
		st, err := cache.Open(dir, cache.Options{Budget: w.cfg.CacheBudget, Now: w.cfg.TestHooks.CacheNow})
		if err != nil {
			return
		}
		w.store.st = st
		if !w.cfg.BuildID.IsZero() {
			w.store.id = w.cfg.BuildID
			return
		}
		done := make(chan struct{})
		w.store.idDone = done
		go func() {
			defer close(done)
			_ = source.Guard("build id", source.Span{}, func() {
				exe, err := os.Executable()
				if err != nil {
					return
				}
				if h, err := st.FileHash(exe); err == nil {
					w.store.id = iface.BuildIDFrom(h)
				}
			})
		}()
	})
	return w.store.st
}

// buildID is the world's build ID; zero until openStore succeeded (or if
// hashing the executable failed).
func (w *world) buildID() source.Hash {
	if w.store.idDone != nil {
		<-w.store.idDone
	}
	return w.store.id
}

// objKey is the key of one pack's object: the build ID, the toolchain
// identity, the triple, the CPU, the -O level, -flto, and the pack's IR. The
// IR is everything codegen decided for the pack: its instances, their
// content and their rows (positions), so equal keys mean equal objects, and
// an object is clang's function of its .ll and these options. The store
// stays keyed so; a pack whose pre-emission key (prekey.go) maps to a stored
// object key is not generated at all.
func objKey(build source.Hash, tools *toolchain.Tools, j toolchain.CompileJob, ll []byte) source.Hash {
	h := source.NewHasher("mk.obj.v1")
	h.Hash(build)
	h.Hash(tools.Identity())
	h.String(j.Triple)
	h.String(j.CPU)
	h.Uvarint(uint64(j.Opt))
	h.Bool(j.LTO)
	if k := j.CompileKey(); k != "" {
		h.String(k) // only an experiment's fatlto or unroll=<N> objects
	}
	h.Bytes(ll)
	return h.Sum()
}

// libKey is the key of one import library: iface.LibKey over
// the target and the hash of the toolchain identity, the DLL name and its
// name list.
func libKey(tools *toolchain.Tools, target string, l importLib) source.Hash {
	h := source.NewHasher("mk.lib.v1")
	h.Hash(tools.Identity())
	h.String(l.dll)
	if l.file != "" {
		// A named link's exact file name; std's and the system
		// DLLs' keys are as they were before named links existed.
		h.String("file " + l.file)
	}
	h.Uvarint(uint64(len(l.names)))
	for _, n := range l.names {
		h.String(n)
	}
	return iface.LibKey(target, h.Sum())
}

// storeFile enters the finished file at path into the store under (kind,
// key) and returns the path the linker should read: path itself, whether
// the store took the file or refused it (the cache is an optimization). The
// store takes a hard link (a copy across volumes), so path stays valid for
// this build whatever another process or GC does to the entry.
func storeFile(st *cache.Store, kind cache.Kind, key source.Hash, path string) string {
	tmp := path + ".put"
	if err := os.Link(path, tmp); err != nil {
		if err := copyFile(path, tmp); err != nil {
			return path
		}
	}
	if err := st.PutFile(kind, key, tmp); err != nil {
		os.Remove(tmp)
	}
	return path
}

// verifyFile is MK_CACHE_VERIFY's comparison of a stored raw-file entry with
// the file just recomputed: nil when the bytes are equal, else E0000 with
// where "cache verify" and what "<kind> <key>". The build goes on with the
// recomputed file, and the stale entry is replaced by it (RemoveFile, then
// storeFile), so the next build does not link it.
func verifyFile(st *cache.Store, kind cache.Kind, key source.Hash, stored, fresh string) *diag.Diagnostic {
	a, aerr := os.ReadFile(stored)
	b, berr := os.ReadFile(fresh)
	if aerr == nil && berr == nil && bytes.Equal(a, b) {
		return nil
	}
	if st.RemoveFile(kind, key) == nil {
		storeFile(st, kind, key, fresh)
	}
	return diag.FromInternal(&source.InternalError{Where: "cache verify",
		What: fmt.Sprintf("%s %s: the stored entry differs from the recomputed one", kind, key)})
}

// copyFile copies src to dst.
func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

// ltoDir is the ThinLTO cache of a release link:
// Store.Dir("thinlto"), pruned by the linker to a quarter of the budget,
// entries older than 48 h removed. "" with no store.
func ltoDir(st *cache.Store) (string, int64) {
	if st == nil {
		return "", 0
	}
	d, err := st.Dir("thinlto")
	if err != nil {
		return "", 0
	}
	if abs, err := filepath.Abs(d); err == nil {
		d = abs
	}
	return d, st.Budget() / 4
}

// packLog is the native part of the work log (Trace.PacksCompiled,
// PacksFromCache, Linked), written by the pack workers of every bin.
type packLog struct {
	mu        sync.Mutex
	compiled  []string
	fromCache []string
	// emitted are the packs whose IR codegen generated (Trace.PacksEmitted).
	emitted []string
	linked  bool
	// verify holds MK_CACHE_VERIFY's mismatches, reported with the build's
	// diagnostics while the build goes on with the recomputed files.
	verify []*diag.Diagnostic
	// stale holds the functions a --pgo build found no usable profile data
	// for (codegen.Report.Stale of every pack), printed under --timings.
	stale []string
}

// noteStale records a pack's PGO-stale functions.
func (l *packLog) noteStale(names []string) {
	if len(names) == 0 {
		return
	}
	l.mu.Lock()
	l.stale = append(l.stale, names...)
	l.mu.Unlock()
}

// takeStale returns the recorded stale functions sorted and unique, and
// clears them.
func (l *packLog) takeStale() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := l.stale
	l.stale = nil
	slices.Sort(out)
	return slices.Compact(out)
}

// verified records a verification result; nil (equal bytes) is dropped.
func (l *packLog) verified(d *diag.Diagnostic) {
	if d == nil {
		return
	}
	l.mu.Lock()
	l.verify = append(l.verify, d)
	l.mu.Unlock()
}

// takeVerify returns and clears the recorded mismatches.
func (l *packLog) takeVerify() []*diag.Diagnostic {
	l.mu.Lock()
	defer l.mu.Unlock()
	ds := l.verify
	l.verify = nil
	return ds
}

func (l *packLog) note(name string, hit bool) {
	l.mu.Lock()
	if hit {
		l.fromCache = append(l.fromCache, name)
	} else {
		l.compiled = append(l.compiled, name)
	}
	l.mu.Unlock()
}

// noteAll notes the n packs of a bin (the n-1 packs and the support pack)
// with hit: a link-key hit compiles none of them.
func (l *packLog) noteAll(n int, hit bool) {
	for i := range n {
		l.note(packFile(i, n-1), hit)
	}
}

func (l *packLog) noteLink() {
	l.mu.Lock()
	l.linked = true
	l.mu.Unlock()
}

// into copies the log into a Trace (sorted by Trace.sort).
func (l *packLog) into(t *Trace) {
	l.mu.Lock()
	defer l.mu.Unlock()
	t.PacksCompiled = append(t.PacksCompiled, l.compiled...)
	t.PacksFromCache = append(t.PacksFromCache, l.fromCache...)
	t.PacksEmitted = append(t.PacksEmitted, l.emitted...)
	t.Linked = t.Linked || l.linked
}

// maybeGC runs once at command end, after the output is written
// and every child has exited, with --no-cache never. An error is ignored:
// the cache is an optimization.
func (w *world) maybeGC() {
	if w != nil && w.store.st != nil {
		if w.store.idDone != nil {
			<-w.store.idDone
		}
		_ = w.store.st.MaybeGC()
	}
}
