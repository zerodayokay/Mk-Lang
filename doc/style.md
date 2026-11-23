# Code style

This page says how Mk code is written, the standard library included. Read it once you can write Mk; the [reference](reference.md) and the [cookbook](cookbook.md) show the forms, and this page says which form to choose.

There are two kinds of rule. A **mechanical** rule is one the tools apply for you. A **judged** rule is one no tool can decide, so the author and whoever reads the change hold it.

```sh
mk fix --canonical   # rewrite to the canonical forms
mk fmt               # lay out every file
mk fmt --check       # list unformatted files, change nothing
```

`mk fix --canonical` runs one round per mechanical rule. A round rewrites only what it can prove means the same, and recompiles the result before keeping it. What it cannot prove, it leaves for you.

## 1. Mechanical rules

| Rule | What it asks for | `mk fix` round |
|---|---|---|
| Value arm | A value chosen by a branch is a value arm, `x := if c { a } else { b }`, not a local declared and then assigned in each arm | `value-arm` |
| For over an iterator | A loop over an iterator is `for x in e`, not `var it := e` and `for x := it.Next()`, when `it` is used nowhere else. A record payload is destructured in the loop head | `for-in` |
| Default with else | A default is `e else { v }`. There is no `Or` method | the fix of E0207 |
| No empty record | A `Print` or `Fmt` with no hole has no record: `sys.Print("done\n")`, not `sys.Print("done\n", ())` | `short form` |
| Release linear elements | `Free` on a container of linear elements that are not `Free` is `FreeEach` or `FreeEmpty`. A `Set` that would drop a linear value is `Replace` | `release` |
| Errors print themselves | An error that is matched only to print it is printed with a `{e}` hole | `error-format` |
| Layout | Four spaces, one statement a line, braces on the line they open | `mk fmt` |

The value-arm round applies when every arm assigns only that local and nothing else assigns it. The release round covers `Free` and `Set`. Other calls that would drop a linear value are errors, and the error names the call to use instead.

```mk
// verdict: ok
// value arm, for over an iterator, default with else
enum Shape {
    Circle(r: f64)
    Rect(w: f64, h: f64)
    Empty
}

Name(s: Shape): str {
    kind := match s { // the match is the value
        .Circle { "circle" }
        .Rect(w, h) {
            if w == h { "square" } else { "rect" }
        }
        .Empty { return "nothing" }
    }
    return kind
}

Total(m: &Map[str, int]): int {
    var sum := 0
    for (key, value) in m.Iter() { // the pair is destructured in the head
        if key.StartsWith("#") {
            continue
        }
        sum += *value
    }
    return sum
}

Port(text: str): i64 {
    return text.ParseInt() else { 8080 } // 8080 when text is not a number
}
```

```mk
// verdict: ok
// no empty record, and an error prints itself
import std.fs

export Main(sys: &System): WriteError!void {
    text := fs.ReadText(sys.arena, sys.io, "config.txt") else e {
        try sys.PrintErr("config.txt: {e}\n", (e))
        return
    }
    _ = text
    try sys.Print("loaded\n")
}
```

```mk
// verdict: ok
// a container of files is closed element by element
import std.fs

CloseAll(a: &Alloc, files: HeapList[fs.File]) {
    files.FreeEach(a, fn(f: fs.File) { f.Abandon() })
}
```

## 2. Judged rules

| Rule | What it asks for |
|---|---|
| Names | Parameters go in the order receiver, arena or allocator, `io`, then the rest. Use the library's [naming words](std/README.md#2-naming): `New`, `Free`, `Get`, `Push`, `Is`, `To`, `As` and the rest. Name a file for what it holds, never for when or by whom it was written |
| Comments | A comment states an invariant, a reference (the algorithm and its source, an RFC section, a Unicode annex) or a reason the code cannot show. History belongs in version control |
| Documentation | Every exported item has a `///` line saying what it does, then what its errors mean, its worst-case cost and what it allocates, in that order, briefly |
| Errors | Error sets are precise: no catch-all where a specific error is known. Report an error with `{e}`. Use `Must()` only where failure is a bug in this module, with a comment saying why it cannot fail, and never on a path that input reaches |
| Ownership | A heap value that owns only allocator memory implements `Free`. One that owns an OS handle or a peer's state does not, and a container of them is released with `FreeEach`. No remove-then-free: `Set`, `Replace` and `GetMut` say what they mean. Use `Swap`, `Replace` and `GetPair`, not a copy made to get around a borrow |
| Callbacks | Pass a capturing literal where the callback runs during the call. Where a callback is kept, follow [Keep a callback for later](cookbook.md#keep-a-callback-for-later). Do not write a context struct that exists only to carry captures |
| Control flow | Use value arms where a value is chosen. Use early `return` and `else { return }` for guard clauses, and no `else` after a block that does not fall through. A `Print` hole names a field path, `{r.name}`, rather than a local bound only to be printed |
| Unsafe | Every `unsafe` block's reason states the invariant it relies on. Add an `unsafe` block only when safe code cannot do the job, and say why |
| Tests | `tests_*.mk` files follow this style too. A test's name says the behaviour it checks |
| File size | A file holds one concept. When it grows to hold two, split it |

```mk
// verdict: ok
// names, documentation, errors and control flow
error Empty
error TooLong(len: int)

type NameError = Empty | TooLong

/// Copies `name` into `a` with its spaces trimmed. Fails with `Empty` when
/// nothing is left and `TooLong` past 64 bytes. O(n); allocates n bytes in `a`.
CleanName(a: &Arena, name: str): NameError|OutOfMemory!str from a {
    s := name.Trim()
    if s.Len() == 0 { // guard clauses return early
        fail Empty
    }
    if s.Len() > 64 {
        fail TooLong(len: s.Len())
    }
    return try a.Dup(s)
}
```

```mk
// verdict: ok
// a comment that states an invariant, Swap instead of a copy, and an impl Free
struct Buffers {
    // front is what readers see; back is never shared, so it may be cleared
    front: HeapText
    back: HeapText
}

Buffers.Flip(mut self) {
    Swap(mut self.front, mut self.back)
    self.back.Clear()
}

impl Free for Buffers {
    Free(self, a: &Alloc) {
        Buffers(front, back) := self
        front.Free(a)
        back.Free(a)
    }
}
```

```mk
// verdict: ok
// the unsafe reason names the invariant
FirstByte(p: *u8, len: int): ?u8 {
    if len == 0 {
        return none
    }
    var b: u8 = 0
    unsafe "p points to len readable bytes, and len is above 0" {
        b = *p
    }
    return some(b)
}
```

```mk
// verdict: ok
// a test is named for the behaviour it checks
Clamp(x: int, lo: int, hi: int): int {
    return if x < lo { lo } else if x > hi { hi } else { x }
}

test "Clamp keeps a value inside the range unchanged" {
    Assert(Clamp(5, 0, 10) == 5)
}

test "Clamp moves a value below the range up to lo" {
    Assert(Clamp(-3, 0, 10) == 0)
}
```

## 3. Bringing a file to the style

1. Run `mk fix --canonical` and `mk fmt`, and read the diff.
2. Read the file against the judged rules, and rewrite what falls short. Where `Swap`, `Replace`, `GetPair`, `SplitAt` or `Sweep` replace a workaround, use them.
3. Run the module's tests with `mk test`.
4. If you changed an algorithm, a data layout or a hot loop, run its benchmark with `mk bench` before and after, and compare.
