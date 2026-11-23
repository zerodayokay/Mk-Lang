// Why: std.regex accepts RE2's syntax without back-references, and must
// accept exactly what Go's regexp.Compile accepts, with fixed limits on
// size and nesting.
//
// What: ParseTree, the Parser and its Node and Tree, with the node kinds
// (KLit, KClass, KCat, KAlt, KRep, KCap, KAssert, KEmpty), the flags
// (FFold, FMulti, FDotNL, FUngreedy) and the limits MaxDepth and MaxRepeat.
//
// How: a recursive descent parser from the pattern's bytes to an AST in the
// caller's arena. The grammar, its flags and its class and escape rules
// follow Go's regexp/syntax parse.go with syntax.Perl (RE2's flags:
// ClassNL, OneLine, PerlX, UnicodeGroups), rule by rule
// (TestSTDREGEX_SyntaxParity), but for the limits: 4,096 instructions
// (MaxInsts) and 1,000 levels of nesting (MaxDepth), which Go does not
// count as Mk does. \p{..} and (?i) read the tables of tables.mk through
// unicode.mk, Go's names over UCD 18.0.0.

// Flags of the parser, as Go's syntax.Flags.
const FFold: u8 = 1
const FDotNL: u8 = 2
const FMulti: u8 = 4
const FUngreedy: u8 = 8

// Node kinds.
const KEmpty: u8 = 0
const KLit: u8 = 1
const KClass: u8 = 2
const KCat: u8 = 3
const KAlt: u8 = 4
const KRep: u8 = 5
const KCap: u8 = 6
const KAssert: u8 = 7

// Assertion kinds (Inst.lo of OpAssert).
const ABeginText: u8 = 1
const AEndText: u8 = 2
const ABeginLine: u8 = 3
const AEndLine: u8 = 4
const AWord: u8 = 5
const ANotWord: u8 = 6

// Node.flags of a KRep.
const NGreedy: u8 = 1
const NBrace: u8 = 2

// MaxRepeat is Go's bound on a repeat count, and on the product of nested
// counted repeats (repeatIsValid).
const MaxRepeat: int = 1000

// MaxDepth bounds the nesting of groups, of every kind: a group inside more
// than 1,000 others is TooLarge, which bounds the recursion of the parser and
// the compiler. It is not Go's maxHeight, the height of Go's syntax tree, which
// counts no (?:..) and counts a capture and a concatenation each: Go accepts
// 5,000 nested (?: and refuses 1,000 nested ( and 600 nested (b, where Mk
// refuses the first and accepts the other two (TestSTDREGEX_NestingLimit).
const MaxDepth: int = 1000

const MaxRune: u32 = 0x10FFFF

// Node is one AST node. KLit: a is the scalar. KClass: ranges a .. a + b.
// KCat, KAlt: kids a .. a + b. KRep: child a, min b, max c (-1: none).
// KCap: child a, group b (from 1). KAssert: a is the kind.
struct Node {
    kind: u8
    flags: u8
    a: int
    b: int
    c: int
}

// Rr is a closed range of scalars, lo <= hi.
struct Rr {
    lo: u32
    hi: u32
}

// Tree is a parsed pattern. names[i] is group i's name ("" when unnamed),
// names[0] the whole match's.
struct Tree {
    nodes: []Node
    kids: []int
    ranges: []Rr
    names: []str
    root: int
    groups: int
}

struct Parser {
    b: []u8
    pat: str
    pos: int
    flags: u8
    depth: int
    groups: int
    nodes: List[Node]
    kids: List[int]
    stack: List[int]
    ranges: List[Rr]
    tmp: List[Rr]
    names: List[str]
    arena: &Arena
}

