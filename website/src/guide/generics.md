---
description: Generic functions and types with type parameters in brackets, bounds that say what a body may do, inference, built-in bounds, const parameters and specialization.
---

# Generics

Functions and types take type parameters in brackets. A parameter's bounds say what the body may do with it. The body is checked once, against those bounds, not again at each use.

```mk
// verdict: ok
Max[T: Ord + Plain](xs: []T): ?T {
    if xs.Len() == 0 {
        return none
    }
    var best := xs[0]
    for x in xs {
        if best.Less(&x) { // Ord's one method
            best = x
        }
    }
    return some(best)
}

struct Stack[T] {
    items: List[T]
}

Stack[T].New(a: &Arena): Stack[T] {
    return Stack[T](items: List[T].New(a))
}

Stack[T].Push(mut self, x: T into self): OutOfMemory!void {
    try self.items.Push(x)
}

Stack[T].Pop(mut self): ?T {
    return self.items.Pop()
}

export Main(sys: &System): OutOfMemory|WriteError!void {
    var s := Stack[int].New(sys.arena)
    try s.Push(1)
    try s.Push(2)
    nums := [4, 9, 2]
    top := s.Pop().Must() + Max(nums[..]).Must()
    try sys.Print("{top}\n", (top))
}
```

```text
11
```

- Type arguments are inferred from the arguments, then from the expected type: `Max(nums[..])` needs no `[int]`. Otherwise you write them, as in `List[int].New(a)`.
- Type arguments go in brackets: `Stack[int]`, never `Stack<int>`.
- Methods of a generic type repeat its parameters: `Stack[T].Push`.

## Bounds

A body that uses an operation its bounds do not give is an error at the definition, not at some distant call:

```mk
// verdict: reject E0703
Biggest[T](a: &T, b: &T): bool {
    return *a > *b // <- E0703
}
```

Bound `T` by `Ord` and call `a.Less(b)` instead. A bound is an interface, several joined with `+`, or one of the built-in bounds:

| Bound | Means |
|---|---|
| An interface, such as `Ord` | `T` implements it, so the body may call its methods |
| `Plain` | `T` is copied on use. Without it, `T` may need releasing, and the body must consume each `T` it is given |
| `Send` | A `T` may move to another task |
| `Share` | A `&T` may be shared between tasks |
| `Fields[B]` | The body may visit `T`'s fields, each bounded by `B` ([compile-time code](./compile-time)) |

That is why `Max` above says `T: Ord + Plain`: it copies elements into `best`, which only a plain type allows.

## Const parameters

A type parameter can also be a compile-time value, such as a size:

```mk
// verdict: ok
struct Matrix[const R: int, const C: int] {
    cells: [R][C]f64
}

Matrix[R, C].Rows(&self): int {
    return R
}

export Main(sys: &System): WriteError!void {
    m := Matrix[2, 3](cells: [[0.0; 3]; 2])
    try sys.Print("{r}\n", (r: m.Rows()))
}
```

```text
2
```

[Const parameters](/reference/language/generics-and-interfaces#const-parameters) has the rules, and [specialization](/reference/language/generics-and-interfaces#specialization-if-t-is) shows `if T is u8 { }`, which picks a faster body for one type at compile time.

## No hidden cost

Each instance of a generic function is compiled for its type arguments, so a call through a generic is as fast as a call to a hand-written copy. There is no boxing unless you ask for it with [`dyn.Of`](./methods-and-interfaces#values-of-different-types-dynof).

## Next

[Optionals and errors](./errors).
