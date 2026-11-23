// Why: programs need a map that keeps its keys in order, for range queries
// and sorted iteration, at a cost close to a hash map's.
//
// What: BTree (in an arena) and HeapBTree (linear, on an allocator), each
// with New, Set, Replace, Get, GetMut, Has, Remove, Len, Iter and Range, and
// BTreeIter; HeapBTree adds Free, FreeEmpty and FreeEach.
//
// How: a B-tree with parent links (Bayer and McCreight 1972; the layout and
// the remove strategy of Rust's std::collections::BTreeMap, node.rs);
// mem_btree.mk holds the raw node access. A node holds up to
// BtCap = 2 BtB - 1 keys and as many values, an internal node also BtCap + 1
// edges; every node but the root holds at least BtMin = BtB - 1 keys. A node
// is one allocation: the header BtNode, the keys, the values, then (internal
// only) the edges, so leaves, which are most nodes, carry no edge array.
// Search in a node is a linear scan (Rust's choice for small B). Get, Set
// and Remove touch O(log_B n) nodes; Iter and Range step in O(1) amortized.
// A merged-away node goes on the tree's free list and is reused by the next
// split, so churn does not grow an arena without bound; the heap form
// returns every node at Free.

import std.raw

// BtB is the B of a tree of K and V, chosen by benchmark. The key gives
// the most: 16 for keys of at most 8 bytes, whose compares are one
// instruction, so wider nodes save cache misses; 6 (Rust's) for wider keys such as str, whose compares cost more
// than the misses they save. The slot (key plus value) caps it, as
// Abseil's btree sizes its nodes from the slot size: an insert or remove
// shifts about B slots and a split copies B - 1, so B is at most
// BtSlotBudget / (SizeOf[K] + SizeOf[V]), which keeps 16 for u64 keys and
// values and gives a 256-byte value Rust's 6; never below BtLo. A
// function of the types alone, which release builds fold to a constant.
const BtSlotBudget: int = 256
const BtLo: int = 6

BtB[K, V](): int {
    b := if SizeOf[K]() <= 8 { 16 } else { 6 }
    slot := SizeOf[K]() + SizeOf[V]()
    if slot == 0 || slot * b <= BtSlotBudget {
        return b
    }
    return if BtSlotBudget / slot < BtLo { BtLo } else { BtSlotBudget / slot }
}

BtCap[K, V](): int {
    return 2 * BtB[K, V]() - 1
}

BtMin[K, V](): int {
    return BtB[K, V]() - 1
}

// BtNode is the header of every node. parent is null at the root; pidx is
// this node's edge index in its parent; len is the number of keys.
struct BtNode[K, V] {
    parent: *BtNode[K, V]
    pidx: int
    len: int
    leaf: bool
}

// Tree is the state both maps share: root is null exactly when n = 0.
// free and ifree are lists of spare leaves and internal nodes, linked
// through parent, with nfree and nifree entries.
struct Tree[K, V] {
    root: *BtNode[K, V]
    n: int
    free: *BtNode[K, V]
    nfree: int
    ifree: *BtNode[K, V]
    nifree: int
}

// BtPos is a key's place, or none when node is null.
struct BtPos[K, V] {
    node: *BtNode[K, V]
    idx: int
}

/// An ordered map in an arena: Get, Set and Remove in O(log n), and
/// iteration in key order. K needs Ord. Its nodes live in the arena and
/// are freed with it; a node emptied by Remove is reused by a later Set,
/// so a tree that keeps changing does not grow the arena without bound.
export view struct BTree[K: Ord, V] {
    t: Tree[K, V]
    alloc: &Alloc
}

/// An ordered map on an allocator, with the operations of BTree. It is
/// linear: give it back with Free, FreeEmpty or FreeEach. It is Send and
/// Share when K and V are.
export linear view struct HeapBTree[K: Ord, V] {
    t: Tree[K, V]
    owner: *Alloc
    mk: [0]&K
    mv: [0]&V
}

// Send and Share if the keys and values are, as HeapMap.
unsafe impl[K: Ord + Send, V: Send] Send for HeapBTree[K, V] {}
unsafe impl[K: Ord + Share, V: Share] Share for HeapBTree[K, V] {}