ParseTree(a: &Arena, pattern: str): Error!Tree from a {
    // The pattern is copied into a first, so the tree and the names in it
    // hold loans of a alone.
    pat := try a.Dup(pattern)
    var p := Parser(b: pat.AsBytes(), pat: pat, pos: 0, flags: 0, depth: 0, groups: 0, nodes: List[Node].New(a), kids: List[int].New(a), stack: List[int].New(a), ranges: List[Rr].New(a), tmp: List[Rr].New(a), names: List[str].New(a), arena: a)
    try p.names.Push("")
    root := try p.Alt()
    if p.pos < p.b.Len() {
        // Only an unmatched ) stops Alt early at depth 0.
        fail Syntax(offset: p.pos)
    }
    Parser(nodes, kids, ranges, names, groups, stack: _, tmp: _, ..) := p
    return Tree(nodes: nodes.Done()[..], kids: kids.Done()[..], ranges: ranges.Done()[..], names: names.Done()[..], root: root, groups: groups)
}

Parser.Node(mut self, kind: u8, flags: u8, a: int, b: int, c: int): OutOfMemory!int {
    try self.nodes.Push(Node(kind: kind, flags: flags, a: a, b: b, c: c))
    return self.nodes.Len() - 1
}

// Seal turns the items pushed since base into one node: the item itself
// when there is one, KEmpty when there is none, else a KCat or KAlt.
Parser.Seal(mut self, kind: u8, base: int): OutOfMemory!int {
    n := self.stack.Len() - base
    if n == 1 {
        // The stack holds the one item pushed since base.
        return self.stack.Pop().Must()
    }
    if n == 0 {
        return try self.Node(KEmpty, 0, 0, 0, 0)
    }
    first := self.kids.Len()
    for i in base..self.stack.Len() {
        try self.kids.Push(self.stack[i])
    }
    for self.stack.Len() > base {
        _ = self.stack.Pop()
    }
    return try self.Node(kind, 0, first, n, 0)
}

Parser.Alt(mut self): Error!int {
    base := self.stack.Len()
    first := try self.Concat()
    try self.stack.Push(first)
    for self.pos < self.b.Len() && self.b[self.pos] == b'|' {
        self.pos += 1
        n := try self.Concat()
        try self.stack.Push(n)
    }
    return try self.Seal(KAlt, base)
}

Parser.Concat(mut self): Error!int {
    base := self.stack.Len()
    var repeated := false
    for self.pos < self.b.Len() {
        c := self.b[self.pos]
        if c == b'|' || c == b')' {
            break
        }
        if c == b'*' || c == b'+' || c == b'?' {
            try self.Repeat(base, repeated, 0, 0)
            repeated = true
            continue
        }
        if c == b'{' {
            r := self.Counts(self.pos)
            if r.ok {
                try self.Repeat(base, repeated, r.min, r.max)
                repeated = true
                continue
            }
        }
        repeated = false
        try self.Atom()
    }
    return try self.Seal(KCat, base)
}

// Counts reads {n}, {n,} or {n,m} at i, as Go's parseRepeat: no sign, no
// leading zero, a number past 10^8 is -1. ok is false when it is not one,
// and then { is a literal.
Parser.Counts(&self, i: int): (ok: bool, min: int, max: int, end: int) {
    no := (ok: false, min: 0, max: 0, end: i)
    b := self.b
    var j := i + 1
    lo := Int(b, j)
    if lo.end == j {
        return no
    }
    j = lo.end
    var hi := lo.n
    if j >= b.Len() {
        return no
    }
    if b[j] == b',' {
        j += 1
        if j >= b.Len() {
            return no
        }
        if b[j] == b'}' {
            hi = -1
        } else {
            h := Int(b, j)
            if h.end == j {
                return no
            }
            j = h.end
            hi = h.n
            if hi < 0 {
                return (ok: true, min: -1, max: 0, end: j + 1)
            }
        }
    }
    if j >= b.Len() || b[j] != b'}' {
        return no
    }
    return (ok: true, min: lo.n, max: hi, end: j + 1)
}

// Int reads a decimal number at i: end == i when there is none (or it has
// a leading zero); n is -1 past 10^8.
Int(b: []u8, i: int): (n: int, end: int) {
    if i >= b.Len() || b[i] < b'0' || b[i] > b'9' {
        return (n: 0, end: i)
    }
    if i + 1 < b.Len() && b[i] == b'0' && b[i + 1] >= b'0' && b[i + 1] <= b'9' {
        return (n: 0, end: i)
    }
    var n := 0
    var j := i
    for j < b.Len() && b[j] >= b'0' && b[j] <= b'9' {
        if n >= 0 {
            n = if n >= 100_000_000 { -1 } else { n * 10 + Cast[int](b[j] - b'0') }
        }
        j += 1
    }
    return (n: n, end: j)
}

