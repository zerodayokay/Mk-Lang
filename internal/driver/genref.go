// Why: a module with gen output must hit the module cache like any other,
// but its generated file is a virtual source file that no parse summary
// covers.
//
// What: GenRef for the module cache: anchorLayers (the parse summaries'
// anchors plus those of generated files), anchorGen, encodeGenText and
// rebuildGen for a hit, and dropBundles, which frees a module's function
// bundles once its check is done.
//
// How: the generated file (gen's "<module dir>/gen/<target>/gen.mk") gets
// its anchors when stage 4 makes it, and a hit rebuilds it from the stored
// text, parses it and adds the same anchors before the interface decodes.
// The anchors of generated files live in a layer beside the parse
// summaries' set, which is fixed after the parse step and read lock-free;
// the layer is locked, and is read only for a file or key the base set
// lacks. What reruns a stored gen module is the module's own key: its
// tokens (the gen lines, the declarations a generator reads of its own
// module) are in the prefix, and every read a generator run made of another
// module (a signature, fields, a body it entered, a doc comment's file, a
// data file) is among its external reads, so an edit of a body a generator
// only read the signature of hits, and an edit of that signature misses.

package driver

import (
	"path"
	"runtime"
	"sync"
	"sync/atomic"

	"mk/internal/ast"
	"mk/internal/iface"
	"mk/internal/source"
	"mk/internal/types"
)

// anchorLayers is the module cache's Anchors: the parse summaries' set,
// complete after the parse step, and the generated files' anchors, added
// while modules run.
type anchorLayers struct {
	base     *iface.AnchorSet
	hasExtra atomic.Bool
	mu       sync.RWMutex
	extra    *iface.AnchorSet
}

func newAnchorLayers(base *iface.AnchorSet) *anchorLayers {
	return &anchorLayers{base: base, extra: iface.NewAnchorSet()}
}