// BTreeIter yields the keys from node/idx and stops before end/eidx (end
// null: at the last key).
/// The entries of a BTree or HeapBTree in ascending key order, made by
/// Iter and Range.
export view struct BTreeIter[K, V] {
    tree: &Tree[K, V]
    node: *BtNode[K, V]
    idx: int
    end: *BtNode[K, V]
    eidx: int
}

BtEdge[K, V](n: *BtNode[K, V], j: int): *BtNode[K, V] {
    return SlotRead(BtEdges(n), j)
}

// BtLink puts child c at edge j of p and records it in c.
BtLink[K, V](p: *BtNode[K, V], j: int, c: *BtNode[K, V]) {
    SlotWrite(BtEdges(p), j, c)
    BtSetParent(c, p, j)
}

// BtRelink records edges lo..hi of p in their children after a shift.
BtRelink[K, V](p: *BtNode[K, V], lo: int, hi: int) {
    for j in lo..hi {
        BtSetParent(BtEdge(p, j), p, j)
    }
}

EmptyTree[K, V](): Tree[K, V] {
    z := raw.Null[BtNode[K, V]]()
    return Tree[K, V](root: z, n: 0, free: z, nfree: 0, ifree: z, nifree: 0)
}

// TreeNeed is what an insertion below leaf (null: into the empty tree)
// takes: a leaf and an internal node per full ancestor, and one more for a
// new root when the whole path is full.
struct BtNeed {
    leaves: int
    inner: int
}

TreeNeed[K, V](leaf: *BtNode[K, V]): BtNeed {
    if raw.IsNull(leaf) {
        return BtNeed(leaves: 1, inner: 0)
    }
    var splits := 0
    var m := leaf
    for BtLen(m) == BtCap[K, V]() {
        splits += 1
        p := BtParent(m)
        if raw.IsNull(p) {
            splits += 1
            break
        }
        m = p
    }
    if splits == 0 {
        return BtNeed(leaves: 0, inner: 0)
    }
    return BtNeed(leaves: 1, inner: splits - 1)
}

TreeShort[K, V](t: &Tree[K, V], need: BtNeed): bool {
    return t.nfree < need.leaves || t.nifree < need.inner
}

// TreeReserve fills the free lists to need from a; false when a is out of
// memory, and then the tree is unchanged. A heap tree (free set) gives back
// the nodes this call allocated before the failure, so the live bytes are
// unchanged too; an arena tree keeps them as spares (an arena frees
// nothing).
TreeReserve[K, V](t: mut Tree[K, V], a: &Alloc, need: BtNeed, free: bool): bool {
    var leaves := 0
    var inner := 0
    var ok := true
    for ok && t.nfree < need.leaves {
        n := BtAllocNode[K, V](a, true)
        if raw.IsNull(n) {
            ok = false
        } else {
            TreeGive(t, n)
            leaves += 1
        }
    }
    for ok && t.nifree < need.inner {
        n := BtAllocNode[K, V](a, false)
        if raw.IsNull(n) {
            ok = false
        } else {
            TreeGive(t, n)
            inner += 1
        }
    }
    if ok || !free {
        return ok
    }
    for leaves > 0 {
        BtFreeNode(a, TreeTake(t, true))
        leaves -= 1
    }
    for inner > 0 {
        BtFreeNode(a, TreeTake(t, false))
        inner -= 1
    }
    return false
}

// BtFind is the first index of n whose key is not less than k, and
// whether that key equals k.
struct BtHit {
    i: int
    found: bool
}

BtFind[K: Ord, V](n: *BtNode[K, V], k: &K): BtHit {
    keys := BtKeys(n)
    len := BtLen(n)
    var i := 0
    for i < len {
        c := SlotRef(keys, i)
        if !c.Less(k) {
            return BtHit(i: i, found: !k.Less(c))
        }
        i += 1
    }
    return BtHit(i: len, found: false)
}

// TreeLocate is k's place when present (found), else the leaf and index
// where it goes (node null: the tree is empty).
struct BtLoc[K, V] {
    node: *BtNode[K, V]
    idx: int
    found: bool
}

