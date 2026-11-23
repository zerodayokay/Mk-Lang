// Why: a DFA matches in one table step per byte, the fastest linear-time
// matcher, but a Regex is plain and Share, so no state can be added at
// match time: the DFA must be built eagerly at Compile.
//
// What: Dfa and BuildDfa (the builder DBuild), the scans Scan and ScanRev,
// and the prefilter FindPre over a rare byte pair (RarePair, PrePair).
//
// How: RE2's subset construction (Cox 2010, "Regular Expression Matching in
// the Wild"; RE2 dfa.cc) over the byte program of compile.mk. A state is
// the ordered kernel of a Pike step (the pcs that byte instructions
// reached, before their epsilon closure) and the context flags of the byte
// before it; the closure runs at transition time, when the next byte is
// known, so ^ $ \A \z \b \B are decided as RE2's empty-width flags decide
// them. The kernel keeps Pike's priority order and a match cuts the lower
// threads, so the last match a scan sees before the state dies is the end
// of the leftmost-first match: the DFA and pike.mk agree position by
// position. The alphabet
// is cut into byte classes at every boundary the program, the assertions
// and the scalar-start rule can tell apart. Past MaxDfaStates, MaxDfaCells
// or MaxDfaWork the DFA is dropped and every call runs the Pike VM, which
// is linear too.

import std.core
import std.raw
import std.simd

// A transition cell: the next state's row offset, and DfaMatch when a
// match ended before the byte. Row 0 is the dead state.
const DfaMatch: u32 = 0x80000000
const DfaNext: u32 = 0x7FFFFFFF

// Context flags of a state: the position is 0, the byte before it is \n,
// the byte before it is a word byte, a match was seen (no new thread
// starts, as in pike.mk's Round).
const CBegin: u8 = 1
const CLine: u8 = 2
const CWord: u8 = 4
const CMatched: u8 = 8
// CStart: the scan begins here, where an anchored DFA starts its thread.
const CStart: u8 = 16

// Budgets: states, transition cells, and closure steps, which bound
// Compile's time. The cells (4 bytes each) admit MaxDfaStates states at
// any stride (at most 257: 256 byte classes and the end of the text), so
// the state budget is the one a pattern meets; a table
// is at most MaxDfaCells x 4 bytes, 4,112 KiB (RE2's default budget is
// 8 MB for both directions, filled lazily). A budget of 65,536 cells would
// hold \pL+ (stride 107) to 612 states and drop its DFAs.
const MaxDfaStates: int = 4096
// MaxGenStates is the budget of gen regex.Matcher, whose construction runs
// in the evaluator under its fuel and whose tables become source.
const MaxGenStates: int = 512
const MaxDfaCells: int = 4096 * 257
const MaxDfaWork: int = 4_000_000

// Dfa is the built automaton. ok false: over budget, use the Pike VM.
// cls maps a byte to its class; eot is the class of the end of the text
// and stride the row length (eot + 1); starts holds the row offset of the
// start state for each context of Ctx.
struct Dfa {
    cls: []u8
    trans: []u32
    starts: [4]u32
    eot: u32
    stride: u32
    top: u32
    ok: bool
}

struct DState {
    off: int
    n: int
    flags: u8
}

struct DBuild {
    insts: []Inst
    edges: []Edge
    start: u32
    anchor: bool
    rev: bool
    max: int
    need: u8
    nclass: int
    stride: int
    reps: List[u8]
    states: List[DState]
    pool: List[u32]
    trans: List[u32]
    table: List[u32]
    sparse: List[u32]
    dense: List[u32]
    vn: int
    stack: List[u32]
    kern: List[u32]
    live: List[u32]
    sigs: List[u8]
    mark: List[u32]
    stamp: u32
    work: int
    over: bool
}

