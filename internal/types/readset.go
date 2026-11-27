// Why: the incremental cache reuses a result only while everything it read
// is unchanged, including what it looked for and did not find.
//
// What: ReadKind (with its negative kinds), Read, and ReadSet (Add, Merge,
// Sorted, Hash).
//
// How: a read names an item by stable key with the hash of the part read; a
// negative read records an absence (no impl, no item). A ReadSet keeps each
// read once, by scanning while small and through a map past readSetScan.

package types

import (
	"cmp"
	"slices"
	"strings"

	"mk/internal/source"
)

// ReadKind is what part of the world a read looked at.
type ReadKind uint8

const (
	// ReadSignature: the signature of Key (API part).
	ReadSignature ReadKind = iota + 1
	// ReadShape: the shape record of type item Key (shape part).
	ReadShape
	// ReadFields: the exported fields of Key (API part); with Name "all",
	// all its fields, the view of Key's own module (a gen line there) or of a
	// borrow walk through private fields.
	ReadFields
	// ReadValue: the value of const Key.
	ReadValue
	// ReadBody: the MIR body of Key, entered by an evaluation or inlined.
	ReadBody
	// ReadLayout: the full layout of Key with generic arguments Name (an
	// evaluation that read memory of that type).
	ReadLayout
	// ReadImpl: the impl of interface Key for type Name exists; Hash is the
	// impl's API hash.
	ReadImpl
	// ReadNoImpl: no impl of interface Key for type Name exists (negative).
	ReadNoImpl
	// ReadNoItem: module or type Key has no item or member Name (negative).
	ReadNoItem
	// ReadExports: the export list of module Key, or the member list of type
	// Key (glob-like readers, a member lookup that missed).
	ReadExports
	// ReadTarget: Target field Name; Hash is the hash of its value.
	ReadTarget
	// ReadPkg: pkg.mk field Name; Hash is the hash of its value.
	ReadPkg
	// ReadFile: file Name (a generator or C import input); Hash is its content
	// hash, the zero Hash for "no such file" (negative).
	ReadFile
	// ReadData: a data file read through build.File or Gen.Input: Key is
	// ModuleKey of the reading module, Name the path as written, relative to
	// its package root; Hash is source.HashBytes("mk.data.v1", bytes), or for a
	// failed read source.HashBytes("mk.datafail.v1", reason, a zero byte,
	// path).
	ReadData

	numReadKinds
)

var readKindNames = [numReadKinds]string{"", "signature", "shape", "fields", "value", "body", "layout",
	"impl", "no-impl", "no-item", "exports", "target", "pkg", "file", "data"}

// IsValid reports whether k names a recorded dependency.
func (k ReadKind) IsValid() bool { return k > 0 && k < numReadKinds }

// String names the kind.
func (k ReadKind) String() string {
	if k < numReadKinds {
		return readKindNames[k]
	}
	return "read?"
}

// IsNegative reports whether the read recorded an absence: it is invalidated
// when the thing appears.
func (k ReadKind) IsNegative() bool { return k == ReadNoImpl || k == ReadNoItem }

// Read is one recorded read: what was read (Kind, Key, Name) and the hash of
// what was found. A reader is invalid exactly when, for one of its reads, the
// current hash differs (or, for a negative read, the thing now exists).
// Comparable; never holds an ID.
type Read struct {
	Kind ReadKind
	Key  Key
	// Name qualifies the read: the missing item's name, the impl's target
	// type in canonical spelling, a Target or pkg.mk field, a file path.
	Name string
	Hash source.Hash
}

// Compare orders reads by kind, key, name, then hash: the one order in which
// a read set is stored and hashed.
func (r Read) Compare(o Read) int {
	return cmp.Or(cmp.Compare(r.Kind, o.Kind), r.Key.Compare(o.Key), strings.Compare(r.Name, o.Name), r.Hash.Compare(o.Hash))
}

// ReadSet is the reads of one item or evaluation, deduplicated. The zero
// ReadSet is empty and ready. Not safe for concurrent writes: each worker owns
// its item's set.
type ReadSet struct {
	reads []Read
	// seen indexes reads once there are more than readSetScan of them; below
	// that, Add scans reads. Either way reads holds no duplicate.
	seen map[Read]struct{}
}

// readSetScan is the size up to which Add finds a repeat by scanning reads
// instead of a map: most read sets are small, and a map for each was the
// front end's largest allocation.
const readSetScan = 16

// Add records a read; a repeated read is ignored.
func (s *ReadSet) Add(r Read) {
	if !r.Kind.IsValid() {
		source.ICEf(source.Span{}, "ReadSet.Add: kind %d", r.Kind)
	}
	if s.seen != nil {
		if _, ok := s.seen[r]; ok {
			return
		}
		s.seen[r] = struct{}{}
		s.reads = append(s.reads, r)
		return
	}
	if slices.Contains(s.reads, r) {
		return
	}
	s.reads = append(s.reads, r)
	if len(s.reads) > readSetScan {
		s.seen = make(map[Read]struct{}, 2*len(s.reads))
		for _, x := range s.reads {
			s.seen[x] = struct{}{}
		}
	}
}

// Merge adds every read of o.
func (s *ReadSet) Merge(o *ReadSet) {
	for _, r := range o.reads {
		s.Add(r)
	}
}

// Len is the number of distinct reads.
func (s *ReadSet) Len() int { return len(s.reads) }

// Sorted returns the reads in canonical order (Read.Compare), a new slice.
func (s *ReadSet) Sorted() []Read {
	out := slices.Clone(s.reads)
	slices.SortFunc(out, Read.Compare)
	return out
}

// Hash is the stable hash of the sorted reads, in the domain "mk.reads.v1".
func (s *ReadSet) Hash() source.Hash {
	h := source.NewHasher("mk.reads.v1")
	rs := s.Sorted()
	h.Uvarint(uint64(len(rs)))
	for _, r := range rs {
		h.Uvarint(uint64(r.Kind))
		h.String(r.Key.String())
		h.String(r.Name)
		h.Hash(r.Hash)
	}
	return h.Sum()
}
