// Why: every build that imports a system view opens its archive, so opening
// must cost nearly nothing, and a corrupt or newer archive must be an error,
// never a crash.
//
// What: Archive, Open (header checks, the index's tables taken in place),
// and the reads: Meta, ID, Len, Item, ItemDeps, Lookup and Text (the texts
// of a set of items, inflating only the chunks that hold them).
//
// How: the index's fixed-width tables are sliced out of the archive's bytes
// and read with binary.LittleEndian on demand; an item's record is decoded
// only when it is visited; a name is found by binary search over the
// folded-name table. Every offset is bounds-checked before use.

package sysview

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Archive is an opened view archive. It reads data in place; data must not
// change while the Archive is used.
type Archive struct {
	meta  Meta
	id    [32]byte
	nStr  int
	nItem int
	nFold int
	// strOff, itemOff and folds are the index's tables; strs and items its
	// blobs.
	strOff, itemOff, folds []byte
	strs, items            []byte
	chunks                 []chunk
}

// chunk is one compressed run of item texts.
type chunk struct {
	raw  int
	data []byte
}

// maxChunk bounds a chunk's inflated size, so a corrupt length never asks
// for a huge buffer (Write's chunks are far smaller; a one-chunk archive of
// the largest view holds under 8 MiB).
const maxChunk = 64 << 20

// errCorrupt is the error of an archive whose bytes do not hold together.
var errCorrupt = errors.New("sysview: the archive is corrupt")

// reader walks the bytes of an archive.
type reader struct {
	b   []byte
	bad bool
}

func (r *reader) uvarint() int {
	v, n := binary.Uvarint(r.b)
	if n <= 0 || v > 1<<31 {
		r.bad = true
		return 0
	}
	r.b = r.b[n:]
	return int(v)
}

func (r *reader) take(n int) []byte {
	if n < 0 || n > len(r.b) {
		r.bad = true
		return nil
	}
	out := r.b[:n:n]
	r.b = r.b[n:]
	return out
}

// Open reads an archive's header and index. It refuses bytes that are not
// an archive, an archive of a newer format, and one whose tables do not fit.
func Open(data []byte) (*Archive, error) {
	if !bytes.HasPrefix(data, []byte(magic)) {
		return nil, errors.New("sysview: not a system view archive")
	}
	r := &reader{b: data[len(magic):]}
	if v := r.uvarint(); r.bad || v != FormatVersion {
		if !r.bad && v > FormatVersion {
			return nil, fmt.Errorf("sysview: the archive has format %d, newer than this mk reads (%d): use the mk that generated it, or regenerate it with go run ./tools/sysview", v, FormatVersion)
		}
		return nil, errCorrupt
	}
	a := &Archive{}
	copy(a.id[:], r.take(32))
	flags := r.uvarint()
	mj := r.take(r.uvarint())
	idx := r.take(r.uvarint())
	if r.bad || json.Unmarshal(mj, &a.meta) != nil {
		return nil, errCorrupt
	}
	if flags&flagFlateIndex != 0 {
		raw, err := io.ReadAll(io.LimitReader(flate.NewReader(bytes.NewReader(idx)), maxChunk))
		if err != nil {
			return nil, errCorrupt
		}
		idx = raw
	}
	nc := r.uvarint()
	for i := 0; i < nc && !r.bad; i++ {
		raw := r.uvarint()
		if raw > maxChunk {
			return nil, errCorrupt
		}
		a.chunks = append(a.chunks, chunk{raw: raw, data: r.take(r.uvarint())})
	}
	if r.bad || len(r.b) != 0 || !a.index(idx) {
		return nil, errCorrupt
	}
	return a, nil
}

// index slices the index's tables out of idx.
func (a *Archive) index(idx []byte) bool {
	if len(idx) < 12 {
		return false
	}
	a.nStr = int(binary.LittleEndian.Uint32(idx))
	a.nItem = int(binary.LittleEndian.Uint32(idx[4:]))
	a.nFold = int(binary.LittleEndian.Uint32(idx[8:]))
	r := &reader{b: idx[12:]}
	a.strOff = r.take(4 * (a.nStr + 1))
	a.itemOff = r.take(4 * (a.nItem + 1))
	a.folds = r.take(8 * a.nFold)
	if r.bad {
		return false
	}
	a.strs = r.take(int(a.u32(a.strOff, a.nStr)))
	a.items = r.b
	return !r.bad && int(a.u32(a.itemOff, a.nItem)) == len(a.items)
}

func (a *Archive) u32(tab []byte, i int) uint32 { return binary.LittleEndian.Uint32(tab[4*i:]) }

