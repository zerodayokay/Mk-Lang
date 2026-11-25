# Mk error codes

Every error the compiler reports has a code, such as `E0501`, and a short name, such as `exclusive-conflict`. This page shows how to read an error, then lists every code with what triggers it and the kind of fix the compiler offers. `mk explain <code>` prints the same entry in the terminal.

## Reading a diagnostic

`mk check`, `mk build`, `mk run` and `mk test` print each error as a location, the code and name, a message, the source line, and then the facts behind the error:

```text
src/main.mk:4:9: E0207 naming: `Total` is written `total`
    |         Total += x
    |         ^^^^^
    declared: total
    declared_at: src/main.mk:2:9
    fix (proven): names are exact; this is the declared spelling
    explain: mk explain E0207
1 error
```

- The location is `file:line:column`, with the column counted in bytes from 1.
- The fact lines (`declared`, `declared_at`) are what the compiler knew when it stopped: a type and its members, the loan that conflicts and the use that keeps it live, a call path.
- A `fix` line names a fix and its class. A `try` line names an alternative for you to choose.
- The compiler reports every error of the earliest phase that fails, and nothing from later phases except the errors those follow from. Fix those, and the next phase runs, so new errors can appear after a fix. A `gen` line whose generator fails emits nothing, so a name its output would have declared is unknown (E0201, names, or E0905 in another gen line's output). The error of the module's first failed line, E0903 (types) for a `g.Fail`, is reported with it as its cause, and the E0201 cites that line. A second failed line's error shows once the first is fixed. A line that emits nothing because a module it depends on failed to check (the generator's module or one it imports, or the module of a type argument or of the fuel const) is reported the same way: of that module's errors, the one the report would list first is the cause of the E0201s, which cite the line and name the module. A borrow error rejects only its own function, so it is the cause only when no module the line depends on has another error. When several modules failed, one is named, and the next shows once its error is fixed.

