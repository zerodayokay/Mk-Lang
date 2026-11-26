// Why: one package may be named by many manifests under many local names;
// the build must find each package once, give it one canonical id for
// keys and symbols, and still show users the names they wrote.
//
// What: depPkg and depWalk, resolveDeps (the walk of the manifest step),
// identityOf, canonicalID and urlCID, walk and source (one Dep's package),
// depBuilds, and the display side: displayNames, localizeText,
// localizeDiags, plus resolvedImports for the cache key.
//
// How: the walk goes by identity:
//
//   - A path package's identity is its directory relative to the workspace
//     root (the root package's directory outside a workspace),
//     written with "/" and case-folded on Windows; a url package's is its
//     url and archive hash. One identity is one package however many
//     manifests name it, under whatever local names.
//   - Its canonical id is `<name>_<first 8 hex of SHA-256 over its identity
//     text>`, where name, made an identifier, is the manifest name for a
//     path package, the url's last path element (archive extension cut) for
//     a url package and the source's last element for a fetched source; the
//     identity text is the relative directory, the url, or
//     "<source>@v<major>". The canonical id is the first segment of the
//     package's module paths, so item keys, symbols, the SourceMap prefix
//     and cache keys are the same whatever a manifest calls it.
//   - A Dep's name is an alias in the manifest that writes it:
//     depBuilds maps each manifest's local names to builds, and
//     lookupImport (graph.go) resolves a first segment through the
//     importing package's own manifest.
//   - Two identities with one canonical id are E0202 `id-clash`.
//
// vendor/ is read through vendor/index.txt (vendor.go): an identity found
// there is read from vendor/<canonical id>/; with an index, source packages
// take their versions from it and skip the version walk
// (deps_source.go vendoredSources). A vendor/ in the older layout,
// vendor/<name>, is still read, with a note naming mk vendor.

package driver

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"mk/internal/cache"
	"mk/internal/diag"
	"mk/internal/fetch"
	"mk/internal/source"
)

// depPkg is one dependency of the build: one identity.
type depPkg struct {
	// member: a workspace member, never vendored.
	member bool
	// testOnly: only the root's test_deps reach it (deps_testdeps.go);
	// wholeMan is then its manifest as written, for the perimeter, when
	// man is the copy without what it links.
	testOnly bool
	wholeMan *Manifest
	// depTestOnly: only some dependency's own test_deps reach it; the root
	// never grants or links it.
	depTestOnly bool
	// name is the first local name that brought it into the build, for
	// messages; names are all of them.
	name  string
	names []string
	// cid is its canonical id, the first segment of its module paths.
	cid string
	// identity is "path <relative dir, folded>" or "url <url> <hash>".
	identity string
	// rel is a path package's directory relative to the workspace root,
	// with "/", as spelled (the identity folds it); "" for a url package.
	rel  string
	tree *srcTree
	man  *Manifest
	// osDir is its directory on disk: a fetched package's tree in the
	// package cache, "" for one held only in memory (--no-cache). origDir is where its manifest's own path dependencies are
	// relative to: osDir, or for a vendored path package the directory it
	// was vendored from.
	osDir, origDir string
	vendored       bool
	// from is the source as mk vendor reports it: the path as given, or the
	// url.
	from string
	// archive is a fetched package's unpacked tree (mk vendor writes it);
	// treeDir its directory in the package cache when it was read there.
	archive *fetch.Tree
	treeDir string
	// indexCID is the canonical id vendor/index.txt gave, checked against
	// the computed one once the manifest is read.
	indexCID string
	// local maps the local names of its manifest's Deps to their packages.
	local map[string]*depPkg
	done  bool // its own deps are walked
	// source and version are a source package's (source "" otherwise).
	source, version string
	// parent is the package whose manifest first brought it in (nil for
	// the root's, or a workspace member's), and viaMan and via that Dep's
	// manifest and field: E0215 is reported at the root's Dep (grants).
	parent *depPkg
	viaMan *Manifest
	via    string
}

// owner is the package whose manifest the walk is reading: nil for the
// root's.
func (dw *depWalk) owner() *depPkg {
	if len(dw.stack) == 0 {
		return nil
	}
	return dw.stack[len(dw.stack)-1]
}

