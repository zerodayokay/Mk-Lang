// Why: a warm `mk build` with no source or configuration changes still ran the
// manifest, load, parse-summary, resolve, front-cache lookup, mono.Collect,
// pack-key and link-key stages (~130-150 ms on a 50-module package) before
// discovering that the stored executable in <cache>/bin already matched.
//
// What: Session.tryFastBuild and Session.recordFastBuild, a sound whole-build
// fingerprint fast path keyed by compiler build ID, toolchain identity, root,
// and every build-affecting Config flag.
//
// How: after a clean cacheable Build finishes, recordFastBuild stores a
// KindManifest record naming:
//   - every tracked directory and a digest of its .mk files and non-skipped
//     subdirectories (so any added, removed, or renamed file/directory misses);
//   - every tracked input file and its SHA-256 content hash (package files,
//     pkg.mk, any MK_STD files, extra objects, PGO profile);
//   - any paths whose absence was relied upon (e.g. default.mkprof);
//   - the stored <cache>/bin executable (and PDB if --debug) paths, sizes,
//     and SHA-256 hashes, plus trace/note metadata.
// On a subsequent warm Build, tryFastBuild verifies all tracked directories
// and files in parallel (using cache.Hints with the 2s racy-stat rule, falling
// back to reading & SHA-256 hashing any racy or unhinted file), checks that
// the stored executable exists, and either verifies the existing output file
// already holds the exact bytes (avoiding an unnecessary rewrite) or copies
// the stored executable to the requested output path. Any mismatch or error
// falls back silently to the full build pipeline.

package driver

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mk/internal/cache"
	"mk/internal/iface"
	"mk/internal/source"
	"mk/internal/toolchain"
	"mk/internal/types"
)

const (
	fastBuildVersion   uint64 = 1
	fastBuildKeyDomain        = "mk.driver.fastbuild.key.v1"
	fastBuildDirDomain        = "mk.driver.fastbuild.dir.v1"
	fastBuildFileDom          = "mk.driver.fastbuild.file.v1"
)

// fastBinOut receives the cache/bin paths written or reused by world.native,
// plus optional precomputed SHA-256 hashes from staging copyOutHash so
// recordFastBuild avoids re-reading the freshly linked executable.
type fastBinOut struct {
	name      string
	storedExe string
	exeSize   int64
	exeHash   source.Hash
	storedPDB string
	pdbSize   int64
	pdbHash   source.Hash
}

// copyOutHash copies src to dst (like copyOut) while computing src's SHA-256
// and byte length in a single streaming pass.
func copyOutHash(src, dst string) (source.Hash, int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return source.Hash{}, 0, err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return source.Hash{}, 0, err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fi.Mode().Perm())
	if err != nil {
		return source.Hash{}, 0, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dst)
		return source.Hash{}, 0, err
	}
	var sum source.Hash
	copy(sum[:], h.Sum(nil))
	return sum, n, nil
}

type fastDirEntry struct {
	path      string // absolute OS path
	recursive bool   // true: track non-skipped subdirs + .mk files; false: track .mk files directly in dir
	skipDirs  []string
	sig       source.Hash
}

type fastFileEntry struct {
	path string // absolute OS path
	hash source.Hash
}

type fastBinEntry struct {
	name      string
	storedExe string
	exeSize   int64
	exeHash   source.Hash
	storedPDB string
	pdbSize   int64
	pdbHash   source.Hash
}

type fastBuildRec struct {
	dirs    []fastDirEntry
	files   []fastFileEntry
	absent  []string
	bins    []fastBinEntry
	packs   []string
	modules []string
	notes   []string
}

// fastBuildEligible reports whether this Session.Build may use the whole-build
// fingerprint fast path. Any option or test hook that alters build behavior or
// observes internal stages bypasses the fast path cleanly.
func (s *Session) fastBuildEligible() bool {
	c := &s.cfg
	if c.NoCache || c.CacheVerify || !c.Target.Built {
		return false
	}
	if s.root.kind == rootNone || s.testing || s.prop || s.fullStd {
		return false
	}
	if len(c.Overlay) > 0 || c.StdDir != "" || c.StdTests != "" || c.KeepSyntax || c.KeepSyntaxFor != nil || c.BrowseStd || c.Cover {
		return false
	}
	if s.customReadFile {
		return false
	}
	h := &c.TestHooks
	if h.StoreWritesDelay != 0 || h.PanicIn != "" || h.UntypeConst != "" || h.MaxFileSize != 0 ||
		h.ICEAt != "" || h.WrapFS != nil || h.OnStage != nil || h.CheckPastNames ||
		h.SlowFunc != 0 || h.OnLL != nil || h.OnPack != nil || h.LLDir != "" ||
		h.CImportWhole || h.PlaceObject != nil || h.EmitMissObjects {
		return false
	}
	return true
}