TreeLocate[K: Ord, V](t: &Tree[K, V], k: &K): BtLoc[K, V] {
    var n := t.root
    if raw.IsNull(n) {
        return BtLoc[K, V](node: n, idx: 0, found: false)
    }
    for {
        h := BtFind(n, k)
        if h.found || BtIsLeaf(n) {
            return BtLoc[K, V](node: n, idx: h.i, found: h.found)
        }
        n = BtEdge(n, h.i)
    }
}

// BtInsertFit stores k, v at idx of n, which has room, and for an
// internal n the edge e right of it.
BtInsertFit[K, V](n: *BtNode[K, V], idx: int, k: K, v: V, e: *BtNode[K, V]) {
    len := BtLen(n)
    SlotShift(BtKeys(n), idx + 1, idx, len - idx)
    SlotWrite(BtKeys(n), idx, k)
    SlotShift(BtVals(n), idx + 1, idx, len - idx)
    SlotWrite(BtVals(n), idx, v)
    if !BtIsLeaf(n) {
        SlotShift(BtEdges(n), idx + 2, idx + 1, len - idx)
        BtLink(n, idx + 1, e)
        BtRelink(n, idx + 2, len + 2)
    }
    BtSetLen(n, len + 1)
}

// TreeInsert stores k, v at leaf/idx (from TreeLocate, not found), with
// the free lists filled to TreeNeed(leaf). A full node splits around its
// key BtB - 1: the left half keeps BtB - 1 keys, the right half takes the
// last BtB - 1 and, internal, the last BtB edges; k goes into the half it
// belongs to, and the middle key goes up with the right half as its edge.
TreeInsert[K: Ord, V](t: mut Tree[K, V], leaf: *BtNode[K, V], idx: int, k: K, v: V) {
    t.n += 1
    if raw.IsNull(leaf) {
        r := TreeTake(t, true)
        BtInsertFit(r, 0, k, v, raw.Null[BtNode[K, V]]())
        t.root = r
        return
    }
    var n := leaf
    var i := idx
    var ck := k
    var cv := v
    var e := raw.Null[BtNode[K, V]]()
    for {
        if BtLen(n) < BtCap[K, V]() {
            BtInsertFit(n, i, ck, cv, e)
            return
        }
        is_leaf := BtIsLeaf(n)
        r := TreeTake(t, is_leaf)
        SlotCopy(BtKeys(r), 0, BtKeys(n), BtB[K, V](), BtB[K, V]() - 1)
        SlotCopy(BtVals(r), 0, BtVals(n), BtB[K, V](), BtB[K, V]() - 1)
        if !is_leaf {
            SlotCopy(BtEdges(r), 0, BtEdges(n), BtB[K, V](), BtB[K, V]())
            BtRelink(r, 0, BtB[K, V]())
        }
        mk := SlotRead(BtKeys(n), BtB[K, V]() - 1)
        mv := SlotRead(BtVals(n), BtB[K, V]() - 1)
        BtSetLen(n, BtB[K, V]() - 1)
        BtSetLen(r, BtB[K, V]() - 1)
        if i < BtB[K, V]() {
            BtInsertFit(n, i, ck, cv, e)
        } else {
            BtInsertFit(r, i - BtB[K, V](), ck, cv, e)
        }
        p := BtParent(n)
        if raw.IsNull(p) {
            root := TreeTake(t, false)
            SlotWrite(BtKeys(root), 0, mk)
            SlotWrite(BtVals(root), 0, mv)
            BtLink(root, 0, n)
            BtLink(root, 1, r)
            BtSetLen(root, 1)
            t.root = root
            return
        }
        i = BtPidx(n)
        n = p
        ck = mk
        cv = mv
        e = r
    }
}

// BtRotateRight moves the separator i of p to the front of x and the last
// key of l, x's left sibling, up in its place.
BtRotateRight[K, V](p: *BtNode[K, V], i: int, l: *BtNode[K, V], x: *BtNode[K, V]) {
    xl := BtLen(x)
    ll := BtLen(l)
    SlotShift(BtKeys(x), 1, 0, xl)
    SlotWrite(BtKeys(x), 0, SlotRead(BtKeys(p), i))
    SlotWrite(BtKeys(p), i, SlotRead(BtKeys(l), ll - 1))
    SlotShift(BtVals(x), 1, 0, xl)
    SlotWrite(BtVals(x), 0, SlotRead(BtVals(p), i))
    SlotWrite(BtVals(p), i, SlotRead(BtVals(l), ll - 1))
    if !BtIsLeaf(x) {
        SlotShift(BtEdges(x), 1, 0, xl + 1)
        BtLink(x, 0, BtEdge(l, ll))
        BtRelink(x, 1, xl + 2)
    }
    BtSetLen(x, xl + 1)
    BtSetLen(l, ll - 1)
}

