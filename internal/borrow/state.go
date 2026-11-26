// Why: the fixpoint keeps one state per block entry, each with an entry per
// root; copying whole states on every edge would make large bodies
// quadratic.
//
// What: initBits, entry (init, held, moved, changed, on, fv, sh, stored,
// cover),
// state with get, set, clone, freeze and equal, joinEntry, joinSh,
// joinStored, joinInto and takenToMoved.
//
// How: a state is a persistent vector of fixed-size chunks copied on write;
// the zero entry reads as {U}, holding nothing. A join is the pointwise
// union, after T becomes M at a block with two or more predecessors.

package borrow

// initBits is a subset of {U, I, M, T, N}: unassigned, initialized, moved,
// taken, narrowed (a temporary that holds its value, none of it linear on
// this path: a switch edge selected plain variants). Never read raw: initOf
// maps the zero value to {U}.
type initBits uint8

const (
	initU initBits = 1 << iota
	initI
	initM
	initT
	initN
)

// entry is one root's state. The zero entry means "unassigned, holds
// nothing": an absent, purged or never-assigned root.
type entry struct {
	init initBits
	// held is H(root); always empty for a non-carrier.
	held setID
	// moved is the move sites that reach here (E0401 grouping, E0405,
	// E0406); cleared by a whole assignment.
	moved setID
	// changed is the E0411 sites of a by-value parameter of a fallible
	// function; never cleared but by the error-edge restore of a fallible
	// call.
	changed setID
	// on is a superset of the loans whose place is rooted at this root and
	// that some root holds in this state: the state-local filter of an
	// access's candidates. Only checker.put and its helpers write it (access.go).
	on setID
	// fv is the root's per-field held sets when it is split (fields.go), 0
	// when whole; every field's set is a subset of held, which stays the
	// union.
	fv fvID
	// sh is the shallow marks (marks.go): the loans of held that only ever
	// reached this root through a call's steps 3 and 4 from a value of a
	// shallow type. Every other loan of held is deep, so the zero set means
	// "all deep"; checker.put keeps sh within held, and every write that adds
	// a loan other than through a marked store drops it from sh (deep wins).
	sh setID
	// stored is the loans of held that only ever reached this root through
	// a store into its memory (storeInto: directWrite and storeArg), never
	// as its own value. An ends call on a view or allocator root that is
	// not moved drops stored and keeps the rest, the root's provenance.
	stored setID
	// cover is the paths of a linear temporary that hold nothing linear on
	// this path, taken or narrowed (cover.go); a join keeps the paths every
	// predecessor covers.
	cover setID
}

// initOf is the one reader of entry.init.
func initOf(e entry) initBits {
	if e.init == 0 {
		return initU
	}
	return e.init
}

const chunkShift = 5
const chunkSize = 1 << chunkShift

type chunk [chunkSize]entry

// groupShift is the number of chunk index bits a group covers: one group is
// 32 chunks, 1,024 roots.
const groupShift = 5
const groupSize = 1 << groupShift

// group is the second level of a state: 32 chunk pointers, and the mask of
// those the state that owns the group may write in place. A clone copies
// only the group pointers, so it costs roots/1,024 words, not roots/32: a
// body with a block and a local per call clones once per block.
type group struct {
	c   [groupSize]*chunk
	own uint32
}

// state maps every root to an entry. Groups and chunks are shared between
// states; a state writes a group only after copying it, unless own marks it
// as its own, and a chunk only after copying it, unless its group is its own
// and the group's mask marks the chunk. A state that has been stored as a
// block's entry or out-state is never written again (clone first).
type state struct {
	groups []*group
	own    []bool
}

var zeroChunk chunk

func newState(roots int) *state {
	n := (roots + chunkSize*groupSize - 1) / (chunkSize * groupSize)
	return &state{groups: make([]*group, n), own: make([]bool, n)}
}

// split is r's group, chunk and entry indexes.
func split(r rootID) (gi, ci, ei uint32) {
	return uint32(r) >> (chunkShift + groupShift), uint32(r) >> chunkShift & (groupSize - 1), uint32(r) & (chunkSize - 1)
}

func (s *state) get(r rootID) entry {
	gi, ci, ei := split(r)
	g := s.groups[gi]
	if g == nil {
		return entry{}
	}
	c := g.c[ci]
	if c == nil {
		return entry{}
	}
	return c[ei]
}

func (s *state) set(r rootID, e entry) {
	gi, ci, ei := split(r)
	g := s.groups[gi]
	switch {
	case g == nil:
		if e == (entry{}) {
			return
		}
		g = new(group)
		s.groups[gi] = g
		s.own[gi] = true
	case !s.own[gi]:
		if c := g.c[ci]; c == nil && e == (entry{}) || c != nil && c[ei] == e {
			return
		}
		ng := *g
		ng.own = 0
		g = &ng
		s.groups[gi] = g
		s.own[gi] = true
	}
	c := g.c[ci]
	switch {
	case c == nil:
		if e == (entry{}) {
			return
		}
		c = new(chunk)
		g.c[ci] = c
		g.own |= 1 << ci
	case g.own&(1<<ci) == 0:
		if c[ei] == e {
			return
		}
		nc := *c
		c = &nc
		g.c[ci] = c
		g.own |= 1 << ci
	}
	c[ei] = e
}