// fastBuildID returns the compiler build ID for st without opening a world.
func (s *Session) fastBuildID(st *cache.Store) (source.Hash, bool) {
	if !s.cfg.BuildID.IsZero() {
		return s.cfg.BuildID, true
	}
	exe, err := os.Executable()
	if err != nil {
		return source.Hash{}, false
	}
	h, err := st.FileHash(exe)
	if err != nil {
		return source.Hash{}, false
	}
	return iface.BuildIDFrom(h), true
}

// fastBuildKey hashes every build-affecting configuration field plus build ID
// and toolchain identity. Output path (-o) is intentionally excluded so a warm
// build to a new output path still hits and copies from <cache>/bin.
func (s *Session) fastBuildKey(buildID source.Hash, tools *toolchain.Tools, resolvedCPU string) source.Hash {
	c := &s.cfg
	h := source.NewHasher(fastBuildKeyDomain)
	h.Uvarint(fastBuildVersion)
	h.Hash(buildID)
	h.Hash(tools.Identity())
	h.Uvarint(uint64(s.root.kind))
	h.String(s.root.dir)
	h.String(s.root.file)
	h.String(c.Target.Name)
	h.Uvarint(uint64(c.Profile))
	h.String(resolvedCPU)
	h.String(c.Bin)
	h.Bool(c.Debug)
	h.Bool(c.Size)
	h.Bool(c.Symbols)
	h.Bool(c.FramePointers)
	h.Bool(c.GenView)
	h.Uvarint(uint64(c.PGO))
	pgoFile := c.PGOFile
	if pgoFile != "" {
		if abs, err := filepath.Abs(pgoFile); err == nil {
			pgoFile = abs
		}
	}
	h.String(pgoFile)
	h.String(c.StdDir)
	h.String(c.Tune.String())
	if c.Target.OS == types.OSMacos {
		ld := tools.LD64()
		h.String(ld)
		var sz int64 = -1
		if fi, err := os.Stat(ld); err == nil {
			sz = fi.Size()
		}
		h.Varint(sz)
	}
	h.Bool(c.TestHooks.NoBoundsChecks)
	h.Bool(c.TestHooks.NoOverflowChecks)
	h.Bool(c.TestHooks.NoTrapForm)
	h.Bool(c.Offline)
	h.Bool(c.Locked)
	h.Uvarint(uint64(len(c.ExtraObjects)))
	for _, o := range c.ExtraObjects {
		h.String(o)
	}
	return h.Sum()
}

// fastResolveCPU returns the CPU name used by the build, resolving "native"
// through the store-backed cache when needed.
func (s *Session) fastResolveCPU(tools *toolchain.Tools, st *cache.Store) (string, bool) {
	if s.cfg.CPU != toolchain.CPUNative {
		return s.cfg.CPU, true
	}
	w := newWorld(s.cfg)
	cpu, d := w.buildCPU(context.Background(), tools, st, s.cfg.Target.LLVMTriple)
	if d != nil {
		return "", false
	}
	return cpu, true
}