// BtRotateLeft moves the separator i of p to the end of x and the first
// key of r, x's right sibling, up in its place.
BtRotateLeft[K, V](p: *BtNode[K, V], i: int, x: *BtNode[K, V], r: *BtNode[K, V]) {
    xl := BtLen(x)
    rl := BtLen(r)
    SlotWrite(BtKeys(x), xl, SlotRead(BtKeys(p), i))
    SlotWrite(BtKeys(p), i, SlotRead(BtKeys(r), 0))
    SlotShift(BtKeys(r), 0, 1, rl - 1)
    SlotWrite(BtVals(x), xl, SlotRead(BtVals(p), i))
    SlotWrite(BtVals(p), i, SlotRead(BtVals(r), 0))
    SlotShift(BtVals(r), 0, 1, rl - 1)
    if !BtIsLeaf(x) {
        BtLink(x, xl + 1, BtEdge(r, 0))
        SlotShift(BtEdges(r), 0, 1, rl)
        BtRelink(r, 0, rl)
    }
    BtSetLen(x, xl + 1)
    BtSetLen(r, rl - 1)
}

// BtMerge joins l, the separator i of p and r (l's right sibling) into l,
// removes the separator and r's edge from p, and frees r to the list.
BtMerge[K, V](t: mut Tree[K, V], p: *BtNode[K, V], i: int, l: *BtNode[K, V], r: *BtNode[K, V]) {
    ll := BtLen(l)
    rl := BtLen(r)
    pl := BtLen(p)
    SlotWrite(BtKeys(l), ll, SlotRead(BtKeys(p), i))
    SlotCopy(BtKeys(l), ll + 1, BtKeys(r), 0, rl)
    SlotWrite(BtVals(l), ll, SlotRead(BtVals(p), i))
    SlotCopy(BtVals(l), ll + 1, BtVals(r), 0, rl)
    if !BtIsLeaf(l) {
        SlotCopy(BtEdges(l), ll + 1, BtEdges(r), 0, rl + 1)
        BtRelink(l, ll + 1, ll + rl + 2)
    }
    BtSetLen(l, ll + 1 + rl)
    SlotShift(BtKeys(p), i, i + 1, pl - i - 1)
    SlotShift(BtVals(p), i, i + 1, pl - i - 1)
    SlotShift(BtEdges(p), i + 1, i + 2, pl - i - 1)
    BtRelink(p, i + 1, pl)
    BtSetLen(p, pl - 1)
    TreeGive(t, r)
}

// TreeFix restores the minimum fill from x upward after x lost a key:
// steal from a sibling that has more than BtMin keys, else merge with one
// and continue at the parent; a root left with no key is dropped.
TreeFix[K, V](t: mut Tree[K, V], x0: *BtNode[K, V]) {
    var x := x0
    for {
        p := BtParent(x)
        if raw.IsNull(p) {
            if BtLen(x) == 0 {
                if BtIsLeaf(x) {
                    t.root = raw.Null[BtNode[K, V]]()
                } else {
                    c := BtEdge(x, 0)
                    BtSetParent(c, raw.Null[BtNode[K, V]](), 0)
                    t.root = c
                }
                TreeGive(t, x)
            }
            return
        }
        if BtLen(x) >= BtMin[K, V]() {
            return
        }
        j := BtPidx(x)
        if j > 0 {
            l := BtEdge(p, j - 1)
            if BtLen(l) > BtMin[K, V]() {
                BtRotateRight(p, j - 1, l, x)
                return
            }
        }
        if j < BtLen(p) {
            r := BtEdge(p, j + 1)
            if BtLen(r) > BtMin[K, V]() {
                BtRotateLeft(p, j, x, r)
                return
            }
        }
        if j > 0 {
            BtMerge(t, p, j - 1, BtEdge(p, j - 1), x)
        } else {
            BtMerge(t, p, j, x, BtEdge(p, j + 1))
        }
        x = p
    }
}

