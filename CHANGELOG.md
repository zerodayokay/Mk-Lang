# Changelog

Every user-visible change to Mk, newest first. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/). What a 0.x release may change, at each stability level, is in [stability](doc/std/README.md#9-stability).

## Unreleased

### Language

- `if` and `match` are expressions wherever an expression stands, operands included: `1 + if c { 2 } else { 3 }`, `xs[if c { 0 } else { 1 }]`, `if n == if c { 1 } else { 2 } { }`, `match k { .. }.Len()`. They were values only on the right of a binding or an assignment, after `return`, as an argument and in a literal, and E0101 elsewhere, even in parentheses, so a choice inside a larger expression needed a local. Suffixes and operators after the closing brace apply to the whole construct, operands are still evaluated left to right, and a value `if` still needs its `else` (E0311). At the start of a statement `if` and `match` are still statements, so `if c { a } else { b }.Len()` there is E0101: wrap it in parentheses.
- A named const is a match pattern, compared by value: `OpAdd { }`, `codes.OpAdd | 3 { }`, `some(Limit)`, `P(name: Stop)`. Its type must be the scrutinee's and one a literal pattern takes (an integer, `bool`, `char` or `str`), and it covers its one value for exhaustiveness as that literal would. It was E0101 (`constants are not patterns`) for a bare name and E0301 for a qualified one, so a set of op-code consts needed an `if` chain. A float const is E0308, a const of another type E0301, and a const whose value calls a function of its own module E0908, as in an array length.
- A field and a method of one type may have names that differ only in case and underscores: a struct with a field `cpu` may declare `Machine.Cpu` (or implement an interface method `Cpu`), and `m.cpu` reads the field while `m.Cpu()` calls the method. It was E0206, so a getter had to be named apart from its field. Two fields, two methods, a method and a variant, or a field spelled exactly as a method still collide (E0206), and a misspelling such as `m.CPU` is still E0207, its fix picking the field for a read and the method for a call or a `Machine.CPU` path.
- An error's payload may hold a `str`, if the string lives as long as the program: `error Unsupported(feature: str)` built with a literal, a `const`, a global, or a `str` a function returns that comes from none of its parameters. A `str` field was E0408, so a message had to be a code or a fixed array and a length. A `str` that borrows, such as a parameter or a view of a local buffer, is E0605 where the error is built or the field assigned, and a `mut` borrow of the field is E0605, so an error still borrows from no frame and travels up the stack and into other tasks; reading the field gives a `str` with no loan, even through a reference. A slice, `&T`, `?str`, `[N]str` or a struct holding a `str` is still E0408 ([declaring errors](doc/reference.md#declaring-errors)).
- An array pattern takes a fixed array apart by position: `[a, b] := xs` moves element i of a `[N]T` into the i-th binding and consumes the array (or copies it, for plain elements), and `var [a, b] := xs` makes the bindings `var`. An array of linear values, such as `[2]fs.File`, could be built but never consumed, since `xs[0]` is a move out of a place (E0409) and `(a, b) := xs` needed a record (E0301). The pattern names every element (fewer or more names than `N` is E0305), takes only names (E0101), and needs `N` known where it is checked (E0301 for a const generic length) ([destructuring](doc/reference.md#destructuring)).
- `void` may be written as a type argument of a generic call wherever inference may choose it: `raw.Null[void]()`, `raw.FromAddr[void](a)` and `raw.PtrCast[T, void](p)` build a `*void`, C's `void *`, and `raw.PtrCast[void, T](p)` gives the typed pointer back. The written argument was E0301 (`void` is only a result type), while `raw.Addr(p)` of a `*void` inferred the same argument, so a `void *` parameter of a C function could be passed only what C had returned. `void` as the argument of a generic type (`Box[void]`), in `SizeOf[void]()` and as a local's type is E0301 as before.

### Packages

- `pkg.Link(kind: "lazy")` binds a DLL lazily on `x86_64-windows`: it is not in the import table, the first call of one of its functions loads it (on the carrier's system stack when the caller is a task) and finds the function, and later calls cost one indirect call, as an import-table call does. A program that names an optional DLL (Direct3D 12, Vulkan, a newer Windows component) starts where that DLL is absent; a hard import of a missing DLL ends the process before `Main`, and the only way around was `dl.Open` and an address per function. The DLL is searched as a hard import is (the executable's directory and System32, never the current directory or `PATH`), a `.dll` path is copied beside the executable, and a program that calls none of its functions carries nothing of it. Calling a function that cannot be resolved panics with the new code `unresolved function`, naming the function and the DLL. `kind: "lazy"` for any other target is E0202 `reason: link` ([linking libraries](doc/reference.md#linking-libraries)).
- `dl.Present(io, f)` reports whether the bodiless extern `f` can be called: for a lazily bound one it loads the DLL if needed and finds `f` without calling it, for any other it is `true` when the program is built. Its argument names the function directly (E0705 `reason: present-argument` otherwise), and needs no `unsafe` ([std.dl](doc/std/dl.md#present)).
- Std binds `crypt32.dll` lazily through the same mechanism (`std/sys/windows/crypt32.lazy.def`), in place of its hand-written loader; a program that verifies against the Windows store still imports nothing of crypt32.

### Runtime

- On Windows, a C call from a task runs under the system stack's real `StackLimit`. `mk_call_system` installed the limit read when the carrier's thread started, so after one call had grown the system stack, the next call on that carrier ran below the limit the thread claimed. A stack probe trusts every page below `StackLimit` to be unused, and MSVC's `__chkstk`, which every system DLL uses, stores a zero in each: the second deep call wrote over its own live frames and crashed (two `D3DCompile` calls from one task, on any carrier). The limit a call leaves is now kept for the scheduler and for the carrier's next call.

### Checker

- `f32(x)` of a float literal or named float constant whose value is exactly an `f32` compiles, as `u8(200)` does for an integer: `f32(0.5)`, `f32(-1.5e3)`, `f32(Half)` for a `const Half: f64 = 0.5`. A literal was E0303 (`f32(f64)` can lose information), since it took `f64` and `T(x)` judged only integer constants by value; a constant of the same module was accepted without being judged, so `f32(Big)` of a `const Big = 1e300` compiled to an infinity. A value that rounds or overflows in `f32` (`f32(0.1)`, `f32(Big)`) is now E0303 in both forms; `x: f32 = 0.1` and `Cast[f32](0.1)` round it. Such a constant was also accepted by an integer conversion, `u8(Half)` truncating to 0; a float to an integer is never `T(x)`, so that is now E0303 too, as `u8(0.5)` and `u8(x)` of an `f64` already were: write `Cast[u8](Half)`. An operator over named float constants, `f32(Big + Big)` or `u8(Half + Half)`, was accepted without being judged too; it is now E0303, as one over float literals is.
- A call infers a function's const generic from an array argument's length: `Total[const N: int](xs: &[N]int)` called as `Total(&xs)`, alone or beside a type parameter (`First[T, const N: int](xs: [N]T)`). Every such call was an internal compiler error in check (`types.Table.Intern(array): ... length kind 0`), so the length had to be written in brackets. Arguments that disagree on the length are E0301, and a const generic no argument names is still E0711.

### Fixed

- A C import whose headers declare an extern variable twice (`extern int x; extern int x;`, as the Windows SDK does for 147 `IID_` names) binds it once, in the module of its first declaration. It was bound twice, so the view was E0206 as soon as an importer named the variable. When the declarations' types differ, as an array's do when only one gives its length (`extern int z[]; extern int z[3];`), it binds with the type C composes from them, `[3]i32`, which Clang gives the last declaration, in that declaration's module ([C imports](doc/reference.md#c-imports)).
- A C import's shim defines only the static inline functions and declared function-like macros the program uses. It defined every one the headers have, so an unused one that calls a function nothing links broke the link (`undefined symbol`), as the Windows SDK's ucrt inlines do with `_errno` and `wcsnlen`. The translation's cache is unchanged by a new use; the shim's object is compiled again, as its text changes.
- Converting another module's negative integer const to a wider type gives its value: `i64(m.C)` and `int(m.C)` of an `export const C: i32 = -4` are -4, as `i64` of the module's own const is. Lowering folded the conversion from the const's bits without sign-extending them, so it gave 4294967292 (an `i8` or `i16` const likewise), in a run-time expression, a loop bound and a `const` initializer alike, in every profile. A C import's negative macro or enum constant (`i64(k.Neg)` of `#define NEG (-4)`) had the same wrong value. A const argument (`F(i64(m.C))` for a `const k: i64`), `Cast`, `Trunc`, operators and patterns over such a const were right.

### Diagnostics

- Writing another module's extern var directly, such as a C import's `r.r_count = 8` or `r.r_count += 1` inside `unsafe`, is E0404 (`extern var r_count is no Mk var: write it through its address, *(&r.r_count), in unsafe`), as the same write of the module's own extern var is. It was reported as E0301 (`only a local, a field, an element or a dereference can be assigned`, reason `not a place`), which named no way to write it.
- E0203 (unknown member) at a member access lists only the members the calling module can use: another module's private fields and private `Type.Name` functions are left out, and a struct's `invariant` no longer shows as a member named `""`.
- A `gen` line that emits nothing because a module it depends on failed to check (the generator's own module, a module the generator imports, directly or not, or the module of a type argument or of the fuel const; a borrow error included) no longer hides that error behind the E0201s on the names its output would have declared: the module's first error, in report order, is reported with them as their cause, whatever its phase, and each E0201 cites the gen line and names the module. A borrow error, which rejects only its own function, is the cause only when no module the line depends on has another error. A line that read a type of its own module it cannot see before checking keeps the old note.
- A `gen` line whose generator fails (E0903, or any error of its own that makes it emit nothing) is reported even when a name its output would have declared is used: that E0201 (in its module, also when another module exports the spelling, or an importer's `m has no item`) and the E0905 of another gen line's output that names it are now its consequences and no longer hide it under the earliest-phase rule; an E0201 cites the gen line. The rule now keeps every error a reported error names as its cause, whatever its phase, and `--max-diagnostics` never cuts such a cause.

### Tools

- `import c.windows`, `c.dxgi`, `c.d3d12` and `c.d3dcompiler` work in any package with no `pkg.CImport`, no header, no SDK and no Clang, on any host: they are system views, bindings generated from the Windows SDK 10.0.28000.0 and shipped inside mk. A build takes only the items it names and what they need. A package's own `pkg.CImport` of the same name wins; any other name is E0202 as before, and its message now lists the views. A standard-library module may import a view and is not late. Calls need `unsafe`, function pointers are E1101 and extern vars are C's, as in any C import ([system views](doc/reference.md#system-views)).
- `mk sdk views` lists the system views mk carries, with the inputs each was generated from, its counts and its identity; `mk sdk` adds, for each target, the system headers a `pkg.CImport` reads and where they come from. `mk version` has one line naming them ([mk sdk](doc/tools.md#mk-sdk)).
- `pkg.CImport(..., sdk: true)` reads a C import's headers with the host's SDK: `MK_SYSROOT_<target>`, else the newest Windows SDK the registry or `%ProgramFiles(x86)%\Windows Kits\10` holds (for `x86_64-windows` on Windows), or `/usr/include` with its multiarch directory (for a Linux target on Linux), else the evidence directory; none is E0213 `no-sysroot`. A declaration a system view the program imports also binds is the view's item, and a record whose layout differs is refused. Without the field nothing changes ([C imports](doc/reference.md#c-imports)).
- A C import binds the Windows SDK when `MK_SYSROOT_x86_64-windows` (or `MK_SYSROOT_x86_64_windows`) lists its `um`, `shared` and `ucrt` directories: `windows.h`, `d3d12.h`, `dxgi1_6.h` and `d3dcompiler.h` in one import bind 95% of their declarations, and a program creates a window with no declaration written by hand. Such a list (`windows.h` beside `winnt.h`, no `_mingw.h`) is read for Clang's `x86_64-pc-windows-msvc` triple, the only one the SDK parses under, and for Visual C++ 19.33 on every machine (Clang would take `_MSC_VER` from an installed Visual Studio, and from 1950 the ucrt's inline functions are no longer static, so the bindings would differ), with mk's own `vcruntime.h`, `vadefs.h`, `vcruntime_string.h` and `excpt.h` first, which the SDK includes from the MSVC toolset and does not ship. The SDK did not parse at all. Every other sysroot, and no sysroot, is read for `x86_64-w64-windows-gnu` as before, with the same cache key ([C imports](doc/reference.md#c-imports)).
- `pkg.CImport` takes `include_dirs`, package directories searched for headers after the package root: a C library whose header includes another from its own include directory (`#include "zconf.h"` under `third_party/z/include/`) could not be imported, since the root was the only `-I` (E0001 `file not found`). The C imports' directories form one search path, which the translation, the shim and the package's C sources use, and which is part of the C import's cache key. Each is a relative directory inside the package, else E0202 `invalid`; the field may be left out ([C imports](doc/reference.md#c-imports)).
- `mk query sig`, `sig --member` and the other query answers that print a signature spell an alias as the declaration writes it: `Load(i: u64): Value` for `type Value = u64`, not `u64`, so a reader can tell a function that returns the alias from one that returns its type; also under `&`, `mut`, `[]`, `?`, an array, `E!T` (`AB!Value` for `type AB = A | B`) and a generic type's arguments. `sig --member --json` gains a field `resolved`, the signature with every alias spelled as its type (still version 1: a field added). `mk apidiff` compares the resolved spelling, as before.
- `mk query` and `mk lsp`'s queries spell a dependency with the root's local name only in a module path (a `module` line, `import`, `find`, `errors`, and the module paths in an impl method's key, which `stack` prints): a signature, a contract, a doc comment and a test's name are answered as their source writes them. Every word equal to the dependency's canonical id was renamed, so where a workspace member's manifest name was also a parameter's, `sig Run` answered `Run(heap: mut Heap)` for the source's `Run(hp: mut Heap)` (a member `hp` that the root names `heap`).
- Generic instances and fn literals get new symbol names: mk's module interface encoding is now version 3, and the 16 hex digits that end a generic instance's symbol (`mk.<item>.<digits>`) or a fn literal's (`mk.fn.<digits>`) come from hashes whose domains name that version. A `.mkprof` profile matches functions by symbol, so one recorded by an earlier mk gives those functions no data, never another function's: record it again with `--pgo-gen` ([profile-guided optimization](doc/tools.md#10-debugging-coverage-fuzzing-and-profiling)).
- A header added to the package root or to an earlier `include_dirs` directory, where it shadows one found later on the search path, now rebuilds the C import's shim and the package's C sources. Their cached objects checked only the headers the compile had read, so the binding was translated again while the old objects were linked, and the program used both headers' values. A C object's cache entry now keeps the paths that would shadow its headers, as the translation's entry does, and both now count a header under nested include directories (`include_dirs: ["inc", "inc/sub"]`) by its name under each, so a `sub/y.h` added to the root shadows `inc/sub/y.h` included as `"sub/y.h"`; the binding and the objects had both kept the old one. A header added beside the file that includes it with quotes is not yet among them.
- A C import whose header includes, with quotes, a header beside it that an earlier search directory also holds (`b/inc/x.h` including `"y.h"`, with `a/inc/y.h` and `b/inc/y.h`) is no longer translated again on every build. The translation's cache entry counted `a/inc/y.h` as a header that would shadow the one read, though Clang reads the one beside the includer first, so the entry never hit. A file at such a path when an entry is saved is now no shadow of it; a directory there still is, since Clang passes over a directory named like the header, and a header later put in its place shadows the one read.
- `mk query stack` bounds a recursion whose depth parameter (a `u8` or `u16` parameter that every call inside the recursion passes on unchanged or less a positive constant, lowered around every cycle) bounds it, counting it as at most (max + 1) frames per function, and lists it as `depth-bounded recursion`; any other recursion still answers `no static bound` ([tools](doc/tools.md#52-depth-bounded-recursion)).
- `mk query stack` answers `no static bound` when its walk stops at 20,000 functions; it answered `bounded` without seeing the functions past the stop, so a recursion there went unreported.
- `mk query sig --member <name> [--json]` lists every function and method of a workspace member's modules with its signature and contracts, never capped, including impl methods and the functions `gen` lines write ([section 5.3 of the tools guide](doc/tools.md#53-member-wide-signatures)). `sig Name` is unchanged.

### Standard library

- `vm.CodeRegion` is `Send` and `Share`, so a code space crosses tasks: a task may own, commit, edit and release a region another made, a `Mutex[vm.CodeRegion]` or an `atomic.Spin` of a pool of regions is `Share`, and several tasks may run a region's code through a `&`. It was neither (E1001, E1002), since it holds its views as raw pointers, so a JIT whose compile tasks install code had to assert `Send` on a wrapper of its own. `vm.CodeWriter` is still neither, so a write window stays on the task that opened it; `vm.Region` was already both ([tasks](doc/std/vm.md#tasks)).
- `mem.Zeroed[T]()` is a `T` of zero bytes: every integer and float 0, every `bool` false, every raw pointer null, every optional `none`. It fills a plain record of many fields without naming each one, as C's `= {0}` does, so a C import's struct of any size starts zeroed with no `unsafe`: `mem.Zeroed[geom.GeomPoint]()`. `T` must be zero-valid, the bound `Arena.Slice` has, and not an error union (E0701 otherwise); it also runs at compile time ([zero-valid types](doc/reference.md#zero-valid-types)).
- `math.ShortestDigitsInto(out: mut []u8, x: f64): (len: int, exp: int)` writes a float's shortest digits into a caller's buffer of 17 bytes (an `f64` has at most 17), zeroing and copying nothing else; a shorter `out` panics with code 8. `ShortestDigits` returns a 792-byte `Digits` whose 768-byte array it zeroes on every call, and is now its wrapper.
- `List[T].Extend(xs)` appends a copy of each element of a slice to an arena list, as `HeapList[T].Extend(a, xs)` does for a heap one: one growth to fit them all, then one copy, so each new element is written once (`Resize` then a copy wrote every byte twice, and a `Push` per element checked the capacity per element). `T` must be plain (`E0701` otherwise); on `OutOfMemory` the list is unchanged.
- `testing.BenchSys(sys, f)` makes `f: fn(sys: &System, n: int)` a benchmark, so a benchmark can allocate from the heap: `testing.Bench` takes `fn(n: int)`, a function value that captures nothing, so it could not reach `sys.heap`. The test block takes `(sys: &System)` and passes it; `mk bench` times `f(sys, n)` and finds the call as it finds `testing.Bench(`, and any other run calls `f(sys, 1)` once. `Bench` is unchanged.
- `Arena.Alloc()`'s free gives back a block that has a chunk of its own (a large request the current chunk could not hold) while that chunk is the arena's newest, as it already gave back the last block bumped. So a frame, `Arena.NewIn(scratch.Alloc())` taken per call and freed when the call returns, rewinds `scratch` last in first out with its large requests too; they stayed in `scratch` until its `Reset`, so a loop of such calls grew it on every turn. Frames nest as calls do, and while the frames live at once fit in one chunk of `scratch` they make no heap call once warm, where `Sub` takes its first chunk from the heap on every call ([a frame per call](doc/std/core.md#a-frame-per-call)).
- `std.big`'s recursions (Karatsuba and Toom-3 products and squares and their scratch sizing, recursive division, `Text`, `Parse` and `Sqrt`) take a `u8` depth, and the operand swaps of addition and of unbalanced products no longer call themselves, so `mk query stack` gives every `std.big` operation a static bound.
- `List[T].Resize(n, x)` makes a list's length `n`, keeping the first `n` elements and appending copies of a plain `x` (`E0701` for any other `T`), growing at most once. A body generic in a plain `T` can fill a list with one value through it; there a loop of `Push(x)` is `E0604`, since the second `Push` stores `x` through the copies already in the list.
- `simd.Utf16LenBytes(b: []u8): int`, `simd.Utf16Len` for bytes: the UTF-16 length of UTF-8 or WTF-8 held as `[]u8`, such as a lone surrogate's three bytes, which `str.From` refuses. It shares `Utf16Len`'s vector kernels, and `Utf16Len` is now `Utf16LenBytes` over the string's bytes.
- `simd.Utf16Len` and `Utf16LenBytes` give the same count on every CPU for any bytes, not only UTF-8 and WTF-8: their scalar form (below 16 bytes and on `x86_64-freestanding`) now counts as the vector forms do, one unit per byte that is not a continuation byte and one more per byte from `0xF0` on.
- `hash.Sip13State` is SipHash-1-3 over input that arrives in pieces: `Sip13State.New(key)`, then `Bytes(data)` any number of times, then `Sum()`. Any split gives `hash.Sip13` of the concatenation. Like `Xx64State` and the SHA and BLAKE3 states it is not zero-valid, so `Arena.Slice[hash.Sip13State]` is E0701.

### Loan checker

- A store through a local or temporary goes on through the loans it holds only when its type can write a loan below its own bytes (R11). A view built over one field of a `mut` parameter, whose field leads to memory with no carrier, no longer makes `*self into self` store the parameter's own loan into the parameter (a false `E0604` whose suggested clause then locked every caller). The walk of a stored value's own loans, and a spawn's store of its arguments through a `&Scope`, do not take this cut, since they guard against an `unsafe` or `extern` body that writes past its signature: a view that holds an arena's `List` of non-carriers beside another borrowed piece, passed with `*h into h`, still makes the arena take that piece's loan (`E0504` where the piece is used while the arena lives).

## 0.6.0 (2026-11-24)

### Language

- `plain view struct` declares a bitwise-copyable shared-loan handle or view (`Kind: Plain`, `Freeze: true`, `Carrier: true`, `View: true`), inhabiting the same quadrant as `&T`, `[]T`, and `str`. Phantom type parameters on a `plain view struct` (such as `plain view struct Ref[T] { idx: u32 }`) stay `Plain + Freeze` even when `T` is `Unique` or `Linear`. Every field of a `plain view struct` must be `Plain` and `Freeze` under its generic bounds (`E0408`), `ends` and shared holder `into` clauses (`v: Sym, x into v` / `&Sym`) are rejected (`E0507`), `plain mut view` is forbidden so `plain view struct` stays read-only for loans under the type-level R16 invariant, `E0410` suggests `plain view` when private carrier fields are `Plain + Freeze`, and `E0401` on moving a `view struct` whose fields are `Plain + Freeze` suggests `plain view struct`.
- `view struct` and `mut view struct` are now distinct: a plain `view struct` is read-only through `&self` for loans (propagating loan-store writability only from its type arguments), while `mut view struct` declares an interior-writable view whose `&self` methods store loans behind raw pointers or interior state (such as `Arena`, `Scope`, `dyn.Ref`, and allocator wrappers). E0410 requires an exported struct with private interior-writable fields to declare `mut view` (or `linear mut view`).
- Non-escaping `fn` literals passed directly to a `const` `fn` parameter (outside `s.go`) may now mutably capture enclosing `var` locals and reborrow `mut T` / `mut []T` bindings with no new syntax: assignments, `mut` borrows, and `mut self` calls on a capture form an exclusive borrow at the call site alongside its other operands (R13) that ends with the statement (R14), so R5 catches conflicting borrows (`E0501`/`E0502`/`E0504`). Mutating a non-`var` capture is `E0508`; writing through a captured `&T`/`[]T` stays `E0507`; escaping, moving out of a capture, mutating in a contract or invariant, returning or storing a capture loan, and storing a non-static loan into a captured carrier remain `E0609` (and storing a literal-local loan is `E0505`). Passing the same `const` `fn` parameter to two `const` parameters of the same call is `E0705`, preventing aliased mutable capture environments.

### Loan checker

- Passing a local split struct to a `mut` / `mut self` call without explicit `into` clauses no longer collapses unrelated carrier fields (`fv = 0`); instead, the checker computes per-struct store-compatibility components (`canReceiveFrom`) and merges only fields whose reachable carriers can hold/supply loans into each other.
- Bundled mutable context structs (`cx: mut CallCx`) track distinct tagged depth-2 inner loans (`n2F`, `outerKeep`, and `directWrite`), preventing inner allocations and field borrows (such as `Alloc(cx.heap, cx.roots)`) from falsely conflating unrelated context fields while preserving depth-3 interior and raw-pointer/view/`SpinCore` alias soundness.
- `Cell[T]`, `Mutex[T]`, `Chan[T]`, `atomic.Spin[T]` and `raw.Atomic[T]` whose payload `T` holds no loans (a non-carrier) no longer restore loan-store writability through `&` or mark global memory as taking loan stores, while remaining not `Freeze` for codegen.
- Closed all six R16 global-store soundness gaps for views by deriving loan-store writability directly from the explicit `mut view` / carrier-interior type invariant instead of scanning imported module closures: cross-module free constructors, unseen-module impl methods, local `fn` literals, factory/wrapper constructors, and generic `into` parameters are now judged soundly, while read-only views (containers, iterators, `Response`, `Inflater`, `ZstdDecoder`, `net.Conn`, `tls.Conn`) no longer produce false `*x into x` clauses or false global-store rejections.
- Fixed `fn` literal checking so explicit parameter `into` clauses on local `fn` literals without an expected type are preserved.

### Changed

- `Slice[T].SortStable` now takes an explicit allocator (`Slice[T].SortStable(mut self, a: &Alloc, const less: fn(&T, &T): bool): OutOfMemory!void`), removing the hidden process-heap allocation hook (`SortHeap` / `SortHeapInit`) and eliminating the sole exception to Mk's zero-hidden-allocation invariant. Scratch memory up to 4,096 bytes (for types aligned to at most 8) stays on the stack; larger scratch comes from `a` (falling back to `Len() / 2` elements before failing with `OutOfMemory`, with the slice unchanged) and is freed before returning. `Slice[T].SortStableIn` is removed, superseded by `SortStable`.
- Added `Slice[T].SortStableInPlace(mut self, const less: fn(&T, &T): bool)` in layer 0 (`std.core`): a strictly zero-heap-allocation stable block merge sort that works at compile time and in freestanding programs.

### Standard library

- `std.http` (`Serve`, `ServeWith`, `ServeOn`, `ServeChan`, `Dispatch`, `Route.handler`, `Response.Header`, `Response.Body`) and `std.compress` (`Inflater.Read`, `ZstdDecoder.Read`, `ZstdDecoder.ReaderLazy`, `Content.wrap`) no longer require `*res into res`, `*self into self`, or `a: &Arena into a` clauses.
- Arena containers (`List`, `Map`, `Set`, `Text`, `Bitset`, `BTree`, `Deque`, `PriorityQueue`, `Pool`) and `net.Conn`/`tls.Conn` remain read-only `view struct`s; interior-writable views (`Arena`, `Scope`, `dyn.Ref`, `dyn.Of` wrappers, `mem.Counting`/`Limit`/`Failing`/`Fixed`/`FixedHeap`/`Fallback`/`Sim`, `http.Client`, `websocket.Conn`) are `mut view struct`.
- `std.vm.CodeRegion` now opens writes through an exclusive linear scoped guard: `CodeRegion.Edit(mut self): Error!CodeWriter from self` and `CodeRegion.EditRange(mut self, offset: int, len: int): Error!CodeWriter from self` return a `CodeWriter` with `Bytes(mut self): mut []u8 from self`, `Flush(&self, offset: int, len: int)`, and infallible `Done(self)` (replacing `Writable`, `Flush`, `Seal`, and `Unseal`), and `CodeRegion.Commit` takes `mut self`. Regions start sealed and stay sealed outside an active `CodeWriter`, enforcing W^X at compile time, while `EditRange` restricts unsealing/re-sealing on per-page targets to `[offset, offset + len)` (with aligned zero-length edits acting as clean no-ops).
- Added `std.runtime` branch prediction and cold-path hints (`Likely(cond: bool): bool`, `Unlikely(cond: bool): bool`, and `Cold()`), emitting `llvm.expect.i1` intrinsics, cold `!prof` branch weights, stripped `alwaysinline`, and `cold` call attributes (and matching compile-time evaluator behavior).
- Added a wait-free 64-entry comparison ring buffer in `std.rt` (`CoverCmpEnable`, `CoverCmp`, `CoverCmpReset`, `CoverCmpCount`, `CoverCmpAt`, `CoverCmpWidthAt`) for live fuzzing guidance.

### Tools

- `mk check --all-targets` now checks declared `pkg.Select` target OSes, reports missing `pkg.Select` entries for declared OSes, clears incompatible host `--cpu` flags on cross-architecture targets, seeds comparisons cleanly when the host OS is omitted, and compares exported `Breaking` APIs (`signature`, `int` vs `i64`, `from`/`into` flows, errors, contracts, modifiers, exported fields, and `impl`s) across targets via `E0213`, while letting private helpers, private fields, sizes, and constant values differ per OS.
- `mk test --prop` now generates full `f32`/`f64` IEEE-754 edge values (`±0.0`, `±Inf`, canonical/payload/negative `NaN`, subnormals, `MinNormal`, `MaxFloat`), bounds fixed edge-case enumeration to both the parameter edge-product and `--prop-cases` so small budgets mix random cases without stalling on small integer domains, renders and persists non-finite and subnormal floats losslessly with `f64.FromBits`/`f32.FromBits` (JSON-safe without `NaN`/`Inf` errors), and shrinks floats under a strictly terminating bit-exact order.
- `mk test --fuzz` (`std.testing`) now tracks per-run block counter deltas using AFL 8-bit hit-count buckets, splices across pooled inputs, and instruments 2/4/8-byte integer `==`/`!=` comparisons and non-boolean integer switches with live `std.rt.CoverCmp` operands for Redqueen replacement and constant splicing.
- Default pull-request and `main`-push CI runs an 18-job fast matrix (`TEST_SHORT=1` for compiler/driver suites) while nightly and release workflows run the full non-short multi-OS test and bundle suites.

### Platforms

- `aarch64-macos` and `x86_64-macos` now implement `std.vm.CodeRegion`: arm64 macOS uses `MAP_JIT` with per-thread `pthread_jit_write_protect_np` switching (and `sys_icache_invalidate`) when the host supports it, falling back to private anonymous `mprotect` W^X toggling (`PROT_READ|PROT_WRITE` vs `PROT_READ|PROT_EXEC`, as on x86_64 macOS) when per-thread JIT write protection is unavailable or `MAP_JIT` is refused by policy; both are wired into `runtime.AddFaultFilter`.

### Performance

- Compiler lowering and codegen (`internal/lower`, `internal/codegen`):
  - Added sound accumulator and direct tail-recursion loop lowering (`elimAccTailRec`), monotone while-loop bounds versioning (`versionMonotone`), 2D counted-loop nest bounds/overflow versioning (`versionNestedBounds`) with cold fallback branch weights, two-local exact add/sub interval inversion in `refineStep`, small flat-loop single-callsite inlining on leaf-loop spines (`solecall.go`), and branchless bitmask lowering for 2-target integer switches (span < 128).
- Standard library (`std`):
  - Upgraded `std.core` SwissTable control groups from 8-byte scalar SWAR to 16-byte SIMD (`Vec[u8, 16]`) groups (with scalar fallback on freestanding) and outlined `Text.GrowFor` so `Text.PushBytes` inlines cleanly.
  - Added a safe SIMD multi-branch prefilter, tighter DFA scan loops, and direct zero-revalidation `Replace` in `std.regex`.
  - Split register-only `HuffFast` with inline SIMD bit-refill and match copies in `std.compress`.
  - Added per-carrier `ClassDefault` slot caches, cache-line padding of hot scheduler atomics, Go-style FIFO work-stealing `runq`, direct task-to-task handoff on park, and faster `Chan` wakeups in `std.rt`/`std.task`/`std.core`.
- Compile speed (`internal/driver`, `internal/toolchain`, `internal/mono`, `internal/iface`):
  - Added lazy `std` loading for `mk build`/`run`/`test`/`check` (skipping embedded `std` test files outside `--std` testing), deterministic stratified pack coalescing with largest-first `clang` and ThinLTO backend scheduling, probed `clang -cc1` argv template reuse (`-###`), whole-build fingerprint fast path for warm no-change builds, lock-free `iface.Hasher` dense-ID caches, indexed `AnchorSet` lookups, and overlapped cold-cache prep / import-lib / snapshot work.
- In optimized builds outside `--debug` and `--profile size` on `x86_64` and `aarch64`, both module-private `const` function dispatch tables used via `become` (`become handlers[op](...)`) and direct mutual/self-recursive `become` call cycles automatically emit `preserve_nonecc` handler bodies (`<sym>$pnone`) and companion dispatch tables (`<table_sym>$pnone`) alongside C ABI entry wrappers, keeping hot state arguments in registers across threaded tail-call dispatches with no stack spills.
- `aarch64-linux` and `aarch64-macos`: non-`var` slice parameters (`mut []T`, `str`, and `[]T` of a `Freeze` element type) now emit scoped alias metadata (`!alias.scope`, `!noalias`, and entry-block `llvm.experimental.noalias.scope.decl`) whenever a function takes at least two qualifying slice parameters including a `mut []T` (alongside Win64 C ABI `Indirect` slices and System V `byval` stack-spilled slices). Because AAPCS64 passes 16-byte slices as `[2 x i64]` aggregates where parameter-level `noalias` cannot attach, this lets LLVM eliminate runtime alias checks and vectorize hot slice loops on AArch64.

### Fixed

- The borrow checker now keeps the loans of moved call arguments live in argument slots across the remaining arguments (including reborrows) of the same call, catching aliasing exclusive borrows such as `F(p, mut *p)`, `xs.Sort(fn(a, b) { xs[0] = 1; ... })`, and `LineConnRead(l, io, mut l.rb[..])` (`E0501`), and scopes `allocSelf` on hidden capture environments per capture field so storing a shorter-lived capture loan into a captured `Arena` is rejected (`E0609`).
- Fixed Windows task-slot stack guard initialization (`std.rt`/`std.task`): fresh, grown, and trimmed task slots (and `Start`'s non-task stack) now establish the full 20 KiB `TaskGuarantee + 4096` `PAGE_GUARD` window via `sys.GuardPages` instead of a single 4 KiB guard page, preventing hardware exceptions (including `x86_64-windows` release `TrapForm` `ud1` calls) near the top of a fresh stack from overflowing past a single guard page into uncommitted memory on hosts with larger CET / AVX-512 / AMX exception frames.

## 0.5.0 (2026-11-23)

### Language

- A receiver takes `into` like any parameter: `Names.Keys(&self into arena, arena: &Arena)`, `mut self into p`, `self into p`. Adding or removing the clause on a public method is a breaking change, and `mk apidiff` reports it.

### Loan checker

- A spawned task's clauses hold until its scope joins. A later store into what a task's `*q into p` reads, or into what its `x into p` receives (another task's send, the parent's own `Chan.Send`, a plain call), also reaches `p`. This rejects, with E0505 or E0604, programs 0.4.1 accepted: a task that moves a value out of a channel into a reference that outlives what a later send put there.

The following accept programs 0.4.1 rejected, and reject none it accepted.

- `*a into a` on an arena no longer makes the loans the arena holds by its making (its heap, its parent) a store target, so two calls `Keep(a into o)` then `Tidy(*a into a)` with a local `Arena.New(sys.heap)` check. One call declaring both still chains them into `*a into o` and stays E0505.
- A call that stores a shallow value beside an arena, such as `Keep(a: &Arena into out, names: []str, out: mut List[str]) *names into out` called with `sys.args[1..]`, is accepted, as two sequential calls were.
- A value read back out of a sub-arena holds no loan of the sub-arena's parent, so storing it through a call no longer makes the sub-arena's earlier `Put` reach the parent (E0505). A store into the memory of an arena parameter `a: &Arena` no longer asks for `*a into a` when it stores the arena's own loans.
- The checker's walk over a body's reference types is linear in its depth: a body with 64,000 nested references checks in under a second, where 0.4.1 took minutes.

### Diagnostics

- E0301 for a `mut T` where a `&T` is expected names the shared reborrow `&*p` (`&x` for a loan `mut x`); `mk fix` applies it as the whole operand of `return` in a body with no `defer`, reading through a parameter or a call result.
- E0604 on a store of a receiver's own loan offers `&self into p`, and `mk fix` writes it.
- E0604 at a store into the state a `Ctx` parameter refers to names the field, says to pass it beside the `Ctx`, and gives the clause that then checks (`*c into st`).
- E0604 judged where the walk over a parameter's referents stopped (more than 64 instances of one generic type, or 4,096 referents) names the bound and the parameter.

### Tools

- The soundness generator draws an arena reset under a live reset arena (shape S89, generator version 15), and `MK_SOUND_GENERATED` bounds a rejection sample by the number of programs generated.

## 0.4.1 (2026-11-22)

### Changed

- `str.SplitOnce` panics on an empty separator (code 8), as `str.Split` does. In 0.4.0 it returned `("", self)`.

## 0.4.0 (2026-11-22)

### Standard library

- `str.SplitOnce(sep)` returns borrowed views before and after the first match, or `none` if the separator is absent.

### Platforms

- `aarch64-linux` builds libraries: `pkg.Bin` with `kind: "dynamic"` gives a shared object a glibc host loads, and `kind: "static"` an archive of AArch64 objects, as on `x86_64-linux`.
- `pkg.Link` takes `target: "aarch64-linux"`, static and dynamic. A bin with a dynamic link for `aarch64-linux` is built in system-libc mode: it loads `/lib/ld-linux-aarch64.so.1`, starts through `__libc_start_main` and makes its threads with `pthread_create`. std's `libc.so.6` list there pins glibc's aarch64 versions (`GLIBC_2.17`, and `GLIBC_2.34` for the names glibc 2.34 moved into libc), and the check that C built without `-fPIC` only calls a listed name reads AArch64 relocations.
- In system-libc mode a thread's stack is at least glibc's `PTHREAD_STACK_MIN` (128 KiB on aarch64).

## 0.3.1 (2026-11-21)

### Fixed

- On Windows, a panic, fault or stack-overflow report ends the process with `TerminateProcess` instead of `ExitProcess`, so no DLL detach code runs on the overflowed thread's stack. Before, on CPUs with hardware-enforced stack protection, a stack overflow could end in an access violation instead of exit code 101.
- On Windows, the main thread's stack has its top 64 KiB committed at start instead of one page, so a fault taken near the top of the stack is reported. Before, on CPUs with hardware-enforced stack protection, the larger exception frame ran past the guard page and the process died without a report.

## 0.3.0 (2026-11-21)

### Standard library

- `raw.Select(c, a, b)` is `a` when `c` holds, else `b`: the compiler is told `c` is unpredictable, so for an integer, bool, pointer or record the choice is a conditional move even in a loop whose next load depends on it; a float may still branch on x86_64.
- `Slice.Search` is faster on random keys over a large slice: its loop runs a trip count fixed by the length and picks each half by `raw.Select`, so a probe costs no misprediction. It returns the same index as before on a sorted slice, duplicates included, and makes at most ceil(log2(n + 1)) calls of `less`, as documented.

### Changed

- On Windows a `proc.Pipe` holds 64 KiB unread, as a Linux pipe does by default, instead of one page, so a writer waits for the reader less often.
- `SortStable` now allocates: past 20 elements it merges through scratch memory of `Len() / 2` elements or more (the whole slice up to 8,000,000 bytes), on the stack when that fits in 4,096 bytes and the element is aligned to at most 8, and from the process heap otherwise, freed before it returns. It is Rust's driftsort. When its scratch is larger than the stack's, it falls back to the in-place block merge sort it replaces if the heap is out of memory, at compile time and in a freestanding program. The result is the same: a stable order is unique. `SortStableIn` sorts the same way, with its `Len() / 2` elements of scratch from its allocator, as before.
- `TextMap` keeps a key of up to 15 bytes inside its table instead of copying it into memory from the allocator: such a key costs no allocation, and `Remove` and `Free` have no copy of it to give back. Keys of 16 bytes or more are copied as before. Hashes, iteration order, results and the allocator checks (code 12) are unchanged.
- `hash.Crc32` and `Crc32State` fold 256 bytes at a time with VPCLMULQDQ on 512-bit registers on x86_64 CPUs with AVX-512 and VPCLMULQDQ, from 256 bytes of input; other CPUs keep the 128-bit fold or the table. The checksums are unchanged.
- A heap block over 1 MiB (a growing `HeapList`, `HeapText` or map buffer, or C `realloc` through `std.libc`) grows in place instead of moving: its reservation spans up to 16 times its first pages (at most 16 GiB more per block, 64 GiB for all blocks together), and growth commits more of it. A list that doubles from a huge block's first size grows in place four times before it moves, instead of copying its elements and touching fresh pages at each growth; once the 64 GiB budget is spent, new blocks reserve no spare and move as before. Virtual size grows with it, also for requests aligned over 4 KiB (100 bytes at an alignment of 8 KiB reserve 2 MiB). On Linux, where `RLIMIT_AS` is finite (`ulimit -v`) when the program starts (it is read once, at heap start), a block reserves only its own pages. On Linux such a block is two mappings, one more against `vm.max_map_count`.

### Fixed

- The borrow checker keeps a view's or allocator's loans on its backing buffer or parent allocator across `Reset` (`Arena.Reset` of an arena from `Arena.NewIn` or `Arena.Sub`, `mem.Fixed.Reset`, `mem.FixedHeap.Reset`, `mem.Fallback.Reset`). Before, `Reset` ended them, so a reset allocator could outlive its buffer's frame or its freed parent, or let the buffer be written while it was still in use. Such programs are now rejected (E0501, E0502, E0503, E0504, E0603).

### Performance

- `str.Find`, `str.Has`, `str.Split` and `str.Replace` search for a needle of two bytes or more with a vector prefilter: two bytes of the needle (rare ones on a long text) are compared at 16 starts a step (32 where the CPU has AVX2) and each hit is verified; when near misses make verification stop paying, the search goes on with Two-Way, so it stays linear. Results are unchanged.
- `text.ToUtf16` converts any mix of one- to three-byte scalars up to twelve bytes a step (one table lookup and one byte shuffle), not only runs of one length; mixed-script text no longer goes a scalar at a time.
- `Encoding.Encode` to UTF-16LE and UTF-16BE (`encoding.Utf16Le`, `encoding.Utf16Be`) takes the same steps: sixteen ASCII bytes, or up to twelve bytes of any mix of one- to three-byte scalars, a vector step.
- `x86_64-windows`: outside `--debug`, a Mk function that only Mk calls directly takes and returns a value of 9 to 16 bytes made of integers and pointers (a slice, a `str`, a `?int`) in two registers instead of through memory. `extern` and `export extern` functions, functions used as values (tables and `dyn` included), `asm` `sym` operands, functions a `become` reaches, `naked` functions and functions with a `section` clause keep the platform C ABI; a plain Mk symbol, `export` ones included, is not callable by name from C or assembly.

## 0.2.1 (2026-11-20)

### Platforms

- Releases carry an AArch64 Linux bundle, `mk-<version>-aarch64-linux.tar.gz`, with LLVM's official ARM64 Linux build; `install.sh` and `mk upgrade` pick it on an AArch64 Linux machine.

## 0.2.0 (2026-11-20)

### Targets

- `mk build` builds `aarch64-linux`: static AArch64 executables, cross-compiled from any host, and tested natively on GitHub's ARM runner. System-libc mode and libraries stay x86_64-only. Atomics are always inline (`-mno-outline-atomics`), so a build on an AArch64 Linux host with GCC installed no longer needs libgcc's `__aarch64_*` helpers.
- New preliminary targets `aarch64-macos` and `x86_64-macos`: Mach-O executables for macOS 11 or later, linked by `ld64.lld` against libSystem, with no macOS SDK needed. Never run by the project; parts of `std.sys` answer `SysErr.Unsupported` there ([macOS and AArch64](doc/reference.md#macos-and-aarch64)).
- `asm` works on aarch64, with the register classes `reg` and `vreg`, the `w`, `x`, `b`, `h`, `s`, `d` and `q` template modifiers, and `x18`, `x19`, `x29` and `sp` reserved; E1104 `reason: no-register-table` no longer occurs for aarch64.
- `runtime.AddFaultFilter` works on aarch64 (Linux and macOS) and on x86_64-macos. A `FaultContext` holds the target's registers: on aarch64 `Reg(0)` to `Reg(30)` are x0 to x30 and `Reg(31)` is sp; the new `runtime.RegCount()` says how many there are.

### Fixed

- Linux: processes that share a listener (Supervise's workers, a child given the listener, a listener passed over a Unix socket) take every connection when one of them is killed. On io_uring with Linux 6.10 and later, a connection could stall until the next one arrived.
- Linux: a worker that closes its copy of a shared listener, or a parent that closes its own while workers hold it, no longer stops the listener for the other processes.

## 0.1.0 (2026-11-20)

The first public release of Mk: a small, memory-safe systems language with no garbage collector, compiled by a Go compiler that emits LLVM IR, with a runtime and standard library written in Mk and no C runtime.

### Language

- The language of [the reference](doc/reference.md) and [the memory model](doc/memory.md): ownership, moves, linear values and borrowing checked from signatures alone, with `from` and `into` clauses instead of lifetime parameters.
- Error sets in every signature that can fail, `try`, `fail`, `else` and `onerr`; no exceptions, and integer overflow traps in every build.
- Structs, enums, open sets (`open enum`, `open type E = A | B`), interfaces with explicit `impl`, generics with const parameters, `if` and `match` as values, range patterns, and `for` over iterators with record destructuring.
- Capturing function literals that cannot escape, generic instances as `fn` values, and `become f(args)`, a guaranteed tail call.
- Contracts: `requires`, `ensures` and `invariant`.
- Compile-time evaluation and generators (`gen`, `quote`), which can read the package's data files.
- Structured concurrency: tasks in scopes, channels, cancellation as an error, and deadlines.
- Systems features: `unsafe` blocks with a stated reason, raw pointers, SIMD vectors with `cpu` dispatch, inline `asm`, `naked` functions, `align(n)`, `section` and `extern var`.
- C interop: `import c.<name>` through Clang, `c_sources` linked against `std.libc` (written in Mk), linking DLLs, shared objects and static archives, and building a C-callable dynamic or static library with a generated header.
- A method declared without `export` is private to its module: calling it from another module (`x.M()`, `T.M(..)`, `T.M` as a value, `T.M[..]`, or a `for` loop's `Next`) is E0204, as for a private function. std's `json.Codec`, `csv.Codec`, `cli.Args` and `regex.Matcher` generators, and the web kit's `Body` and `Validate`, export the methods they write when the type is exported. A call from another module to a method a third-party generator writes without `export` does not build either: a generator should write `export` on the methods of an exported type.
- An operator whose operands are all integer literals is E0303 when the run-time operator would panic: `x: u8 = 200 + 100`, `0 - 1` as an unsigned type, a division by a literal zero, a shift past the width. The check does not depend on whether the code runs. The wrapping operators are unaffected, and initializers evaluated at compile time report E0901.
- The constant folder reports the type's minimum divided by -1, with `/` or `%`, as `divide by zero` (E0901), as the compile-time evaluator does.
- `fuel Limit` with a named const uses that const's value; any other fuel expression is E0705. `unsafe " "` (a reason of only white space) is E0101, as `unsafe ""` is.
- A variant, `some`, `ok` or `err` pattern on the left of `:=` is E0306 (a type parameter or primitive there is E0301); a same-module const that reads a same-module layout with `SizeOf` or `AlignOf`, named in a type, is E0908; a field of an indexed named const (`S[0].a`) as an array length is E0705, as `P.a` is.
- A store of a static through a parameter's referents is E0604 past the loan checker's bound too: also when the referents include more than 64 instances of one generic type, or more than 64 records holding distinct instances of one.

### Library

- A runtime written in Mk: the heap and arenas, a work-stealing M:N scheduler on IOCP (Windows) and io_uring with an epoll fallback (Linux), timers, limits, pinned threads, and panic reports with backtraces.
- A panic's backtrace shows the program's frames and std's, ending at Main, at the task's function or at the test (`test "name"`): the runtime's start, its support routines (printed as raw addresses) and the compiler's wrapper frames (`Main#report`, the test runner's Main) are left out.
- A dev build prints the error-return trace of an error that leaves Main, also in a program without tasks that never uses `sys.io`.
- The prelude: strings, slices, `List`, `Map`, `Arena`, `Io`, `System` and channels.
- Files, processes and pseudo-terminals, networking (TCP, UDP, Unix sockets, socket options), an HTTP/1.1 client and server, WebSocket, and TLS 1.3 (with a TLS 1.2 client) and x509, both Experimental.
- std.net: `Dial` resolves a host name. On Linux it reads `/etc/hosts`, then the nameservers, and dials each address in order, failing with the first one's error; on Windows it dials the system's first IPv4 answer, else its first IPv6 one. `Dial` of a host name that does not resolve fails with `InvalidAddress`, as `Resolve` does (on Linux, on Windows and under std.sim).
- std.http: a chunked request body's buffer doubles as it grows, so its arena use is linear in the body, not quadratic in the chunk count, and one-byte chunks cannot use far more than `body_bytes`.
- JSON, CSV, regex, text and Unicode, time with time zones, math, big numbers, URLs with IDNA, encodings with DER and PEM, compression (deflate, gzip, zlib, zstd decoding), hashes and crypto primitives (HMAC, HKDF, AES-GCM).
- `str.ToLower` and `str.ToUpper` follow Unicode 18.0.0, the version of std.text's and regex's tables, including the case mappings of U+0277 and U+A7DD, U+AB4B and U+AB6C, U+1DF40 and U+1DF41 and the other scalars Unicode 18.0.0 gave case mappings. `White_Space` is generated from UCD 18.0.0 too.
- Collections, sync primitives and atomics, SIMD routines, logging, command-line parsing, the terminal, embedded SQLite (`std.db.sqlite`), and `std.dl` for loading libraries at run time.
- std.term: on a Windows console, and under std.sim on Windows, Ctrl+Space (a NUL key) reads as `Char(' ')` with ctrl.
- Allocators in `std.mem`. `FixedHeap` is a general-purpose allocator over a caller's buffer: any block is freed and reused, and an allocation, a resize and a free are each O(1). `Fallback` serves from a buffer while requests fit it and from another allocator once they do not. `Limit` refuses what would take the bytes live through another allocator past a limit. `Failing` fails the requests a test picks by number, to reach the code that handles `OutOfMemory`.
- `std.sim`: simulated `Io` with a seeded scheduler, and record and replay at the `Io` boundary.
- `std.testing`: fuzz targets and benchmarks.
- `kits/web`, a first-party web framework: routing through plain middleware functions, path and query parameters, JSON bodies and generated validation.
- [Stability levels](doc/std/README.md#9-stability) (Stable, Provisional, Experimental) on every standard library module.

### Tools

- `mk build`, `run`, `check` and `test` (with `--sim`, `--prop`, `--fuzz` and `--cover`), `bench`, `fmt`, `fix`, `explain`, `query`, `doc`, `apidiff`, `cimport`, `repl`, `cache` and `profile merge`.
- Packages: manifests in Mk (`pkg.mk`), path, URL and git dependencies chosen by minimal version selection, `pkg.sum`, workspaces, `replace`, test-only dependencies, grants for a dependency's unsafe code, `internal` modules, vendoring, and `mk new`, `install`, `remove`, `update`, `tree` and `why`.
- Build profiles: dev, release, `--profile size` and `--profile checked`; incremental, deterministic and reproducible builds; profile-guided optimization.
- Every diagnostic has a code; `mk explain` explains it, and `mk fix` applies the fixes that are certain.
- Diagnostics for other languages' habits name the Mk form: a float written `1.` or `.5`; `G<int>(x)`; `Point{x: 1}` and Zig's `Point{ .x = 1 }`; Go's `F() int`; `c ? a : b`; `v, ok := m[k]`; `x, err := F()`; Zig's `.{x}`; `dyn I`. `.unwrap()`, `.expect()`, `.clone()` and `.to_string()` are E1221 naming the Mk form, and `mk fix` renames `.unwrap()` to `.Must()`.
- E0303 names `Trunc` only for a conversion between two integers, and E0705 at a run-time array length prints no empty `param` fact.
- `mk check`, `mk fix` and `mk lsp` recompile a proven `into` or `from` clause, following the callers' own clauses when the error moves up. A clause whose chain ends in another error (an E0505 or E0503 no clause clears), or which was not recompiled within the bound, is offered as an alternative naming that error, and `mk fix` never writes it.
- `mk check` handles a polymorphically recursive type whose recursion goes through a record (`next: ?&(a: G[?T], b: int)`) without taking minutes.
- `mk build -o dir/`, or `-o` naming an existing directory, writes `dir/<bin>[.exe]` for each bin. `mk doc std.fs` documents std's module, as `mk doc --std std.fs` does, and an unknown `std.` module is an error. `mk test --fuzz` output agrees in number ("1 input saved").
- `mk query find unwrap` lists `?T.Must` and `E!T.Must`; `mk query members Eq` (Hash, Format) answers the prelude interface; in a package that does not parse, `find` and `members` answer std's names with a note.
- `mk lsp`, a language server, and a VS Code extension.

### Platforms

- x86_64 Windows, x86_64 Linux (static executables) and x86_64 freestanding.
- `--target` with a target std has no system layer for (such as `aarch64-macos`) is refused with one E0202, `reason: no-std-target`, naming the targets `mk` builds for and those it only checks.
- LLVM 21 or newer (`clang` and `lld`) and, to build `mk` from source, Go 1.27 or newer.

### Known issues

- `--cover` counts a block on its first statement's line, and a function that nothing reaches has no row, so totals cover the reached functions only.
- A static Mk library linked into a host DLL or `.so` treats that host library's image as its own when it decides whether a fault is its own.
- A static Mk library linked into a host that uses MSVC's C runtime reports duplicate `memcpy` and `memset` symbols.
- `std.json`'s parser reaches the stack limit before 512 levels of nesting in a dev build. Parse untrusted text in a release build, or bound its depth.
- A test dependency that depends back on the package that tests it is E0202 `cycle`.
- The symbol-clash check reads C sources without the preprocessor, so a definition inside a disabled `#if` block can cause a false clash.
- Not supported: object outputs, Windows resources, a system-libc dynamic link in a Linux library, two Mk libraries in one process, weak symbols, aliases and delay-loading.