// depWalk walks the dependency graph from the package's manifest.
type depWalk struct {
	s       *Session
	ms      *manifestStep
	rootDir string
	// wsRoot is the workspace root: identities are relative to it.
	wsRoot string
	// vendor: read vendor/ when it exists (false for mk vendor).
	vendor  bool
	vindex  *vendorIndex
	fetcher *fetch.Fetcher
	// rootFirsts is moduleFirsts of the package being built. Its modules
	// carry no prefix, so a canonical id equal to one of their first
	// segments would share their module paths.
	rootFirsts map[string]string
	byID       map[string]*depPkg
	byCID      map[string]*depPkg
	order      []*depPkg // post-order: every package after its dependencies
	stack      []*depPkg
	rootLocal  map[string]*depPkg
	// The version walk's state (deps_source.go): the root's replace
	// entries, the source requirements met and the Deps waiting for their
	// chosen version, pkg.sum and the versions chosen.
	replace []Dep
	root    *Manifest
	reqs    []requirement
	pending []pendingDep
	sum     *pkgSum
	sumDirt bool
	chosen  map[string]*selected
	notes   []string
	diags   []*diag.Diagnostic
	// ws is the workspace (deps_workspace.go), nil for none; loaded holds
	// the member manifests it read, by pathKey, so a member walked as a
	// path dependency is not read twice; extraReqs: other members'
	// requirements joined the version choice.
	ws        *wsInfo
	loaded    map[string]*Manifest
	extraReqs bool
	// old12 is each vendor/<name> of the older layout read so far, by its
	// folded name: Deps that reach one directory are one package, as that
	// layout always read them.
	old12 map[string]*depPkg
	// secretURLs are the urls met that hold credentials (redactURL
	// changes them): no diagnostic or note shows them.
	secretURLs map[string]bool
}

// resolveDeps walks the dependencies of root, the manifest of the package
// in pkgT. It returns them in build order, or the diagnostics of the first
// faults (each at its Dep, or in a dependency's manifest), and keeps the
// root's local names in s.depRoot.
func (s *Session) resolveDeps(ms *manifestStep, pkgT *srcTree, root *Manifest, vendor bool) ([]*depPkg, []*diag.Diagnostic) {
	s.depRoot, s.walked, s.walkedRoot, s.walkedMan, s.walkedGrants = nil, nil, nil, root, nil
	s.walkedSum, s.walkedChosen, s.wsDir, s.walkedSelf = nil, nil, s.root.dir, root
	if root == nil {
		return nil, nil
	}
	dw := &depWalk{s: s, ms: ms, rootDir: s.root.dir, wsRoot: s.root.dir, vendor: vendor,
		byID: map[string]*depPkg{}, byCID: map[string]*depPkg{}, loaded: map[string]*Manifest{},
		rootFirsts: moduleFirsts(pkgT, root, "main")}
	defer func() {
		redactDiags(dw.secretURLs, dw.diags, dw.notes) // no url with credentials is shown
		ms.notes = append(ms.notes, dw.notes...)       // on every return
	}()
	// The workspace: its root holds pkg.sum, replace and the
	// grants, and identities are relative to it.
	grantMan := root
	if dw.ws = dw.findWorkspace(root); len(dw.diags) > 0 {
		return nil, dw.diags
	} else if dw.ws != nil {
		dw.wsRoot, grantMan = dw.ws.dir, dw.ws.man
		s.walkedMan = dw.ws.man
	}
	if len(root.Deps) == 0 && len(root.TestDeps) == 0 {
		return nil, nil
	}
	if vendor {
		vi, err := readVendorIndex(filepath.Join(s.root.dir, "vendor"))
		if err != nil {
			return nil, []*diag.Diagnostic{depFail(root, "deps", "invalid", "vendor/index.txt: %v; run mk vendor again", err)}
		}
		dw.vindex = vi
	}
	dw.replace = grantMan.Replace
	dw.root = root
	dw.rootLocal = dw.walk(root, s.root.dir)
	if dw.ws != nil {
		dw.memberRequirements()
	}
	if len(dw.diags) == 0 && len(dw.pending) > 0 {
		dw.sources(root)
	}
	if dw.ws != nil && len(dw.diags) == 0 {
		dw.reachableOnly()
	}
	if len(dw.diags) == 0 {
		dw.checkSymbolClash() // two majors may not define one C symbol (deps_symclash.go)
	}
	if len(dw.diags) == 0 {
		dw.classifyTests(root)                                 // before the grants
		defer dw.markTestOnly(root, pkgT, s.testing || s.prop) // after the grants, which read each whole manifest
	}
	if len(dw.diags) == 0 {
		dw.saveSum()
		// Grants once the build list is known; the perimeter
		// is kept even when a grant is missing, for mk query perimeter.
		grants := dw.grantSet(grantMan)
		dw.checkGrants(grantMan, grants)
		s.walked, s.walkedRoot, s.walkedGrants = dw.order, dw.rootLocal, grants
		s.walkedSum, s.walkedChosen, s.wsDir = dw.sum, dw.chosen, dw.wsRoot
	}
	if len(dw.diags) > 0 {
		return nil, dw.diags
	}
	s.depRoot = dw.rootLocal
	return dw.order, nil
}