// TreeRemoveAt takes the value at n/i out and drops its key (the caller
// checked raw.IsLinear[K]). A key of an internal node is replaced by its
// predecessor, the last key of the rightmost leaf of its left subtree.
TreeRemoveAt[K, V](t: mut Tree[K, V], n: *BtNode[K, V], i: int): V {
    k := SlotRead(BtKeys(n), i)
    v := SlotRead(BtVals(n), i)
    Drop(k)
    t.n -= 1
    if BtIsLeaf(n) {
        len := BtLen(n)
        SlotShift(BtKeys(n), i, i + 1, len - i - 1)
        SlotShift(BtVals(n), i, i + 1, len - i - 1)
        BtSetLen(n, len - 1)
        TreeFix(t, n)
        return v
    }
    var m := BtEdge(n, i)
    for !BtIsLeaf(m) {
        m = BtEdge(m, BtLen(m))
    }
    last := BtLen(m) - 1
    SlotWrite(BtKeys(n), i, SlotRead(BtKeys(m), last))
    SlotWrite(BtVals(n), i, SlotRead(BtVals(m), last))
    BtSetLen(m, last)
    TreeFix(t, m)
    return v
}

// BtSettle is the key at or after edge i of n in order: i itself when
// i < len, else the parent's separator above, climbing; null past the end.
BtSettle[K, V](n0: *BtNode[K, V], i0: int): BtPos[K, V] {
    var n := n0
    var i := i0
    for i == BtLen(n) {
        p := BtParent(n)
        if raw.IsNull(p) {
            return BtPos[K, V](node: p, idx: 0)
        }
        i = BtPidx(n)
        n = p
    }
    return BtPos[K, V](node: n, idx: i)
}

// TreeLower is the first key not less than k; null when there is none.
TreeLower[K: Ord, V](t: &Tree[K, V], k: &K): BtPos[K, V] {
    var n := t.root
    if raw.IsNull(n) {
        return BtPos[K, V](node: n, idx: 0)
    }
    for {
        h := BtFind(n, k)
        if h.found {
            return BtPos[K, V](node: n, idx: h.i)
        }
        if BtIsLeaf(n) {
            return BtSettle(n, h.i)
        }
        n = BtEdge(n, h.i)
    }
}

TreeFirst[K, V](t: &Tree[K, V]): BtPos[K, V] {
    var n := t.root
    if !raw.IsNull(n) {
        for !BtIsLeaf(n) {
            n = BtEdge(n, 0)
        }
    }
    return BtPos[K, V](node: n, idx: 0)
}

// The value of k in t. `from t` names the tree its result is read from: t
// holds raw pointers only, so without it the result would name no source,
// and a generic caller would count it as possibly global memory.
TreeGet[K: Ord, V](t: &Tree[K, V], k: &K): ?&V from t {
    var n := t.root
    if raw.IsNull(n) {
        return none
    }
    for {
        h := BtFind(n, k)
        if h.found {
            return some(SlotRef(BtVals(n), h.i))
        }
        if BtIsLeaf(n) {
            return none
        }
        n = BtEdge(n, h.i)
    }
}

// TreeGetMut is TreeGet's descent with the value as a mutable reference;
// the caller borrows the tree mutably, so no other loan reaches the slot.
// `from t` as for TreeGet.
TreeGetMut[K: Ord, V](t: mut Tree[K, V], k: &K): ?mut V from t {
    at := TreeLocate(&*t, k)
    if !at.found {
        return none
    }
    return some(SlotMut(BtVals(at.node), at.idx))
}

TreeIter[K, V](t: &Tree[K, V]): BTreeIter[K, V] {
    s := TreeFirst(t)
    return BTreeIter[K, V](tree: t, node: s.node, idx: 0, end: raw.Null[BtNode[K, V]](), eidx: 0)
}