// tryFastBuild attempts to serve Session.Build directly from the cached
// whole-build fingerprint record. Returns ok=false on any miss or error so the
// caller falls back transparently to the full build pipeline.
func (s *Session) tryFastBuild(out string) (Result, []string, bool) {
	if !s.fastBuildEligible() {
		return Result{}, nil, false
	}
	start := time.Now()
	// On a cold cache (no manifest directory yet), skip immediately without
	// blocking on toolchain discovery so clang --version overlaps runCheck.
	cacheDir := s.cacheDir()
	if _, err := os.Stat(filepath.Join(cacheDir, "v1", string(cache.KindManifest))); err != nil {
		return Result{}, nil, false
	}
	st, err := cache.Open(cacheDir, cache.Options{Budget: s.cfg.CacheBudget, Now: s.cfg.TestHooks.CacheNow})
	if err != nil {
		return Result{}, nil, false
	}
	s.preStore = st
	buildID, ok := s.fastBuildID(st)
	if !ok || buildID.IsZero() {
		return Result{}, nil, false
	}
	s.preBuildID = buildID
	tools, d := s.tools()
	if d != nil {
		return Result{}, nil, false
	}
	cpu, ok := s.fastResolveCPU(tools, st)
	if !ok {
		return Result{}, nil, false
	}
	key := s.fastBuildKey(buildID, tools, cpu)
	raw, found, _ := st.Get(cache.KindManifest, key)
	if !found {
		return Result{}, nil, false
	}
	rec, err := decodeFastBuildRec(raw)
	if err != nil || len(rec.bins) == 0 {
		return Result{}, nil, false
	}

	// Verify all absent paths are still absent.
	for _, p := range rec.absent {
		if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
			return Result{}, nil, false
		}
	}

	// Verify every tracked directory and file in parallel using stat hints
	// with a sound fallback to full content SHA-256 hashing.
	if !s.verifyFastInputs(st, rec) {
		return Result{}, nil, false
	}

	// Verify all stored executables (and PDBs) still exist and place them.
	paths := make([]string, 0, len(rec.bins))
	for _, b := range rec.bins {
		if !isRegular(b.storedExe) {
			return Result{}, nil, false
		}
		if b.storedPDB != "" && !isRegular(b.storedPDB) {
			return Result{}, nil, false
		}
	}
	for _, b := range rec.bins {
		markExe(st, b.storedExe)
		if b.storedPDB != "" {
			markExe(st, b.storedPDB)
		}
		dest := out
		if dest == "" || len(rec.bins) > 1 || outIsDir(out) {
			dest = filepath.Join(out, b.name+exeSuffix(s.cfg.Target))
		}
		abs, err := filepath.Abs(dest)
		if err != nil {
			return Result{}, nil, false
		}
		if !fastOutputUpToDate(abs, b.storedExe, b.exeSize, b.exeHash, b.storedPDB, b.pdbSize, b.pdbHash) {
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				return Result{}, nil, false
			}
			if !copyStoredTo(b.storedExe, abs) {
				return Result{}, nil, false
			}
			if b.storedPDB != "" {
				destPDB := filepath.Join(filepath.Dir(abs), filepath.Base(b.storedPDB))
				if !copyStoredTo(b.storedPDB, destPDB) {
					return Result{}, nil, false
				}
			}
		}
		paths = append(paths, abs)
	}

	_ = st.MaybeGC()
	tr := &Trace{
		PacksFromCache:   slices.Clone(rec.packs),
		ModulesFromCache: slices.Clone(rec.modules),
	}
	tr.sort()
	res := Result{
		Sources: source.NewSourceMap(),
		Reached: StageNative,
		Trace:   tr,
		Notes:   slices.Clone(rec.notes),
	}
	if s.w == nil {
		// Callers/tests may inspect s.w.probeRuns / s.w.cpuRuns after Build.
		s.w = newWorld(s.cfg)
	}
	if s.cfg.Timings != nil {
		res.Timings = []StageTime{{Stage: "cache", Took: time.Since(start)}}
	}
	return res, paths, true
}

// fastOutputUpToDate reports whether abs already holds the exact bytes of
// storedExe (and its companion PDB if any) without being the same file inode
// as the store entry.
func fastOutputUpToDate(abs, storedExe string, exeSize int64, exeHash source.Hash, storedPDB string, pdbSize int64, pdbHash source.Hash) bool {
	if !fastFileMatches(abs, storedExe, exeSize, exeHash) {
		return false
	}
	if storedPDB != "" {
		destPDB := filepath.Join(filepath.Dir(abs), filepath.Base(storedPDB))
		if !fastFileMatches(destPDB, storedPDB, pdbSize, pdbHash) {
			return false
		}
	}
	return true
}

func fastFileMatches(dst, stored string, wantSize int64, wantHash source.Hash) bool {
	fi, err := os.Stat(dst)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() != wantSize {
		return false
	}
	sfi, err := os.Stat(stored)
	if err != nil || os.SameFile(fi, sfi) || fi.Mode().Perm() != sfi.Mode().Perm() {
		return false
	}
	h, _, err := hashFileSHA256(dst)
	return err == nil && h == wantHash
}