// BuildDfa builds the DFA of the program insts, edges, start. Forward
// (rev false): leftmost-first, a thread starting at every scalar start
// until a match is seen, or only at offset 0 when anchor. Reverse (rev,
// the program of the reversed pattern, compile.mk): anchored at the scan's
// first position and longest, so a match cuts nothing and the last match
// a backward scan sees is the leftmost start. Tables and scratch live in a,
// which Compile frees after KeptDfa copies the tables out.
BuildDfa(a: &Arena, insts: []Inst, edges: []Edge, start: u32, anchor: bool, rev: bool, max: int): OutOfMemory!Dfa from a, insts, edges {
    var cls := List[u8].New(a)
    var b := DBuild(insts: insts, edges: edges, start: start, anchor: anchor || rev, rev: rev, max: max, need: CMatched, nclass: 0, stride: 0, reps: List[u8].New(a), states: List[DState].New(a), pool: List[u32].New(a), trans: List[u32].New(a), table: List[u32].New(a), sparse: List[u32].New(a), dense: List[u32].New(a), vn: 0, stack: List[u32].New(a), kern: List[u32].New(a), live: List[u32].New(a), sigs: List[u8].New(a), mark: List[u32].New(a), stamp: 0, work: 0, over: false)
    try b.Classes(mut cls)
    m := insts.Len()
    for _ in 0..m {
        try b.sparse.Push(0)
        try b.dense.Push(0)
        try b.mark.Push(0)
    }
    for _ in 0..2 * max {
        try b.table.Push(0)
    }
    // State 0 is dead: an empty kernel that has matched.
    _ = try b.Lookup(CMatched)
    var starts: [4]u32 = [0; 4]
    var ctx: [4]u8 = [CBegin, CLine, CWord, 0]
    for k in 0..4 {
        f := (ctx[k] | CStart) & b.need
        starts[k] = if anchor && !rev && k != 0 { 0 } else { try b.Lookup(f) }
    }
    var k := 1
    for k < b.states.Len() && !b.over {
        try b.Row(k)
        k += 1
    }
    DBuild(trans, over, stride, nclass, reps: _, states: _, pool: _, table: _, sparse: _, dense: _, stack: _, kern: _, live: _, sigs: _, mark: _, ..) := b
    t := trans.Done()
    // The start states were made first, so a row offset up to top is one:
    // an empty kernel with no match seen, where a scan may skip ahead.
    var top: u32 = 0
    for x in starts {
        if x > top {
            top = x
        }
    }
    return Dfa(ok: !over, cls: cls.Done()[..], trans: t[..], starts: starts, eot: Trunc[u32](nclass), stride: Trunc[u32](stride), top: top)
}

// Classes cuts 0 .. 255 into classes: at every byte range of the program,
// at the continuation bytes 0x80 .. 0xBF (a thread starts only at a scalar
// start), at \n when a line assertion can see it, and at the word bytes
// when \b or \B can. It also sets need, the context flags that matter.
DBuild.Classes(mut self, cls: mut List[u8]): OutOfMemory!void {
    var cut: [257]bool = [false; 257]
    cut[0x80] = true
    cut[0xC0] = true
    if self.anchor {
        self.need = self.need | CStart
    }
    for ins in self.insts {
        if ins.op == OpByte {
            cut[Cast[int](ins.lo)] = true
            cut[Cast[int](ins.hi) + 1] = true
        } else if ins.op == OpSparse {
            f := Cast[int](ins.x)
            for e in f..f + Cast[int](ins.y) {
                ed := self.edges[e]
                cut[Cast[int](ed.lo)] = true
                cut[Cast[int](ed.hi) + 1] = true
            }
        } else if ins.op == OpAssert {
            kind := ins.lo
            if kind == ABeginText {
                self.need = self.need | CBegin
            } else if kind == ABeginLine || kind == AEndLine {
                self.need = self.need | CBegin | CLine
                cut[10] = true
                cut[11] = true
            } else if kind == AWord || kind == ANotWord {
                self.need = self.need | CWord
                var w: [8]int = [48, 58, 65, 91, 95, 96, 97, 123]
                for x in w {
                    cut[x] = true
                }
            }
        }
    }
    var c: u8 = 0
    try self.reps.Push(0)
    try cls.Push(0)
    for x in 1..256 {
        if cut[x] {
            c += 1
            try self.reps.Push(Trunc[u8](x))
        }
        try cls.Push(c)
    }
    self.nclass = Cast[int](c) + 1
    self.stride = self.nclass + 1
    for k in 0..self.stride {
        g := self.Sig(k)
        try self.sigs.Push(g)
    }
}

