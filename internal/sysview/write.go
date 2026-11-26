// Why: a view archive is committed and embedded in mk, so the same inputs
// must give the same bytes, and a build must open it without decoding it.
//
// What: Write, which encodes a view (Meta and Items) as an archive, Options
// (the text chunk size and whether the index is compressed), and Identity,
// the hash of a view's content that names the archive whatever its options.
//
// How: the archive is a header (magic, format version, identity, flags,
// the metadata's JSON), an index of fixed-width little-endian tables
// (string offsets, item offsets, the folded-name table sorted for binary
// search) with a string blob and an item blob of varints, then the item
// texts in chunks, each compressed with DEFLATE on its own. Strings are
// numbered in order of first use, items keep the order they are given, and
// nothing holds a time or a path, so the bytes are a function of the input
// (and of Go's DEFLATE, which the identity does not depend on).

package sysview

import (
	"bytes"
	"compress/flate"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// magic starts every archive.
const magic = "MKSV"

// flagFlateIndex marks an index stored compressed.
const flagFlateIndex = 1

// Options choose an archive's layout; they do not change its identity.
type Options struct {
	// ChunkSize is the most text bytes one chunk holds (an item larger than
	// it is a chunk alone); 0 puts every text in one chunk.
	ChunkSize int
	// FlateIndex stores the index compressed: smaller, but every open
	// inflates it.
	FlateIndex bool
}

// DefaultOptions is the layout the generator writes (measured: see
// doc/compiler.md, "System views").
var DefaultOptions = Options{ChunkSize: 16 << 10, FlateIndex: false}

// Identity is the SHA-256 of a view's content: its metadata's JSON and
// every item's fields and text, in order, each length-prefixed.
func Identity(m Meta, items []Item) ([32]byte, error) {
	mj, err := json.Marshal(m)
	if err != nil {
		return [32]byte{}, err
	}
	h := sha256.New()
	var buf []byte
	str := func(s string) {
		buf = binary.AppendUvarint(buf[:0], uint64(len(s)))
		h.Write(buf)
		h.Write([]byte(s))
	}
	num := func(n int) {
		buf = binary.AppendUvarint(buf[:0], uint64(n))
		h.Write(buf)
	}
	str("mk.sysview.identity.v1")
	str(string(mj))
	num(len(items))
	for _, it := range items {
		str(it.Name)
		str(it.Kind)
		num(len(it.Defs))
		for _, d := range it.Defs {
			str(d)
		}
		num(len(it.Deps))
		for _, d := range it.Deps {
			num(d.View)
			num(d.Item)
		}
		str(it.Text)
		num(itemFlags(it))
		str(it.Link.archiveLib())
	}
	var id [32]byte
	h.Sum(id[:0])
	return id, nil
}

// itemFlags packs an item's booleans: bit 0 Raw, bit 1 Link.Lazy.
func itemFlags(it Item) int {
	f := 0
	if it.Raw {
		f |= 1
	}
	if it.Link.Lazy {
		f |= 2
	}
	return f
}

// Write encodes a view. Every Ref must name an item that exists in its
// own view (View 0) and a view of m.Requires; the names of an item must not
// be empty.
func Write(m Meta, items []Item, o Options) ([]byte, error) {
	if m.Name == "" || strings.ContainsAny(m.Name, ". /\\") {
		return nil, fmt.Errorf("sysview: view name %q is not one import path segment", m.Name)
	}
	for i, it := range items {
		if it.Name == "" || len(it.Defs) == 0 {
			return nil, fmt.Errorf("sysview: view %s: item %d defines no name", m.Name, i)
		}
		if strings.Contains(it.Link.Lib, "@") || strings.Contains(it.Link.Version, "@") || it.Link.Version != "" && it.Link.Lib == "" {
			return nil, fmt.Errorf("sysview: view %s: item %s has an invalid library or symbol version", m.Name, it.Name)
		}
		for _, d := range it.Deps {
			if d.View < 0 || d.View > len(m.Requires) || d.Item < 0 || d.View == 0 && d.Item >= len(items) {
				return nil, fmt.Errorf("sysview: view %s: item %s refers to %v, which is not an item", m.Name, it.Name, d)
			}
		}
	}
	id, err := Identity(m, items)
	if err != nil {
		return nil, err
	}
	mj, _ := json.Marshal(m)

	// The string table, numbered in order of first use.
	strIdx := map[string]uint32{}
	var strs []string
	intern := func(s string) uint32 {
		if i, ok := strIdx[s]; ok {
			return i
		}
		i := uint32(len(strs))
		strIdx[s] = i
		strs = append(strs, s)
		return i
	}
	// The texts, cut into chunks between items.
	type place struct{ chunk, off, n int }
	places := make([]place, len(items))
	var chunks [][]byte
	var cur []byte
	for i, it := range items {
		if o.ChunkSize > 0 && len(cur) > 0 && len(cur)+len(it.Text) > o.ChunkSize {
			chunks, cur = append(chunks, cur), nil
		}
		places[i] = place{len(chunks), len(cur), len(it.Text)}
		cur = append(cur, it.Text...)
	}
	chunks = append(chunks, cur)

	var blob []byte
	itemOff := make([]uint32, 0, len(items)+1)
	// The folded-name table points at the defined names themselves; a
	// reader folds them as it searches.
	type foldEnt struct {
		s, def string
		item   uint32
	}
	var folds []foldEnt
	for i, it := range items {
		itemOff = append(itemOff, uint32(len(blob)))
		blob = binary.AppendUvarint(blob, uint64(intern(it.Name)))
		blob = binary.AppendUvarint(blob, uint64(intern(it.Kind)))
		blob = binary.AppendUvarint(blob, uint64(len(it.Defs)))
		seen := map[string]bool{}
		for _, d := range it.Defs {
			blob = binary.AppendUvarint(blob, uint64(intern(d)))
			if f := Fold(d); !seen[f] {
				seen[f] = true
				folds = append(folds, foldEnt{f, d, uint32(i)})
			}
		}
		blob = binary.AppendUvarint(blob, uint64(len(it.Deps)))
		for _, d := range it.Deps {
			blob = binary.AppendUvarint(blob, uint64(d.View))
			blob = binary.AppendUvarint(blob, uint64(d.Item))
		}
		p := places[i]
		blob = binary.AppendUvarint(blob, uint64(p.chunk))
		blob = binary.AppendUvarint(blob, uint64(p.off))
		blob = binary.AppendUvarint(blob, uint64(p.n))
		blob = binary.AppendUvarint(blob, uint64(itemFlags(it)))
		lib := uint64(0)
		if it.Link.Lib != "" {
			lib = uint64(intern(it.Link.archiveLib())) + 1
		}
		blob = binary.AppendUvarint(blob, lib)
	}
	itemOff = append(itemOff, uint32(len(blob)))
	slices.SortFunc(folds, func(a, b foldEnt) int {
		if c := strings.Compare(a.s, b.s); c != 0 {
			return c
		}
		return int(a.item) - int(b.item)
	})

	var idx []byte
	u32 := func(v uint32) { idx = binary.LittleEndian.AppendUint32(idx, v) }
	u32(uint32(len(strs)))
	u32(uint32(len(items)))
	u32(uint32(len(folds)))
	off := uint32(0)
	for _, s := range strs {
		u32(off)
		off += uint32(len(s))
	}
	u32(off)
	for _, v := range itemOff {
		u32(v)
	}
	for _, f := range folds {
		u32(strIdx[f.def])
		u32(f.item)
	}
	for _, s := range strs {
		idx = append(idx, s...)
	}
	idx = append(idx, blob...)

	flags := uint64(0)
	if o.FlateIndex {
		flags |= flagFlateIndex
		if idx, err = deflate(idx); err != nil {
			return nil, err
		}
	}
	var out []byte
	out = append(out, magic...)
	out = binary.AppendUvarint(out, FormatVersion)
	out = append(out, id[:]...)
	out = binary.AppendUvarint(out, flags)
	out = binary.AppendUvarint(out, uint64(len(mj)))
	out = append(out, mj...)
	out = binary.AppendUvarint(out, uint64(len(idx)))
	out = append(out, idx...)
	out = binary.AppendUvarint(out, uint64(len(chunks)))
	for _, c := range chunks {
		z, err := deflate(c)
		if err != nil {
			return nil, err
		}
		out = binary.AppendUvarint(out, uint64(len(c)))
		out = binary.AppendUvarint(out, uint64(len(z)))
		out = append(out, z...)
	}
	return out, nil
}

// deflate compresses b at the best compression.
func deflate(b []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, err := flate.NewWriter(&buf, flate.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(b); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
