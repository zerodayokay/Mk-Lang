# std.libc

`std.libc` is the part of the C library that C code linked into a Mk program can call. It holds the string and memory functions and `malloc` and its family, written in Mk and exported under their C names. Mk links against no C runtime, so a package's C sources (`c_sources`) and static archives get these functions from `std.libc` instead. You do not call them from Mk; you write C that calls them, and the build supplies them.

Read the [reference's section on unsafe code and C](../reference.md#14-unsafe-and-c) for `extern` functions and C imports, and [packages](../packages.md#2-the-manifest) for the manifest fields.

## Contents

- [The functions](#the-functions)
- [C sources and archives](#c-sources-and-archives)
- [System-libc mode on Linux](#system-libc-mode-on-linux)
- [Examples](#examples): [a C source that allocates](#a-c-source-that-allocates)
- [API](#api)

## The functions

| Header | Declares |
|---|---|
| `string.h` | `memchr`, `strlen`, `strchr`, `strrchr`, `strcmp`, `strncmp`, `strspn`, `strcspn`, and `memcpy`, `memmove`, `memset` and `memcmp`, which every Mk executable already contains |
| `stdlib.h` | `malloc`, `calloc`, `realloc`, `free`, glibc's `malloc_usable_size`, and `__C_specific_handler`, which C's `__try`, `__except` and `__finally` need on Windows; it hands each call to ntdll's |
| `math.h` | `fabs`, and the macros `INFINITY`, `NAN` and `HUGE_VAL` |
| `assert.h` | `assert`, which traps when it fails, since there is no `abort`; and `static_assert` |
| `stdio.h` | `FILENAME_MAX` only |
| `ctype.h`, `time.h` | Nothing: they exist so that C code which includes them compiles |

These are the functions sqlite needs when built with `SQLITE_OS_OTHER` ([std.db.sqlite](sqlite.md)). The build writes these headers from the functions' Mk signatures, with the C standard's types where they match. It puts them on the C sources' system include path, so a C source includes them as usual. A C function that is not listed is not there: a call to it fails at link (E0001).

- The allocator works over the process `Heap`. Every block is 16-aligned, `malloc(0)` returns a unique block, and `calloc` returns null when `count * size` overflows. Each block costs 16 bytes more than its size.
- Every allocation failure is a null pointer. The allocator never panics, since a panic under a C frame aborts the process.
- `memchr` is safe with a length that runs past the object when the object holds the byte.
- `strcmp` and `strncmp` compare bytes as `unsigned char`, as C requires. `fabs` clears the sign bit, so it also clears the sign of `-0` and of a negative NaN.

## C sources and archives

Outside system-libc mode, C sources and static archives link against `std.libc` and the bin's import libraries, and nothing else.

- A C source is compiled by Clang in the build's own directory, with the package root on its include path: `#include "include/x.h"` finds `<package>/include/x.h`. A relative `-I` in `CSource.flags` names a directory under the build's directory, not the package's.
- A static archive must be built with no C runtime: `-ffreestanding -fno-stack-protector` for Clang or GCC, `/GS- /Zl` for MSVC.
- A symbol that C code needs and nothing supplies is E0001, naming the archive, its member and the symbol. So is an archive member that the link pulls in and that holds static constructors or thread-local storage, which a Mk program never sets up.
- An import library (`.lib` for a DLL) is not an archive (E0202).

## System-libc mode on Linux

An executable for `x86_64-linux` or `aarch64-linux` enters system-libc mode when its package graph declares a dynamic link for that target, or when its compiled code needs a glibc function from `c.posix`. The POSIX view supplies a pinned ELF symbol version after the program's explicit links and definitions have had their turn. Type references and calls outside the executable's reachable code leave its linkage unchanged.

In this mode the executable loads the system's dynamic loader (`/lib64/ld-linux-x86-64.so.2` or `/lib/ld-linux-aarch64.so.1`) and links `libc.so.6` and the named `.so` files. A Linux executable with neither trigger is static. [std.dl](dl.md#linux) shows the manifest of a program that needs only `std.dl`.

This Linux package prints its parent process ID. Its `pkg.mk` grants `app`
permission to use `unsafe`.

```mk
// verdict: parse
const Package = pkg.Package(
    name: "posix-demo",
    bins: [pkg.Bin(name: "posix-demo", root: "app", subsystem: "console")],
    unsafe_modules: ["app"],
)
```

Save this as `app/main.mk`.

```mk
// verdict: parse
import c.posix

export Main(sys: &System): WriteError!void {
    var parent: i32 = 0
    unsafe "getppid takes no arguments and returns the parent process ID" {
        parent = posix.getppid()
    }
    try sys.Print("{parent}\n", (parent))
}
```

In system-libc mode:

- `std.libc` is not linked, and a bin that reaches it is E0001. C sources and archives use the system's C library, so all C code shares one allocator.
- The system stub includes std's `libc.so.6` list and the reached glibc functions of `c.posix`. For additional functions, supply a link whose `.def` names them. For example, `fabs` belongs to `libm.so.6`.
- The view records default symbol versions through `GLIBC_2.34`. The generator omits function declarations redirected to a different ABI symbol, such as `strerror_r` and `scanf`.
- mk links against a stub made from each link's list of names, so it builds from any host. A `.def` name with `@VERSION` binds that version. A name without one binds whichever version the loader picks, which for glibc is the oldest, so a list should give a version for every name. Versions differ by architecture: glibc's oldest is `GLIBC_2.2.5` on x86_64 and `GLIBC_2.17` on aarch64, so each target takes its own link and list. The oldest supported glibc is 2.34.
- The program starts through `__libc_start_main`, and its threads are made with `pthread_create`. std keeps its own system calls for I/O, signals (never signals 32 and 33), processes and exit. So C `atexit` handlers do not run, and C stdio buffers are not flushed, at exit.
- The executable exports nothing. A name that a link's list supplies and the executable also defines is E0001. Such names are the compiler's `memcpy`, `memmove`, `memset`, `memcmp` and `bcmp`, an `export extern` function, or a function of a C source.
- A list cannot say whether a name is a function or data. So C built without `-fPIC` may only call a listed name, and taking its address there is E0001. Position-independent C, Clang's default, reaches a listed data object through the GOT.
- Read `errno` before anything that may wait, since the task may then move to another thread. If a library needs to be called from one thread, pin the code that calls it yourself ([std.runtime](runtime.md#threads-of-their-own)).

## Examples

### A C source that allocates

The package has a manifest, one C file and the Mk program.

```mk
// verdict: parse
const Package = pkg.Package(
    name: "words",
    bins: [pkg.Bin(name: "words", root: "src", subsystem: "console")],
    unsafe_modules: ["src"],
    c_sources: [pkg.CSource(path: "c/words.c")],
)
```

`c/words.c` calls `strspn`, `strcspn`, `strlen`, `memcpy`, `malloc` and `free`, all from `std.libc`:

```c
#include <stdlib.h>
#include <string.h>

/* longest_word returns a copy of the longest space-separated word of s,
   in a block from malloc, and its length in *len; NULL when out of
   memory. The caller frees it with release. */
char *longest_word(const char *s, size_t *len) {
    size_t best = 0;
    const char *best_at = s;
    while (*s) {
        s += strspn(s, " ");
        size_t n = strcspn(s, " ");
        if (n > best) { best = n; best_at = s; }
        s += n;
    }
    char *copy = malloc(best + 1);
    if (copy) {
        memcpy(copy, best_at, best);
        copy[best] = 0;
        *len = strlen(copy);
    }
    return copy;
}

void release(char *p) { free(p); }
```

`src/main.mk` declares the two C functions and calls them:

```mk
// verdict: ok
import std.raw

// The C functions of c/words.c.
extern longest_word(s: *u8, len: *uint): *u8
extern release(p: *u8)

export Main(sys: &System): WriteError!void {
    line := "the quick brown fox jumps\x00" // C wants a NUL at the end
    unsafe "line ends in a NUL; longest_word writes len and returns a block or null" {
        var len: uint = 0
        p := longest_word(raw.StrData(line), raw.OfMut(mut len))
        if raw.IsNull(p) {
            try sys.Print("out of memory\n")
            return
        }
        word := raw.Str(p, Cast[int](len)) // ASCII, so valid UTF-8
        try sys.Print("longest word: {word}\n", (word))
        release(p) // free, through std.libc's allocator
    }
}
```

```text
$ mk run .
longest word: quick
```

- The Mk program does not import `std.libc`: the build links it because the package has C sources.
- `malloc`'s block lives on the program's `Heap`, so C and Mk share one heap.

## API

<!-- api:begin -->
Stability: Stable.

### `__C_specific_handler`

```
export extern __C_specific_handler(rec: *u8, frame: *u8, ctx: *u8, dc: *u8): i32
```

The Windows C language handler for __try, __except and __finally, which
C code built for Windows calls. It runs ntdll's handler, looked up at
each call, with the dispatcher's four arguments, and answers
continue-search when ntdll has none. O(1) besides ntdll's own scope
walk; allocates nothing.

### `calloc`

```
export extern calloc(count: uint, size: uint): *u8
```

C's calloc: count * size zeroed bytes, or null when the product
overflows or the heap has no room. It zeroes the count * size bytes and
allocates that many plus 16 from the process Heap.

### `fabs`

```
export extern fabs(x: f64): f64
```

C's fabs: x with its sign bit cleared, so -0 and -NaN lose their sign
too. O(1); allocates nothing.

### `free`

```
export extern free(p: *u8)
```

C's free: gives the block back to the process Heap. A null p does
nothing.

### `malloc`

```
export extern malloc(n: uint): *u8
```

C's malloc: a 16-aligned block of n bytes, or null when the heap has no
room. malloc(0) gives a unique block. It never panics; it allocates
n + 16 bytes from the process Heap.

### `malloc_usable_size`

```
export extern malloc_usable_size(p: *u8): uint
```

glibc's malloc_usable_size: the bytes the block at p may hold, 0 for
null. Here it is the size asked for. O(1); allocates nothing.

### `memchr`

```
export extern memchr(s: *u8, c: i32, n: uint): *u8
```

C's memchr: the first byte equal to the low byte of c among the n bytes
at s, or null. An n that runs past the object is safe when the object
holds the byte. O(n); allocates nothing.

### `realloc`

```
export extern realloc(p: *u8, n: uint): *u8
```

C's realloc: with a null p it is malloc. The block grows or shrinks in
place when the heap allows it; else the bytes move to a new block and
the old one is freed. On failure the old block is untouched and null is
returned. It copies the bytes kept when they move, and then allocates
n + 16 bytes.

### `strchr`

```
export extern strchr(s: *u8, c: i32): *u8
```

C's strchr: the first byte equal to the low byte of c, the NUL itself
when c is 0, or null. O(n); allocates nothing.

### `strcmp`

```
export extern strcmp(a: *u8, b: *u8): i32
```

C's strcmp: compares two NUL-terminated strings byte by byte, as
unsigned char, as C requires. O(n); allocates nothing.

### `strcspn`

```
export extern strcspn(s: *u8, set: *u8): uint
```

C's strcspn: the length of the prefix of s with no byte of set.
O(len(s) + len(set)); allocates nothing.

### `strlen`

```
export extern strlen(s: *u8): uint
```

C's strlen: the number of bytes before the first NUL. O(n); allocates
nothing.

### `strncmp`

```
export extern strncmp(a: *u8, b: *u8, n: uint): i32
```

C's strncmp: compares at most n bytes, stopping at a NUL. O(n);
allocates nothing.

### `strrchr`

```
export extern strrchr(s: *u8, c: i32): *u8
```

C's strrchr: the last byte equal to the low byte of c, the NUL itself
when c is 0, or null. O(n); allocates nothing.

### `strspn`

```
export extern strspn(s: *u8, set: *u8): uint
```

C's strspn: the length of the prefix of s made of bytes in set.
O(len(s) + len(set)); allocates nothing.

<!-- api:end -->