// Repeat applies the operator at pos to the last item: min and max come
// from Counts for {}, else from the operator.
Parser.Repeat(mut self, base: int, repeated: bool, cmin: int, cmax: int): Error!void {
    start := self.pos
    c := self.b[self.pos]
    var min := cmin
    var max := cmax
    var flags: u8 = 0
    if c == b'{' {
        r := self.Counts(self.pos)
        if r.min < 0 || r.min > MaxRepeat || r.max > MaxRepeat || r.max >= 0 && r.min > r.max {
            fail Syntax(offset: start)
        }
        self.pos = r.end
        flags = NBrace
    } else {
        min = 0
        max = -1
        if c == b'+' {
            min = 1
        } else if c == b'?' {
            max = 1
        }
        self.pos += 1
    }
    var greedy := self.flags & FUngreedy == 0
    if self.pos < self.b.Len() && self.b[self.pos] == b'?' {
        self.pos += 1
        greedy = !greedy
    }
    if repeated || self.stack.Len() == base {
        fail Syntax(offset: start)
    }
    if greedy {
        flags = flags | NGreedy
    }
    // The stack holds more than base items: the check above failed otherwise.
    sub := self.stack.Pop().Must()
    n := try self.Node(KRep, flags, sub, min, max)
    if flags & NBrace != 0 && (min >= 2 || max >= 2) && !RepeatValid(self.nodes[..], self.kids[..], n, MaxRepeat) {
        fail Syntax(offset: start)
    }
    try self.stack.Push(n)
}

// RepeatValid is Go's repeatIsValid: the counted repeats nested in node i
// make at most n copies of the innermost item.
RepeatValid(nodes: []Node, kids: []int, i: int, n: int): bool {
    nd := nodes[i]
    var left := n
    if nd.kind == KRep && nd.flags & NBrace != 0 {
        var m := nd.c
        if m == 0 {
            return true
        }
        if m < 0 {
            m = nd.b
        }
        if m > left {
            return false
        }
        if m > 0 {
            left = left / m
        }
    }
    if nd.kind == KRep || nd.kind == KCap {
        return RepeatValid(nodes, kids, nd.a, left)
    }
    if nd.kind == KCat || nd.kind == KAlt {
        for k in nd.a..nd.a + nd.b {
            if !RepeatValid(nodes, kids, kids[k], left) {
                return false
            }
        }
    }
    return true
}

// Atom parses one item at pos and pushes it; a flag group pushes nothing.
Parser.Atom(mut self): Error!void {
    c := self.b[self.pos]
    if c == b'(' {
        try self.Group()
        return
    }
    if c == b'[' {
        try self.Class()
        return
    }
    if c == b'.' {
        self.pos += 1
        try self.tmp.Push(Rr(lo: 0, hi: MaxRune))
        if self.flags & FDotNL == 0 {
            self.tmp[0] = Rr(lo: 0, hi: 9)
            try self.tmp.Push(Rr(lo: 11, hi: MaxRune))
        }
        try self.PushClass(false)
        return
    }
    if c == b'^' {
        self.pos += 1
        try self.PushAssert(if self.flags & FMulti != 0 { ABeginLine } else { ABeginText })
        return
    }
    if c == b'$' {
        self.pos += 1
        try self.PushAssert(if self.flags & FMulti != 0 { AEndLine } else { AEndText })
        return
    }
    if c == b'\\' {
        try self.Backslash()
        return
    }
    r := Decode(self.b, self.pos)
    self.pos += r.n
    try self.PushLit(r.c)
}

Parser.PushAssert(mut self, kind: u8): OutOfMemory!void {
    n := try self.Node(KAssert, 0, Cast[int](kind), 0, 0)
    try self.stack.Push(n)
}