// Anchor implements iface.Anchors.
func (l *anchorLayers) Anchor(k iface.AnchorKey) (iface.Anchor, bool) {
	if a, ok := l.base.Anchor(k); ok {
		return a, true
	}
	if !l.hasExtra.Load() {
		return iface.Anchor{}, false
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.extra.Anchor(k)
}

// Locate implements iface.Anchors. A generated file has no anchor in the
// base set, so the base answers every other file without the lock.
func (l *anchorLayers) Locate(file source.FileID, off uint32) (iface.AnchorKey, bool) {
	if k, ok := l.base.Locate(file, off); ok {
		return k, true
	}
	if !l.hasExtra.Load() {
		return "", false
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.extra.Locate(file, off)
}

// LocateAnchor implements iface's fast single-lookup anchor locator.
func (l *anchorLayers) LocateAnchor(file source.FileID, off uint32) (iface.AnchorKey, iface.Anchor, bool, bool) {
	if k, a, leaf, ok := l.base.LocateAnchor(file, off); ok {
		return k, a, leaf, true
	}
	if !l.hasExtra.Load() {
		return "", iface.Anchor{}, false, false
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.extra.LocateAnchor(file, off)
}

// addFile adds the anchors of a generated file: its file anchor and its
// items' anchors, as a parse summary gives a source file's.
func (l *anchorLayers) addFile(sum *iface.ParseSummary) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.extra.Add(iface.FileAnchorKey(sum.FileAnchor.Path), sum.FileAnchor)
	for _, it := range sum.Items {
		l.extra.Add(it.Key, it.Anchor)
	}
	l.hasExtra.Store(true)
}

// anchorGen adds the anchors of a generated file f, parsed as a, and
// returns its parse summary (nil with the cache off).
func (fc *frontCache) anchorGen(w *world, f *source.File, a *ast.File) *iface.ParseSummary {
	if fc == nil || f == nil || a == nil {
		return nil
	}
	sum := w.summarize(&fileNode{src: f, ast: a})
	fc.anchors.addFile(sum)
	return sum
}

// genTextDomain hashes a stored generated file (Interface.GenText).
const genTextDomain = "mk.driver.gentext.v1"

// encodeGenText is the stored generated file: its virtual path, its text
// and its parse summary, whose anchors a hit adds without parsing it.
func encodeGenText(p, text string, sum *iface.ParseSummary) []byte {
	var e iface.Encoder
	e.String(p)
	e.String(text)
	e.Bytes(iface.EncodeParseSummary(sum))
	return e.Out()
}

func decodeGenText(b []byte) (string, string, []byte, bool) {
	d := iface.NewDecoder(b)
	p := d.String()
	text := d.String()
	sum := d.Bytes()
	if d.Done() != nil {
		return "", "", nil, false
	}
	return p, text, sum, true
}

// rebuildGen is a hit's half of GenRef: the generated file stored in the
// interface's frame (raw, empty for a module with no gen output), added to
// the session's sources at its virtual path and anchored by its stored
// parse summary, before the interface decodes. False when the file cannot
// be rebuilt as stored (a miss).
func (fc *frontCache) rebuildGen(w *world, m *modNode, raw []byte) bool {
	if len(raw) == 0 {
		return true
	}
	p, text, sb, ok := decodeGenText(raw)
	if !ok || p != genVirtualPath(w, m) {
		return false
	}
	sum, err := iface.DecodeParseSummary(sb)
	if err != nil || sum.FileAnchor.Path != p || sum.ContentHash != source.HashString("mk.src.v1", text) {
		return false
	}
	if _, dup := w.sm.ByPath(p); dup {
		return false // made already in this session: the module ran
	}
	w.sm.Add(p, []byte(text))
	if !sum.Bind(w.sm) {
		return false
	}
	fc.anchors.addFile(sum)
	return true
}

// genVirtualPath is gen's virtual path of m's generated file (gen
// output.go virtualPath, the first Run's): "<dir of its first file>/gen/
// <target>/gen.mk".
func genVirtualPath(w *world, m *modNode) string {
	dir := ""
	if m.mod != nil && len(m.mod.Files) > 0 {
		if f := w.sm.File(m.mod.Files[0]); f != nil {
			dir = path.Dir(f.Path)
		}
	} else if len(m.files) > 0 && m.files[0].src != nil {
		dir = path.Dir(m.files[0].src.Path)
	}
	return path.Join(dir, "gen", w.cfg.Target.Name, "gen.mk")
}

// bodyPart answers a ReadBody read as SessionWorld does, memoizing the
// hash per body: a body is fixed once ready, and a generator's bodies are
// read by every module its gen lines run in, so a warm check would hash
// each of them once per module.
func (fw *frontWorld) bodyPart(r types.Read) (source.Hash, bool) {
	sw := fw.SessionWorld
	if sw.Body == nil || fw.w.front == nil {
		return sw.Part(r)
	}
	id, ok := fw.w.items.ByKey(r.Key)
	if !ok || !sw.Sees(fw.w.items.Get(id).Module) {
		return sw.Part(r)
	}
	b := sw.Body(id)
	if b == nil {
		return source.Hash{}, false
	}
	if h, ok := fw.w.front.bodyHashes.Load(b); ok {
		return h.(source.Hash), true
	}
	h, ok := sw.Part(r)
	if ok {
		fw.w.front.bodyHashes.Store(b, h)
	}
	return h, ok
}

// storeSlot takes one of the store slots, NumCPU of them, and returns its
// release (once). A store holds its slot from its encoding to the end of
// its writes, so at most NumCPU modules' encodings are in memory at once.
func (fc *frontCache) storeSlot() func() {
	fc.storeOnce.Do(func() { fc.storeSem = make(chan struct{}, max(2, runtime.NumCPU())) })
	fc.storeSem <- struct{}{}
	var once sync.Once
	return func() { once.Do(func() { <-fc.storeSem }) }
}

// dropBundles forgets the function-result bundles m's unit lookups read
// (its function index's), once m's check is done:
// fc.bundles kept every bundle read for the session, and on an edit that
// reruns every module (the 1M synth's signature edit) that was every stored
// result beside the session's own. A result a reused unit took keeps only
// its own bytes.
func (fc *frontCache) dropBundles(m *modNode) {
	if fc == nil {
		return
	}
	if m.fidx == nil {
		return
	}
	fc.mu.Lock()
	for _, bk := range m.fidx.refs {
		delete(fc.bundles, bk)
	}
	fc.mu.Unlock()
}

// moduleClosure is a module and its dependency closure, as the validation
// worlds read it: mods the module first, in its paths.
type moduleClosure struct {
	mods []*modNode
	in   map[string]bool
}

// closureOf is m's moduleClosure, memoized once the semantics exist (every
// edge, std.rt's implicit one included, is set by then): fc.world built it
// for every unit lookup of a module.
func (fc *frontCache) closureOf(w *world, m *modNode) *moduleClosure {
	if w.sem5 != nil {
		if v, ok := fc.closures.Load(m); ok {
			return v.(*moduleClosure)
		}
	}
	cl := &moduleClosure{mods: append([]*modNode{m}, closure(m)...), in: map[string]bool{}}
	for _, d := range cl.mods {
		cl.in[d.path] = true
	}
	if w.sem5 != nil {
		fc.closures.Store(m, cl)
	}
	return cl
}