// [lo, hi): empty unless lo < hi.
TreeRange[K: Ord, V](t: &Tree[K, V], lo: &K, hi: &K): BTreeIter[K, V] from t {
    z := raw.Null[BtNode[K, V]]()
    if !lo.Less(hi) {
        return BTreeIter[K, V](tree: t, node: z, idx: 0, end: z, eidx: 0)
    }
    s := TreeLower(t, lo)
    e := TreeLower(t, hi)
    return BTreeIter[K, V](tree: t, node: s.node, idx: s.idx, end: e.node, eidx: e.idx)
}

// BtFreeAll returns the subtree at n to a, depth first (depth log_B n).
BtFreeAll[K, V](a: &Alloc, n: *BtNode[K, V]) {
    if !BtIsLeaf(n) {
        for j in 0..BtLen(n) + 1 {
            BtFreeAll(a, BtEdge(n, j))
        }
    }
    BtFreeNode(a, n)
}

BtFreeList[K, V](a: &Alloc, n0: *BtNode[K, V]) {
    var n := n0
    for !raw.IsNull(n) {
        next := BtParent(n)
        BtFreeNode(a, n)
        n = next
    }
}

/// An empty BTree in a. O(1); allocates nothing until the first Set.
export BTree[K, V].New(a: &Arena): BTree[K, V] {
    return BTree[K, V](t: EmptyTree[K, V](), alloc: a.Alloc())
}

/// Stores v under k. A present key keeps the stored key and gets v; the
/// given key and the old value are dropped, so neither K nor V may be
/// linear (E0701 at the call, which names Replace). Fails with
/// OutOfMemory when a node split needs a node the arena cannot give; the
/// nodes are allocated before anything moves, so k and v are then not
/// consumed. O(log n) nodes and O(B log n) compares, where B, from 6 to
/// 16, depends on the sizes of K and V; allocates at most one node per
/// level.
export BTree[K, V].Set(mut self, k: K into self, v: V into self): OutOfMemory!void {
    at := TreeLocate(&self.t, &k)
    if at.found {
        Drop(SlotRead(BtVals(at.node), at.idx))
        SlotWrite(BtVals(at.node), at.idx, v)
        Drop(k)
        return
    }
    need := TreeNeed(at.node)
    if TreeShort(&self.t, need) {
        if !TreeReserve(mut self.t, self.alloc, need, false) {
            fail OutOfMemory
        }
    }
    TreeInsert(mut self.t, at.node, at.idx, k, v)
}

/// Stores v under k and returns the old value, or none when k was absent.
/// A present key keeps the stored key and drops the given k, so K may not
/// be linear (E0701 at the call); V may be anything, since its old value
/// comes back. Fails with OutOfMemory as Set does, and then k and v are
/// not consumed. O(log n) nodes and O(B log n) compares; allocates at most
/// one node per level, and nothing for a present key.
export BTree[K, V].Replace(mut self, k: K into self, v: V into self): OutOfMemory!?V {
    at := TreeLocate(&self.t, &k)
    if at.found {
        Drop(k)
        return some(Replace(SlotMut(BtVals(at.node), at.idx), v))
    }
    need := TreeNeed(at.node)
    if TreeShort(&self.t, need) {
        if !TreeReserve(mut self.t, self.alloc, need, false) {
            fail OutOfMemory
        }
    }
    TreeInsert(mut self.t, at.node, at.idx, k, v)
    return none
}

/// The value of k; none when k is absent. O(log n).
export BTree[K, V].Get(&self, k: &K): ?&V {
    return TreeGet(&self.t, k)
}

/// The value of k, to update in place. O(log n), one descent.
export BTree[K, V].GetMut(mut self, k: &K): ?mut V {
    return TreeGetMut(mut self.t, k)
}

/// Whether k is present. O(log n).
export BTree[K, V].Has(&self, k: &K): bool {
    return TreeGet(&self.t, k).IsSome()
}

/// The value of k, removed; none when k is absent. The stored key is
/// dropped, so K may not be linear (E0701 at the call). O(log n); it
/// allocates nothing, and a node it empties is kept for the next split.
export BTree[K, V].Remove(mut self, k: &K): ?V {
    at := TreeLocate(&self.t, k)
    if !at.found {
        return none
    }
    if raw.IsLinear[K]() {
        raw.Fail(8, "BTree.Remove would drop a linear value")
    }
    return some(TreeRemoveAt(mut self.t, at.node, at.idx))
}