// PushLit pushes the scalar c; under (?i), when c folds, its case
// orbit as a class.
Parser.PushLit(mut self, c: u32): OutOfMemory!void {
    if self.flags & FFold != 0 && Folds(c) {
        try self.tmp.Push(Rr(lo: c, hi: c))
        try self.FoldTmp(0)
        try self.PushClass(false)
        return
    }
    n := try self.Node(KLit, 0, Cast[int](c), 0, 0)
    try self.stack.Push(n)
}

// Backslash parses an escape outside a class.
Parser.Backslash(mut self): Error!void {
    start := self.pos
    if start + 1 >= self.b.Len() {
        fail Syntax(offset: start)
    }
    d := self.b[start + 1]
    if d == b'A' || d == b'z' || d == b'b' || d == b'B' {
        self.pos += 2
        kind := if d == b'A' {
            ABeginText
        } else if d == b'z' {
            AEndText
        } else if d == b'b' {
            AWord
        } else {
            ANotWord
        }
        try self.PushAssert(kind)
        return
    }
    if d == b'Q' {
        self.pos += 2
        for self.pos < self.b.Len() {
            if self.b[self.pos] == b'\\' && self.pos + 1 < self.b.Len() && self.b[self.pos + 1] == b'E' {
                self.pos += 2
                return
            }
            r := Decode(self.b, self.pos)
            self.pos += r.n
            try self.PushLit(r.c)
        }
        return
    }
    if d == b'p' || d == b'P' {
        try self.Unicode()
        try self.PushClass(false)
        return
    }
    if try self.Perl(start) {
        try self.PushClass(false)
        return
    }
    c := try self.Escape()
    try self.PushLit(c)
}

// Perl reads \d \s \w or a negation at i into tmp, folded under (?i), and
// moves past it; false (and nothing read) when there is none.
Parser.Perl(mut self, i: int): Error!bool {
    if i + 1 >= self.b.Len() || self.b[i] != b'\\' {
        return false
    }
    d := self.b[i + 1]
    base := self.tmp.Len()
    if d == b'd' || d == b'D' {
        try self.tmp.Push(R(b'0', b'9'))
    } else if d == b's' || d == b'S' {
        try self.tmp.Push(R(b'\t', b'\n'))
        try self.tmp.Push(R(12, b'\r'))
        try self.tmp.Push(R(b' ', b' '))
    } else if d == b'w' || d == b'W' {
        try self.tmp.Push(R(b'0', b'9'))
        try self.tmp.Push(R(b'A', b'Z'))
        try self.tmp.Push(R(b'_', b'_'))
        try self.tmp.Push(R(b'a', b'z'))
    } else {
        return false
    }
    self.pos = i + 2
    try self.Group1(base, d == b'D' || d == b'S' || d == b'W')
    return true
}

// Group1 finishes a Perl or POSIX group read into tmp from base: folded
// under (?i), then negated when neg, as Go's appendGroup.
Parser.Group1(mut self, base: int, neg: bool): OutOfMemory!void {
    if self.flags & FFold != 0 {
        try self.FoldTmp(base)
    }
    if neg {
        Normalize(mut self.tmp, base)
        try Negate(mut self.tmp, base)
    }
}