// Lookup is the row offset of the state (flags, kern), made if it is new.
DBuild.Lookup(mut self, flags: u8): OutOfMemory!u32 {
    var h: u32 = 2166136261 ^ Cast[u32](flags)
    for pc in &self.kern {
        h = (h ^ *pc) *% 16777619
    }
    mask := self.table.Len() - 1
    var i := Cast[int](h ^ (h >> 15)) & mask
    for {
        v := self.table[i]
        if v == 0 {
            break
        }
        s := Cast[int](v) - 1
        if self.Same(s, flags) {
            return Trunc[u32](s * self.stride)
        }
        i = (i + 1) & mask
    }
    s := self.states.Len()
    if s >= self.max || (s + 1) * self.stride > MaxDfaCells {
        self.over = true
        return 0
    }
    off := self.pool.Len()
    for pc in &self.kern {
        try self.pool.Push(*pc)
    }
    try self.states.Push(DState(off: off, n: self.kern.Len(), flags: flags))
    self.table[i] = Trunc[u32](s + 1)
    return Trunc[u32](s * self.stride)
}

DBuild.Same(&self, s: int, flags: u8): bool {
    st := self.states[s]
    if st.flags != flags || st.n != self.kern.Len() {
        return false
    }
    for j in 0..st.n {
        if self.pool[st.off + j] != self.kern[j] {
            return false
        }
    }
    return true
}

// Row fills the transitions of state k (the rows of states 0 .. k - 1 are
// already in trans; state 0's is all dead).
// The closure of a kernel depends on the next byte only through its
// signature (Sig), so Row computes one closure per signature and steps
// every class of that signature over it.
DBuild.Row(mut self, k: int): OutOfMemory!void {
    if k == 1 {
        for _ in 0..self.stride {
            try self.trans.Push(0)
        }
    }
    base := self.trans.Len()
    for _ in 0..self.stride {
        try self.trans.Push(0)
    }
    for sg in 0..9 {
        var have := false
        for c in 0..self.stride {
            if Cast[int](self.sigs[c]) != sg {
                continue
            }
            if !have {
                try self.Closure(k, c)
                have = true
            }
            if self.over {
                return
            }
            e := try self.Step(k, c)
            self.trans[base + c] = e
            if self.over {
                return
            }
        }
    }
}

// Sig is the signature of class c: what the assertions and the start rule
// can see of its bytes (\n, a word byte, a continuation byte), or 8 for
// the end of the text.
DBuild.Sig(&self, c: int): u8 {
    if c == self.nclass {
        return 8
    }
    b := self.reps[c]
    s: u8 = if b == b'\n' { 1 } else if Word(b) { 2 } else { 0 }
    return if b & 0xC0 == 0x80 { s | 4 } else { s }
}

// Closure puts into live, in priority order, the byte and match
// instructions of the closure of state k's kernel before a byte of class
// c's signature, with the start thread last while no match was seen; in
// a forward DFA nothing after the first match, which cuts them.
DBuild.Closure(mut self, k: int, c: int): OutOfMemory!void {
    st := self.states[k]
    eot := c == self.nclass
    b: u8 = if eot { 0 } else { self.reps[c] }
    self.vn = 0
    for j in 0..st.n {
        pc := self.pool[st.off + j]
        try self.Close(pc, st.flags, b, eot)
    }
    if st.flags & CMatched == 0 && (!self.anchor || st.flags & CStart != 0) && (self.rev || eot || b & 0xC0 != 0x80) {
        try self.Close(self.start, st.flags, b, eot)
    }
    if self.work > MaxDfaWork {
        self.over = true
        return
    }
    self.live.Clear()
    for j in 0..self.vn {
        pc := self.dense[j]
        op := self.insts[Cast[int](pc)].op
        if op == OpByte || op == OpSparse || op == OpMatch {
            try self.live.Push(pc)
            if op == OpMatch && !self.rev {
                return
            }
        }
    }
}

