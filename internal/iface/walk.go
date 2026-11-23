// Why: every contract type has a hash form and a store form, and two
// separate writers for them would drift apart field by field.
//
// What: Hasher (memoized stable hashes over one session's tables) and
// NewHasher, and walker, the one walk that writes either form, with its
// reference writers typ, errset, cnst, item, key, sym, span and constArg.
//
// How: in hash form references are written by stable identity (keys,
// strings, 32-byte hashes) and spans never; in store form by pool index,
// with spans rebased onto anchors. No session ID is ever written.

package iface

import (
	"sync"
	"sync/atomic"

	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

// hashSlot holds one memoized hash for a dense integer ID. val is written once
// before ready is stored true, so concurrent readers see it lock-free.
type hashSlot struct {
	ready atomic.Bool
	val   source.Hash
}

// hashCache is a grow-only lock-free dense-ID memo table for Hasher.
type hashCache struct {
	slots atomic.Pointer[[]hashSlot]
	mu    sync.Mutex
}

func (c *hashCache) get(id uint32) (source.Hash, bool) {
	if p := c.slots.Load(); p != nil && int(id) < len(*p) {
		if s := &(*p)[id]; s.ready.Load() {
			return s.val, true
		}
	}
	return source.Hash{}, false
}

func (c *hashCache) set(id uint32, v source.Hash) {
	c.mu.Lock()
	p := c.slots.Load()
	var cur []hashSlot
	if p != nil {
		cur = *p
	}
	if int(id) >= len(cur) {
		newCap := max(len(cur)*2, 64)
		for newCap <= int(id) {
			newCap *= 2
		}
		next := make([]hashSlot, newCap)
		for i := range cur {
			if cur[i].ready.Load() {
				next[i].val = cur[i].val
				next[i].ready.Store(true)
			}
		}
		c.slots.Store(&next)
		cur = next
	}
	if !cur[id].ready.Load() {
		cur[id].val = v
		cur[id].ready.Store(true)
	}
	c.mu.Unlock()
}

// Hasher memoizes stable hashes over one session's tables. The TypeID-,
// ErrorSetID-, ConstID- and plain-FnRef-keyed memo tables are per session and
// never reach output. Safe for concurrent use: reads are lock-free, writes
// grow under per-table locks, computation is outside the lock, and a value
// computed twice is equal.
type Hasher struct {
	tab    *types.Table
	items  *types.Items
	in     *source.Interner
	consts *types.ConstTable
	body   func(types.ItemID) *mir.Body

	tyMemo      hashCache
	esMemo      hashCache
	cMemo       hashCache
	plainFnMemo hashCache // non-literal FnRef with len(Args)==0, keyed by ItemID

	idx itemIndex // the item table by module, owner and kind (index.go)
}

// NewHasher takes the session's tables and a body accessor. Two hash domains
// need bodies: mk.sig hashes every contract body, and mk.fn tag
// 2 hashes a fn literal's body. body returns the lowered body, or the
// one decoded from a body store; nil for a body a hash requires is an ICE
// (hash form is total). body may be nil when no hash asked of
// this Hasher needs a body.
func NewHasher(tab *types.Table, consts *types.ConstTable, body func(types.ItemID) *mir.Body) *Hasher {
	if consts == nil {
		consts = tab.Consts()
	}
	return &Hasher{tab: tab, items: tab.Items(), in: tab.Interner(), consts: consts, body: body}
}

// Table is the type table the Hasher reads.
func (h *Hasher) Table() *types.Table { return h.tab }

// walker writes one walk into e, in hash form (p nil) or store form (p set).
type walker struct {
	e *Encoder
	h *Hasher // hash form, and the session's tables in both forms
	p *pools  // store form
}

func (w *walker) store() bool { return w.p != nil }

// typ writes a type: its mk.ty hash (32 zero bytes for NoType), or its
// pool index (0 for NoType).
func (w *walker) typ(t types.TypeID) {
	if w.p != nil {
		w.e.Uvarint(uint64(w.p.typ(t)))
		return
	}
	if t == types.NoType {
		w.e.Hash(source.Hash{})
		return
	}
	w.e.Hash(w.h.Type(t))
}

// errset writes an error set: its mk.errset hash, or its pool index (0 is
// the empty set).
func (w *walker) errset(es types.ErrorSetID) {
	if w.p != nil {
		w.e.Uvarint(uint64(w.p.errset(es)))
		return
	}
	w.e.Hash(w.h.ErrorSet(es))
}

// cnst writes a const: its deep mk.const hash (zero for NoConst), or its
// pool index.
func (w *walker) cnst(c types.ConstID) {
	if w.p != nil {
		w.e.Uvarint(uint64(w.p.cnst(c)))
		return
	}
	if c == types.NoConst {
		w.e.Hash(source.Hash{})
		return
	}
	w.e.Hash(w.h.Const(c))
}

// item writes a nominal reference: the item's key text ("" for NoItem), or
// its pool index.
func (w *walker) item(it types.ItemID) {
	if w.p != nil {
		w.e.Uvarint(uint64(w.p.item(it)))
		return
	}
	if it == types.NoItem {
		w.e.String("")
		return
	}
	w.e.String(w.h.items.Get(it).Key.String())
}

// key writes a stable key: its text, or its key-pool index.
func (w *walker) key(k types.Key) {
	if w.p != nil {
		w.e.Uvarint(uint64(w.p.key(k.String())))
		return
	}
	w.e.String(k.String())
}

// sym writes a symbol: its text ("" for NoSymbol), or its string-pool index.
func (w *walker) sym(s source.Symbol) {
	if w.p != nil {
		w.e.Uvarint(uint64(w.p.sym(s)))
		return
	}
	if s == source.NoSymbol {
		w.e.String("")
		return
	}
	w.e.String(w.h.in.Str(s))
}

// span writes nothing in hash form (a span is never hashed) and the
// rebased span in store form.
func (w *walker) span(sp source.Span) {
	if w.p != nil {
		w.p.span(w.e, sp)
	}
}

// fnref writes a const's function reference: its mk.fn hash, or the
// item's pool index and its generic arguments.
func (w *walker) fnref(f types.FnRef) {
	if w.p != nil {
		w.item(f.Item)
		w.genericArgs(f.Args)
		return
	}
	w.e.Hash(w.h.FnRef(f))
}

// constArg writes a const argument: 0 none, 1 + param index, 2 + const,
// 3 + type.
func (w *walker) constArg(c types.ConstArg) {
	switch c.Kind {
	case types.CANone:
		w.e.Uvarint(0)
	case types.CAParam:
		w.e.Uvarint(1)
		w.e.Uvarint(uint64(c.Param))
	case types.CAValue:
		w.e.Uvarint(2)
		w.cnst(c.Value)
	case types.CASizeOf:
		w.e.Uvarint(3)
		w.typ(c.Type)
	default:
		source.ICEf(source.Span{}, "iface: const arg kind %d", c.Kind)
	}
}

// genericArg writes 1 + type, or 2 + const arg.
func (w *walker) genericArg(a types.GenericArg) {
	if a.IsConst {
		w.e.Uvarint(2)
		w.constArg(a.Const)
		return
	}
	w.e.Uvarint(1)
	w.typ(a.Type)
}

// genericArgs writes a count, then each argument.
func (w *walker) genericArgs(as []types.GenericArg) {
	w.e.Uvarint(uint64(len(as)))
	for _, a := range as {
		w.genericArg(a)
	}
}