// Escape reads the escape at pos (a backslash), as Go's parseEscape, and
// returns its scalar.
Parser.Escape(mut self): Error!u32 {
    start := self.pos
    b := self.b
    if start + 1 >= b.Len() {
        fail Syntax(offset: start)
    }
    c := b[start + 1]
    self.pos = start + 2
    if c < 0x80 && !Alnum(c) {
        return Cast[u32](c)
    }
    if c >= b'1' && c <= b'7' {
        // A single non-zero digit is a back-reference: refused.
        if self.pos >= b.Len() || b[self.pos] < b'0' || b[self.pos] > b'7' {
            fail Syntax(offset: start)
        }
    }
    if c >= b'0' && c <= b'7' {
        var r := Cast[u32](c - b'0')
        var more := 0
        for more < 2 {
            more += 1
            if self.pos >= b.Len() || b[self.pos] < b'0' || b[self.pos] > b'7' {
                break
            }
            r = r * 8 + Cast[u32](b[self.pos] - b'0')
            self.pos += 1
        }
        return r
    }
    if c == b'x' {
        if self.pos >= b.Len() {
            fail Syntax(offset: start)
        }
        if b[self.pos] == b'{' {
            self.pos += 1
            var r: u32 = 0
            var digits := 0
            for {
                if self.pos >= b.Len() {
                    fail Syntax(offset: start)
                }
                h := b[self.pos]
                self.pos += 1
                if h == b'}' {
                    break
                }
                v := Hex(h)
                if v < 0 {
                    fail Syntax(offset: start)
                }
                r = r * 16 + Cast[u32](v)
                if r > MaxRune {
                    fail Syntax(offset: start)
                }
                digits += 1
            }
            if digits == 0 {
                fail Syntax(offset: start)
            }
            return r
        }
        if self.pos + 1 >= b.Len() {
            fail Syntax(offset: start)
        }
        x := Hex(b[self.pos])
        y := Hex(b[self.pos + 1])
        if x < 0 || y < 0 {
            fail Syntax(offset: start)
        }
        self.pos += 2
        return Cast[u32](x * 16 + y)
    }
    if c == b'a' {
        return 7
    }
    if c == b'f' {
        return 12
    }
    if c == b'n' {
        return 10
    }
    if c == b'r' {
        return 13
    }
    if c == b't' {
        return 9
    }
    if c == b'v' {
        return 11
    }
    fail Syntax(offset: start)
}

Alnum(c: u8): bool {
    return c >= b'0' && c <= b'9' || c >= b'A' && c <= b'Z' || c >= b'a' && c <= b'z'
}

Word(c: u8): bool {
    return Alnum(c) || c == b'_'
}

Hex(c: u8): int {
    if c >= b'0' && c <= b'9' {
        return Cast[int](c - b'0')
    }
    if c >= b'a' && c <= b'f' {
        return Cast[int](c - b'a') + 10
    }
    if c >= b'A' && c <= b'F' {
        return Cast[int](c - b'A') + 10
    }
    return -1
}

// Decode reads the scalar at i of valid UTF-8 b.
Decode(b: []u8, i: int): (c: u32, n: int) {
    c := b[i]
    if c < 0x80 {
        return (c: Cast[u32](c), n: 1)
    }
    if c < 0xE0 {
        return (c: Cast[u32](c & 0x1F) << 6 | Cast[u32](b[i + 1] & 0x3F), n: 2)
    }
    if c < 0xF0 {
        return (c: Cast[u32](c & 0x0F) << 12 | Cast[u32](b[i + 1] & 0x3F) << 6 | Cast[u32](b[i + 2] & 0x3F), n: 3)
    }
    return (c: Cast[u32](c & 0x07) << 18 | Cast[u32](b[i + 1] & 0x3F) << 12 | Cast[u32](b[i + 2] & 0x3F) << 6 | Cast[u32](b[i + 3] & 0x3F), n: 4)
}

// Group parses ( ... ) at pos: a capture, a named capture, a flag group
// (?flags) that pushes nothing, or (?flags:re), as Go's parsePerlFlags.
Parser.Group(mut self): Error!void {
    start := self.pos
    b := self.b
    n := b.Len()
    if start + 1 < n && b[start + 1] == b'?' {
        name_at := if start + 4 < n && b[start + 2] == b'P' && b[start + 3] == b'<' {
            start + 4
        } else if start + 3 < n && b[start + 2] == b'<' {
            start + 3
        } else {
            0
        }
        if name_at > 0 {
            var e := name_at
            for e < n && b[e] != b'>' {
                e += 1
            }
            if e >= n || e == name_at {
                fail Syntax(offset: start)
            }
            for k in name_at..e {
                if !Word(b[k]) {
                    fail Syntax(offset: start)
                }
            }
            name := self.pat[name_at..e]
            self.pos = e + 1
            try self.Capture(start, name)
            return
        }
        var flags := self.flags
        var neg := false
        var saw := false
        var i := start + 2
        for i < n {
            c := b[i]
            i += 1
            if c == b'i' || c == b'm' || c == b's' || c == b'U' {
                bit := if c == b'm' {
                    FMulti
                } else if c == b's' {
                    FDotNL
                } else if c == b'U' {
                    FUngreedy
                } else {
                    FFold
                }
                flags = if neg { flags & ~bit } else { flags | bit }
                saw = true
                continue
            }
            if c == b'-' {
                if neg {
                    break
                }
                neg = true
                saw = false
                continue
            }
            if c == b':' || c == b')' {
                if neg && !saw {
                    break
                }
                self.pos = i
                if c == b')' {
                    self.flags = flags
                    return
                }
                outer := self.flags
                self.flags = flags
                inner := try self.Body(start)
                self.flags = outer
                try self.stack.Push(inner)
                return
            }
            break
        }
        fail Syntax(offset: start)
    }
    self.pos = start + 1
    try self.Capture(start, "")
}