// foldPath is a slash path as an identity: case-folded on Windows, whose
// file system matches names that way.
func foldPath(p string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(p)
	}
	return p
}

// pathKey is a cleaned absolute directory as an identity, case-folded on
// Windows.
func pathKey(abs string) string { return foldPath(filepath.Clean(abs)) }

// relToRoot is dir relative to the workspace root, with "/"; an absolute
// path when it has none (another volume).
func (dw *depWalk) relToRoot(dir string) string {
	rel, err := filepath.Rel(dw.wsRoot, dir)
	if err != nil || filepath.IsAbs(rel) || strings.ContainsRune(rel, ':') {
		return filepath.ToSlash(filepath.Clean(dir))
	}
	return filepath.ToSlash(rel)
}

// identityOf is the identity d gives from a manifest whose path
// dependencies are relative to origDir, and a path package's relative
// directory.
func (dw *depWalk) identityOf(d Dep, origDir string) (id, rel string) {
	if d.URL != "" {
		if redactURL(d.URL) != d.URL {
			if dw.secretURLs == nil {
				dw.secretURLs = map[string]bool{}
			}
			dw.secretURLs[d.URL] = true
		}
		h, _ := fetch.ParseHash(d.Hash)
		return "url " + d.URL + " " + h.String(), ""
	}
	rel = dw.relToRoot(filepath.Join(origDir, filepath.FromSlash(d.Path)))
	return "path " + foldPath(rel), rel
}

// canonicalID is `<name made an identifier>_<8 hex of SHA-256 over key>`.
func canonicalID(name, key string) string {
	sum := sha256.Sum256([]byte(key))
	return identifierOf(name) + "_" + hex.EncodeToString(sum[:4])
}

// identifierOf lowers s and makes it one import path segment: every byte
// that is no letter, digit or underscore becomes "_", and a leading digit
// gets a "_" before it.
func identifierOf(s string) string {
	b := []byte(strings.ToLower(s))
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			b[i] = '_'
		}
	}
	if len(b) == 0 || b[0] >= '0' && b[0] <= '9' {
		b = append([]byte{'_'}, b...)
	}
	return string(b)
}

// urlCID is a url package's canonical id: the last element of the url's
// path without its archive extension, made an identifier, and 8 hex of
// SHA-256 over the url (like a source's last element, it is known before
// the archive is read, so the archive's SourceMap prefix can carry it).
func urlCID(raw string) string {
	u, err := url.Parse(raw)
	name := "pkg"
	if err == nil {
		base := path.Base(u.Path)
		for _, ext := range []string{".tar.gz", ".tgz", ".tar", ".zip"} {
			if b, ok := strings.CutSuffix(strings.ToLower(base), ext); ok {
				base = base[:len(b)]
				break
			}
		}
		if base != "" && base != "." && base != "/" {
			name = base
		}
	}
	return canonicalID(name, raw)
}

// cidOf is p's canonical id, once its manifest is read.
func cidOf(p *depPkg) string {
	if strings.HasPrefix(p.identity, "url ") {
		return urlCID(strings.Fields(p.identity)[1])
	}
	return canonicalID(p.man.Name, foldPath(p.rel))
}