// Step is the cell of state k on class c (c == nclass: the end of the
// text), from live (Closure of the same signature): a match there is a
// match before the byte; the byte instructions that take the class's
// bytes give the next kernel.
DBuild.Step(mut self, k: int, c: int): OutOfMemory!u32 {
    st := self.states[k]
    eot := c == self.nclass
    b: u8 = if eot { 0 } else { self.reps[c] }
    self.kern.Clear()
    self.stamp += 1
    var matched := false
    for j in 0..self.live.Len() {
        pc := Cast[int](self.live[j])
        ins := self.insts[pc]
        if ins.op == OpMatch {
            matched = true
            continue
        }
        if eot {
            continue
        }
        if ins.op == OpByte {
            if b >= ins.lo && b <= ins.hi {
                try self.Keep(ins.x)
            }
        } else if ins.op == OpSparse {
            f := Cast[int](ins.x)
            for e in f..f + Cast[int](ins.y) {
                ed := self.edges[e]
                if b < ed.lo {
                    break
                }
                if b <= ed.hi {
                    try self.Keep(ed.next)
                    break
                }
            }
        }
    }
    cell: u32 = if matched { DfaMatch } else { 0 }
    if eot {
        return cell
    }
    var f: u8 = if b == b'\n' { CLine } else if Word(b) { CWord } else { 0 }
    if matched && !self.rev || st.flags & CMatched != 0 {
        f = f | CMatched
    }
    f = f & self.need
    if self.kern.Len() == 0 && (f & CMatched != 0 || self.anchor) {
        return cell
    }
    next := try self.Lookup(f)
    return cell | next
}

// Keep adds pc to the next kernel unless it is there.
DBuild.Keep(mut self, pc: u32): OutOfMemory!void {
    i := Cast[int](pc)
    if self.mark[i] == self.stamp {
        return
    }
    self.mark[i] = self.stamp
    try self.kern.Push(pc)
}

// Close adds the epsilon closure of pc to the visited list, in pike.mk
// Add's order, deciding each assertion from the context flags and the next
// byte b (eot: none).
DBuild.Close(mut self, pc0: u32, flags: u8, b: u8, eot: bool): OutOfMemory!void {
    self.stack.Clear()
    try self.stack.Push(pc0)
    for top := self.stack.Pop() {
        var pc := Cast[int](top)
        for {
            i := Cast[int](self.sparse[pc])
            if i < self.vn && Cast[int](self.dense[i]) == pc {
                break
            }
            self.sparse[pc] = Trunc[u32](self.vn)
            self.dense[self.vn] = Trunc[u32](pc)
            self.vn += 1
            self.work += 1
            ins := self.insts[pc]
            op := ins.op
            if op == OpSplit {
                try self.stack.Push(ins.y)
                pc = Cast[int](ins.x)
                continue
            }
            if op == OpSave || op == OpNop {
                pc = Cast[int](ins.x)
                continue
            }
            if op == OpAssert && Truth(ins.lo, flags, b, eot) {
                pc = Cast[int](ins.x)
                continue
            }
            break
        }
    }
}

// Truth decides assertion kind between the context flags and the next
// byte b (eot: the end of the text), as pike.mk's Holds does on the text.
Truth(kind: u8, flags: u8, b: u8, eot: bool): bool {
    if kind == ABeginText {
        return flags & CBegin != 0
    }
    if kind == AEndText {
        return eot
    }
    if kind == ABeginLine {
        return flags & (CBegin | CLine) != 0
    }
    if kind == AEndLine {
        return eot || b == b'\n'
    }
    before := flags & CWord != 0
    after := !eot && Word(b)
    if kind == AWord {
        return before != after
    }
    return before == after
}