/// The number of entries. O(1).
export BTree[K, V].Len(&self): int {
    return self.t.n
}

/// Every entry in ascending key order. O(log n) to start, O(1) amortized
/// a step.
export BTree[K, V].Iter(&self): BTreeIter[K, V] {
    return TreeIter(&self.t)
}

/// The entries with lo <= key < hi in ascending order: from lo up to, not
/// including, hi. Empty unless lo < hi. O(log n) to start, then O(1)
/// amortized a step.
export BTree[K, V].Range(&self, lo: &K, hi: &K): BTreeIter[K, V] from self {
    return TreeRange(&self.t, lo, hi)
}

// BtStep is the in-order position after key i of n. It reads only the
// links, so a walk that moves keys and values out of the nodes can still
// take it before freeing them.
BtStep[K, V](n: *BtNode[K, V], i: int): BtPos[K, V] {
    if BtIsLeaf(n) {
        return BtSettle(n, i + 1)
    }
    var m := BtEdge(n, i + 1)
    for !BtIsLeaf(m) {
        m = BtEdge(m, 0)
    }
    return BtPos[K, V](node: m, idx: 0)
}

/// The next entry, or none after the last. O(1) amortized.
export BTreeIter[K, V].Next(mut self): ?(key: &K, value: &V) from self.tree {
    n := self.node
    if raw.IsNull(n) || (raw.Same(n, self.end) && self.idx == self.eidx) {
        self.node = raw.Null[BtNode[K, V]]()
        return none
    }
    i := self.idx
    s := BtStep(n, i)
    self.node = s.node
    self.idx = s.idx
    return some((key: SlotRef(BtKeys(n), i), value: SlotRef(BtVals(n), i)))
}

/// An empty HeapBTree on a. O(1); allocates nothing until the first Set.
export HeapBTree[K, V].New(a: &Alloc): HeapBTree[K, V] {
    return HeapBTree[K, V](t: EmptyTree[K, V](), owner: raw.Of(a), mk: [], mv: [])
}

/// Stores v under k, as BTree.Set, except that for a present key the old
/// value and the given key are freed with a, as HeapMap.Set does. So each
/// of K and V must be not linear or Free (E0701 at the call, which names
/// Replace). Panics (code 12) when a is not the allocator given to New;
/// it checks only when it allocates a node. O(log n); allocates at most
/// one node per level.
export HeapBTree[K, V].Set(mut self, a: &Alloc, k: K into self, v: V into self): OutOfMemory!void {
    at := TreeLocate(&self.t, &k)
    if at.found {
        old := Replace(SlotMut(BtVals(at.node), at.idx), v)
        CollRelease(old, a)
        CollRelease(k, a)
        return
    }
    need := TreeNeed(at.node)
    if TreeShort(&self.t, need) {
        OwnerCheck(a, self.owner, "HeapBTree.Set")
        if !TreeReserve(mut self.t, a, need, true) {
            fail OutOfMemory
        }
    }
    TreeInsert(mut self.t, at.node, at.idx, k, v)
}

/// Stores v under k and returns the old value, or none when k was absent,
/// as BTree.Replace, except that for a present key the given key is freed
/// with a. So K must be not linear or Free (E0701 at the call); V may be
/// anything. Panics (code 12) when a is not the allocator given to New;
/// it checks only when it allocates a node. Fails with OutOfMemory as Set
/// does, and then k and v are not consumed. O(log n); allocates at most
/// one node per level.
export HeapBTree[K, V].Replace(mut self, a: &Alloc, k: K into self, v: V into self): OutOfMemory!?V {
    at := TreeLocate(&self.t, &k)
    if at.found {
        old := Replace(SlotMut(BtVals(at.node), at.idx), v)
        CollRelease(k, a)
        return some(old)
    }
    need := TreeNeed(at.node)
    if TreeShort(&self.t, need) {
        OwnerCheck(a, self.owner, "HeapBTree.Replace")
        if !TreeReserve(mut self.t, a, need, true) {
            fail OutOfMemory
        }
    }
    TreeInsert(mut self.t, at.node, at.idx, k, v)
    return none
}

/// The value of k; none when k is absent. O(log n).
export HeapBTree[K, V].Get(&self, k: &K): ?&V {
    return TreeGet(&self.t, k)
}