// walk visits the Deps of man, a manifest whose path dependencies are
// relative to origDir ("" for a fetched one), and returns its local names.
func (dw *depWalk) walk(man *Manifest, origDir string) map[string]*depPkg {
	local := map[string]*depPkg{}
	manDir := origDir
	// test_deps too: a package's modules import them for their test blocks,
	// so they bind whenever the package is checked.
	for _, d := range append(slices.Clone(man.Deps), man.TestDeps...) {
		origDir := manDir // a replace below changes it for its Dep alone
		if d.Source != "" {
			// A requirement for the version walk (deps_source.go); its
			// local name is bound once the versions are chosen. A root
			// replace by path makes it a path dependency here.
			if rd, ok := dw.replaced(d); ok {
				d, origDir = rd, dw.wsRoot // relative to the manifest holding the replace
			} else {
				dw.require(man, local, d)
				continue
			}
		}
		if origDir == "" && d.Path != "" {
			dw.diags = append(dw.diags, depFail(man, d.Field+".path", "invalid",
				"dependency %s: a fetched package cannot name a path dependency (it has no directory to be relative to)", d.Name))
			continue
		}
		id, rel := dw.identityOf(d, origDir)
		p := dw.byID[id]
		if q := dw.old12[foldPath(d.Name)]; p == nil && q != nil && dw.vendor && dw.vindex.empty() {
			// As the older layout always did: two path Deps of one name
			// are the one vendored package; any other mix is a second source.
			if d.Path == "" || !strings.HasPrefix(q.identity, "path ") {
				dw.diags = append(dw.diags, depFail(man, d.Field, "conflict",
					"dependency %s has a second source in this build: %s, where another Dep gave %s, and vendor/%s holds one of them; run mk vendor",
					d.Name, redactURL(cmp.Or(d.Path, d.URL)), redactURL(q.from), d.Name))
				continue
			}
			p = q
		}
		if p != nil {
			if !p.done {
				var chain []string
				for _, q := range dw.stack[slices.Index(dw.stack, p):] {
					chain = append(chain, q.name)
				}
				dw.diags = append(dw.diags, depFail(man, d.Field, "cycle", "dependency cycle: %s",
					strings.Join(append(chain, d.Name), " -> ")))
				continue
			}
			local[d.Name] = p
			if !slices.Contains(p.names, d.Name) {
				p.names = append(p.names, d.Name)
			}
			continue
		}
		if d.Path != "" && id == "path "+foldPath(dw.relToRoot(dw.rootDir)) {
			var chain []string
			for _, q := range dw.stack {
				chain = append(chain, q.name)
			}
			dw.diags = append(dw.diags, depFail(man, d.Field, "cycle", "dependency cycle: %s names the package being built",
				strings.Join(append(chain, d.Name), " -> ")))
			continue
		}
		p = &depPkg{name: d.Name, names: []string{d.Name}, identity: id, rel: rel, from: cmp.Or(d.Path, d.URL),
			parent: dw.owner(), viaMan: man, via: d.Field}
		if !dw.source(p, d, man, origDir) {
			continue
		}
		dw.byID[id] = p
		m, ds := dw.loaded[pathKey(p.osDir)], []*diag.Diagnostic(nil)
		if m == nil || p.osDir == "" || p.vendored {
			m, ds = dw.ms.load(p.tree, "pkg.mk", d.Name)
		}
		if len(ds) > 0 {
			dw.diags = append(dw.diags, ds...)
			p.done = true
			continue
		}
		p.man = m
		if p.osDir != "" && !p.vendored && p.rel != "" {
			dw.loaded[pathKey(p.osDir)] = m // read once (replacementManifest)
		}
		p.cid = cidOf(p)
		if dw.ws != nil && !p.vendored && p.osDir != "" && dw.ws.isMember(p.osDir) {
			p.cid = identifierOf(m.Name) // a member keeps its manifest name
			p.member = true
		}
		if p.indexCID != "" && p.indexCID != p.cid {
			// vendor/index.txt is read, never trusted: the id
			// it names must be the one the identity and manifest give.
			dw.diags = append(dw.diags, depFail(man, d.Field, "invalid",
				"dependency %s: vendor/index.txt names %s for %s, but its canonical id is %s; run mk vendor again",
				d.Name, p.indexCID, p.vendorKey(), p.cid))
			p.done = true
			continue
		}
		if q := dw.byCID[p.cid]; q != nil {
			dw.diags = append(dw.diags, depFail(man, d.Field, "id-clash",
				"dependency %s (%s) and dependency %s (%s) have one canonical id, %s; give one a replace or rename its package",
				d.Name, p.from, q.name, q.from, p.cid))
			p.done = true
			continue
		}
		if dir := dw.rootFirsts[p.cid]; dir != "" {
			dw.diags = append(dw.diags, depFail(man, d.Field, "id-clash",
				"dependency %s's canonical id %s is the first segment of the module in %s of the package being built; rename that directory",
				d.Name, p.cid, dir))
			p.done = true
			continue
		}
		dw.byCID[p.cid] = p
		local[d.Name] = p
		dw.stack = append(dw.stack, p)
		p.local = dw.walk(m, p.origDir)
		dw.stack = dw.stack[:len(dw.stack)-1]
		p.done = true
		dw.order = append(dw.order, p)
	}
	return local
}