// copyStoredTo copies stored into a unique temp file beside dst, preserves
// stored's permission bits (including +x on POSIX), and moves it into place,
// never sharing an inode with the store entry.
func copyStoredTo(stored, dst string) bool {
	sfi, err := os.Stat(stored)
	if err != nil {
		return false
	}
	tf, err := os.CreateTemp(filepath.Dir(dst), ".mk-fast-*.tmp")
	if err != nil {
		return false
	}
	tmp := tf.Name()
	_ = tf.Close()
	if err := copyOut(stored, tmp); err != nil {
		os.Remove(tmp)
		return false
	}
	if err := os.Chmod(tmp, sfi.Mode().Perm()); err != nil {
		os.Remove(tmp)
		return false
	}
	if err := moveFile(tmp, dst); err != nil {
		os.Remove(tmp)
		return false
	}
	return true
}

// verifyFastInputs verifies every tracked file hash and directory digest in
// parallel across at most Config.Jobs guarded workers. Files are verified
// first so an edited build fails fast without reading all tracked directories.
func (s *Session) verifyFastInputs(st *cache.Store, rec *fastBuildRec) bool {
	hints := st.LoadHints(s.root.dir)
	statAt := st.Now()

	jobs := max(1, s.cfg.Jobs)
	var failed atomic.Bool
	var hintsDirty atomic.Bool

	// Step 1: verify every tracked file's content hash in parallel first so
	// single-file edit builds miss immediately without scanning directories.
	if len(rec.files) > 0 {
		wjobs := min(jobs, len(rec.files))
		var next atomic.Int64
		var wg sync.WaitGroup
		for range wjobs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if ie := source.Guard("fastbuild verify files", source.Span{}, func() {
					for !failed.Load() {
						i := int(next.Add(1) - 1)
						if i >= len(rec.files) {
							break
						}
						f := &rec.files[i]
						fi, err := os.Lstat(f.path)
						if err != nil {
							failed.Store(true)
							return
						}
						if fi.Mode().IsRegular() {
							if h, ok := hints.Lookup(f.path, fi); ok {
								if h != f.hash {
									failed.Store(true)
									return
								}
								continue
							}
						}
						b, err := os.ReadFile(f.path)
						if err != nil {
							failed.Store(true)
							return
						}
						h := source.HashBytes(fastBuildFileDom, b)
						if h != f.hash {
							failed.Store(true)
							return
						}
						if fi.Mode().IsRegular() {
							hints.Record(f.path, fi, statAt, h)
							hintsDirty.Store(true)
						}
					}
				}); ie != nil {
					failed.Store(true)
				}
			}()
		}
		wg.Wait()
	}
	if failed.Load() {
		return false
	}

	// Step 2: verify all tracked directories in parallel so added/removed/renamed
	// .mk files or subdirectories invalidate the fast build.
	if len(rec.dirs) > 0 {
		wjobs := min(jobs, len(rec.dirs))
		var next atomic.Int64
		var wg sync.WaitGroup
		for range wjobs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if ie := source.Guard("fastbuild verify dirs", source.Span{}, func() {
					for !failed.Load() {
						i := int(next.Add(1) - 1)
						if i >= len(rec.dirs) {
							break
						}
						d := &rec.dirs[i]
						sig, ok := computeDirSig(d.path, d.recursive, d.skipDirs, nil)
						if !ok || sig != d.sig {
							failed.Store(true)
							return
						}
					}
				}); ie != nil {
					failed.Store(true)
				}
			}()
		}
		wg.Wait()
	}
	if failed.Load() {
		return false
	}
	if hintsDirty.Load() {
		_ = hints.Save()
	}
	return true
}

// fastSnapshot holds the tracked directory signatures, input file hashes, and
// absent paths computed concurrently with the native stages on Session.Build.
type fastSnapshot struct {
	done   chan struct{}
	ok     bool
	dirs   []fastDirEntry
	files  []fastFileEntry
	absent []string
}

