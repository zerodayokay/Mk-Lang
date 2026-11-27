---
description: str is a read-only view of UTF-8 bytes. String literals, byte offsets and scalars, building text with Fmt and Text, and format strings with checked holes and specs.
---

# Strings and formatting

A `str` is a read-only view of valid UTF-8 bytes that it does not own: a literal, a slice of a file you read, or text built in an arena. Its length and its slice offsets are in bytes.

## Literals

| Literal | Type | Notes |
|---|---|---|
| `'a'`, `'\u{1F600}'` | `char` | One scalar |
| `b'a'` | `u8` | One byte |
| `"a\tb\n"` | `str` | With escapes |
| `r"C:\temp"` | `str` | Raw: no escapes |
| `b"GET /"` | `[]u8` | Bytes |
| `"""` ... `"""` | `str` | Multi-line |

A multi-line string drops the newline after the opening quotes, and removes the closing line's indentation from every line. [String literals](/reference/language/lexical-rules#string-literals) lists the escapes.

## Working with `str`

```mk
// verdict: ok
const Usage = """
    usage: tool [options] file
      -v    verbose
    """

export Main(sys: &System): OutOfMemory|WriteError!void {
    s := "naïve café"
    try sys.Print("{n} bytes\n", (n: s.Len())) // prints 12 bytes: ï and é take two each
    var scalars := 0
    for c in s.Scalars() {
        if c != ' ' {
            scalars += 1
        }
    }
    try sys.Print("{scalars} letters\n", (scalars)) // prints 9 letters
    first := s[0..6]                                // byte offsets: "naïve"
    if at := s.Find(" ") {
        try sys.Print("[{first}] space at {at}\n", (first, at)) // prints [naïve] space at 6
    }
    loud := try s.ToUpper(sys.arena)
    try sys.Print("{loud}\n", (loud)) // prints NAÏVE CAFÉ
    try sys.Print(Usage)
}
```

```text
12 bytes
9 letters
[naïve] space at 6
NAÏVE CAFÉ
usage: tool [options] file
  -v    verbose
```

- `Len()` counts bytes. `Scalars()` iterates `char`s, and `Lines()` and `Words()` iterate views of the text.
- `Find` returns an optional byte offset, so it goes in an `if x := ...` ([optionals](./errors#optionals)).
- A slice of a `str` must fall on a scalar boundary, or it panics.
- Strings compare with `==` and `<`, byte by byte.
- A method that makes new text, such as `ToUpper`, takes the arena to put it in and can fail with `OutOfMemory`.

Unicode-aware operations such as graphemes, normalization, case folding and display width are in [`std.text`](/reference/std/text). The full list of `str` methods is in [strings and text](/reference/std/core#strings-and-text).

## Building text

`str` has no `+`. To build text, format it into an arena with `Fmt`, or append to a `Text`:

```mk
// verdict: ok
Label(a: &Arena, name: str, n: int): OutOfMemory!str from a {
    return try a.Fmt("{name} #{n}", (name, n))
}

Csv(a: &Arena, cells: []str): OutOfMemory!str from a {
    var t := Text.New(a)
    for i, f in cells {
        if i > 0 {
            try t.PushChar(',')
        }
        try t.Push(f)
    }
    return t.Done()
}
```

`from a` says the result lives in the arena `a`; [memory and ownership](./memory#where-results-come-from) explains it.

## Formatting

`Print`, `PrintErr` and `Fmt` take a format string and a record whose field names match its `{holes}`:

- `(x)` is short for `(x: x)`, and a format with no holes takes no record.
- The compiler checks the holes against the record: every hole needs a field and every field a hole (E0706).
- A hole may name a field path: `{it.name}`.
- Write `{{` and `}}` for literal braces.

After a colon, a hole may carry a spec: fill and alignment, width, sign, zero padding, precision and radix.

```mk
// verdict: ok
struct Item {
    export name: str
    export qty: int
    export price: f64
}

export Main(sys: &System): WriteError!void {
    items := [Item(name: "bolt", qty: 12, price: 0.25), Item(name: "washer", qty: 300, price: 0.05)]
    try sys.Print("{a:<8}{b:>5}{c:>8}\n", (a: "name", b: "qty", c: "price"))
    for it in items[..] {
        try sys.Print("{it.name:<8}{it.qty:>5}{it.price:>8.2}\n", (it)) // a hole may name a field path
    }
    flags := 42
    try sys.Print("{flags:08b} {flags:x} {flags:#>6}\n", (flags)) // prints 00101010 2a ####42
}
```

```text
name      qty   price
bolt       12    0.25
washer    300    0.05
00101010 2a ####42
```

| Spec | Meaning |
|---|---|
| `<8`, `>5`, `^7` | Align left, right or centre in a width of bytes |
| `#>6` | Fill with `#` instead of spaces |
| `+` | Give a non-negative number a sign |
| `08` | Pad a number with zeros to a width |
| `.2` | Two digits after the point |
| `b`, `o`, `x`, `X` | Binary, octal, hexadecimal |

A spec is `[[fill]align][+][0][width][.N][type]`; [specs](/reference/std/core#specs) has the rules for each part.

A value prints through the `Format` interface. Numbers, `bool`, `char`, `str`, errors and the library's types implement it, and you can implement it for your own types ([methods and interfaces](./methods-and-interfaces#interfaces)).

## Next

[Arrays, lists and maps](./collections).
