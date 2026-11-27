---
description: How a Mk source file is laid out - declarations, statements, line breaks instead of semicolons, the naming rule the compiler checks, keywords and comments.
---

# Syntax and structure

A Mk source file is UTF-8 text holding declarations: functions, types, constants and imports. A program is a module that exports a function `Main`.

## The shape of a file

```mk
// verdict: ok
const Limit = 3 // a constant, computed by the compiler

struct Pixel { // a type
    x: int
    y: int
}

Pixel.Sum(&self): int { // a method: a function in Pixel's namespace
    return self.x + self.y
}

Double(n: int): int { // a function: name, parameters, result type, body
    return n * 2
}

export Main(sys: &System): WriteError!void { // the program's entry point
    p := Pixel(x: 1, y: Limit)
    try sys.Print("{s}\n", (s: Double(p.Sum()))) // prints 8
}
```

- `import` lines come first, one module each: `import std.fs`. An unused import is an error (E0208).
- Declarations at module level may come in any order. A constant may call a function declared below it.
- Everything is private to its module unless it is marked `export`: functions, types, constants and struct fields alike.
- A function's result type follows a colon. A function with no result type returns nothing.
- There is no top-level code: statements live in function bodies.

## Statements and line breaks

Mk has no semicolons. A newline ends a statement when the last token on the line can end one: a name, a literal, `)`, `]`, `}`, `return`, `break` or `continue`. Elsewhere a newline is ignored, so a statement continues on the next line.

```mk
// verdict: ok
Add(
    a: int,
    b: int, // a trailing comma is allowed in a comma list
): int {
    return a + b
}

export Main(sys: &System): WriteError!void {
    xs := [
        1,
        2,
    ]
    total := Add(xs[0], xs[1])
    small := total > 2 && // an operator at the end: the line goes on
        total < 10
    if small { try sys.Print("small\n") } // `}` ends the statement inside
    try sys.Print("{total} {small}\n", (total, small))
}
```

```text
small
3 true
```

The rules that follow from this:

- Newlines inside `( )` and `[ ]` are ignored.
- An operator ends a line, never starts one. Break a long expression *after* the operator.
- A method chain breaks after the dot: `s.` then `Len()` on the next line.
- `else` goes on the line of the closing brace: `} else {`.
- A function body's `{` goes at the end of the signature's line.
- `return` followed by a newline returns nothing. Write the value on the line of `return`.

Starting a line with an operator is an error, because the line before has already ended:

```mk
// verdict: reject E0101
Sum(a: int, b: int): int {
    x := a
        + b // <- E0101
    return x
}
```

`mk fmt` lays code out in the one standard format, so you rarely need to think about this. [Line breaks](/reference/language/lexical-rules#line-breaks) has every rule.

## Names

A name starts with an ASCII letter and continues with letters, digits and underscores. Its case says what it names, and the compiler checks it:

| Case | Used for | Examples |
|---|---|---|
| `PascalCase` | Functions, methods, types, interfaces, enum variants, errors, constants, globals, type parameters | `MaxLen`, `HTTPServer`, `Area`, `T` |
| `snake_case` | Locals, parameters, fields, loop labels | `user_count`, `x2`, `self` |

```mk
// verdict: reject E0207
Count(s: str): int {
    user_count := s.Len()
    return userCount // <- E0207
}
```

The error message names the spelling you meant (`user_count`), and `mk fix` corrects it. Two more rules:

- A name cannot start with `_`. The blank `_` alone discards a value: `_ = F()`.
- Two names in one scope cannot differ only in case and underscores, such as `max_len` and `maxlen` (E0206).

## Keywords

```text
import export struct enum error interface impl type const var linear view gen
quote test extern if else for in match return break continue defer onerr fail
try scope go unsafe asm become mut fn is requires ensures invariant
```

A keyword cannot be a name (E0102). A few more words are keywords only where the grammar expects them and ordinary names elsewhere: `from into ends fuel packed plain open self Self result`. The names `true false some none ok err` and the built-ins such as `Assert`, `Panic`, `Cast` and `SizeOf` are visible everywhere with no import. [Keywords](/reference/language/lexical-rules#keywords) lists where each contextual word applies.

## Comments

```mk
// verdict: ok
// A line comment runs to the end of the line.

/// A doc comment documents the item or field below it.
/// `mk doc` and the language server show it.
struct Celsius {
    /// Degrees, in hundredths.
    centi: i64
}
```

- `//` starts a comment that runs to the end of the line.
- `///`, exactly three slashes, starts a doc comment for the item or field that follows. Give every exported item one ([code style](./style)).
- There are no block comments: `/* ... */` is E0101.

## Characters outside the language

Names and keywords are ASCII; text in other scripts belongs in strings and comments. `@`, `#` and backquotes outside a literal are errors: Mk has no attributes, annotations or preprocessor. Tokens from other languages are recognised so the compiler can name the Mk form: `;` at the end of a statement is E1206, `->` is E1211 and `::` is E1213 ([habits from other languages](/reference/language/habits-from-other-languages)).

## Next

[Variables and scope](./variables): bindings, `var`, constants and globals.
