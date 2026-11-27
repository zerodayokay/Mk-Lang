---
layout: home
title: Mk · Memory-safe systems programming without a garbage collector
titleTemplate: false
description: Mk is a small compiled language for memory-safe systems programming without a garbage collector. The compiler checks ownership and borrowing with no lifetime annotations.

hero:
  name: Mk
  text: Memory-safe systems programming without a garbage collector
  tagline: A small compiled language for tools, servers, runtimes and kernels. The compiler checks ownership and borrowing, with no lifetime annotations.
  actions:
    - theme: brand
      text: Get started
      link: /guide/installation
    - theme: alt
      text: Quick start
      link: /guide/quick-start
    - theme: alt
      text: Language reference
      link: /reference/

features:
  - title: Small
    details: The whole language, borrowing rules included, fits in your head. A feature gets in only if it pays for itself.
  - title: Explicit
    details: Allocation, errors, I/O and borrowing show in signatures. You never read a callee's body to know whether it allocates, fails or keeps a reference.
  - title: One way to write each thing
    details: No macros, no inheritance, no implicit conversions, no hidden control flow.
  - title: Errors that lead to the fix
    details: Every diagnostic has a code. mk explain explains it, and mk fix applies the fixes that are certain.
  - title: Structured concurrency
    details: Tasks run in scopes that wait for them. Two tasks cannot hold exclusive loans on one place, so a data race does not compile.
  - title: Checked in every build
    details: Overflow, out-of-bounds indexing and division by zero panic in release builds too, unless you ask for wrapping arithmetic.
---

<div class="mk-home">

<section>

## A taste of Mk

<p class="mk-lede"><code>Long</code> returns the words of <code>text</code> longer than <code>n</code> bytes, in a list allocated in the arena <code>a</code>.</p>

<div class="mk-taste">
<div class="vp-doc">

```mk
// verdict: ok
Long(a: &Arena, text: str into a, n: int): OutOfMemory!List[str] from a, text {
    var out := List[str].New(a)
    for word in text.Words() {
        if word.Len() > n {
            try out.Push(word)
        }
    }
    return out
}

export Main(sys: &System): OutOfMemory|WriteError!void {
    var arena := try Arena.New(sys.heap) // freed in one step at the end
    defer arena.Free()
    words := try Long(&arena, "the quick brown fox jumps over the lazy dog", 4)
    for w in words {
        try sys.Print("{w}\n", (w: w))
    }
}
```

```text
quick
brown
jumps
```

</div>
<div class="mk-taste-notes">

`OutOfMemory!List[str]` is the signature's promise: `Long` fails only with `OutOfMemory`, and `try` passes the error up.

`from a, text` says the result borrows from the arena and the text. `into a` lets the body store words of `text` in the arena's list.

The compiler checks the body and every caller against the signature. Free the arena while something still borrows from it, and the program does not compile. [See how →](/guide/memory#borrowing)

</div>
</div>

</section>

<section>

## Where to go next

<div class="mk-paths">
  <a href="./guide/"><strong>What is Mk?</strong><span>The design in a few minutes, and what it means for the code you write.</span></a>
  <a href="./guide/first-program"><strong>Your first program</strong><span>Build a small word counter, one step at a time.</span></a>
  <a href="./reference/memory"><strong>Memory model</strong><span>Ownership, moves and borrowing, rule by rule.</span></a>
  <a href="./reference/std/"><strong>Standard library</strong><span>Files, networking, HTTP, JSON, text, time and more.</span></a>
</div>

</section>

</div>