// Capture parses the body of a capturing group, numbered at its (.
Parser.Capture(mut self, start: int, name: str into self): Error!void {
    self.groups += 1
    k := self.groups
    try self.names.Push(name)
    outer := self.flags
    inner := try self.Body(start)
    self.flags = outer
    n := try self.Node(KCap, 0, inner, k, 0)
    try self.stack.Push(n)
}

// Body parses a group's alternation and its ), the group opened at start.
Parser.Body(mut self, start: int): Error!int {
    self.depth += 1
    if self.depth > MaxDepth {
        fail TooLarge
    }
    inner := try self.Alt()
    if self.pos >= self.b.Len() || self.b[self.pos] != b')' {
        fail Syntax(offset: start)
    }
    self.pos += 1
    self.depth -= 1
    return inner
}

// Class parses [ ... ] at pos, as Go's parseClass with ClassNL and PerlX.
Parser.Class(mut self): Error!void {
    start := self.pos
    b := self.b
    n := b.Len()
    self.pos += 1
    var neg := false
    if self.pos < n && b[self.pos] == b'^' {
        neg = true
        self.pos += 1
    }
    var first := true
    for self.pos >= n || b[self.pos] != b']' || first {
        if self.pos >= n {
            fail Syntax(offset: start)
        }
        first = false
        if self.pos + 2 < n && b[self.pos] == b'[' && b[self.pos + 1] == b':' {
            if try self.Posix(start) {
                continue
            }
        }
        if b[self.pos] == b'\\' && self.pos + 1 < n && (b[self.pos + 1] == b'p' || b[self.pos + 1] == b'P') {
            try self.Unicode()
            continue
        }
        if try self.Perl(self.pos) {
            continue
        }
        at := self.pos
        lo := try self.ClassChar(start)
        var hi := lo
        if self.pos + 1 < n && b[self.pos] == b'-' && b[self.pos + 1] != b']' {
            self.pos += 1
            hi = try self.ClassChar(start)
            if hi < lo {
                fail Syntax(offset: at)
            }
        }
        k := self.tmp.Len()
        try self.tmp.Push(Rr(lo: lo, hi: hi))
        if self.flags & FFold != 0 {
            try self.FoldTmp(k)
        }
    }
    self.pos += 1
    try self.PushClass(neg)
}

// ClassChar reads one scalar in a class, escapes included.
Parser.ClassChar(mut self, start: int): Error!u32 {
    if self.pos >= self.b.Len() {
        fail Syntax(offset: start)
    }
    if self.b[self.pos] == b'\\' {
        return try self.Escape()
    }
    r := Decode(self.b, self.pos)
    self.pos += r.n
    return r.c
}