// vendoredDir is vendor/<canonical id> of the index's line e when it holds
// a pkg.mk (and, under MK_CACHE_VERIFY=1, the tree the line records); a
// diagnostic at d, the Dep of man that names it, otherwise.
func (dw *depWalk) vendoredDir(e vendorEntry, man *Manifest, d Dep) (string, bool) {
	dir := filepath.Join(dw.rootDir, "vendor", e.cid)
	if !hasManifest(dir) {
		dw.diags = append(dw.diags, depFail(man, d.Field, "invalid",
			"dependency %s: vendor/%s holds no pkg.mk; run mk vendor again", d.Name, e.cid))
		return "", false
	}
	if dw.s.cfg.CacheVerify {
		// A vendored tree is verified as a cached tree is:
		// MK_CACHE_VERIFY=1 hashes it against vendor/index.txt.
		if got, err := dirTreeHash(dir); err != nil || got != e.hash {
			dw.diags = append(dw.diags, depFail(man, d.Field, fetch.ReasonSumMismatch,
				"dependency %s: vendor/%s has %s, but vendor/index.txt records %s; run mk vendor again", d.Name, e.cid, got, e.hash))
			return "", false
		}
	}
	return dir, true
}

// source finds p's tree: vendor/<canonical id> through vendor/index.txt,
// vendor/<name> in the older layout, the path, or the fetched archive. A
// fault is a diagnostic at d, and false.
func (dw *depWalk) source(p *depPkg, d Dep, man *Manifest, origDir string) bool {
	w := dw.s.w
	if dw.vendor {
		if e, ok := dw.vindex.lookup(p); ok {
			dir, ok := dw.vendoredDir(e, man, d)
			if !ok {
				return false
			}
			p.vendored, p.osDir, p.indexCID = true, dir, e.cid
			// A vendored fetched package is still fetched: no directory
			// its path dependencies could be relative to.
			p.origDir = ""
			if p.rel != "" {
				p.origDir = filepath.Join(dw.wsRoot, filepath.FromSlash(p.rel))
			}
			p.tree = w.diskTree(dir, "vendor/"+e.cid+"/")
			p.tree.sitePrefix = p.tree.prefix
			return true
		}
		if !dw.vindex.empty() && !(d.Path != "" && dw.ws != nil && dw.ws.isMember(filepath.Join(origDir, filepath.FromSlash(d.Path)))) {
			// Never silent: the index is the vendored build
			// list, and this dependency is not in it (a workspace member
			// never is).
			dw.note("note: vendor/index.txt has no line for dependency %s, so it is read from %s; run mk vendor", d.Name, redactURL(cmp.Or(d.Path, d.URL)))
		}
		if dw.vindex.empty() {
			// The older layout: vendor/<name>, with a note.
			dir := filepath.Join(dw.rootDir, "vendor", d.Name)
			if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
				if !hasManifest(dir) {
					dw.diags = append(dw.diags, depFail(man, d.Field, "invalid",
						"dependency %s: vendor/%s holds no pkg.mk; run mk vendor again", d.Name, d.Name))
					return false
				}
				dw.note("note: vendor/%s is in the older layout; run mk vendor to write vendor/<canonical id>/ and vendor/index.txt", d.Name)
				p.vendored, p.osDir = true, dir
				p.origDir = "" // fetched, as above
				if p.rel != "" {
					p.origDir = filepath.Join(dw.wsRoot, filepath.FromSlash(p.rel))
				}
				p.tree = w.diskTree(dir, "vendor/"+d.Name+"/")
				p.tree.sitePrefix = p.tree.prefix
				if dw.old12 == nil {
					dw.old12 = map[string]*depPkg{}
				}
				dw.old12[foldPath(d.Name)] = p
				return true
			}
		}
	}
	if d.Path != "" {
		dir := filepath.Join(origDir, filepath.FromSlash(d.Path))
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() || !hasManifest(dir) {
			dw.diags = append(dw.diags, depFail(man, d.Field+".path", "invalid",
				"dependency %s: %s is not a package directory (a directory holding pkg.mk)", d.Name, d.Path))
			return false
		}
		p.osDir, p.origDir = dir, dir
		p.tree = w.diskTree(dir, dw.prefixOf(p, dir))
		p.tree.sitePrefix = p.tree.prefix
		return true
	}
	if d.Hash == "" && checkSumURL(d.URL) == nil {
		// A hashless url package is cached by its tree like a source package
		// once pkg.sum records its tree line: the first fetch records the
		// line and fills the cache; a later build reads <cache>/pkg/<tree
		// hash>/, offline too.
		if sum := dw.loadSum(dw.root); sum != nil {
			if line, ok := sum.get(d.URL, "", false); ok {
				if h, ok := fetch.ParseTreeHash(line.Hash); ok {
					dir, err := dw.s.cachedTree(h)
					if err != nil {
						dw.diags = append(dw.diags, depFail(man, d.Field, reasonOf(err), "dependency %s: %v", d.Name, err))
						return false
					}
					if dir != "" {
						p.cid = urlCID(d.URL)
						pfx := p.cid + "@" + hex.EncodeToString(h[:6]) + "/"
						p.treeDir, p.osDir = dir, dir
						p.tree = w.diskTree(dir, pfx)
						p.tree.sitePrefix = pfx
						return true
					}
				}
			}
		} else {
			return false
		}
	}
	tr, err := dw.urlTree(d)
	if err != nil {
		reason := "fetch"
		if fe, ok := err.(*fetch.Error); ok {
			reason = fe.Reason
		}
		dw.diags = append(dw.diags, depFail(man, d.Field, reason, "dependency %s: %v", d.Name, err))
		return false
	}
	h, _ := fetch.ParseHash(d.Hash)
	mem := map[string]string{}
	for _, f := range tr.Files {
		mem[f.Path] = string(f.Data)
	}
	if _, ok := mem["pkg.mk"]; !ok {
		dw.diags = append(dw.diags, depFail(man, d.Field, "invalid",
			"dependency %s: the archive holds no pkg.mk at its root", d.Name))
		return false
	}
	// The SourceMap prefix is "<canonical id>@<12 hex of the archive>/",
	// so no key or site path depends on what a manifest calls it; the id
	// of a url package is known before its manifest is read.
	p.cid = urlCID(d.URL)
	key := h.String()[len("sha256:"):]
	if d.Hash == "" {
		key = tr.Hash().String()[len("tree-sha256:"):]
	}
	pfx := p.cid + "@" + key[:12] + "/"
	p.archive = tr
	p.tree = &srcTree{mem: mem, prefix: pfx, sitePrefix: pfx}
	if d.Hash == "" && checkSumURL(d.URL) == nil {
		if dir := dw.s.storeTree(tr, tr.Hash()); dir != "" {
			p.treeDir, p.osDir = dir, dir
		}
	}
	return true
}