// Ctx is the start context of a scan at pos of s: 0 at the start of the
// text, 1 after \n, 2 after a word byte, 3 otherwise.
Ctx(s: []u8, pos: int): int {
    if pos == 0 {
        return 0
    }
    return CtxOf(s[pos - 1])
}

CtxOf(c: u8): int {
    if c == b'\n' {
        return 1
    }
    if Word(c) {
        return 2
    }
    return 3
}

// Scan runs the forward DFA d over s from pos: the end of the leftmost-first
// match that starts at or after pos, or -1 when there is none. earliest: the
// end of the first match the scan sees, which tells only whether one exists.
// pre is the literal every match begins with ("" for none), pair its RarePair,
// and alt an optional multi-branch fixed-offset SIMD prefilter. Needs d.ok.
Scan(d: &Dfa, s: str, pos: int, earliest: bool, pre: str, pair: PrePair, alts: []PreAlt): int {
    b := s.AsBytes()
    cls := d.cls
    trans := d.trans
    if cls.Len() != 256 {
        return -1
    }
    var st := d.starts[Ctx(b, pos)]
    if st == 0 {
        return -1
    }
    n := b.Len()
    same := d.starts[0] == d.starts[1] && d.starts[0] == d.starts[2] && d.starts[0] == d.starts[3]
    var end := -1
    var i := pos
    if pre.Len() > 0 {
        pb := pre.AsBytes()
        one := pb.Len() == 1
        p0 := pb[0]
        for i < n {
            if st <= d.top {
                j := if one {
                    if k := simd.IndexByte(b[i..n], p0) {
                        i +% k
                    } else {
                        return end
                    }
                } else {
                    FindPrePair(s, pre, pair, i) else {
                        return end
                    }
                }
                i = j
                st = if same { d.starts[0] } else { d.starts[Ctx(b, i)] }
            }
            e := trans[Cast[int](st) +% Cast[int](cls[Cast[int](b[i])])]
            if e & DfaMatch != 0 {
                end = i
                if earliest {
                    return end
                }
            }
            st = e & DfaNext
            if st == 0 {
                return end
            }
            i = i +% 1
        }
    } else if alts.Len() > 0 {
        alt := &alts[0]
        lim := Cast[int](alt.max_off) +% 16
        for i < n {
            if st <= d.top && i +% lim <= n {
                i = FindPreAlt(b, alt, i)
                if i >= n {
                    return end
                }
                st = if same { d.starts[0] } else { d.starts[Ctx(b, i)] }
            }
            e := trans[Cast[int](st) +% Cast[int](cls[Cast[int](b[i])])]
            if e & DfaMatch != 0 {
                end = i
                if earliest {
                    return end
                }
            }
            st = e & DfaNext
            if st == 0 {
                return end
            }
            i = i +% 1
        }
    } else {
        for i < n {
            e := trans[Cast[int](st) +% Cast[int](cls[Cast[int](b[i])])]
            if e & DfaMatch != 0 {
                end = i
                if earliest {
                    return end
                }
            }
            st = e & DfaNext
            if st == 0 {
                return end
            }
            i = i +% 1
        }
    }
    if trans[Cast[int](st) +% Cast[int](d.eot)] & DfaMatch != 0 {
        return n
    }
    return end
}

// LetterRank is ByteRank's order of the lowercase letters a to z: English
// text's letter frequency order "etaoinshrdlcumwfgypbvkjxqz" as 250 minus
// 6 per place (e 250, t 244, ..., z 100).
const LetterRank: [26]u8 = [238, 136, 184, 196, 250, 160, 154, 208, 226, 118, 124, 190, 172, 220, 232, 142, 106, 202, 214, 244, 178, 130, 166, 112, 148, 100]

