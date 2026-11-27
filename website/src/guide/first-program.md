---
description: Build wordfreq, a small command-line tool that reads a file and prints its most common words, one step at a time.
---

# Your first program

This page builds `wordfreq`, a tool that reads a text file and prints its three most common words. It takes three steps, and each one is a complete program you can run. Along the way you meet arguments, exit statuses, files, errors, maps, structs and sorting.

Make a directory for it and save this text as `poem.txt`:

```text poem.txt
the cat and the dog
saw the bird in the tree
```

## Step 1: read the arguments

A tool needs a file name. `sys.args` holds the arguments; `sys.args[0]` is the program's own path, so one argument means `Len()` is 2. Save this as `step1.mk`:

```mk
// verdict: ok
export Main(sys: &System): int {
    if sys.args.Len() != 2 {
        sys.PrintErr("usage: wordfreq FILE\n") else {}
        return 2
    }
    path := sys.args[1]
    sys.Print("counting words in {path}\n", (path)) else {}
    return 0
}
```

```text
$ mk run step1.mk
usage: wordfreq FILE
$ echo $?
2
$ mk run step1.mk -- poem.txt
counting words in poem.txt
```

- `Main` returns `int` here, and the value is the process's exit status. Status 2 is the usual signal for a usage error.
- `Print` and `PrintErr` can fail, because writing can fail. `else {}` handles a failure by doing nothing. In a `Main` that returns `int` there is no error to pass up, so the failure has to be handled here.
- `{path}` in the format string is a *hole*. The record `(path)` fills it: `(path)` is short for `(path: path)`, and the compiler checks that every hole has a field and every field a hole.

## Step 2: read the file

`std.fs` reads files. `fs.ReadText` reads a whole file into memory, so it needs somewhere to put the text: an arena. `sys.arena` lives as long as the program. Save this as `step2.mk`:

```mk
// verdict: ok
import std.fs

export Main(sys: &System): fs.Error|OutOfMemory|WriteError!void {
    if sys.args.Len() != 2 {
        try sys.PrintErr("usage: wordfreq FILE\n")
        return
    }
    text := try fs.ReadText(sys.arena, sys.io, sys.args[1])
    var lines := 0
    var words := 0
    for line in text.Lines() {
        lines += 1
        for _ in line.Words() {
            words += 1
        }
    }
    try sys.Print("{lines} lines, {words} words\n", (lines, words))
}
```

```text
$ mk run step2.mk -- poem.txt
2 lines, 11 words
```

- `fs.Error|OutOfMemory|WriteError!void` is the list of everything `Main` can fail with. Reading can fail with a file-system error or run out of memory, and printing can fail to write. The compiler checks that the list covers every `try` in the body.
- `try` passes an error up. Out of `Main`, an error is printed with the path it took, and the process exits with status 1.
- `fs.ReadText` takes the arena `sys.arena` and the I/O handle `sys.io`. A function can allocate or do I/O only through what its parameters give it.
- `Lines()` and `Words()` give views into `text`; nothing is copied.
- `lines` and `words` change, so they are `var`. `for _ in` ignores each word, because every local must be used.

Run it on a file that does not exist and the error leaves `Main`:

```text
$ mk run step2.mk -- missing.txt
error: fs.NotFound
    at std/fs/fs.mk:18:21
    at std/fs/fs.mk:46:10
    at std/fs/fs.mk:71:10
    at std/fs/fs.mk:98:10
    at step2.mk:8:13
$ echo $?
1
```

The trace is the path of `try`s the error took, which a development build records. A release build prints only the error.

## Step 3: count each word

To count each word, keep a `Map` from word to count. To print the most common first, copy the counts into a `List` of structs and sort it. Save this as `wordfreq.mk`:

```mk
// verdict: ok
import std.fs

struct Entry {
    word: str
    count: int
}

/// ByCount orders entries by count, highest first, then by word.
ByCount(a: &Entry, b: &Entry): bool {
    if a.count != b.count {
        return a.count > b.count
    }
    return a.word < b.word
}

/// Count returns each distinct word of text with its count, most common first.
Count(a: &Arena, text: str into a): OutOfMemory!List[Entry] from a, text {
    var counts := Map[str, int].New(a)
    for word in text.Words() {
        n := try counts.Ensure(word, 0)
        *n += 1
    }
    var out := List[Entry].New(a)
    for (key, value) in counts.Iter() {
        try out.Push(Entry(word: *key, count: *value))
    }
    out.Sort(ByCount)
    return out
}

export Main(sys: &System): OutOfMemory|WriteError!int {
    if sys.args.Len() != 2 {
        try sys.PrintErr("usage: wordfreq FILE\n")
        return 2
    }
    path := sys.args[1]
    text := fs.ReadText(sys.arena, sys.io, path) else e {
        try sys.PrintErr("wordfreq: cannot read {path}: {e}\n", (path, e))
        return 1
    }
    entries := try Count(sys.arena, text)
    for i, entry in entries {
        if i == 3 {
            break
        }
        try sys.Print("{count:>3} {word}\n", (count: entry.count, word: entry.word))
    }
    return 0
}
```

```text
$ mk run wordfreq.mk -- poem.txt
  4 the
  1 and
  1 bird
$ mk run wordfreq.mk -- missing.txt
wordfreq: cannot read missing.txt: NotFound
$ echo $?
1
```

What is new in this step:

- **A struct.** `Entry` groups a word and its count. `Entry(word: ..., count: ...)` builds one, naming every field.
- **A map.** `Map[str, int].New(a)` makes a hash map in the arena. `Ensure` returns a reference to the count for a word, inserting 0 first if the word is new. `*n += 1` changes the count through that reference.
- **Destructuring by name.** `counts.Iter()` yields records `(key: &str, value: &int)`, and `for (key, value) in` takes them apart *by field name*, not by position. Writing `(word, count)` there is error E0309: the record has no field `word`.
- **Sorting.** `out.Sort(ByCount)` takes a function that says whether `a` goes before `b`. Breaking ties by word makes the order the same on every run, since a map has no fixed order.
- **Signatures that say where data lives.** The words in the result are views into `text`, stored in lists in the arena. `text: str into a` says `Count` stores parts of `text` in something made from `a`. `from a, text` says the result borrows from both. The compiler checks the body against these clauses, and checks each caller against the signature alone.
- **Handling an error where it happens.** `fs.ReadText(...) else e { ... }` runs the block when the call fails, with `e` bound to the error. The block returns from `Main`, so `fs.Error` no longer appears in `Main`'s error list.
- **A format spec.** `{count:>3}` right-aligns the count in three columns ([formatting](./strings#formatting)).

## Build it

```sh
mk build --release wordfreq.mk
./wordfreq poem.txt
```

To grow it into a package with tests, run `mk new wordfreq` and move the code into `src/main.mk` ([modules and packages](./modules), [testing](./testing)).

## Where to go from here

- The fundamentals, starting with [syntax and structure](./syntax).
- The [cookbook](/examples/cookbook): short programs for files, maps, tasks, HTTP and more. Its [count words recipe](/examples/cookbook#count-words-and-print-the-ten-most-common) is a close cousin of this program.
- [Memory and ownership](./memory), to understand `from` and `into`.