With `--json`, the same report is one JSON document, which [the tools guide](tools.md#3-diagnostics) describes. `mk fix` applies the proven fixes for you ([fix classes](#fix-classes)).

## `mk explain`

`mk explain` prints one code's entry from the table below, with a rejected and an accepted example where one exists. The code is not case-sensitive.

```text
$ mk explain E0207
E0207 naming (names)
trigger: A name written in a different case from its declaration. ...
fix: Proven: recase, ...
rejected (...):
    ...
```

`mk explain --json E0207` prints the same as one JSON object with the keys `code`, `name`, `phase`, `trigger`, `fix_class`, `reject` and `accept`.

## Fix classes

| Class | Meaning | Applied by `mk fix` |
|---|---|---|
| Proven | The edit keeps the program's behaviour, and is checked by recompiling | Yes |
| Compiles | The edit is checked to compile, but changes what the program does | No, it is offered |
| Alternatives | Named options for you to choose from. Nothing is checked | No |
| None | No fix exists | No |

Each fix cell below starts with the class. Some codes have a proven fix only in some cases, and the cell says which.

## The codes

The first two digits group the codes by topic:

| Prefix | Topic | Prefix | Topic |
|---|---|---|---|
| `E00` | The compiler and toolchain | `E07` | Generics and interfaces |
| `E01` | Syntax | `E08` | Errors |
| `E02` | Names, modules and packages | `E09` | Compile-time evaluation and generators |
| `E03` | Types | `E10` | Tasks |
| `E04` | Moves and assignment | `E11` | Unsafe code and the machine |
| `E05` | Borrows | `E12` | Habits from other languages |
| `E06` | Result flows | | |

For the borrow and flow codes, [the memory model](memory.md) explains the rules behind them.

| Code | Name | Trigger | Fix class |
|---|---|---|---|
| E0000 | internal-error | The compiler itself failed. This is a compiler bug; the report says where, and what went wrong | None |
| E0001 | toolchain | `clang` or `lld` is missing or failed, a symbol is unresolved at link time, or an output file cannot be written. The report gives the command and its error output | Alternatives |
| E0101 | syntax | A token that is not allowed here. Common cases: a suffix or operator after the closing brace of an `if` or `match` that starts a statement (wrap it in parentheses), and `for i, x in` over an iterator, which has no index form | Alternatives |
| E0102 | reserved-word | A keyword, a primitive type name, or a universe name other than `ok` and `err`, used as the name of an item, local, parameter or field | Proven: rename, for a keyword-named local or parameter; alternatives otherwise |
| E0201 | unknown-name | A name with no declaration, import or prelude entry | Proven if one export matches: add import |
| E0202 | unknown-module | An import path that names no module. The same code reports these module and package faults, most with a `reason` fact. A standard-library internal module (`std.core`, `std.raw`, `std.rt`, `std.sys`, `std.target`, `std.task`, `std.simd.kernel`) imported from outside the standard library; `std.raw` is allowed from a module listed in `unsafe_modules`. A package directory whose import path would start with `std`, `c` or `pkg`. `no-cimport`: `import c.<name>` with no `pkg.CImport` of that name. `needs-cimport`: a standard-library module's C import that the package does not provide. `target-selected`: `std.linux` imported on a target other than `x86_64-linux`. `freestanding`: on `x86_64-freestanding`, a standard-library module other than those of layers 0 and 1, `std.free`, `derive`, `dyn` and `std.raw`. A `pkg.Bin` whose `cpu` is not `""`, `"general-regs-only"` or `"baseline"`, or that sets a `link_script` or `"general-regs-only"` on a hosted target. `link`: a `pkg.Link` whose `kind`, `target`, `name` or `path` is not allowed. `no-std-target`: a target the standard library has no system layer for, as with a std given through `MK_STD` whose `pkg.mk` selects none for its OS; the diagnostic names the targets mk builds for and those it only checks. `not-built`: `mk build`, `run` or `test` for a target that is only checked, such as `aarch64-nutshell`, or a library for a target other than `x86_64-windows`, `x86_64-linux` and `aarch64-linux`. The dependency faults `invalid`, `conflict`, `cycle`, `fetch`, `offline`, `hash-mismatch`, `unsafe-archive`, `symbol-clash`, `id-clash`, `no-version`, `replace`, `sum-mismatch`, `sum-missing`, `tag-moved`, `protocol` and `nested-workspace`, which the packages guide describes | Alternatives |
| E0203 | unknown-member | A type has no field or method of that name, even ignoring case. This includes a field named in a per-field `from` clause that the result does not have, and any field named there when the result is not a struct or record. A name that differs only in case is E0207. The report lists the type's members | Alternatives |
| E0204 | not-exported | A private name used from another module | Alternatives |
| E0205 | import-cycle | The imports form a cycle. The report gives the cycle | Alternatives |
| E0206 | duplicate-name | One name declared twice in a scope, compared ignoring case; a local that shadows another local; one field named twice in a per-field `from` clause; or two module directories with the same import path | Proven: rename; two imports of one last segment: alternatives `import <name> = <path>` |
| E0207 | naming | A name written in a different case from its declaration. Also `.Or(x)` on a `?T` or `E!T`, which Mk writes `e else { x }` | Proven: recase, and `e else { x }` for a literal, name or field default; compiles otherwise |
| E0208 | unused | An unused local or import | Proven: remove |
| E0209 | unused-result | A result that is not `void` is dropped. A dropped `E!T` is E0802, and a dropped linear value is E0402 | Proven: `_ =` |
| E0213 | target-api-mismatch | `mk check --all-targets`: a C import or `pkg.Select` module differs between targets, an unselected target fails to check, a declared OS lacks a `pkg.Select` entry, or a target has no sysroot | Alternatives |
| E0214 | internal-module | An import of a module with an `internal` segment in its path, from another package. Workspace members count as separate packages. The report names the package and the nearest public module | Alternatives |
| E0215 | grant-missing | A fetched package declares `unsafe_modules`, `asm_sources`, `c_sources`, `c_imports` or `links`, and the root manifest's `unsafe_deps` does not name it. Reported at the `Dep` that brings it in, with what it asks for and the line to add | Alternatives: the grant line the message gives |
| E0216 | mk-too-old | A package in the build sets `mk` in its manifest to a version newer than the running mk. The report gives the version needed | Alternatives: `mk upgrade` |
| E0217 | test-dep-outside-test | A name from a `test_deps` package used outside a `test` block. Reported where the name is used | Alternatives |
| E0301 | type-mismatch | Two types that should agree differ. This includes a scalar beside a `simd.Vec` in an operator (use `Splat`), and a `simd.Mask` used as a condition or with `&&` or `\|\|` (use `.All()` or `.Any()`). A `mut T` where a `&T` is expected is one too: no conversion makes a `&T` of it, so write the shared reborrow `&*p`, or `&x` for a loan `mut x` | Proven: `&*p` for a `mut T` where a `&T` is expected, when it is the whole operand of `return` in a body with no `defer` or `onerr` and reads through a parameter or is a call or `try` result; compiles: the same elsewhere, `&x` for a loan `mut x`, and `xs[..]` for a `mut []T` where a `[]T` is expected |
| E0302 | no-conversion | No conversion exists between the two types | Alternatives |
| E0303 | lossy-conversion | `T(x)` could change the value, so the conversion needs `Cast`, or `Trunc` between integers. Also a literal that does not fit its type, and an operator whose operands are all literals whose value at its type overflows, divides by zero or shifts out of range, as `x: u8 = 200 + 100` | Compiles: `Cast`, or `Trunc` between integers |
| E0304 | argument-count | A call with the wrong number of arguments. The report gives the signature | Proven if each missing parameter has one in-scope candidate |
| E0305 | field-names | A construction that leaves out a field or names a wrong one. In `pkg.mk`, a `std.pkg` field left out is filled in empty, so there this applies only to a positional call or a misspelled field | Compiles: stubs (a `std.pkg` field a manifest leaves out is supplied empty) |
| E0306 | non-exhaustive | A `match` misses a case. A match on an `open` enum or error set of another package needs a `_` arm for the members the set may gain later (`reason: open`). A destructure whose pattern can fail, or names a variant, `some`, `ok` or `err`, is E0306 `reason: refutable` | Compiles: stubs; proven for `reason: open`, a `_` arm that panics |
| E0307 | not-optional | A binding condition, such as `if x := e`, on a value that is not optional | Alternatives |
| E0308 | no-operator | The operator is not defined on the type. For integer vector lanes, the report maps `+ - *` to `+% -% *%` or to `AddSat` and `SubSat`, unary `-` to `Splat(0) -% v`, and `/ %` to `DivTrunc`. Float lanes have no `%` | Proven for listed mappings; compiles for the vector mappings, which choose wrapping |
| E0309 | record-names | A destructuring names a field the record does not have | Proven: rename |
| E0310 | arm-without-value | In an `if` or `match` used as a value, an arm whose last line is not a value: a binding, an assignment, a `defer`, an `onerr`, a loop, or nothing | Alternatives: the value on the arm's last line, or the statement form |
| E0311 | value-if-without-else | An `if` with no `else` used as a value | Alternatives: add the `else`, or the statement form |
| E0312 | tail-call-signature | A `become` whose callee differs from the calling function in parameter types, result, error set or flows, or, through a `fn` value, in its type. Also a `become` in a function whose frame cannot be reused: one with an `ensures` clause, a `const` parameter or a capture, a `mut self` method of a struct with invariants, or one that checks its result's invariants when it returns | Alternatives: `return` the call |
| E0313 | tail-call-argument | A `become` argument that some target passes in memory rather than in a register, unless it is the caller's own parameter in the same position and not `var`; through a `fn` value, not even that when some target passes the parameter as a copy in memory (x86_64-linux: a struct of more than 16 bytes, or of more than 8 once the argument registers run out). Register-sized are integers, floats, `bool`, `char`, references, raw pointers, `fn` values, and plain structs and records of 0, 1, 2, 4 or 8 bytes with no unaligned field. The report names the argument | Alternatives: pass a reference |
| E0314 | tail-call-target | A `become` to an `extern`, an intrinsic, a function whose `cpu` features the caller lacks, a construction or a builtin. Also a `become` through an interface bound in a generic body (`x.M(a)` with `x` of a type parameter's type), and a `become` in a `naked` function or outside a function, as in a `test` block | Alternatives: `return` the call |
| E0315 | empty-range | A range pattern whose low end is not below its high end | Alternatives |
| E0401 | use-after-move | A value used after it was moved | Alternatives: reborrow, copy |
| E0402 | not-consumed | A linear value is not consumed on some path. Also one that is overwritten before it is moved, dropped with `_ =`, left as a live temporary when a `try` or `else` exits, live at a `become`, or in different states where two paths join. The report gives the path and the methods that release the value | Proven only when one void release exists, its arguments are unique in scope, and the value does not escape |
| E0403 | use-before-assign | A local that may be read before it is assigned | Alternatives |
| E0404 | assign-immutable | An assignment to something not declared `var`. This includes an `extern var`, even in `unsafe`: write to one through `*(&name)` | Proven: add `var` |
| E0405 | moved-in-loop | A value moved in an earlier iteration of the loop | Alternatives |
| E0406 | reserved-by-defer | A move of a value a `defer` uses, or a `become` while a `defer`, `onerr` or `scope` is pending | Alternatives |
| E0408 | not-plain | A copy of a value whose type is not plain | Alternatives |
| E0409 | move-out-of-reference | A unique or linear value moved out of a field, an element or a reference | Compiles: destructure |
| E0410 | must-declare-kind | An exported struct whose private fields make it a carrier, unique, interior-writable for loans, or linear, without the `view`, `mut view` or `linear` words that say so | Proven: add the words |
| E0411 | moved-before-failure | A by-value parameter that is not plain is moved or changed at a point after which the function can still fail. The report gives the move or change and the later error exit | Alternatives: move after the last failure point, or split the function |
| E0412 | private-in-signature | An exported signature or field mentions a private type. `Main` is exempt | Alternatives |
| E0501 | exclusive-conflict | A `mut` borrow overlaps a live loan. The report gives the loan, its holder and the use that keeps it live | Alternatives; `from self.f` when the callee body shows it, proven for a private function and compiles for an exported one |
| E0502 | write-while-borrowed | A write overlaps a live loan | Alternatives |
| E0503 | move-while-borrowed | A move overlaps a live loan | Alternatives |
| E0504 | access-while-exclusive | A read or `&` overlaps a live exclusive loan | Alternatives |
| E0505 | outlives-owner | A loan on a local must outlive the local | Alternatives |
| E0506 | temporary-held | A loan on a temporary is held past its statement | Proven: bind a local |
| E0507 | mut-through-shared | A `mut` access through a `&` reference | Alternatives |
| E0508 | mut-of-immutable | `mut p` on a local not declared `var`, or a `const` `fn` literal mutating a captured binding not declared `var` | Proven: add `var` |
| E0601 | source-required | Several parameters could supply a function's result, or a private field would change the automatic answer, so the signature must say which with `from` | Minimal clause from the body: proven for a private function, compiles for an exported one |
| E0602 | source-undeclared | A result flows from a source the signature does not name. Under a per-field clause: a field built from a source not among its own, or a value that arrives whole (read through a reference, or a whole local) and is not covered by the sources of every field that holds loans. Also a result that points into writable global memory and is returned under a source, beside a raw-pointer parameter, or by an impl method whose interface method names a source | Clause from the body: proven for a private function, compiles for an exported one |
| E0603 | returns-local-borrow | A result holds a loan on a local, or a `become` argument borrows one | Alternatives |
| E0604 | store-undeclared | A store into a parameter with no clause that allows it. Also a reference into writable global memory, stored into a parameter. No clause can name a global, so for that form, return the reference from a function whose result names no source instead. A store into the state a `Ctx` parameter refers to (a struct holding capabilities, such as an allocator or `Io`, beside one reference to state) names that field and the clause to write once the state is passed beside the `Ctx` ([`Ctx` structs](codebase.md#3-ctx-structs)) | Proven for a private function; compiles for an exported one, whose new clause can move errors to callers |
| E0605 | loan-into-global | A loan on memory that is not static, stored in a global or in an error's payload | Alternatives |
| E0607 | from-own-bytes | `from p.f` where `f` is stored inline or is `mut`, or `p` is passed by value, or a per-field clause names a field that holds no loans. A raw-pointer field of a `view` is allowed in `from p.f`, and a `view` result takes a per-field clause as a struct does | Proven: use `from p`, or drop the field from the clause |
| E0608 | flow-mismatch | An `impl` method or a `fn` value has flows wider than its declared type allows. Under a per-field clause this is checked field by field, and a whole flow must fit every field that holds loans | Compiles |
| E0609 | literal-captures | A closure such as `\|x\| e`, which Mk does not have. A `fn` literal that uses a local, unless it is passed directly as the argument of a `const` `fn` parameter: binding it, storing it, returning it, passing it to another parameter, or using it in an `s.go` call is an error. Even as a `const` argument, a `fn` literal may not move out of a captured name, mutate a capture in a contract or invariant, capture a name its `fields` loop declares, return a loan on a captured name, store a capture loan into a parameter, or store a non-static loan into a captured variable. A method named without a call, such as `c.Get`. The message names the rewrite: a struct plus a `fn`, or a loop. To store a callback, store a `fn` value and its plain state, as the cookbook's stored-callback recipe shows | Alternatives: struct plus `fn`, or a loop |
| E0701 | bound-unmet | A type argument lacks a bound. This includes `simd.Lane` and the lane class a `std.simd` intrinsic needs (`lanes: integer`, `lanes: float`); a `std.mmio` type that is not an integer, `int`, `uint` or raw pointer; an `extern var` whose type is not plain (`bound: Plain`); and the release bounds of linear elements. Freeing a heap container, or a map's `Set`, `Ensure` or `Replace`, when the key or element type is linear and has no `Free`: the report names `FreeEach`, `FreeEmpty`, `Replace` or `Take` instead. A call that would drop a linear value with no allocator to free it (`Remove` by key, `Guard.Store`, `SpinGuard.Store`, `Pool.Free`, `Pool.Sweep`, or an arena map's `Set`, `Ensure`, `Replace` or `Set.Add`) is reported at the call | Compiles: add `impl` stub; `FreeEmpty` for a container's `Free` whose elements are closed, linear and not `Free` |
| E0702 | no-impl | An interface method called on a type with no `impl` of the interface. Also, inside a function that belongs to an interface and has no type parameters of its own or of the interface, a call of that interface's methods or functions on a value of the interface's type | Compiles |
| E0703 | needs-bound | A generic body uses an operation its bounds do not provide | Compiles: add bound |
| E0704 | impl-conflict | A second `impl` of one interface for one type; an orphan `impl`, for an interface and a type that the module declares neither of; an `impl` of a computed interface such as `Plain` or `simd.Lane`; or an `impl Format` for an error, which has one already | Alternatives |
| E0705 | not-const | A value that is not constant passed to a `const` parameter; a call result is not constant. Also a function with `const` parameters used as a `fn` value. Inside a body: a `const` parameter of `fn` type used other than by calling it, or by passing it to a `const` `fn` parameter of a call that is not `s.go` at most once per call; and any use of a `const` parameter inside a `fn` literal or a contract. In a `Print`-style function, a use of the record argument that needs its type to meet `Fields[Format]`, other than passing the record, with its format string unchanged, to another such call, or passing that format string to another `const` parameter | Alternatives |
| E0706 | requires-failed | A compile-time `requires` is false at a call, or where a function is named as a value. Common cases: a format string that does not match its argument record, a string with holes and no record, or a hole whose value has no `Format`; a `simd` lane count other than 2, 4, 8, 16, 32 or 64, or a `Shuffle` index outside `0` to `2N - 1`; `align(n)` with `n` not a power of two from 1 to 65,536, or `align` with `packed`; a `section` name that is not 1 to 64 printable ASCII bytes with no space or `"`; `open` on an alias that is not a non-generic error set, whose members are not all declared in this package, or that sits in a module declaring none of them. Also a compile-time `requires` on an impl method that uses the impl's or the method's type parameters or `const` parameters, and a clause that no caller can restate when it passes a `const` on | Proven: the restated `requires` of a `const` passed on, when the clause keeps no name of the callee; alternatives otherwise |
| E0707 | impl-signature | An `impl` method's signature differs from its interface's | Proven: copy signature |
| E0708 | instantiation-limit | Generic instantiation does not stop growing: deeper than 64 along one chain, or more than 100,000 instances in one build. Reported at the outermost concrete call, with the chain | Alternatives |
| E0709 | const-arithmetic | Arithmetic on a `const` parameter inside a generic | Alternatives: use a slice |
| E0711 | cannot-infer | No argument or expected type fixes a type parameter. Also a generic function named as a value without its type arguments, which are never inferred there | Proven when one type fits |
| E0801 | error-not-in-set | A `fail` or `try` whose error is not in the function's error set | Alternatives: widen or handle |
| E0802 | error-unhandled | An `E!T` used without `try`, `else` or `match`, or a `scope` over a `Scope[E]` without `try` | Compiles: add `try` |
| E0803 | else-falls-through | An `else` block where a value is needed whose last line is not a value | Alternatives: the value on its last line, or `return` |
| E0804 | try-on-non-error | `try` on a value that is not an `E!T` | Proven: remove |
| E0805 | missing-return | A path through the function reaches its end with no `return` | Proven for a tail expression; Compiles for a last `if` or `match` whose arms end in values: `return` before it |
| E0806 | try-in-defer | A `defer` or `onerr` body that exits through `try`, `fail`, `return`, `become`, or a `break` or `continue` to a loop outside it | Alternatives |
| E0807 | onerr-two-values | One `onerr` body consumes two linear values | Proven: split it |
| E0901 | const-failed | A panic during compile-time evaluation, or an operation that compile-time evaluation cannot do: raw memory access outside what the evaluation allocated (the copies of consts it reads count as allocated, but are read-only, so a raw write to one is refused); reading a pointer's bytes as an integer; a `var` global's memory; an `Io` operation or `sys.Syscall`; a call of a bodiless `extern`; `asm` or a call of a `naked` function (`refused: asm`); and a `std.mmio` access (`refused: volatile`). The report gives what was refused and a trace | Alternatives |
| E0902 | fuel-exhausted | Compile-time evaluation ran out of fuel at this site | Compiles: raise `fuel` |
| E0903 | generator-error | A generator reported an error at a location in your code. Each generator's page lists what it refuses: doc/std/dyn.md and doc/std/derive.md | Alternatives |
| E0904 | generated-edited | A file under `gen/` differs from the hash it was generated with | Proven: regenerate |
| E0905 | generated-invalid | Generated code fails to check. The report cites the generated line, the generator and the `gen` line | Alternatives |
| E0906 | forbidden-input | A generator used an input it did not declare, or read the structure of a type that only generated code defines | Alternatives |
| E0907 | layout-cycle | A type's size depends on itself | Alternatives |
| E0908 | stage-violation | A compile-time evaluation in a type, a `const` argument, a `requires`, a `gen` argument or a const named in a match pattern needs a function body or a type layout from the same module. The report gives the trace | Compiles: move the helper to a submodule |
| E0909 | const-cycle | A const's evaluation reads the const itself | Alternatives |
| E0910 | file-input | A `build.File` or `Gen.Input` path that is absolute, names a drive or holds `..`; or one that is not a regular file under the calling module's package root, is reached through a link, or is larger than 64 MiB | Alternatives |
| E1001 | not-send | A `.go` argument is not `Send`, for any reason E1002 does not cover | Alternatives |
| E1002 | not-share | A `.go` argument holds `&C` or `[]C` at any depth, with `C` not `Share`; a `var` or `const` whose type is not `Share`; or a `const` whose value points to something whose type can hold a `Cell`, `Mutex`, `Chan`, `Arena`, `Atomic`, `Scope` or raw pointer, error payloads included. Views are exempt. Never reported together with E1001 | Alternatives |
| E1003 | scope-escape | A `Scope` used outside its `scope` binding, or `.go` in a `defer` of the scope's block | Alternatives |
| E1101 | needs-unsafe | An operation that needs `unsafe`: `asm`; calling or naming a `cpu` function whose features the caller lacks; calling or naming a `naked` function, a `std.mmio` function, `str.FromC`, an unsafe `std.raw` function or `fs.Map`; reading or taking the address of an `extern var`; reading a C function pointer out of a C import's struct (`c-fn-pointer`); and calling or naming the runtime's low-level entries: any exported function or method of `std.vm` or `std.linux`, `runtime.AddFaultFilter`, `runtime.ForeignIo`, `runtime.ForeignContext`, `mem.Custom`, `Alloc.Bytes` or `Alloc.FreeBytes`; an `impl` of `Send` or `Share` without `unsafe` | Alternatives |
| E1102 | unsafe-not-permitted | `unsafe`, a `section` clause or an `extern var` in a module that `pkg.mk` does not list in `unsafe_modules` | Alternatives |
| E1103 | raw-outside-unsafe | A raw pointer dereferenced outside `unsafe` | Alternatives |
| E1104 | asm-operand | An `asm` operand of a type `asm` does not take: a reference, slice, `str`, `fn` value, aggregate, or a type that names a type parameter. Also a `bool` or `simd.Mask` output; a `const` operand that is not an integer constant; a `sym` that names no non-generic function or global; conflicting `options`; an unknown `clobber_abi`; a template hole that names no operand; a label used twice; or a reserved register (`rbx`, `rbp`, `rsp`, `rip`; on aarch64, at build time, `x18`, `x19`, `x29` and `sp`). At build time, for code the target uses: an unknown register or class, a class whose CPU feature the function lacks, a type the class cannot hold, or any `asm` on a target with no register table | Alternatives: `raw.Of(r)` or `raw.Data(xs)` for a reference or a slice |
| E1105 | naked-body | A `naked` function whose body is not exactly one `unsafe` block holding one `asm`; whose `asm` has an operand other than `const` or `sym`, or an option other than `att` and `raw`; that uses a parameter by name; or that has type parameters, a contract or a `cpu` clause | Alternatives |
| E1106 | cpu-feature | A `cpu` clause or `CpuFeatures` name that is not a known feature; a `cpu` clause on a bodiless `extern` or an `impl` method; `Fma` where the function's features lack `fma`, or a rounding method where they lack `sse4.1`. On x86_64, a call of one of std.raw's crypto intrinsics (`AesRound`, `AesLastRound`, `Clmul64`, `Sha256Rounds4`, `Sha256Schedule4`, `Sha1Rounds4`, `Sha1NextE`, `Sha1Schedule4`), or a `fn` value naming one, where the features lack `sse2`, even inside `unsafe` | Alternatives |
| E1107 | vector-abi | A `simd.Vec` or `simd.Mask`, or an aggregate holding one by value, in an `extern` or `export extern` signature. Its place in the C calling convention would depend on the callee's CPU features | Alternatives |
| E1108 | general-regs-only | A float or vector operation, parameter, result or `asm` operand in code that a `general-regs-only` bin reaches, outside a function whose `cpu` clause holds `sse2` or wider; or such a function taking or returning a float or vector. `mk check` and `mk build` judge the code the bin reaches, and report the call path from its entry | Alternatives: a `cpu "sse2"` function that saves the FPU state, or `cpu: "baseline"` on the bin |
| E1109 | no-c-form | A parameter or result of a library's `export extern` function whose type has no C form, an error-union result, or a `const` parameter. Checked in the modules at or under a library bin's root. A reference to a type with a C form is a C pointer, so it has a C form too, as `MkPanicHook`'s `&PanicInfo` does | Alternatives |
| E1202 | iterator-chain | `.map`, `.filter` or `.collect`. Mk writes a loop | Compiles when the output type is stated |
| E1203 | lifetime-syntax | A lifetime such as `'a` | Proven: delete or replace |
| E1204 | async-syntax | `async` or `await` | Proven: delete |
| E1205 | fn-keyword | `fn`, `func` or `def` before a declaration | Proven |
| E1206 | semicolon | A `;` anywhere but in `[e; n]` | Proven |
| E1207 | let-keyword | `let` | Proven |
| E1208 | postfix-question | `?` after an expression, or C's `c ? a : b` | Proven after a call; alternatives otherwise |
| E1209 | null-word | `null` or `nil` as a value. `None` is E0207 | Proven |
| E1210 | exception-words | `throw`, `catch` or `panic!` | Alternatives |
| E1211 | arrow-return | `->` | Proven |
| E1212 | ampersand-mut | `&mut` | Proven |
| E1213 | double-colon | `::` | Proven |
| E1214 | fat-arrow | `=>` in a match | Proven |
| E1215 | pub-keyword | `pub` | Proven |
| E1216 | loop-keywords | `while` or `loop` | Proven |
| E1217 | if-let | `if let` | Proven |
| E1219 | impl-block | `impl T { }` with no interface | Proven |
| E1220 | inclusive-range | `..=` | Proven, or alternatives when the high end may be the type's maximum |
| E1221 | foreign-name | A name, member or type form from another language (`Vec`, `.unwrap()`, `dyn I`). The report gives the Mk name from the synonym table | Proven if the arguments line up, else alternatives |
| E1222 | as-cast | `x as T` | Compiles: `T(x)` or `Cast` |
| E1223 | assert-statement | `assert c` as a statement, as in C, Python or Zig | Proven: `Assert(c)` |

A number missing from the table is not in use, and is never reused.

## Examples of the systems codes

Most codes have their examples in [the reference](reference.md) and [the memory model](memory.md). The codes for `asm`, `naked` functions, CPU features and `extern var` have theirs here. E1108 has none, because it is judged over a whole bin.

```mk
// verdict: reject E1104
// asm reaches memory through raw pointers, and no bool comes out of it
FirstByte(xs: []u8): u8 {
    var b: u8 = 0
    unsafe "the template reads one byte at the start of xs" {
        asm("mov {b}, byte ptr [{p}]", b: out(reg_byte) b, p: in(reg) xs) // <- E1104
    }
    return b
}

Carry(): bool {
    var c := false
    unsafe "stc sets the carry flag and setc copies it" {
        asm("stc\nsetc {c}", c: out(reg_byte) c) // <- E1104
    }
    return c
}
```

```mk
// verdict: reject E1104
// no Mask comes out of asm, which could leave a lane neither all zeros nor all ones
import std.simd

Ones(): simd.Mask[u8, 16] {
    var m := simd.Mask[u8, 16].FromBits(0)
    unsafe "pcmpeqb of a register with itself sets every lane" {
        asm("pcmpeqb {m}, {m}", m: inout(xmm_reg) m) // <- E1104
    }
    return m
}
```

```mk
// verdict: reject E1105
// a naked body is one unsafe block holding one asm
export extern naked Halt() {
    return // <- E1105
}
```

```mk
// verdict: reject E1106
// a cpu clause names known CPU features only
Count(xs: []u32): int cpu "avx9" { // <- E1106
    return xs.Len()
}
```

```mk
// verdict: reject E1107
// a vector crosses no C signature, since its place there depends on the callee's features
import std.simd

export extern Total(v: simd.Vec[f32, 4]): f32 { // <- E1107
    return v.ReduceSum()
}
```

```mk
// verdict: reject E0404
// an extern var is not a Mk var, so a write goes through its address
extern var BootFlag: u32

Clear() {
    unsafe "the linker script places BootFlag in writable data" {
        BootFlag = 0 // <- E0404
    }
}
```

```mk
// verdict: reject E0706
// a section name is 1 to 64 printable ASCII bytes with no space or quote
const Tag: u32 = 7 section "boot data" // <- E0706
```