// startFastSnapshot launches directory and input-file snapshotting on a
// guarded background goroutine right after runCheck succeeds so it overlaps
// codegen, clang, and linking. Returns a cleanup closure that joins the
// goroutine before Session.Build returns.
func (s *Session) startFastSnapshot(bins []binTarget) func() {
	if !s.fastBuildEligible() || s.w == nil || len(bins) == 0 {
		return func() {}
	}
	// Skip builds with external deps, C imports, named links, data-file reads,
	// or code generators (their extra dependencies are tracked by the normal
	// incremental front/native caches).
	if len(s.deps) > 0 || s.w.cimports != nil || len(s.w.depCImports) > 0 || len(s.w.links) > 0 {
		return func() {}
	}
	s.w.data.mu.Lock()
	hasData := len(s.w.data.m) > 0
	s.w.data.mu.Unlock()
	if hasData || s.pkgB == nil {
		return func() {}
	}
	for _, m := range s.pkgB.mods {
		if m.gen != nil || len(m.genRuns) > 0 || m.genLines > 0 || (m.hit != nil && !m.hit.ifc.GenText.IsZero()) {
			return func() {}
		}
	}
	for _, b := range bins {
		if b.script != "" || b.kind != "" {
			return func() {}
		}
	}
	if s.pkgMan != nil && (len(s.pkgMan.Members) > 0 || len(s.pkgMan.Replace) > 0 ||
		len(s.pkgMan.TestDeps) > 0 || len(s.pkgMan.Targets) > 0 || len(s.pkgMan.UnsafeModules) > 0) {
		return func() {}
	}
	st := s.w.store.st
	if st == nil {
		return func() {}
	}

	// Capture all immutable inputs on the main goroutine before spawning,
	// including the exact compiled source texts so mid-build edits cannot
	// poison the fast-build fingerprint (TOCTOU).
	rkind, rdir, rfile := s.root.kind, s.root.dir, s.root.file
	jobs := max(1, s.cfg.Jobs)
	fileSet := map[string]bool{}
	compiledText := map[string]string{}
	var filePaths []string
	addFile := func(abs string) {
		if abs != "" && !fileSet[abs] {
			fileSet[abs] = true
			filePaths = append(filePaths, abs)
		}
	}
	switch rkind {
	case rootFile:
		addFile(filepath.Join(rdir, filepath.FromSlash(rfile)))
	case rootPackage:
		pkgPath := filepath.Join(rdir, "pkg.mk")
		addFile(pkgPath)
		if sf, ok := s.w.sm.ByPath("pkg.mk"); ok && sf != nil {
			compiledText[pkgPath] = sf.Text
		}
	}
	for _, m := range s.pkgB.mods {
		if m.tree == nil || m.tree.osRoot == "" {
			return func() {}
		}
		for _, f := range m.files {
			abs := filepath.Join(m.tree.osRoot, filepath.FromSlash(f.rel))
			addFile(abs)
			if f.src != nil {
				compiledText[abs] = f.src.Text
			}
		}
	}
	for _, o := range s.cfg.ExtraObjects {
		addFile(o)
	}
	optProfile := s.cfg.Profile.Optimized()
	pgoMode := s.cfg.PGO
	pgoFile := s.cfg.PGOFile
	if pgoFile != "" {
		if abs, err := filepath.Abs(pgoFile); err == nil {
			pgoFile = abs
		}
	}

	snap := &fastSnapshot{done: make(chan struct{})}
	s.fastSnap = snap
	go func() {
		defer close(snap.done)
		_ = source.Guard("fastbuild snapshot", source.Span{}, func() {
			var dirs []fastDirEntry
			var absent []string
			switch rkind {
			case rootDir:
				sig, ok := computeDirSigChecked(rdir, false, nil, fileSet)
				if !ok {
					return
				}
				dirs = append(dirs, fastDirEntry{path: rdir, recursive: false, sig: sig})
			case rootPackage:
				if !collectPackageDirSigsParallel(rdir, nil, jobs, fileSet, &dirs) {
					return
				}
			}
			if optProfile {
				switch pgoMode {
				case PGOUse:
					if pgoFile != "" {
						addFile(pgoFile)
					}
				case PGODefault:
					p := filepath.Join(rdir, "default.mkprof")
					if isRegular(p) {
						addFile(p)
					} else {
						absent = append(absent, p)
					}
				}
			}

			slices.Sort(filePaths)
			hints := st.LoadHints(rdir)
			statAt := st.Now()
			files := make([]fastFileEntry, len(filePaths))
			var failed atomic.Bool
			var hintsDirty atomic.Bool
			wjobs := min(jobs, max(1, len(filePaths)))
			var next atomic.Int64
			var wg sync.WaitGroup
			for range wjobs {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if ie := source.Guard("fastbuild hash files", source.Span{}, func() {
						for !failed.Load() {
							i := int(next.Add(1) - 1)
							if i >= len(filePaths) {
								break
							}
							p := filePaths[i]
							fi, err := os.Lstat(p)
							if err != nil {
								failed.Store(true)
								return
							}
							var h source.Hash
							var got bool
							if fi.Mode().IsRegular() {
								h, got = hints.Lookup(p, fi)
							}
							if !got {
								b, err := os.ReadFile(p)
								if err != nil {
									failed.Store(true)
									return
								}
								h = source.HashBytes(fastBuildFileDom, b)
								if fi.Mode().IsRegular() {
									hints.Record(p, fi, statAt, h)
									hintsDirty.Store(true)
								}
							}
							// Reject TOCTOU mid-build edits: disk bytes must match compiled bytes.
							if srcText, hasSrc := compiledText[p]; hasSrc {
								if h != source.HashBytes(fastBuildFileDom, []byte(srcText)) {
									failed.Store(true)
									return
								}
							}
							files[i] = fastFileEntry{path: p, hash: h}
						}
					}); ie != nil {
						failed.Store(true)
					}
				}()
			}
			wg.Wait()
			if failed.Load() {
				return
			}
			if hintsDirty.Load() {
				_ = hints.Save()
			}
			slices.SortFunc(dirs, func(a, b fastDirEntry) int { return strings.Compare(a.path, b.path) })
			slices.Sort(absent)
			snap.dirs, snap.files, snap.absent, snap.ok = dirs, files, absent, true
		})
	}()
	return func() {
		<-snap.done
		s.fastSnap = nil
	}
}