// Posix reads [:name:] or [:^name:] at pos into tmp. Go looks for the
// first ":]" anywhere after "[:"; with none the [ is a plain character
// (false), and an unknown name is an error.
Parser.Posix(mut self, start: int): Error!bool {
    b := self.b
    i := self.pos
    var e := i + 2
    for e + 1 < b.Len() && !(b[e] == b':' && b[e + 1] == b']') {
        e += 1
    }
    if e + 1 >= b.Len() {
        return false
    }
    var k := i + 2
    var neg := false
    if k < e && b[k] == b'^' {
        neg = true
        k += 1
    }
    name := self.pat[k..e]
    base := self.tmp.Len()
    if name == "alnum" {
        try self.Push2(R(b'0', b'9'), R(b'A', b'Z'))
        try self.tmp.Push(R(b'a', b'z'))
    } else if name == "alpha" {
        try self.Push2(R(b'A', b'Z'), R(b'a', b'z'))
    } else if name == "ascii" {
        try self.tmp.Push(R(0, 0x7F))
    } else if name == "blank" {
        try self.Push2(R(b'\t', b'\t'), R(b' ', b' '))
    } else if name == "cntrl" {
        try self.Push2(R(0, 0x1F), R(0x7F, 0x7F))
    } else if name == "digit" {
        try self.tmp.Push(R(b'0', b'9'))
    } else if name == "graph" {
        try self.tmp.Push(R(b'!', b'~'))
    } else if name == "lower" {
        try self.tmp.Push(R(b'a', b'z'))
    } else if name == "print" {
        try self.tmp.Push(R(b' ', b'~'))
    } else if name == "punct" {
        try self.Push2(R(b'!', b'/'), R(b':', b'@'))
        try self.Push2(R(b'[', b'`'), R(b'{', b'~'))
    } else if name == "space" {
        try self.Push2(R(b'\t', b'\r'), R(b' ', b' '))
    } else if name == "upper" {
        try self.tmp.Push(R(b'A', b'Z'))
    } else if name == "word" {
        try self.Push2(R(b'0', b'9'), R(b'A', b'Z'))
        try self.Push2(R(b'_', b'_'), R(b'a', b'z'))
    } else if name == "xdigit" {
        try self.Push2(R(b'0', b'9'), R(b'A', b'F'))
        try self.tmp.Push(R(b'a', b'f'))
    } else {
        fail Syntax(offset: i)
    }
    self.pos = e + 2
    try self.Group1(base, neg)
    return true
}

Parser.Push2(mut self, x: Rr, y: Rr): OutOfMemory!void {
    try self.tmp.Push(x)
    try self.tmp.Push(y)
}

// Unicode reads \pN, \p{name}, \PN or \P{name} at pos, a leading ^ in the
// name negating it, into tmp, folded under (?i), as Go's
// parseUnicodeClass: the name is the one scalar after \p, or everything
// up to the first } after {, and an unknown name or a missing } is Syntax
// at the backslash.
Parser.Unicode(mut self): Error!void {
    b := self.b
    at := self.pos
    var neg := b[at + 1] == b'P'
    i := at + 2
    if i >= b.Len() {
        fail Syntax(offset: at)
    }
    var lo := i
    var hi := i
    if b[i] != b'{' {
        hi = i + Decode(b, i).n
        self.pos = hi
    } else {
        var e := i + 1
        for e < b.Len() && b[e] != b'}' {
            e += 1
        }
        if e >= b.Len() {
            fail Syntax(offset: at)
        }
        lo = i + 1
        hi = e
        self.pos = e + 1
    }
    if hi > lo && b[lo] == b'^' {
        neg = !neg
        lo += 1
    }
    k := UcdFind(b[lo..hi])
    if k < 0 {
        fail Syntax(offset: at)
    }
    base := self.tmp.Len()
    first := Cast[int](UcdStart[k])
    for j in first..first + Cast[int](UcdCount[k]) {
        try self.tmp.Push(Rr(lo: UcdRanges[2 * j], hi: UcdRanges[2 * j + 1]))
    }
    try self.Group1(base, neg)
}

// FoldTmp adds the simple case-fold orbits of every scalar of tmp[base..]
// (Go's appendFoldedRange): for each orbit key inside a range, the rest of
// its orbit, walked through FoldNext. Time O(r log f + k log f) for r
// ranges, f keys and k orbit members added.
Parser.FoldTmp(mut self, base: int): OutOfMemory!void {
    end := self.tmp.Len()
    nk := FoldKeys.Len()
    for i in base..end {
        r := self.tmp[i]
        var k := FoldAt(r.lo).i
        for k < nk && FoldKeys[k] <= r.hi {
            c := FoldKeys[k]
            var f := FoldNext[k]
            for f != c {
                if !Has(r, f) {
                    try self.tmp.Push(Rr(lo: f, hi: f))
                }
                f = FoldNext[FoldAt(f).i]
            }
            k += 1
        }
    }
}