// ByteRank estimates how common byte c is in text, higher the commoner:
// space first, the lowercase letters in English frequency order, then
// line breaks, sentence punctuation and the punctuation of logs and code
// (= : / _ - " ' ( ) ;), digits, uppercase letters at half their
// lowercase rank, other printable ASCII, UTF-8 bytes, and control bytes
// last. Only the order matters: RarePair takes the lowest.
ByteRank(c: u8): int {
    if c == 0x20 {
        return 255
    }
    if c >= 0x61 && c <= 0x7A {
        return Cast[int](LetterRank[Cast[int](c - 0x61)])
    }
    if c == 0x0A || c == 0x2E || c == 0x2C || c == 0x3D || c == 0x3A || c == 0x2F || c == 0x5F || c == 0x2D || c == 0x22 || c == 0x27 || c == 0x28 || c == 0x29 || c == 0x3B {
        return 150
    }
    if c >= 0x30 && c <= 0x39 {
        return 120
    }
    if c >= 0x41 && c <= 0x5A {
        return Cast[int](LetterRank[Cast[int](c - 0x41)]) / 2
    }
    if (c >= 0x21 && c <= 0x7E) || c == 0x09 || c == 0x0D {
        return 80
    }
    if c >= 0x80 {
        return 60
    }
    return 10
}

// PrePair is the two places of a prefix the pair filter loads, lo < hi.
struct PrePair {
    lo: int
    hi: int
}

// RarePair is the places lo < hi the pair filter loads for p (at least
// 2 bytes): the byte of p with the lowest ByteRank, and the lowest-ranked
// of the bytes at least half p's length (and at least 2) away from it, the
// earlier place on a tie; any other byte when none is that far (2 or 3
// bytes). Rare bytes are the least likely to be in place by chance, as the
// memchr crate picks its packed pair by a frequency rank; bytes close
// together are not independent in text (in a log, "=5" of the prefix
// "status=5" is in place in every "dur=5..."), so the second is taken
// apart from the first, toward Mula's first and last.
RarePair(p: []u8): PrePair {
    m := p.Len()
    var a := 0
    for k in 1..m {
        if ByteRank(p[k]) < ByteRank(p[a]) {
            a = k
        }
    }
    gap := if m / 2 < 2 { 2 } else { m / 2 }
    var b := -1
    for k in 0..m {
        if k + gap <= a || k >= a + gap {
            if b < 0 || ByteRank(p[k]) < ByteRank(p[b]) {
                b = k
            }
        }
    }
    if b < 0 {
        for k in 0..m {
            if k != a && (b < 0 || ByteRank(p[k]) < ByteRank(p[b])) {
                b = k
            }
        }
    }
    if a < b {
        return PrePair(lo: a, hi: b)
    }
    return PrePair(lo: b, hi: a)
}

// FindPre is the first index at or after from where pre occurs in s, as
// core.FindFrom. One byte is simd.IndexByte (memchr). Longer, 16 start
// positions a step are kept where the two bytes of pre that RarePair chose are
// in place (two compares of two loads, ANDed: Mula's SIMD-friendly substring
// search, the memchr crate's packed pair), and each kept one is verified with
// simd.Equal. The verification is budgeted: once its bytes exceed the bytes
// scanned plus 4,096, the rest goes to core.FindFrom's Two-Way search, as the
// memchr crate falls back when its prefilter stops paying, so the whole call is
// O(n + m) whatever the text (TestSTDVECTOR_RegexFindPre).
FindPre(s: str, pre: str, from: int): ?int {
    return FindPrePair(s, pre, PairOf(pre), from)
}

// PairOf is RarePair of pre, or places 0 and 1 for a prefix shorter than 2
// bytes (FindPre does not load a pair for one).
PairOf(pre: str): PrePair {
    if pre.Len() < 2 {
        return PrePair(lo: 0, hi: 1)
    }
    return RarePair(pre.AsBytes())
}

