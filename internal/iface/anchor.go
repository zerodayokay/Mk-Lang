// Why: cached records hold source positions, and an absolute byte offset
// would go stale whenever an edit above it adds a comment, a blank line or a
// line. Positions stored relative to tokens survive such edits.
//
// What: AnchorKey (an item key, or FileAnchorKey for text outside items),
// Anchor (an item's non-trivia token offsets and token hash), the Anchors
// interface, its list implementation AnchorSet, and AnchorUse.
//
// How: a stored position is a token ordinal plus a byte delta within the
// anchor (ordinal and offset invert each other). Decoding rebases it onto the
// current tokens of the same key; a changed token hash makes it stale.

package iface

import (
	"slices"
	"sort"
	"strings"
	"sync"

	"mk/internal/source"
)

// AnchorKey is an item key, or a file's package-relative path for spans
// outside items ("file:" + path).
type AnchorKey string

// FileAnchorKey is the anchor key of the tokens of a file outside its items.
func FileAnchorKey(path string) AnchorKey { return AnchorKey("file:" + path) }

// IsFile reports whether k is a file anchor.
func (k AnchorKey) IsFile() bool { return strings.HasPrefix(string(k), "file:") }

// Anchor is one item's non-trivia tokens, in order: their file, and each
// token's start and end byte offset. TokenHash is the item's mk.tokens
// hash. File is a session ID and is never encoded: a stored form
// holds Path, Starts, Ends and TokenHash, and decoding sets File from
// Env.Sources by Path. Starts is strictly increasing and Ends[i] >= Starts[i].
type Anchor struct {
	File      source.FileID
	Path      string
	Starts    []uint32
	Ends      []uint32
	TokenHash source.Hash
}

// contains reports whether off lies in the anchor's token range, from the
// first token's start to the last token's end inclusive.
func (a *Anchor) contains(off uint32) bool {
	n := len(a.Starts)
	return n > 0 && a.Starts[0] <= off && off <= a.Ends[n-1]
}

// ordinal is the number of tokens whose start is at or before off: 0 means
// before the first token (the delta is then from offset 0 of the file).
func (a *Anchor) ordinal(off uint32) (ord uint32, delta uint32) {
	i := sort.Search(len(a.Starts), func(i int) bool { return a.Starts[i] > off })
	if i == 0 {
		return 0, off
	}
	return uint32(i), off - a.Starts[i-1]
}

// offset inverts ordinal against the current tokens; ok is false for an
// ordinal past the anchor's tokens.
func (a *Anchor) offset(ord, delta uint32) (uint32, bool) {
	if ord == 0 {
		return delta, true
	}
	if int(ord) > len(a.Starts) {
		return 0, false
	}
	off := uint64(a.Starts[ord-1]) + uint64(delta)
	if off > 0xffffffff {
		return 0, false
	}
	return uint32(off), true
}

// Anchors maps a stable key to the current non-trivia token offsets of that
// item, or of a file for spans outside items, so a span stored relative to
// tokens is rebased onto the current text. The driver implements it over the
// parse; AnchorSet is the implementation over a list.
type Anchors interface {
	// Anchor returns the current anchor of k.
	Anchor(k AnchorKey) (Anchor, bool)
	// Locate returns the anchor an encoder uses for a position no preferred
	// anchor contains: the innermost item anchor of file whose token range
	// contains off, else the file's own anchor.
	Locate(file source.FileID, off uint32) (AnchorKey, bool)
}

// anchorSpan is one indexed item anchor of a file, sorted by start offset.
type anchorSpan struct {
	key      AnchorKey
	anchor   Anchor
	start    uint32
	end      uint32
	maxEnd   uint32
	hasChild bool // true if another item anchor in the file overlaps [start, end]
}

// AnchorSet is an Anchors over anchors added one by one. Not safe for
// concurrent Add; safe for concurrent reads after the last Add.
type AnchorSet struct {
	m      map[AnchorKey]Anchor
	byFile map[source.FileID][]AnchorKey
	file   map[source.FileID]AnchorKey

	once    sync.Once
	indexed map[source.FileID][]anchorSpan
}

// NewAnchorSet returns an empty set.
func NewAnchorSet() *AnchorSet {
	return &AnchorSet{m: make(map[AnchorKey]Anchor), byFile: make(map[source.FileID][]AnchorKey),
		file: make(map[source.FileID]AnchorKey)}
}