// recordFastBuild saves a whole-build fingerprint record after a clean Build.
// Errors are silently ignored (the cache is purely an optimization).
func (s *Session) recordFastBuild(bins []binTarget, outs []fastBinOut, res Result) {
	snap := s.fastSnap
	if snap == nil || s.w == nil || len(bins) == 0 || len(bins) != len(outs) || len(res.PGOStale) > 0 {
		return
	}
	st := s.w.store.st
	if st == nil || s.w.buildID().IsZero() || s.cfg.Tools == nil {
		return
	}
	cpu, ok := s.fastResolveCPU(s.cfg.Tools, st)
	if !ok {
		return
	}
	<-snap.done
	if !snap.ok {
		return
	}

	rec := fastBuildRec{
		dirs:   snap.dirs,
		files:  snap.files,
		absent: snap.absent,
	}

	// Collect stored bin entries (reusing hashes captured during copyOutHash).
	for i, o := range outs {
		if o.storedExe == "" {
			return
		}
		eh, esz := o.exeHash, o.exeSize
		if eh.IsZero() {
			var err error
			eh, esz, err = hashFileSHA256(o.storedExe)
			if err != nil {
				return
			}
		}
		be := fastBinEntry{
			name:      bins[i].name,
			storedExe: o.storedExe,
			exeSize:   esz,
			exeHash:   eh,
		}
		if o.storedPDB != "" {
			ph, psz := o.pdbHash, o.pdbSize
			if ph.IsZero() {
				var err error
				ph, psz, err = hashFileSHA256(o.storedPDB)
				if err != nil {
					return
				}
			}
			be.storedPDB, be.pdbSize, be.pdbHash = o.storedPDB, psz, ph
		}
		rec.bins = append(rec.bins, be)
	}

	if res.Trace != nil {
		packs := append(slices.Clone(res.Trace.PacksCompiled), res.Trace.PacksFromCache...)
		slices.Sort(packs)
		rec.packs = slices.Compact(packs)
		mods := append(slices.Clone(res.Trace.ModulesResolved), res.Trace.ModulesFromCache...)
		slices.Sort(mods)
		rec.modules = slices.Compact(mods)
	}
	rec.notes = slices.Clone(res.Notes)

	key := s.fastBuildKey(s.w.buildID(), s.cfg.Tools, cpu)
	_ = st.Put(cache.KindManifest, key, encodeFastBuildRec(&rec))
}