// clone is a copy that shares every group: one slice copy of pointers. It
// freezes s, so neither state writes a group or chunk the other reads.
func (s *state) clone() *state {
	s.freeze()
	return &state{groups: append([]*group(nil), s.groups...), own: make([]bool, len(s.groups))}
}

// freeze makes every group shared, so the state may be stored. A group's
// chunk mask is read only while the state owns the group, and a copied
// group starts with an empty mask, so the masks need no clearing.
func (s *state) freeze() {
	clear(s.own)
}

// chunkAt is chunk ci of group g, the zero chunk for an absent one.
func chunkAt(g *group, ci int) *chunk {
	if g == nil || g.c[ci] == nil {
		return &zeroChunk
	}
	return g.c[ci]
}

// equal compares two states entry by entry, skipping shared groups and
// chunks.
func (s *state) equal(o *state) bool {
	for gi, g := range s.groups {
		h := o.groups[gi]
		if g == h {
			continue
		}
		for ci := range groupSize {
			c, d := chunkAt(g, ci), chunkAt(h, ci)
			if c != d && *c != *d {
				return false
			}
		}
	}
	return true
}

// joinEntry is the pointwise union of two entries, init through
// initOf.
func joinEntry(ss *setStore, a, b entry) entry {
	if a == b {
		return a
	}
	return entry{
		init:    initOf(a) | initOf(b),
		held:    ss.unite(a.held, b.held),
		moved:   ss.unite(a.moved, b.moved),
		changed: ss.unite(a.changed, b.changed),
		on:      ss.unite(a.on, b.on),
		fv:      ss.joinFV(a.fv, b.fv),
		sh:      joinSh(ss, a, b),
		stored:  joinStored(ss, a, b),
		cover:   ss.meet(a.cover, b.cover),
	}
}

// joinSh is the join of the shallow marks (deep wins at every join): a
// loan is shallow after the join when it is shallow on every predecessor
// that holds it.
func joinSh(ss *setStore, a, b entry) setID {
	if a.sh == emptySet && b.sh == emptySet {
		return emptySet
	}
	deep := ss.unite(ss.minus(a.held, a.sh), ss.minus(b.held, b.sh))
	return ss.minus(ss.unite(a.sh, b.sh), deep)
}

// joinStored is the join of the content-stored sets (provenance wins at
// every join): a loan is in stored after the join when it is in stored on
// every predecessor that holds it.
func joinStored(ss *setStore, a, b entry) setID {
	if a.stored == emptySet && b.stored == emptySet {
		return emptySet
	}
	prov := ss.unite(ss.minus(a.held, a.stored), ss.minus(b.held, b.stored))
	return ss.minus(ss.unite(a.stored, b.stored), prov)
}

// joinInto returns old ⊔ in (old may be nil: a block not yet reached) as a
// new frozen state, and whether it differs from old.
func joinInto(ss *setStore, old, in *state) (*state, bool) {
	if old == nil {
		in.freeze()
		return in, true
	}
	out := old.clone()
	changed := false
	for gi, g := range in.groups {
		h := old.groups[gi]
		if g == h {
			continue
		}
		for ci := range groupSize {
			cc, dd := chunkAt(g, ci), chunkAt(h, ci)
			if cc == dd {
				continue
			}
			for j := range chunkSize {
				if cc[j] == dd[j] {
					continue
				}
				e := joinEntry(ss, dd[j], cc[j])
				if e != dd[j] {
					r := rootID(gi<<(chunkShift+groupShift) | ci<<chunkShift | j)
					if e.fv != 0 && splitUnion.Load() {
						splitUnionAt(ss, r, e, joinSpan)
					}
					out.set(r, e)
					changed = true
				}
			}
		}
	}
	out.freeze()
	return out, changed
}

// eachChunk calls f with the first root of every chunk s holds and the
// chunk, in root order.
func (s *state) eachChunk(f func(base rootID, c *chunk)) {
	for gi, g := range s.groups {
		if g == nil {
			continue
		}
		for ci, c := range g.c {
			if c != nil {
				f(rootID(gi<<(chunkShift+groupShift)|ci<<chunkShift), c)
			}
		}
	}
}

// takenToMoved rewrites every T bit to M (at a block entry with two or more
// predecessors). It writes s in place; s must be a scratch state. Each
// chunk is read through the pointer taken before any write to it, which a
// write may replace by a copy; the entries not yet visited are the same in
// both.
func (s *state) takenToMoved() {
	s.eachChunk(func(base rootID, c *chunk) {
		for j := range chunkSize {
			if e := c[j]; e.init&initT != 0 {
				e.init = e.init&^initT | initM
				s.set(base+rootID(j), e)
			}
		}
	})
}