// Add records the anchor of k, replacing an earlier one. A file anchor
// (FileAnchorKey) becomes its file's fallback for Locate.
func (s *AnchorSet) Add(k AnchorKey, a Anchor) {
	if len(a.Starts) != len(a.Ends) {
		source.ICEf(source.Span{}, "iface: anchor %s has %d starts and %d ends", k, len(a.Starts), len(a.Ends))
	}
	if _, had := s.m[k]; !had {
		if k.IsFile() {
			s.file[a.File] = k
		} else {
			s.byFile[a.File] = append(s.byFile[a.File], k)
		}
	}
	s.m[k] = a
	s.once = sync.Once{}
	s.indexed = nil
}

// buildIndex builds the sorted interval index per file once on first Locate.
func (s *AnchorSet) buildIndex() {
	idx := make(map[source.FileID][]anchorSpan, len(s.byFile))
	for file, keys := range s.byFile {
		es := make([]anchorSpan, 0, len(keys))
		for _, k := range keys {
			a := s.m[k]
			if len(a.Starts) == 0 {
				continue
			}
			es = append(es, anchorSpan{
				key:    k,
				anchor: a,
				start:  a.Starts[0],
				end:    a.Ends[len(a.Ends)-1],
			})
		}
		slices.SortFunc(es, func(a, b anchorSpan) int {
			if a.start != b.start {
				if a.start < b.start {
					return -1
				}
				return 1
			}
			if a.end != b.end {
				if a.end > b.end {
					return -1
				}
				return 1
			}
			return strings.Compare(string(a.key), string(b.key))
		})
		var maxEnd uint32
		for i := range es {
			if i > 0 && maxEnd >= es[i].start {
				es[i].hasChild = true
				for j := i - 1; j >= 0 && es[j].maxEnd >= es[i].start; j-- {
					if es[j].end >= es[i].start {
						es[j].hasChild = true
					}
				}
			}
			if i == 0 || es[i].end > maxEnd {
				maxEnd = es[i].end
			}
			es[i].maxEnd = maxEnd
		}
		idx[file] = es
	}
	s.indexed = idx
}

// Anchor returns the anchor of k.
func (s *AnchorSet) Anchor(k AnchorKey) (Anchor, bool) {
	a, ok := s.m[k]
	return a, ok
}

// LocateAnchor returns the smallest item anchor of file containing off (ties by
// key bytes), else the file anchor, along with the anchor itself and whether
// it is a non-overlapping leaf item anchor (safe to cache across consecutive
// spans in the same range).
func (s *AnchorSet) LocateAnchor(file source.FileID, off uint32) (AnchorKey, Anchor, bool, bool) {
	s.once.Do(s.buildIndex)
	es := s.indexed[file]
	if len(es) > 0 {
		idx := sort.Search(len(es), func(i int) bool { return es[i].start > off })
		var best int
		var bestLen uint64
		found := false
		for i := idx - 1; i >= 0 && es[i].maxEnd >= off; i-- {
			if off > es[i].end {
				continue
			}
			n := uint64(es[i].end - es[i].start)
			if !found || n < bestLen || (n == bestLen && es[i].key < es[best].key) {
				best, bestLen, found = i, n, true
			}
		}
		if found {
			e := &es[best]
			return e.key, e.anchor, !e.hasChild, true
		}
	}
	if k, ok := s.file[file]; ok {
		return k, s.m[k], false, true
	}
	return "", Anchor{}, false, false
}

// Locate returns the smallest item anchor of file containing off (ties by
// key bytes), else the file anchor.
func (s *AnchorSet) Locate(file source.FileID, off uint32) (AnchorKey, bool) {
	k, _, _, ok := s.LocateAnchor(file, off)
	return k, ok
}

// AnchorUse names a foreign anchor a stored span uses, with the token hash it
// had when the span was stored; decoding returns ErrStale when the current
// token hash differs.
type AnchorUse struct {
	Key       AnchorKey
	TokenHash source.Hash
}

// sortAnchorUses orders by key bytes.
func sortAnchorUses(us []AnchorUse) {
	slices.SortFunc(us, func(a, b AnchorUse) int { return strings.Compare(string(a.Key), string(b.Key)) })
}