// R is the range of two ASCII bytes.
R(lo: u8, hi: u8): Rr {
    return Rr(lo: Cast[u32](lo), hi: Cast[u32](hi))
}

Has(r: Rr, c: u32): bool {
    return r.lo <= c && c <= r.hi
}

// PushClass moves tmp into ranges as one sorted, merged class (negated
// when neg) and pushes its node; tmp is left empty.
Parser.PushClass(mut self, neg: bool): OutOfMemory!void {
    Normalize(mut self.tmp, 0)
    if neg {
        try Negate(mut self.tmp, 0)
    }
    first := self.ranges.Len()
    for i in 0..self.tmp.Len() {
        try self.ranges.Push(self.tmp[i])
    }
    count := self.tmp.Len()
    self.tmp.Clear()
    n := try self.Node(KClass, 0, first, count, 0)
    try self.stack.Push(n)
}

// Normalize sorts rs[base..] by lo and merges ranges that overlap or
// touch.
Normalize(rs: mut List[Rr], base: int) {
    SortRanges(rs, base)
    if rs.Len() - base < 2 {
        return
    }
    var w := base
    for i in base + 1..rs.Len() {
        r := rs[i]
        if r.lo <= rs[w].hi || rs[w].hi != MaxRune && r.lo == rs[w].hi + 1 {
            if r.hi > rs[w].hi {
                rs[w].hi = r.hi
            }
        } else {
            w += 1
            rs[w] = r
        }
    }
    for rs.Len() > w + 1 {
        _ = rs.Pop()
    }
}

// Negate replaces the sorted, merged rs[base..] by its complement in
// 0 .. MaxRune.
Negate(rs: mut List[Rr], base: int): OutOfMemory!void {
    n := rs.Len() - base
    var next: u32 = 0
    for i in 0..n {
        r := rs[base + i]
        if r.lo > next {
            try rs.Push(Rr(lo: next, hi: r.lo - 1))
        }
        if r.hi == MaxRune {
            next = MaxRune
            break
        }
        next = r.hi + 1
    }
    top := n == 0 || rs[base + n - 1].hi != MaxRune
    if top {
        try rs.Push(Rr(lo: next, hi: MaxRune))
    }
    // Move the complement down over the original ranges.
    m := rs.Len() - base - n
    for i in 0..m {
        rs[base + i] = rs[base + n + i]
    }
    for rs.Len() > base + m {
        _ = rs.Pop()
    }
}

// SortRanges sorts rs[base..] by lo: insertion sort up to 16 ranges, else
// heapsort (Williams 1964), O(k log k) in place with no allocation, so a
// class of many items (a \p{..} table, a folded range, a long pattern)
// cannot make Compile quadratic. It also runs in the evaluator for gen
// regex.Matcher, which pays fuel for every step, and which cannot yet run
// Slice.Swap on a List, so the swaps here are written out.
SortRanges(rs: mut List[Rr], base: int) {
    n := rs.Len() - base
    if n <= 16 {
        for i in base + 1..rs.Len() {
            x := rs[i]
            var j := i
            for j > base && rs[j - 1].lo > x.lo {
                rs[j] = rs[j - 1]
                j -= 1
            }
            rs[j] = x
        }
        return
    }
    var i := n / 2
    for i > 0 {
        i -= 1
        Sift(rs, base, i, n)
    }
    var end := n
    for end > 1 {
        end -= 1
        t := rs[base]
        rs[base] = rs[base + end]
        rs[base + end] = t
        Sift(rs, base, 0, end)
    }
}

// Sift moves rs[base + i0] down the max-heap rs[base..base + n] by lo.
Sift(rs: mut List[Rr], base: int, i0: int, n: int) {
    var i := i0
    for {
        var c := 2 * i + 1
        if c >= n {
            return
        }
        if c + 1 < n && rs[base + c + 1].lo > rs[base + c].lo {
            c += 1
        }
        if rs[base + c].lo <= rs[base + i].lo {
            return
        }
        t := rs[base + i]
        rs[base + i] = rs[base + c]
        rs[base + c] = t
        i = c
    }
}