// collectPackageDirSigsParallel walks root and snapshots every non-skipped
// directory, fanning out top-level subdirectories across up to jobs guarded
// workers so large multi-module packages don't scan directories serially.
func collectPackageDirSigsParallel(root string, skipped []string, jobs int, allowedMk map[string]bool, out *[]fastDirEntry) bool {
	localSkips := func(relDir string) []string {
		var childSkips []string
		for _, sk := range skipped {
			if dirOf(sk) == relDir {
				base := sk
				if relDir != "" {
					base = strings.TrimPrefix(sk, relDir+"/")
				}
				childSkips = append(childSkips, base)
			}
		}
		slices.Sort(childSkips)
		return childSkips
	}
	rootSkips := localSkips("")
	ents, err := os.ReadDir(root)
	if err != nil {
		return false
	}
	sig, ok := hashDirEntries(root, ents, true, rootSkips, nil, allowedMk)
	if !ok {
		return false
	}
	*out = append(*out, fastDirEntry{path: root, recursive: true, skipDirs: rootSkips, sig: sig})
	type subJob struct {
		absDir string
		relDir string
	}
	var subs []subJob
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if walkSkips(name) || slices.Contains(rootSkips, name) {
			continue
		}
		subs = append(subs, subJob{absDir: filepath.Join(root, name), relDir: name})
	}
	if len(subs) == 0 {
		return true
	}
	walkSub := func(absDir, relDir string, dst *[]fastDirEntry) bool {
		var walk func(absDir, relDir string) bool
		walk = func(absDir, relDir string) bool {
			childSkips := localSkips(relDir)
			ents, err := os.ReadDir(absDir)
			if err != nil {
				return false
			}
			for _, e := range ents {
				if !e.IsDir() && e.Name() == "pkg.mk" {
					// Nested package boundary: skip fastbuild so deleting sub/pkg.mk
					// cannot slip past the root directory signature.
					return false
				}
			}
			sig, ok := hashDirEntries(absDir, ents, true, childSkips, nil, allowedMk)
			if !ok {
				return false
			}
			*dst = append(*dst, fastDirEntry{path: absDir, recursive: true, skipDirs: childSkips, sig: sig})
			for _, e := range ents {
				if !e.IsDir() {
					continue
				}
				name := e.Name()
				if walkSkips(name) || slices.Contains(childSkips, name) {
					continue
				}
				if !walk(filepath.Join(absDir, name), relDir+"/"+name) {
					return false
				}
			}
			return true
		}
		return walk(absDir, relDir)
	}
	if len(subs) == 1 || jobs <= 1 {
		for _, sj := range subs {
			if !walkSub(sj.absDir, sj.relDir, out) {
				return false
			}
		}
		return true
	}
	results := make([][]fastDirEntry, len(subs))
	var failed atomic.Bool
	var next atomic.Int64
	var wg sync.WaitGroup
	wjobs := min(jobs, len(subs))
	for range wjobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ie := source.Guard("fastbuild walk dirs", source.Span{}, func() {
				for !failed.Load() {
					i := int(next.Add(1) - 1)
					if i >= len(subs) {
						break
					}
					if !walkSub(subs[i].absDir, subs[i].relDir, &results[i]) {
						failed.Store(true)
						return
					}
				}
			}); ie != nil {
				failed.Store(true)
			}
		}()
	}
	wg.Wait()
	if failed.Load() {
		return false
	}
	for _, r := range results {
		*out = append(*out, r...)
	}
	return true
}

// computeDirSig reads dir and hashes the sorted names and kinds of its .mk
// files and (when recursive) non-skipped subdirectories. When infos is
// non-nil, regular .mk files' FileInfo entries are stored by absolute path.
func computeDirSig(dir string, recursive bool, skipDirs []string, infos map[string]os.FileInfo) (source.Hash, bool) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return source.Hash{}, false
	}
	return hashDirEntries(dir, ents, recursive, skipDirs, infos, nil)
}

func computeDirSigChecked(dir string, recursive bool, skipDirs []string, allowedMk map[string]bool) (source.Hash, bool) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return source.Hash{}, false
	}
	return hashDirEntries(dir, ents, recursive, skipDirs, nil, allowedMk)
}

func hashDirEntries(dir string, ents []os.DirEntry, recursive bool, skipDirs []string, infos map[string]os.FileInfo, allowedMk map[string]bool) (source.Hash, bool) {
	h := source.NewHasher(fastBuildDirDomain)
	h.Bool(recursive)
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() {
			if !recursive || walkSkips(name) || slices.Contains(skipDirs, name) {
				continue
			}
			h.String("D")
			h.String(name)
			continue
		}
		if !strings.HasSuffix(name, ".mk") {
			continue
		}
		full := filepath.Join(dir, name)
		if allowedMk != nil && !allowedMk[full] {
			// A new .mk file appeared mid-build; abort snapshot to avoid TOCTOU poisoning.
			return source.Hash{}, false
		}
		typ := e.Type()
		h.String("F")
		h.String(name)
		h.Bool(typ.IsRegular())
		if infos != nil && typ.IsRegular() {
			if fi, err := e.Info(); err == nil {
				infos[full] = fi
			}
		}
	}
	return h.Sum(), true
}

