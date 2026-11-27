---
description: The Mk reference - the language rule by rule, the memory model, every error code, the grammar, and the standard library.
---

# Reference

The reference states every rule of the language and the library. To learn Mk in order, read the [guide](/guide/) first and come here to look something up.

## The language

[The language reference](./language/) covers each construct with an example, its output, the rules, and the errors the compiler reports.

| Section | What it covers |
|---|---|
| [Start here](./language/) | The program, printing, and how to read the examples |
| [2. Lexical rules](./language/lexical-rules) | Source text, comments, names, keywords, literals, operators |
| [3. Types and kinds](./language/types-and-kinds) | Numbers, `str`, slices, arrays, optionals, results, records, conversions; which values copy and which move |
| [4. Declarations](./language/declarations) | Structs, enums, constants, globals, functions and methods, layout |
| [5. Statements](./language/statements) | Bindings, assignment, `if`, `for`, `match`, `defer`, `return`, tail calls |
| [6. Patterns](./language/patterns) | What `match`, `if x := e` and destructuring can bind |
| [7. Expressions](./language/expressions) | Operators and precedence, indexing, literals, calls, function values |
| [8. Ownership and borrowing](./language/ownership-and-borrowing) | Moves, borrows, `from` and `into`, linear values |
| [9. Errors](./language/errors) | Error sets, `E!T`, `fail`, `try`, `else`, `onerr`, panics |
| [10. Generics and interfaces](./language/generics-and-interfaces) | Type parameters, bounds, `interface`, `impl`, specialization, `const` parameters |
| [11. Compile time](./language/compile-time) | `const` evaluation, `fields`, writing generators |
| [12. Concurrency](./language/concurrency) | `Io`, `scope`, tasks, channels, cancellation |
| [13. Modules and build](./language/modules-and-build) | Modules, `import`, `export`, targets |
| [14. Unsafe and C](./language/unsafe-and-c) | `unsafe`, raw pointers, `extern`, C imports, `asm` |
| [15. Contracts and tests](./language/contracts-and-tests) | `requires`, `ensures`, `invariant`, `test` blocks |
| [16. Habits from other languages](./language/habits-from-other-languages) | C, Go, Rust and Zig forms, and what to write instead |
| [17. Module map](./language/module-map) | The standard library's modules |
| [18. Library surface](./language/library-surface) | Every exported signature of the standard library |
| [19. Test prelude](./language/test-prelude) | Names the reference's examples use without declaring them |

## Rules and diagnostics

| Page | Use it to |
|---|---|
| [Memory model](./memory) | Look up an ownership or borrowing rule. Each comes with an accepted program, a rejected one and the fix |
| [Error codes](./errors) | Find what an error code means and how to fix it. `mk explain E0503` prints the same |
| [Grammar](./grammar) | Read the grammar in EBNF-style notation, for tool writers |

## The standard library

[The library guide](./std/) explains the conventions every module shares: allocators and arenas, `Io`, errors, cancellation and stability levels. Each module has its own page, from [`std.core`](./std/core), the prelude every program sees, to [`std.http`](./std/http) and [`std.json`](./std/json). [The web kit](./kits/web) adds middleware, typed parameters and validation on top of `std.http`.

## Finding a name

- `mk query members List` lists a type's methods with their signatures.
- `mk query find` searches the library by a word you know from another language.
- `mk doc --std std.fs` prints one module's API.
- The site search (<kbd>/</kbd> or <kbd>Ctrl</kbd> <kbd>K</kbd>) covers every page here.

[The mk command](/tools/) documents these and every other command.

::: tip Generated from the repository
The reference pages are built from the Markdown in the repository's [`doc/`](https://github.com/zerodayokay/Mk-Lang/tree/main/doc) directory, whose examples the compiler's test suite checks. "Edit this page" on each of them opens its source file.
:::
