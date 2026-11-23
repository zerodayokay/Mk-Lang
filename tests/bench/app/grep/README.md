# app/grep: count matching lines over a source tree

An `app` row (tests/bench/README.md "Applications"). The Mk side is the mgrep dogfood, unchanged (`Dogfoods/mgrep`, its README and `src/walk`, `src/search` and `src/work`). The row has three variants, `re`, `lit` and `ci`, which differ only in the pattern. Its parallel form, mgrep at `-j 8`, is the concurrency row `app/grep-j8`. Every twin below is checked against this file before its number counts.

## The task

- **Command line:** `grep -c -j 1 --path-separator / FLAG PATTERN DIR`, mgrep's own options, which every twin accepts (and no others):
  - `re`: `-i 'n[aeiou]+dle|omega|s?igma'`, the dogfood's own benchmark pattern: an alternation with a class and an optional letter, case-folded;
  - `lit`: `-F needle`, one literal;
  - `ci`: `-i needle`, one literal, case-folded.
  tools/bench runs it with `{data}` as `DIR`.
- **Input:** `tests/bench/data/grep-tree-100mb/`, from `tools/bench gen` kind `tree` (seed 5, bytes 100,000,000; tools/bench/gen_app.go states the kind): 18,228 files in 2,399 directories, names under eight extensions, hidden names, capitalised names, binary files with NUL bytes, CR LF files, files with no final newline, empty files, a few large files, and keywords and UTF-8 lines in the text. The same bytes for every language. The size is provisional: `verify -publish-size` on a quiet machine fixes it.
- **The work, every step:**
  1. **Walk** `DIR` as mgrep does: list each directory, drop the names that start with `.` (files and directories), sort the rest by their bytes, and visit them in that order, depth first, files and directories interleaved (a directory's files and subdirectories are not grouped). There is no depth limit and no glob. The tree has no `.mgrepignore` file (gen never writes one). mgrep still looks for one in every directory it walks (`Ignored` in `src/walk/walk.mk`, an `fs.ReadText` of `.mgrepignore`): 2,014 failed opens on this tree, the root and the 2,013 directories outside hidden names (counted by a `treestats` probe). No twin looks, since none would find one; that cost is mgrep's own (below).
  2. **Read** each file whole.
  3. **Skip binary files:** a file with a NUL byte in its first 1,024 bytes prints nothing.
  4. **Split lines** at `\n`; a `\r` before it is not part of the line, and a last line with no newline is a line.
  5. **Judge lines as UTF-8:** a file that is not valid UTF-8 is judged line by line, and a line that is not valid UTF-8 is skipped (never counted). So every cell validates each file.
  6. **Match:**
     - `-F`, or a pattern with no regex meta byte (`\ . + * ? ( ) | [ ] { } ^ $`), is a literal;
     - `-i` on an ASCII literal folds ASCII case only (`K`, U+212A KELVIN SIGN, does not match `k`), as mgrep's literal search does (`src/search/matcher.mk`);
     - any other pattern is a regex, and `-i` on it is the regex library's `(?i)`, Unicode simple case folding.
     The row's patterns match no line break, so a cell may search a file's bytes whole and count the lines its matches fall in, or judge each line.
  7. **Print** `PATH:COUNT` and a newline for every file not skipped, in walk order, `COUNT` being the number of its lines that hold a match, `0` included. `PATH` is `DIR` with each `\` written as `/`, then `/` and each name below it.
  8. Exit 0 when any line matched, 1 when none did, 2 on an error.
- **Output:** 14,967 lines, one per file searched (15,201 files outside hidden names, 234 of them binary).

## The check

`"digest": true`: the checksum is the SHA-256 of the standard output, CR LF read as LF, with the tree's path (in either slash form) written back as `$DATA`. Every column must print the same paths in the same order with the same counts. The tree has no line that is not UTF-8 and no `.mgrepignore`: `mgrep --stats` over it printed nothing on standard error and "binary files skipped: 234".

## The cells

| Language | Kind | Program | Libraries |
|---|---|---|---|
| Mk | `std` | mgrep (`Dogfoods/mgrep`, unchanged), `-j 1`: `fs.Dir` and `fs.Stat`, entries sorted by `simd.Compare`; `fs.ReadBytes` for a file up to 64 KiB, else `File.ReadAt` of 1,024 bytes to sniff, then `File.Reader`; `str.From` validates the file; a literal by `simd.IndexByte` (or `simd.IndexAny` over both cases of the first byte for `-i`) and `simd.Equal`, the newlines skipped counted by `simd.Count`; a regex by `std.regex`'s `IsMatch` on each line; one search task, with one arena reset after each file | std only |
| C | `hand` | `grep.c`, `lit` only: the walk on the platform API, not ISO C (Win32 `FindFirstFileExW`, with `FindExInfoBasic` and the performance flag `FIND_FIRST_EX_LARGE_FETCH` that Rust std's `read_dir` leaves out, and `FindNextFileW`, with names converted by `WideCharToMultiByte`; POSIX `opendir` and `readdir` on Linux), `qsort` with `strcmp`, `fread` into one reused buffer, a hand UTF-8 validator, `memchr` on the literal's first byte and `memcmp` | none |
| Rust | `eco` | `rs/` (a crate): `walkdir::WalkDir` with `sort_by_file_name` and `filter_entry`, `std::fs::read`, `std::str::from_utf8`, `regex::bytes::Regex` for every variant (`regex::escape` for a literal, `(?i-u)` for an ASCII-folded one, `(?i)` on the regex), `find_at` from each line's start | regex 1.12.3 and walkdir 2.5.0, from a `cargo vendor` tree in Mk-evidence (`crates/regex-1.12.3-walkdir-2.5.0`) by directory source replacement; bench.json's `libs` says what is inside each |
| Zig | `std` | `grep.zig`, `lit` and `ci`: `Io.Dir.iterate`, `std.mem.sort` with `std.mem.lessThan`, `Dir.readFileAlloc` into an arena reset after each file, `std.unicode.utf8ValidateSlice`, `std.mem.findPos` or `std.ascii.findIgnoreCasePos` | std only |
| Go | `std` | `testdata/grep.go`: `os.ReadDir` (sorted by name), `os.ReadFile`, `utf8.Valid`, `bytes.Index` (for `-i`, on a copy with ASCII capitals lowered), `regexp` with `FindIndex` from each line's start | std only |
| C# | `std` | `grep.cs`: `DirectoryInfo.GetFileSystemInfos` sorted with `string.CompareOrdinal`, reused `byte[]` read buffer, `Utf8.IsValid`, `ReadOnlySpan<byte>.IndexOf` / `IndexOfAny` (ASCII-folded literal), `Regex` (`RegexOptions.Compiled` / `IgnoreCase`, `EnumerateMatches`) | std only |

- **Not done by the twins:** mgrep, unchanged, does more at `-c` than the task needs, and no twin does any of it. A profile of the Mk cell must tell these costs, which are the dogfood's, from std's before it names a std function for the defect line:
  - it collects each matching line's matches (`Scan.Take` calls `Matcher.All` for its `--count-matches` total, which `-c` does not print);
  - it opens `.mgrepignore` in each of the 2,014 directories it walks (step 1), and on Windows every open goes to the blocking pool (`std/task/io_file.mk`), a handoff between threads with one carrier;
  - it counts every searched file's lines (`CountLines`, a `simd.Count` pass, `src/search/searcher.mk`) for a `lines` total that only `--stats` prints;
  - in the literal modes it counts the line numbers of the bytes it skips (`simd.Count` over each skipped span), which `-c` does not print;
  - at `-j 1` it still runs a feeder task, a search task and the printer, with two `Chan`s, a `Semaphore` acquire and release per file, and a `HeapText` copy of each file's output line (`src/work/work.mk`).
- **Skipped:** C in `re` and `ci` (no regex or case-folding search in C's std; the row's C cell is `lit` only); Zig in `re` (no regex in Zig's std).
- **Environment:** `MK_CARRIERS=1` (one search task on one carrier); `publish`'s one-CPU affinity.
- **Columns:** `mk`, `c`, `rust`, `rust-ovf`, `zig-fast`, `zig-safe`, `go`, for each language with a cell in the variant. The `eco` cells are printed "eco (not classed)": their libraries' code is not made checked by the column's flags.
- **The defect line:** the Mk cell against the fastest `std` cell of the other languages (Go, and Zig in `lit` and `ci`). The function a profile of the Mk cell names is the std function the line is about (`std.regex`, `std.fs`, or `std.simd`'s searches).
- **Bans:** every program's own source keeps the other tables' bans (`appRowViolations`): no unsafe in Rust, no hints, no `@setRuntimeSafety(false)`, no fast-math; mgrep calls only `std.simd`'s routines that dispatch inside std (`IndexByte`, `IndexAny`, `Count`, `Equal`, `Compare`, `ValidUtf8`), which the table allows.