// FindPrePair is FindPre with pre's pair given, as Compile keeps it, so a
// scan that restarts the prefilter after each failed candidate does not
// rank the prefix again.
FindPrePair(s: str, pre: str, pr: PrePair, from: int): ?int {
    b := s.AsBytes()
    p := pre.AsBytes()
    m := p.Len()
    n := b.Len()
    if m == 1 {
        k := simd.IndexByte(b[from..n], p[0]) else {
            return none
        }
        return some(from + k)
    }
    if m == 0 || m > n - from {
        return core.FindFrom(s, pre, from)
    }
    l := n - m + 1
    if l - from >= 256 {
        return core.FindFrom(s, pre, from)
    }
    if Target.os == Os.Freestanding || l - from < 16 {
        return core.FindFrom(s, pre, from)
    }
    lo := pr.lo
    hi := pr.hi
    first := simd.Vec[u8, 16].Splat(p[lo])
    second := simd.Vec[u8, 16].Splat(p[hi])
    w1 := b[lo..lo + l]
    w2 := b[hi..hi + l]
    var i := from
    var work := 0
    for i + 16 <= l {
        var mask := ((simd.Vec[u8, 16].FromSlice(w1, i) == first) & (simd.Vec[u8, 16].FromSlice(w2, i) == second)).Bits()
        for mask != 0 {
            c := i +% raw.Ctz64(mask)
            if simd.Equal(b[c..c +% m], p) {
                return some(c)
            }
            work += m
            mask = mask & (mask -% 1)
        }
        i = i +% 16
        if work > i - from + 4096 {
            return core.FindFrom(s, pre, i)
        }
    }
    if i < l {
        t := l - 16
        var mask := ((simd.Vec[u8, 16].FromSlice(w1, t) == first) & (simd.Vec[u8, 16].FromSlice(w2, t) == second)).Bits() >> (16 - (l - i))
        for mask != 0 {
            c := i +% raw.Ctz64(mask)
            if simd.Equal(b[c..c +% m], p) {
                return some(c)
            }
            mask = mask & (mask -% 1)
        }
    }
    return none
}

// One fixed-offset byte check of a multi-branch prefilter:
// (b[i + off] | fold) == want, where fold is 0 (exact) or 0x20 (ASCII letter).
struct PreCheck {
    off: u8
    want: u8
    fold: u8
}

struct PreBranch {
    len: u8
    c: [4]PreCheck
}

// PreAlt is a SIMD prefilter for an unanchored pattern whose every match
// satisfies at least one of n (1..4) fixed-offset branches.
// mode: 0 off, 1 = 1-byte start set (2..4 bytes, off 0, exact),
// 2 = 2-branch exact (up to 4 checks/branch), 3 = up to 4-branch (exact or folded).
struct PreAlt {
    mode: u8
    n: u8
    max_off: u8
    fold: bool
    br: [4]PreBranch
}

EmptyPreAlt(): PreAlt {
    z := PreCheck(off: 0, want: 0, fold: 0)
    b := PreBranch(len: 0, c: [z; 4])
    return PreAlt(mode: 0, n: 0, max_off: 0, fold: false, br: [b; 4])
}