// note records a note once.
func (dw *depWalk) note(format string, args ...any) {
	n := fmt.Sprintf(format, args...)
	if !slices.Contains(dw.notes, n) {
		dw.notes = append(dw.notes, n)
	}
}

// prefixOf is the SourceMap prefix of a path dependency in dir: its path
// relative to the package root, or "<8 hex of its identity>/" when it has
// none (another volume).
func (dw *depWalk) prefixOf(p *depPkg, dir string) string {
	rel, err := filepath.Rel(dw.rootDir, dir)
	if err != nil || filepath.IsAbs(rel) || strings.ContainsRune(rel, ':') {
		sum := sha256.Sum256([]byte(p.identity))
		return hex.EncodeToString(sum[:4]) + "/"
	}
	return filepath.ToSlash(rel) + "/"
}

// fetcherFor is the walk's fetcher, made on the first url dependency: the
// content cache at the session's cache directory keeps archives by their
// hash. Under --no-cache it has no store (--no-cache skips every cache
// read and write), so every url dependency is fetched, and an
// offline build has only vendor/.
func (dw *depWalk) fetcherFor() *fetch.Fetcher {
	if dw.fetcher == nil {
		cfg := dw.s.cfg
		f := &fetch.Fetcher{Client: cfg.TestHooks.HTTPClient, Offline: cfg.Offline}
		if !cfg.NoCache {
			if st, err := cache.Open(dw.s.cacheDir(), cache.Options{Budget: cfg.CacheBudget, Now: cfg.TestHooks.CacheNow}); err == nil {
				f.Store = st
			}
		}
		dw.fetcher = f
	}
	return dw.fetcher
}

