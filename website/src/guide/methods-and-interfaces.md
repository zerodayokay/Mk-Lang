---
description: Methods as functions in a type's namespace, receivers by value, shared or exclusive, interfaces and impl, the library's interfaces, and dyn.Of for values of different types behind one interface.
---

# Methods and interfaces

## Methods

A function named `Type.Name` lives in that type's namespace. When its first parameter is `self`, you call it with a dot. The form of `self` says how the method takes its receiver:

| Receiver | Meaning |
|---|---|
| `self` | By value: a copy, or the value moved in |
| `&self` | A shared reference: the method reads |
| `mut self` | An exclusive reference: the method may write |

```mk
// verdict: ok
struct Counter {
    n: int
}

Counter.New(): Counter { // no self: called as Counter.New()
    return Counter(n: 0)
}

Counter.Add(mut self, k: int) {
    self.n += k
}

Counter.Get(&self): int {
    return self.n
}

export Main(sys: &System): WriteError!void {
    var c := Counter.New()
    c.Add(2) // borrows c for writing, so c must be var
    c.Add(3)
    try sys.Print("{n}\n", (n: c.Get())) // prints 5
}
```

```text
5
```

- There is no `impl` block for methods and no inheritance. You write methods next to the type, in its module.
- `c.Add(2)` is the same call as `Counter.Add(mut c, 2)`.
- A method that takes `self` by value consumes the receiver: that is how a linear value is released ([memory](./memory#plain-unique-and-linear-values)).

## Interfaces

An `interface` lists methods. A type implements it with an `impl`:

```mk
// verdict: ok
interface Shape {
    Area(&self): f64
}

struct Square {
    side: f64
}

impl Shape for Square {
    Area(&self): f64 {
        return self.side * self.side
    }
}

impl Format for Square { // how a Square prints in a {hole}
    Format(&self, w: mut Writer): WriteError!void {
        try Print(w, "square({s})", (s: self.side))
    }
}

Larger[T: Shape](a: &T, b: &T): bool {
    return a.Area() > b.Area()
}

export Main(sys: &System): WriteError!void {
    a := Square(side: 2.0)
    b := Square(side: 3.0)
    try sys.Print("{a} larger than {b}: {x}\n", (a, b, x: Larger(&a, &b))) // prints square(2) larger than square(3): false
}
```

```text
square(2) larger than square(3): false
```

- An interface is a bound on a generic parameter, as in `Larger[T: Shape]`; it is not the type of a value ([generics](./generics)).
- A type has at most one `impl` of each interface, written in the module of the type or of the interface.
- Implementing `Format` makes a type printable in a format hole.

### The library's interfaces

| Interface | Method | Used by |
|---|---|---|
| `Format` | `Format(&self, w: mut Writer): WriteError!void` | `{x}` holes in `Print` and `Fmt` |
| `Eq` | `Eq(&self, other: &Self): bool` | `Has`, `Index`, `Map` and `Set` keys |
| `Ord` | `Less(&self, other: &Self): bool` | `math.Min`, `math.Max`, `BTree` keys, `PriorityQueue` |
| `Hash` | `Hash(&self, h: mut Hasher)` | `Map` and `Set` keys |
| `Free` | `Free(self, a: &Alloc)` | Releasing a value that owns heap memory |
| `Slice[T]` | `Items(&self): []T`, `ItemsMut(mut self): mut []T` | Every slice function: implement it and `Sort`, `Len` and the rest work on your type |

`gen derive.Eq(T)`, `gen derive.Hash(T)` and `gen derive.Format(T)` write the obvious implementation for a struct, field by field ([generators](./compile-time#generators)). [Interfaces in the library](/reference/language/generics-and-interfaces#interfaces-in-the-library) lists them all.

## Values of different types: `dyn.Of`

Because an interface is not a type, a `List[Shape]` does not exist. To keep values of different types behind one interface, generate a boxed type with `gen dyn.Of(Shape)`. It writes a `ShapeDyn` struct that holds the object and a table of its methods:

```mk
// verdict: ok
import std.dyn

interface Shape {
    Area(&self): f64
}

gen dyn.Of(Shape)

struct Square {
    side: f64
}

struct Circle {
    r: f64
}

impl Shape for Square {
    Area(&self): f64 {
        return self.side * self.side
    }
}

impl Shape for Circle {
    Area(&self): f64 {
        return 3.0 * self.r * self.r
    }
}

export Main(sys: &System): OutOfMemory|WriteError!void {
    a := sys.arena
    var shapes := List[ShapeDyn].New(a)
    try shapes.Push(ShapeDyn.Of(try a.Put(Square(side: 2.0))))
    try shapes.Push(ShapeDyn.Of(try a.Put(Circle(r: 1.0))))
    var total := 0.0
    for s in &shapes {
        total += s.Area()
    }
    try sys.Print("{total}\n", (total)) // prints 7
}
```

```text
7
```

The dispatch is explicit and visible: you pay for it only where you write `dyn.Of`. An enum is often the simpler choice when the set of types is closed ([enums](./structs-and-enums#enums)). [`std.dyn`](/reference/std/dyn) has the details.

## Next

[Generics](./generics).
