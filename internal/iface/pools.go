// Why: stored records must not contain session IDs, which differ from one
// run to the next, yet must decode fast and refer to shared things once.
//
// What: Env (the session tables encoding reads and decoding fills), the
// record tags, and pools with its interning methods (str, sym, key, item,
// typ, errset, cnst, anchor), span and finish.
//
// How: a record is a header, then pools ordered by first use in one fixed
// walk of the record, then the body referring to pool entries by index
// (index 0 meaning none). Spans are written relative to anchors. The bytes
// depend on the record's content only, never on intern order.

package iface

import (
	"mk/internal/source"
	"mk/internal/types"
)

// Env is what encoding reads and decoding re-interns into: the session's
// tables. Decoding a module adds its items to Items; foreign items must
// already be there. Anchors serves both directions: encoding rebases spans
// onto it, decoding maps them back and checks token hashes.
type Env struct {
	Interner *source.Interner
	Types    *types.Table
	Items    *types.Items
	Consts   *types.ConstTable
	// Sources maps package-relative paths to FileIDs (Module.Files, anchors).
	Sources *source.SourceMap
	// PointerBytes is the build target's, for ConstValue.Canonicalize.
	PointerBytes uint8
	Anchors      Anchors
}

// Record tags in the store-form header.
const (
	recValues    = 1 // a bare value bundle (tests)
	recInterface = 2
	recFunc      = 3
	recEval      = 4
	recGen       = 5
)

// Value-pool entry classes.
const (
	valType   = 1
	valErrSet = 2
	valConst  = 3
)

// pools collects the pools of one store-form record during its walk.
type pools struct {
	env *Env
	h   *Hasher
	// own is the module whose items the record defines (an interface's
	// module), NoModule for records that only refer to items.
	own types.ModuleID
	// prefer are the anchors tried first when rebasing a span (the record's
	// own item), before Env.Anchors.Locate.
	prefer        []AnchorKey
	preferDone    bool
	preferAnchors []poolAnchor
	lastKey       AnchorKey
	lastAnchor    Anchor
	lastLeaf      bool

	strs    []string
	strIdx  map[string]uint32
	keys    []string
	keyIdx  map[string]uint32
	items   []poolItem
	itemIdx map[types.ItemID]uint32
	anchors []poolAnchor
	ancIdx  map[AnchorKey]uint32
	vals    [][]byte
	tyIdx   map[types.TypeID]uint32
	esIdx   map[types.ErrorSetID]uint32
	cIdx    map[types.ConstID]uint32
}

type poolItem struct {
	key  uint32
	kind types.ItemKind
	own  bool
}

type poolAnchor struct {
	key AnchorKey
	a   Anchor
}

func newPools(h *Hasher, env *Env, own types.ModuleID) *pools {
	return &pools{env: env, h: h, own: own,
		strIdx: make(map[string]uint32), keyIdx: make(map[string]uint32),
		itemIdx: make(map[types.ItemID]uint32), ancIdx: make(map[AnchorKey]uint32),
		tyIdx: make(map[types.TypeID]uint32), esIdx: make(map[types.ErrorSetID]uint32),
		cIdx: make(map[types.ConstID]uint32)}
}

// str interns a string into the string pool; indices start at 1.
func (p *pools) str(s string) uint32 {
	if i, ok := p.strIdx[s]; ok {
		return i
	}
	p.strs = append(p.strs, s)
	i := uint32(len(p.strs))
	p.strIdx[s] = i
	return i
}

// sym is a symbol's string-pool index, 0 for NoSymbol.
func (p *pools) sym(s source.Symbol) uint32 {
	if s == source.NoSymbol {
		return 0
	}
	return p.str(p.env.Interner.Str(s))
}

// key is a key's key-pool index, 0 for the zero key.
func (p *pools) key(k string) uint32 {
	if k == "" {
		return 0
	}
	if i, ok := p.keyIdx[k]; ok {
		return i
	}
	p.keys = append(p.keys, k)
	i := uint32(len(p.keys))
	p.keyIdx[k] = i
	return i
}

// item is an item's item-pool index, 0 for NoItem.
func (p *pools) item(it types.ItemID) uint32 {
	if it == types.NoItem {
		return 0
	}
	if i, ok := p.itemIdx[it]; ok {
		return i
	}
	x := p.env.Items.Get(it)
	e := poolItem{key: p.key(x.Key.String()), kind: x.Kind, own: p.own != types.NoModule && x.Module == p.own}
	p.items = append(p.items, e)
	i := uint32(len(p.items))
	p.itemIdx[it] = i
	return i
}

// addVal appends a value entry (class byte, then its node) and returns its
// index. Children were added while the node was written, so they have lower
// indices.
func (p *pools) addVal(b []byte) uint32 {
	p.vals = append(p.vals, b)
	return uint32(len(p.vals))
}

// typ is a type's value-pool index, 0 for NoType.
func (p *pools) typ(t types.TypeID) uint32 {
	if t == types.NoType {
		return 0
	}
	if i, ok := p.tyIdx[t]; ok {
		return i
	}
	var e Encoder
	e.Uvarint(valType)
	w := walker{e: &e, h: p.h, p: p}
	w.writeType(p.env.Types.Get(t))
	i := p.addVal(e.Out())
	p.tyIdx[t] = i
	return i
}