// depBuilds forms one build per dependency, in build order, each listing
// its own tree under its manifest with every module name prefixed by the
// dependency's canonical id, and gives each build (the package's too) the
// builds of its manifest's local names. A dependency's modules are never
// bin roots (its bins are not built).
func (s *Session) depBuilds(stdB, pkgB *build, pkgMan *Manifest) ([]*build, []*diag.Diagnostic) {
	if len(s.deps) == 0 {
		return nil, nil
	}
	byPkg := map[*depPkg]*build{}
	var out []*build
	for _, dp := range s.deps {
		b := newBuild(s.w, stdB)
		b.prefix = dp.cid + "."
		if err := b.listTree(dp.tree, dp.man, dp.cid); err != nil {
			return nil, []*diag.Diagnostic{listFailure(dp.tree, err)}
		}
		for _, m := range b.mods {
			m.path = m.name
		}
		b.permitUnsafe(dp.man)
		b.findCaseDups()
		b.depBuilds = depsOf(dp.local, byPkg)
		byPkg[dp] = b
		out = append(out, b)
	}
	pkgB.depBuilds = depsOf(s.depRoot, byPkg)
	return out, nil
}

// depsOf maps local names to the builds of their packages.
func depsOf(local map[string]*depPkg, byPkg map[*depPkg]*build) map[string]*build {
	out := map[string]*build{}
	for name, p := range local {
		if b := byPkg[p]; b != nil {
			out[name] = b
		}
	}
	return out
}

// depModule is the module path in db, a dependency's build, of the import
// path p whose first segment is a local name of db's package.
func depModule(db *build, p string) string {
	_, rest, ok := strings.Cut(p, ".")
	cid := strings.TrimSuffix(db.prefix, ".")
	if !ok {
		return cid
	}
	return db.prefix + rest
}

// localModule is the dependency module path q of db as an importer whose
// local name for db's package is first spells it.
func localModule(db *build, first, q string) string {
	cid := strings.TrimSuffix(db.prefix, ".")
	if q == cid {
		return first
	}
	if rest, ok := strings.CutPrefix(q, db.prefix); ok {
		return first + "." + rest
	}
	return q
}

// displayNames maps each dependency's canonical id to the name a reader of a
// file of holder knows it by: holder's manifest's local name for a package
// it names, else the first local name in the build that brought it in;
// holder's own id maps to "" (its modules are written without it). holder
// nil is the root package. The canonical id stays in symbols and vendor/
// only.
func (s *Session) displayNames(holder *depPkg) map[string]string {
	if len(s.deps) == 0 {
		return nil
	}
	out := map[string]string{}
	for _, dp := range s.deps {
		if dp.cid != "" {
			out[dp.cid] = dp.name
		}
	}
	local := s.depRoot
	if holder != nil {
		local = holder.local
		out[holder.cid] = ""
	}
	// A package two names call is shown by the first in name order, the
	// same every run.
	var names []string
	for n := range local {
		names = append(names, n)
	}
	slices.Sort(names)
	for i := len(names) - 1; i >= 0; i-- {
		if dp := local[names[i]]; dp != nil && dp.cid != "" && dp != holder {
			out[dp.cid] = names[i]
		}
	}
	return out
}

