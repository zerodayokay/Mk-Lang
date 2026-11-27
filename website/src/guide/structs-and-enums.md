---
description: Structs with named fields and C layout, enums with payloads and tag types, records as structural types, destructuring, and exported fields.
---

# Structs, enums and records

## Structs

A `struct` groups named fields. You build one by calling its name with every field named; no field has a default.

```mk
// verdict: ok
struct Vec2 {
    x: f64
    y: f64
}

struct Particle {
    pos: Vec2
    vel: Vec2
    mass: f64
}

Step(p: Particle, dt: f64): Particle {
    next := Vec2(x: p.pos.x + p.vel.x * dt, y: p.pos.y + p.vel.y * dt)
    return Particle(pos: next, vel: p.vel, mass: p.mass)
}

Parts(v: Vec2): f64 {
    Vec2(x, y) := v // destructure a struct into locals named after its fields
    return x + y
}
```

- Leaving out a field is an error (E0305).
- Fields are private to the module unless marked `export`, even when the struct itself is exported: `export struct Vec2 { export x: f64 }`.
- A struct is laid out as in C: its fields in order, each at its natural alignment. `packed struct` removes the padding, and `align(64) struct` raises the alignment ([layout](/reference/language/declarations#layout)).
- A struct copies or moves according to its fields: one that holds a file is linear without saying so ([kinds](./memory#plain-unique-and-linear-values)).

```mk
// verdict: reject E0305
struct Size {
    w: int
    h: int
}

Square(n: int): Size {
    return Size(w: n) // <- E0305
}
```

## Enums

An `enum` is a choice between variants, each with its own fields or none. A value names the type, as in `Shape.Empty`; a pattern may leave it out, as in `.Empty`.

```mk
// verdict: ok
enum Shape {
    Circle(r: f64)
    Rect(w: f64, h: f64)
    Empty
}

enum Level: u8 { // an explicit tag type and values, for C and file formats
    Debug = 1
    Info = 2
    Error = 3
}

Area(s: Shape): f64 {
    return match s {
        .Circle(r) { 3.14159 * r * r }
        .Rect(w, h) { w * h }
        .Empty { 0.0 }
    }
}

export Main(sys: &System): WriteError!void {
    a := Area(Shape.Rect(w: 2.0, h: 3.0))
    b := Area(Shape.Circle(r: 1.0))
    tag := u8(Level.Info) // an enum with a tag type converts to it
    try sys.Print("{a} {b} {tag}\n", (a, b, tag))
}
```

```text
6 3.14159 2
```

- A `match` on an enum must name every variant or end with `_`.
- A variant's fields bind by name: `.Rect(w: width, h: height)` renames them, and `.Rect` alone ignores them.
- An enum without payloads compares with `==`. One with payloads is tested with `match`.
- `open enum` lets a library add variants in a later release, so a `match` on one from another package needs a `_` arm ([open sets](/guide/codebase#open-sets)).

## Records

A record is a struct with no name: `(name: str, age: int)`. Two records with the same fields are the same type. Records carry several results from a function, and hold the arguments of `Print`.

```mk
// verdict: ok
type Span = (lo: int, hi: int)

Widen(s: Span, by: int): Span {
    return (lo: s.lo - by, hi: s.hi + by)
}

Width(s: Span): int {
    (lo, hi) := s
    return hi - lo
}
```

A record field written alone, `(lo)`, is short for `(lo: lo)`. A record destructures by field name, never by position.

## Choosing between them

| Use | When |
|---|---|
| `struct` | A named type with its own methods and invariants |
| `enum` | A value that is one of several shapes |
| record | A small, unnamed group of values, such as several results |
| `error` | A failure a function can report ([errors](./errors#declaring-errors)) |

Nothing has a constructor function by default. By convention a type that needs setup gets a `Type.New(...)` function ([methods](./methods-and-interfaces#methods)).

## Next

[Methods and interfaces](./methods-and-interfaces).