// errset is an error set's value-pool index, 0 for the empty set.
func (p *pools) errset(es types.ErrorSetID) uint32 {
	if es == types.EmptyErrors {
		return 0
	}
	if i, ok := p.esIdx[es]; ok {
		return i
	}
	var e Encoder
	e.Uvarint(valErrSet)
	w := walker{e: &e, h: p.h, p: p}
	w.writeErrSet(p.env.Types.Members(es))
	i := p.addVal(e.Out())
	p.esIdx[es] = i
	return i
}

// cnst is a const's value-pool index, 0 for NoConst.
func (p *pools) cnst(c types.ConstID) uint32 {
	if c == types.NoConst {
		return 0
	}
	if i, ok := p.cIdx[c]; ok {
		return i
	}
	var e Encoder
	e.Uvarint(valConst)
	w := walker{e: &e, h: p.h, p: p}
	w.writeConst(p.env.Consts.Get(c))
	i := p.addVal(e.Out())
	p.cIdx[c] = i
	return i
}

// anchor is an anchor's pool index; the anchor's token hash is stored with
// it and checked on decode.
func (p *pools) anchor(k AnchorKey, a Anchor) uint32 {
	if i, ok := p.ancIdx[k]; ok {
		return i
	}
	p.anchors = append(p.anchors, poolAnchor{key: k, a: a})
	i := uint32(len(p.anchors))
	p.ancIdx[k] = i
	return i
}

// anchorFastLocator is optionally implemented by Anchors implementations
// (AnchorSet, driver.anchorLayers) to return the anchor and leaf status in a
// single lookup without a second map lookup.
type anchorFastLocator interface {
	LocateAnchor(file source.FileID, off uint32) (AnchorKey, Anchor, bool, bool)
}

// span writes a span as (anchor index, start ordinal, start delta, end
// ordinal, end delta); the zero span is anchor 0 alone. The anchor is the
// first preferred one whose file and token range hold the start, else
// Env.Anchors.Locate's. A span in a file with no anchor is an ICE: the
// driver gives every file an anchor.
func (p *pools) span(e *Encoder, sp source.Span) {
	if sp.File == source.NoFile {
		e.Uvarint(0)
		return
	}
	if sp.Start > sp.End {
		source.ICEf(sp, "iface: span with start after end")
	}
	if p.env.Anchors == nil {
		source.ICEf(sp, "iface: span stored with no Env.Anchors")
	}
	if !p.preferDone {
		p.preferDone = true
		for _, k := range p.prefer {
			if c, ok := p.env.Anchors.Anchor(k); ok {
				p.preferAnchors = append(p.preferAnchors, poolAnchor{key: k, a: c})
			}
		}
	}
	var key AnchorKey
	var a Anchor
	found := false
	for i := range p.preferAnchors {
		pa := &p.preferAnchors[i]
		if pa.a.File == sp.File && pa.a.contains(sp.Start) {
			key, a, found = pa.key, pa.a, true
			break
		}
	}
	if !found && p.lastLeaf && p.lastAnchor.File == sp.File && p.lastAnchor.contains(sp.Start) {
		key, a, found = p.lastKey, p.lastAnchor, true
	}
	if !found {
		if fl, ok := p.env.Anchors.(anchorFastLocator); ok {
			if k, c, isLeaf, ok := fl.LocateAnchor(sp.File, sp.Start); ok && c.File == sp.File {
				key, a, found = k, c, true
				if isLeaf {
					p.lastKey, p.lastAnchor, p.lastLeaf = k, c, true
				}
			}
		} else if k, ok := p.env.Anchors.Locate(sp.File, sp.Start); ok {
			if c, ok := p.env.Anchors.Anchor(k); ok && c.File == sp.File {
				key, a, found = k, c, true
			}
		}
	}
	if !found {
		source.ICEf(sp, "iface: no anchor for a stored span")
	}
	e.Uvarint(uint64(p.anchor(key, a)))
	so, sd := a.ordinal(sp.Start)
	eo, ed := a.ordinal(sp.End)
	e.Uvarint(uint64(so))
	e.Uvarint(uint64(sd))
	e.Uvarint(uint64(eo))
	e.Uvarint(uint64(ed))
}

// finish assembles header, pools and body: format version, record tag,
// strings, keys, items (key, kind, own), anchors (key, token hash), values
// (each length-prefixed), then the body bytes.
func (p *pools) finish(tag uint64, body []byte) []byte {
	var e Encoder
	e.Uvarint(FormatVersion)
	e.Uvarint(tag)
	e.Uvarint(uint64(len(p.strs)))
	for _, s := range p.strs {
		e.String(s)
	}
	e.Uvarint(uint64(len(p.keys)))
	for _, k := range p.keys {
		e.String(k)
	}
	e.Uvarint(uint64(len(p.items)))
	for _, it := range p.items {
		e.Uvarint(uint64(it.key))
		e.Uvarint(uint64(it.kind))
		e.Bool(it.own)
	}
	e.Uvarint(uint64(len(p.anchors)))
	for _, a := range p.anchors {
		e.String(string(a.key))
		e.Hash(a.a.TokenHash)
	}
	e.Uvarint(uint64(len(p.vals)))
	for _, v := range p.vals {
		e.Bytes(v)
	}
	e.raw(body)
	return e.Out()
}

// usedAnchors lists every anchor the record's spans used, by key.
func (p *pools) usedAnchors() []AnchorUse {
	out := make([]AnchorUse, len(p.anchors))
	for i, a := range p.anchors {
		out[i] = AnchorUse{Key: a.key, TokenHash: a.a.TokenHash}
	}
	sortAnchorUses(out)
	return out
}