// FindPreAlt returns the first position >= from where at least one branch of
// alt matches its SIMD byte checks, or the first unchecked tail position (or
// b.Len() when mode 1 finds no start byte in b[from..]).
// Caller guarantees from + alt.max_off + 16 <= b.Len().
FindPreAlt(b: []u8, alt: &PreAlt, from: int): int {
    if alt.mode == 1 {
        set: [4]u8 = [alt.br[0].c[0].want, alt.br[1].c[0].want, alt.br[2].c[0].want, alt.br[3].c[0].want]
        if k := simd.IndexAny(b[from..], set[0..Cast[int](alt.n)]) {
            return from +% k
        }
        return b.Len()
    }
    if Target.os == Os.Freestanding {
        return from
    }
    l := b.Len() - Cast[int](alt.max_off)
    var i := from
    if alt.mode == 2 {
        a := alt.br[0].c
        d := alt.br[1].c
        wa0 := b[Cast[int](a[0].off)..Cast[int](a[0].off) + l]
        wa1 := b[Cast[int](a[1].off)..Cast[int](a[1].off) + l]
        wa2 := b[Cast[int](a[2].off)..Cast[int](a[2].off) + l]
        wa3 := b[Cast[int](a[3].off)..Cast[int](a[3].off) + l]
        wb0 := b[Cast[int](d[0].off)..Cast[int](d[0].off) + l]
        wb1 := b[Cast[int](d[1].off)..Cast[int](d[1].off) + l]
        wb2 := b[Cast[int](d[2].off)..Cast[int](d[2].off) + l]
        wb3 := b[Cast[int](d[3].off)..Cast[int](d[3].off) + l]
        va0 := simd.Vec[u8, 16].Splat(a[0].want)
        va1 := simd.Vec[u8, 16].Splat(a[1].want)
        va2 := simd.Vec[u8, 16].Splat(a[2].want)
        va3 := simd.Vec[u8, 16].Splat(a[3].want)
        vb0 := simd.Vec[u8, 16].Splat(d[0].want)
        vb1 := simd.Vec[u8, 16].Splat(d[1].want)
        vb2 := simd.Vec[u8, 16].Splat(d[2].want)
        vb3 := simd.Vec[u8, 16].Splat(d[3].want)
        for i +% 16 <= l {
            m0 := (simd.Vec[u8, 16].FromSlice(wa0, i) == va0) & (simd.Vec[u8, 16].FromSlice(wa1, i) == va1) &
                (simd.Vec[u8, 16].FromSlice(wa2, i) == va2) & (simd.Vec[u8, 16].FromSlice(wa3, i) == va3)
            m1 := (simd.Vec[u8, 16].FromSlice(wb0, i) == vb0) & (simd.Vec[u8, 16].FromSlice(wb1, i) == vb1) &
                (simd.Vec[u8, 16].FromSlice(wb2, i) == vb2) & (simd.Vec[u8, 16].FromSlice(wb3, i) == vb3)
            mask := (m0 | m1).Bits()
            if mask != 0 {
                return i +% raw.Ctz64(mask)
            }
            i = i +% 16
        }
        return i
    }
    nb := Cast[int](alt.n)
    for i +% 16 <= l {
        var any := simd.Mask[u8, 16].FromBits(0)
        for k in 0..nb {
            br := alt.br[k]
            c := br.c
            var m := (simd.Vec[u8, 16].FromSlice(b, i +% Cast[int](c[0].off)) | simd.Vec[u8, 16].Splat(c[0].fold)) == simd.Vec[u8, 16].Splat(c[0].want)
            for j in 1..Cast[int](br.len) {
                m = m & ((simd.Vec[u8, 16].FromSlice(b, i +% Cast[int](c[j].off)) | simd.Vec[u8, 16].Splat(c[j].fold)) == simd.Vec[u8, 16].Splat(c[j].want))
            }
            any = any | m
        }
        mask := any.Bits()
        if mask != 0 {
            return i +% raw.Ctz64(mask)
        }
        i = i +% 16
    }
    return i
}

// ScanRev runs the reverse DFA d over s backward from end down to pos:
// the smallest start at or after pos of a match of the pattern that ends
// at end, or -1 when there is none. The context of the start is the byte
// at end (the start of the reversed text when end is s.Len()), and the
// byte before pos decides the assertions at pos.
ScanRev(d: &Dfa, s: []u8, pos: int, end: int): int {
    cls := d.cls
    trans := d.trans
    if cls.Len() != 256 {
        return -1
    }
    n := s.Len()
    c := if end < n { CtxOf(s[end]) } else { 0 }
    var st := d.starts[c]
    var start := -1
    var i := end
    for i > pos {
        e := trans[Cast[int](st) +% Cast[int](cls[Cast[int](s[i -% 1])])]
        if e & DfaMatch != 0 {
            start = i
        }
        st = e & DfaNext
        if st == 0 {
            return start
        }
        i = i -% 1
    }
    e: u32 = if pos == 0 {
        trans[Cast[int](st) +% Cast[int](d.eot)]
    } else {
        trans[Cast[int](st) +% Cast[int](cls[Cast[int](s[pos -% 1])])]
    }
    if e & DfaMatch != 0 {
        return pos
    }
    return start
}
