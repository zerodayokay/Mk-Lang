---
description: Modules as directories, export for visibility, import paths, packages and the pkg.mk manifest, the prelude, and dependencies.
---

# Modules and packages

## Modules

A module is a directory of `.mk` files. Every file in it sees every declaration in the others. Everything is private to its module unless it is marked `export`, and that includes struct fields.

## Packages

A package is a directory with a `pkg.mk` manifest. `mk new hello` makes `pkg.mk` and `src/main.mk`. Add a directory and you have a second module:

```text
hello/
  pkg.mk
  src/
    main.mk
    geom/
      geom.mk
```

The manifest is itself Mk: a constant built at compile time.

```mk
// verdict: parse
const Package = pkg.Package(
    name: "hello",
    mk: "0.6",
    bins: [pkg.Bin(name: "hello", root: "src", subsystem: "console")],
)
```

## Imports

A module's import path is its directory relative to the package root, with `.` for `/`, so `src/geom` is `import src.geom`. `src/geom/geom.mk` could hold:

```mk
// verdict: ok
export struct Vec2 {
    export x: f64
    export y: f64
}

export Vec2.Dot(&self, o: &Vec2): f64 {
    return self.x * o.x + self.y * o.y
}
```

and `src/main.mk` uses it through the last segment of its path:

```mk
// verdict: parse
import src.geom

export Main(sys: &System): WriteError!void {
    v := geom.Vec2(x: 3.0, y: 4.0)
    try sys.Print("{d}\n", (d: v.Dot(&v))) // prints 25
}
```

- Each file imports what it uses, and an unused import is an error.
- Names stay qualified: `geom.Vec2`, `fs.ReadText`. There is no `import *`.
- The standard library's modules are `std.<name>`: `import std.fs`, `import std.json`.
- A small prelude needs no import: `List`, `Map`, `Text`, `Arena`, `Io`, `Print`, the common errors and the other names every program uses ([std.core](/reference/std/core)).
- An error named in an exported signature must be exported itself (E0412).

[Imports](/reference/language/modules-and-build#imports) and [exports and visibility](/reference/language/modules-and-build#exports-and-visibility) have the rules.

## Dependencies

Dependencies are other packages, named in the manifest by path, archive or repository, with a version:

```sh
mk install <name or source>   # add a dependency
mk update                     # raise requirements to the newest compatible version
mk tree                       # print the build list
mk vendor                     # copy every dependency into vendor/
```

`pkg.sum` records the exact versions and hashes, and `--locked` refuses anything it does not list. [Packages](/tools/packages) covers manifests, versions, the lock file and workspaces.

## Larger programs

How to split a program into modules, pass capabilities together in a context struct, and grow a library without breaking its users is in [organising a codebase](./codebase).

## Next

[Testing and contracts](./testing).