// str is string number i of the table.
func (a *Archive) str(i int) []byte {
	if i < 0 || i >= a.nStr {
		return nil
	}
	lo, hi := a.u32(a.strOff, i), a.u32(a.strOff, i+1)
	if lo > hi || int(hi) > len(a.strs) {
		return nil
	}
	return a.strs[lo:hi]
}

// Meta is the view's metadata.
func (a *Archive) Meta() Meta { return a.meta }

// ID is the view's identity (Identity of its content).
func (a *Archive) ID() [32]byte { return a.id }

// Len is the number of items.
func (a *Archive) Len() int { return a.nItem }

// record is an item's decoded index entry, its text not read.
type record struct {
	name, kind, lib int
	defs            []int
	deps            []Ref
	chunk, off, n   int
	flags           int
}

// record decodes item i's entry.
func (a *Archive) record(i int) (record, error) {
	if i < 0 || i >= a.nItem {
		return record{}, fmt.Errorf("sysview: view %s has no item %d", a.meta.Name, i)
	}
	lo, hi := a.u32(a.itemOff, i), a.u32(a.itemOff, i+1)
	if lo > hi || int(hi) > len(a.items) {
		return record{}, errCorrupt
	}
	r := &reader{b: a.items[lo:hi]}
	var e record
	e.name, e.kind = r.uvarint(), r.uvarint()
	nd := r.uvarint()
	for k := 0; k < nd && !r.bad; k++ {
		e.defs = append(e.defs, r.uvarint())
	}
	np := r.uvarint()
	for k := 0; k < np && !r.bad; k++ {
		v := r.uvarint()
		e.deps = append(e.deps, Ref{View: v, Item: r.uvarint()})
	}
	e.chunk, e.off, e.n, e.flags = r.uvarint(), r.uvarint(), r.uvarint(), r.uvarint()
	e.lib = r.uvarint()
	if r.bad || e.chunk >= len(a.chunks) || e.off+e.n > a.chunks[e.chunk].raw {
		return record{}, errCorrupt
	}
	return e, nil
}

// ItemDeps are item i's dependencies and whether its text refers to
// std.raw.
func (a *Archive) ItemDeps(i int) ([]Ref, bool, error) {
	e, err := a.record(i)
	return e.deps, e.flags&1 != 0, err
}

// Item decodes item i whole, its text included (one chunk inflated).
func (a *Archive) Item(i int) (Item, error) {
	e, err := a.record(i)
	if err != nil {
		return Item{}, err
	}
	texts, err := a.Text([]int{i})
	if err != nil {
		return Item{}, err
	}
	it := Item{Name: string(a.str(e.name)), Kind: string(a.str(e.kind)), C: CText(texts[0]),
		Deps: e.deps, Text: texts[0], Raw: e.flags&1 != 0}
	for _, d := range e.defs {
		it.Defs = append(it.Defs, string(a.str(d)))
	}
	if e.lib > 0 {
		it.Link = Link{Lib: string(a.str(e.lib - 1)), Lazy: e.flags&2 != 0}
	}
	return it, nil
}

// Lookup is the items that define a name whose Fold is Fold(name), in
// item order.
func (a *Archive) Lookup(name string) []int {
	key := []byte(Fold(name))
	foldStr := func(k int) []byte { return a.str(int(binary.LittleEndian.Uint32(a.folds[8*k:]))) }
	lo, hi := 0, a.nFold
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if foldCompare(foldStr(mid), key) < 0 {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	var out []int
	for k := lo; k < a.nFold && foldCompare(foldStr(k), key) == 0; k++ {
		out = append(out, int(binary.LittleEndian.Uint32(a.folds[8*k+4:])))
	}
	return out
}

// Text returns the texts of items, in the order given, inflating each chunk
// that holds one of them once.
func (a *Archive) Text(items []int) ([]string, error) {
	recs := make([]record, len(items))
	need := map[int][]byte{}
	for k, i := range items {
		e, err := a.record(i)
		if err != nil {
			return nil, err
		}
		recs[k] = e
		need[e.chunk] = nil
	}
	for c := range need {
		raw := make([]byte, a.chunks[c].raw)
		if _, err := io.ReadFull(flate.NewReader(bytes.NewReader(a.chunks[c].data)), raw); err != nil {
			return nil, errCorrupt
		}
		need[c] = raw
	}
	out := make([]string, len(items))
	for k, e := range recs {
		out[k] = string(need[e.chunk][e.off : e.off+e.n])
	}
	return out, nil
}

// foldCompare compares Fold(name) with key, an already folded name,
// without building the folded string.
func foldCompare(name, key []byte) int {
	k := 0
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c == '_' {
			continue
		}
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		switch {
		case k == len(key):
			return 1
		case c < key[k]:
			return -1
		case c > key[k]:
			return 1
		}
		k++
	}
	if k < len(key) {
		return -1
	}
	return 0
}