/// The value of k, to update in place. O(log n), one descent.
export HeapBTree[K, V].GetMut(mut self, k: &K): ?mut V {
    return TreeGetMut(mut self.t, k)
}

/// Whether k is present. O(log n).
export HeapBTree[K, V].Has(&self, k: &K): bool {
    return TreeGet(&self.t, k).IsSome()
}

/// The value of k, removed, as BTree.Remove: K may not be linear (E0701
/// at the call). It takes no allocator, so a node it empties is kept for
/// the next split or for Free. O(log n).
export HeapBTree[K, V].Remove(mut self, k: &K): ?V {
    at := TreeLocate(&self.t, k)
    if !at.found {
        return none
    }
    if raw.IsLinear[K]() {
        raw.Fail(8, "HeapBTree.Remove would drop a linear value")
    }
    return some(TreeRemoveAt(mut self.t, at.node, at.idx))
}

/// The number of entries. O(1).
export HeapBTree[K, V].Len(&self): int {
    return self.t.n
}

/// Every entry in ascending key order, as BTree.Iter.
export HeapBTree[K, V].Iter(&self): BTreeIter[K, V] {
    return TreeIter(&self.t)
}

/// The entries with lo <= key < hi in ascending order, as BTree.Range.
export HeapBTree[K, V].Range(&self, lo: &K, hi: &K): BTreeIter[K, V] from self {
    return TreeRange(&self.t, lo, hi)
}

// TreeFreeNodes gives every node and every spare back to a: O(nodes).
TreeFreeNodes[K, V](t: Tree[K, V], a: &Alloc) {
    if !raw.IsNull(t.root) {
        BtFreeAll(a, t.root)
    }
    BtFreeList(a, t.free)
    BtFreeList(a, t.ifree)
}

/// Frees every key and value with its Free, in key order, then gives every
/// node, spare ones included, back to a. Panics (code 12) when a is not
/// the allocator given to New. O(nodes) when neither K nor V is linear or
/// Free, since there is nothing to free per entry; O(n) frees besides
/// otherwise. A call where K or V is linear and not Free is E0701: use
/// FreeEach.
impl[K: Ord, V] Free for HeapBTree[K, V] {
    Free(self, a: &Alloc) {
        HeapBTree(t, owner, mk, mv) := self
        OwnerCheck(a, owner, "HeapBTree.Free")
        Drop(mk)
        Drop(mv)
        if CollNeedsRelease[K]() || CollNeedsRelease[V]() {
            var p := TreeFirst(&t)
            for _ in 0..t.n {
                CollRelease(SlotRead(BtKeys(p.node), p.idx), a)
                CollRelease(SlotRead(BtVals(p.node), p.idx), a)
                p = BtStep(p.node, p.idx)
            }
        }
        TreeFreeNodes(t, a)
    }
}

/// Frees the nodes of an empty tree. Panics (code 11) when it holds an
/// entry, and (code 12) when a is not the allocator given to New.
/// O(spare nodes).
export HeapBTree[K, V].FreeEmpty(self, a: &Alloc) {
    if self.t.n > 0 {
        raw.Fail(11, "HeapBTree.FreeEmpty of a non-empty tree")
    }
    HeapBTree(t, owner, mk, mv) := self
    OwnerCheck(a, owner, "HeapBTree.FreeEmpty")
    Drop(mk)
    Drop(mv)
    TreeFreeNodes(t, a)
}

/// Hands every key and value to release, in key order, then frees every
/// node. Use it for keys or values that are linear and not Free. Panics
/// (code 12) when a is not the allocator given to New. release may
/// capture locals and must consume its arguments. O(n) calls of release.
export HeapBTree[K, V].FreeEach(self, a: &Alloc, const release: fn(K, V)) {
    HeapBTree(t, owner, mk, mv) := self
    OwnerCheck(a, owner, "HeapBTree.FreeEach")
    Drop(mk)
    Drop(mv)
    var p := TreeFirst(&t)
    for _ in 0..t.n {
        release(SlotRead(BtKeys(p.node), p.idx), SlotRead(BtVals(p.node), p.idx))
        p = BtStep(p.node, p.idx)
    }
    TreeFreeNodes(t, a)
}