func hashFileSHA256(path string) (source.Hash, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return source.Hash{}, 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return source.Hash{}, 0, err
	}
	var out source.Hash
	copy(out[:], h.Sum(nil))
	return out, n, nil
}

func encodeFastBuildRec(r *fastBuildRec) []byte {
	var e iface.Encoder
	e.Uvarint(fastBuildVersion)
	e.Uvarint(uint64(len(r.dirs)))
	for _, d := range r.dirs {
		e.String(d.path)
		e.Bool(d.recursive)
		e.Uvarint(uint64(len(d.skipDirs)))
		for _, s := range d.skipDirs {
			e.String(s)
		}
		e.Hash(d.sig)
	}
	e.Uvarint(uint64(len(r.files)))
	for _, f := range r.files {
		e.String(f.path)
		e.Hash(f.hash)
	}
	e.Uvarint(uint64(len(r.absent)))
	for _, p := range r.absent {
		e.String(p)
	}
	e.Uvarint(uint64(len(r.bins)))
	for _, b := range r.bins {
		e.String(b.name)
		e.String(b.storedExe)
		e.Uvarint(uint64(b.exeSize))
		e.Hash(b.exeHash)
		e.String(b.storedPDB)
		e.Uvarint(uint64(b.pdbSize))
		e.Hash(b.pdbHash)
	}
	e.Uvarint(uint64(len(r.packs)))
	for _, p := range r.packs {
		e.String(p)
	}
	e.Uvarint(uint64(len(r.modules)))
	for _, m := range r.modules {
		e.String(m)
	}
	e.Uvarint(uint64(len(r.notes)))
	for _, n := range r.notes {
		e.String(n)
	}
	return e.Out()
}

func decodeFastBuildRec(raw []byte) (*fastBuildRec, error) {
	d := iface.NewDecoder(raw)
	if v := d.Uvarint(); d.Err == nil && v != fastBuildVersion {
		d.Failf("fastbuild version %d", v)
	}
	r := &fastBuildRec{}
	nd := d.Count(34)
	r.dirs = make([]fastDirEntry, 0, nd)
	for i := 0; i < nd && d.Err == nil; i++ {
		de := fastDirEntry{path: d.String(), recursive: d.Bool()}
		ns := d.Count(1)
		if ns > 0 {
			de.skipDirs = make([]string, 0, ns)
			for j := 0; j < ns && d.Err == nil; j++ {
				de.skipDirs = append(de.skipDirs, d.String())
			}
		}
		de.sig = d.Hash()
		r.dirs = append(r.dirs, de)
	}
	nf := d.Count(33)
	r.files = make([]fastFileEntry, 0, nf)
	for i := 0; i < nf && d.Err == nil; i++ {
		r.files = append(r.files, fastFileEntry{path: d.String(), hash: d.Hash()})
	}
	na := d.Count(1)
	r.absent = make([]string, 0, na)
	for i := 0; i < na && d.Err == nil; i++ {
		r.absent = append(r.absent, d.String())
	}
	nb := d.Count(66)
	r.bins = make([]fastBinEntry, 0, nb)
	for i := 0; i < nb && d.Err == nil; i++ {
		be := fastBinEntry{
			name:      d.String(),
			storedExe: d.String(),
			exeSize:   int64(d.Uvarint()),
			exeHash:   d.Hash(),
			storedPDB: d.String(),
			pdbSize:   int64(d.Uvarint()),
			pdbHash:   d.Hash(),
		}
		r.bins = append(r.bins, be)
	}
	np := d.Count(1)
	r.packs = make([]string, 0, np)
	for i := 0; i < np && d.Err == nil; i++ {
		r.packs = append(r.packs, d.String())
	}
	nm := d.Count(1)
	r.modules = make([]string, 0, nm)
	for i := 0; i < nm && d.Err == nil; i++ {
		r.modules = append(r.modules, d.String())
	}
	nn := d.Count(1)
	r.notes = make([]string, 0, nn)
	for i := 0; i < nn && d.Err == nil; i++ {
		r.notes = append(r.notes, d.String())
	}
	if err := d.Done(); err != nil {
		return nil, err
	}
	return r, nil
}