// holderOf is the dependency whose tree holds the file f's path, or nil
// for the root package (and for std).
func (s *Session) holderOf(path string) *depPkg {
	var best *depPkg
	for _, dp := range s.deps {
		if dp.tree == nil || dp.tree.prefix == "" || !strings.HasPrefix(path, dp.tree.prefix) {
			continue
		}
		if best == nil || len(dp.tree.prefix) > len(best.tree.prefix) {
			best = dp
		}
	}
	return best
}

// identByte reports whether c can be part of an identifier.
func identByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// localizeText replaces every whole canonical id in t, one not inside a
// longer identifier, by its display name; an id that maps to "" (the
// holder's own) loses itself and the "." after it.
func localizeText(t string, names map[string]string) string {
	for cid, name := range names {
		var b strings.Builder
		rest := t
		changed := false
		for {
			i := strings.Index(rest, cid)
			if i < 0 {
				break
			}
			end := i + len(cid)
			before := i == 0 || !identByte(rest[i-1])
			after := end == len(rest) || !identByte(rest[end])
			if !before || !after {
				b.WriteString(rest[:end])
				rest = rest[end:]
				continue
			}
			b.WriteString(rest[:i])
			switch {
			case name != "":
				b.WriteString(name)
			case end < len(rest) && rest[end] == '.':
				end++ // the holder's own module: "src", not "<id>.src"
			default:
				b.WriteString(cid)
			}
			rest = rest[end:]
			changed = true
		}
		if changed {
			b.WriteString(rest)
			t = b.String()
		}
	}
	return t
}

// echoKeys are fact keys whose text is what the user wrote (an identifier,
// an import path as spelled): never rewritten.
var echoKeys = map[string]bool{"name": true}

// localizeDiags rewrites the message, the text facts and the alternatives
// of ds so a diagnostic names a dependency's module as the file it is in
// imports it (`u.src`), not by its canonical id. An E0202 of an
// import echoes the path as written and is left alone. Fixes are not
// rewritten: their edits are source text, which names local names already.
// mk lsp reads only the facts' spans, so the rewrite changes nothing it
// relies on.
func (s *Session) localizeDiags(ds []*diag.Diagnostic) {
	if len(s.deps) == 0 {
		return
	}
	byHolder := map[*depPkg]map[string]string{}
	for _, d := range ds {
		if d.Code == diag.E0202 {
			if _, ok := d.FactValue("module"); ok {
				continue // an import line's path as written
			}
		}
		var holder *depPkg
		if d.At.File != source.NoFile && s.w != nil {
			if f := s.w.sm.File(d.At.File); f != nil {
				holder = s.holderOf(f.Path)
			}
		}
		names, ok := byHolder[holder]
		if !ok {
			names = s.displayNames(holder)
			byHolder[holder] = names
		}
		d.Message = localizeText(d.Message, names)
		for i := range d.Facts {
			if !echoKeys[d.Facts[i].Key] {
				localizeFact(&d.Facts[i].Value, names)
			}
		}
		for i, a := range d.Alternatives {
			d.Alternatives[i] = localizeText(a, names)
		}
	}
}

// localizeFact rewrites a text fact and the text facts inside a list.
func localizeFact(f *diag.Fact, names map[string]string) {
	if f.Kind == diag.FactText {
		f.Str = localizeText(f.Str, names)
	}
	for i := range f.List {
		localizeFact(&f.List[i], names)
	}
}

// resolvedImports hashes m's import lines as written with the module each
// resolves to, sorted by spelling: the part of a module's meaning its text
// does not hold once a local name is an alias. A line that did not
// resolve hashes as "".
func resolvedImports(m *modNode) source.Hash {
	spell := make([]string, 0, len(m.imports))
	for p := range m.imports {
		spell = append(spell, p)
	}
	slices.Sort(spell)
	h := source.NewHasher("mk.driver.resolvedimports.v2")
	h.Uvarint(uint64(len(spell)))
	for _, p := range spell {
		h.String(p)
		if d := m.imports[p]; d != nil {
			h.String(d.path)
			// An import of a test dependency of m's package:
			// its names are E0217 outside test blocks, so the bit is part
			// of the key.
			first, _, _ := strings.Cut(p, ".")
			h.Bool(m.tree != nil && m.tree.testNames[first])
		} else {
			h.String("")
		}
	}
	return h.Sum()
}
