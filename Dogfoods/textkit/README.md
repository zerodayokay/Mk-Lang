# textkit

Unicode text tools in one binary, in the manner of coreutils with Unicode done right: normalization, case folding, display width, grapheme clusters, sorting and de-duplicating by folded or numeric keys, counting, a small grep, UTF-16 conversion, word and sentence segmentation (UAX 29), and conversion between the encodings of the WHATWG Encoding Standard. It shows `std.text`, `std.encoding`, `std.regex` and streaming readers over inputs of any size.

## Running it

From the program's folder (the package has two bins, so name `textkit`):

```
cd Dogfoods/textkit
mk run --bin textkit . -- fold cases/data/case.txt
```

where `case.txt` holds `Straße`, `STRASSE`, `ǅungla`, `Σίσυφος` and `ẞ`, one a line:

```text
strasse
strasse
ǆungla
σίσυφοσ
ss
```

```
textkit <command> [options] [file...]
```

Every command reads the files it is given in order, or standard input when there are none or a file is `-`, and writes standard output. `textkit --help` lists the commands and `textkit <command> --help` a command's options.

| Command | What it does |
|---|---|
| `norm` | Rewrites each line in NFC, NFD, NFKC or NFKD (`--form`); `--check` lists the lines not already in the form; `--hex` reads and writes code points as the UCD's test files do |
| `fold` | Full case folding for caseless comparison, or `--lower` and `--upper`, which map one scalar to one |
| `width` | Each line's width in terminal columns; `--max` only the widest; `--clusters` shows the grapheme clusters in UAX 29's notation (`÷ 0065 × 0301 ÷`) |
| `columns` | Lines up separated fields (`--separator`, a tab by default) into columns by display width, so CJK and combining text align |
| `sort` | Stable sort by the line or a field (`--key`, `--separator`), folded (`--fold`) or numeric (`--numeric`); `--reverse`, `--unique`, `--zero` for NUL-ended records, `--memory` to bound the bytes held |
| `uniq` | Drops adjacent repeats, or every repeat with `--global`; `--count`, `--repeated`, `--single`, `--fold`, `--key` |
| `wc` | Lines, words, scalars (`--chars`), bytes, grapheme clusters and the widest line |
| `grep` | Lines that match a regular expression (RE2 syntax), with `--ignore-case`, `--invert`, `--count`, `--line-number`, `--only`, `--group`, `--replace`, `--fixed` and `--flush` |
| `utf16` | `encode` and `decode` between UTF-8 and UTF-16, little- or big-endian, with or without a byte order mark, or as hex code units |
| `words` | Each line's words, one a line, split at UAX 29's word boundaries; `--count`, `--marks`, `--hex` |
| `sentences` | Each paragraph's sentences, one a line, split at UAX 29's sentence boundaries; `--from` reads another encoding |
| `convert` | Converts between the encodings of the Encoding Standard (`--from`, `--to`, UTF-8 by default) through the streaming decoder, writing U+FFFD for malformed input or stopping at it with `--strict`; `--dump hex` or `--dump base64`; `--labels` prints the encoding each line names. Multi-byte CJK encodings are refused by name |

Exit status: 0 done; 1 nothing matched (`grep`) or a line not in the form (`norm --check`); 2 a bad command line or an encoding label textkit does not convert; 3 an input that could not be read, was not UTF-8 or was malformed in its encoding, or a scalar the output encoding has no bytes for; 4 out of memory, or `sort --memory` reached. A failed input is reported as `textkit: <name>: <error>` and the next input is still read.

The second bin, `ucdcases`, writes the `ucd-*` cases from the UCD's test files, and with `--whatwg` the `convert-labels` and `whatwg-*` cases from the Encoding Standard's files. It is run by hand when either changes:

```
mk run --bin ucdcases . -- --whatwg <encoding-dir> <ucd-dir> cases
```

Its expected outputs are the sources' own columns, break marks, names and mappings, never textkit's output.

## Design

- Lines of any length are read in chunks with `Reader.Read` and checked as UTF-8 as they arrive, so an invalid byte is reported with its line number after the lines before it are written. Records ended by a byte come from `Reader.Until`, joined across reads.
- Each line is processed in a scratch arena reset after it, so memory stays at the longest line; `sort --memory` bounds the whole input with a `mem.Fixed` budget.
- Normalization, folding, width and segmentation come from `std.text`; `grep` compiles with `std.regex`; `convert` streams through `std.encoding`'s decoders a piece at a time.
- One `ForEach` drives every command over its inputs and keeps the worst exit status.

## Limits

- `grep` reads lines with `Reader.Line`, so a line is bounded by the read buffer.
- `convert` handles the single-byte encodings, UTF-8, UTF-16 and the replacement and x-user-defined encodings; multi-byte CJK encodings are refused.
- Whole-input commands stop at `--limit` bytes.

## Tests

The cases in `cases/` include all of the UCD's normalization and break tests and every WHATWG label and single-byte index, invalid UTF-8, unpaired surrogates, lines longer than the read buffer, out-of-memory under `sort --memory`, and read faults on a simulated disk. Run them from the repository root with `go run ./tools/dogfood run -only textkit`.

## Section 18 lines

The standard-library lines this program uses, as `doc/reference.md` lists them; `go run ./tools/dogfood cover` reads this block.

```mk
struct System {
System.Stdout(&self): Writer
System.Stderr(&self): Writer
System.Print[A: Fields[Format] + Plain](&self, const f: str, args: A): WriteError!void
System.PrintErr[A: Fields[Format] + Plain](&self, const f: str, args: A): WriteError!void
System.Stdin(&self): Reader
str.Len(self): int
str.Byte(self, i: int): u8
str.AsBytes(self): []u8
str.Find(self, sub: str): ?int
str.Has(self, sub: str): bool
str.StartsWith(self, prefix: str): bool
str.EndsWith(self, suffix: str): bool
str.Trim(self): str
str.Split(self, sep: str): Split from self, sep
str.Lines(self): Lines
str.Words(self): Words
str.Scalars(self): Scalars
str.ParseInt(self): ParseError!i64
str.ParseFloat(self): ParseError!f64
str.From(bytes: []u8): Utf8Error!str
str.ToLower(self, a: &Arena): OutOfMemory!str from a
str.ToUpper(self, a: &Arena): OutOfMemory!str from a
Split.Next(mut self): ?str from self.text
Lines.Next(mut self): ?str from self.text
Words.Next(mut self): ?str from self.text
Scalars.Next(mut self): ?char
Slice[T].SortStable(mut self, a: &Alloc, const less: fn(&T, &T): bool): OutOfMemory!void
Slice[T].SortStableInPlace(mut self, const less: fn(&T, &T): bool)
Slice[T].Search[K](&self, key: &K, const less: fn(&T, &K): bool): int
List[T].New(a: &Arena): List[T]
List[T].Push(mut self, x: T into self): OutOfMemory!void
List[T].Len(&self): int
List[T].Clear(mut self)
List[T].Insert(mut self, i: int, x: T into self): OutOfMemory!void
List[T].Done(self): mut []T
Text.New(a: &Arena): Text
Text.Push(mut self, s: str): OutOfMemory!void
Text.PushChar(mut self, c: char): OutOfMemory!void
Text.Str(&self): str
Text.Done(self): str
Text.Clear(mut self)
Text.Drain(mut self, n: int)
Text.Writer(mut self): Writer
HeapList[T].New(a: &Alloc): HeapList[T]
HeapList[T].Push(mut self, a: &Alloc, x: T into self): OutOfMemory!void
HeapList[T].Len(&self): int
HeapList[T].Free(self, a: &Alloc)
HeapText.New(a: &Alloc): HeapText
HeapText.Push(mut self, a: &Alloc, s: str): OutOfMemory!void
HeapText.Set(mut self, a: &Alloc, s: str): OutOfMemory!void
HeapText.Str(&self): str
HeapText.Free(self, a: &Alloc)
Arena.New(h: &Heap): OutOfMemory!Arena
Arena.Slice[T](&self, n: int): OutOfMemory!mut []T
Arena.Dup(&self, s: str): OutOfMemory!str from self
Arena.Fmt[A: Fields[Format] + Plain](&self, const f: str, args: A): OutOfMemory!str from self
Arena.Sub(&self): OutOfMemory!Arena
Arena.Reset(mut self) ends self
Arena.Free(self) ends self
Heap.Alloc(&self): &Alloc
Print[A: Fields[Format] + Plain](w: mut Writer, const f: str, args: A): WriteError!void
Writer.Str(mut self, s: str): WriteError!void
Writer.Bytes(mut self, b: []u8): WriteError!void
Writer.Flush(mut self): WriteError!void
Reader.Read(mut self, buf: mut []u8): ReadError!int
Reader.ReadAll(mut self, buf: mut []u8): ReadError!int
Reader.Line(mut self, a: &Arena): ReadError|OutOfMemory!?str from a
Reader.Until(mut self, delim: u8, buf: mut []u8): ReadError!int
interface Format {
interface Free {
// std.fs. File and Error below are fs.File and fs.Error
fs.ReadText(a: &Arena, io: Io, p: str): fs.Error|OutOfMemory!str from a
fs.WriteAll(io: Io, p: str, data: []u8): fs.Error!void
fs.MakeDir(io: Io, p: str): fs.Error!void
fs.Open(io: Io, p: str): fs.Error!File
File.Reader(mut self, io: Io, buf: mut []u8): Reader from self, buf
File.Close(self, io: Io): fs.Error!void
File.Abandon(self)
// std.mem
Fixed.New(buf: mut []u8): Fixed
Fixed.Alloc(&self): &Alloc from self
// std.text
*
// std.regex. Regex and Error below are regex.Regex and regex.Error
error Syntax(offset: int)
type Error = Syntax | TooLarge | OutOfMemory
regex.Compile(a: &Arena, pattern: str): regex.Error!Regex from a
Regex.IsMatch(&self, s: str): bool
Regex.Find(&self, s: str): ?Match
Regex.FindAll(&self, a: &Arena, s: str): OutOfMemory!List[Match] from a
Regex.Group(&self, s: str, m: Match, i: int): ?str from s
Regex.Replace(&self, a: &Arena, s: str, with: str): OutOfMemory!str from a
struct Match {
// std.cli. Error below is cli.Error
error Unknown(arg: int)
error Missing(field: int)
error Invalid(arg: int)
type Error = Unknown | Missing | Invalid | OutOfMemory
cli.Args(g: mut Gen, t: Type)
// std.encoding. Encoding, Decoder and Error below are encoding.Encoding, encoding.Decoder and encoding.Error
error BadInput(offset: int)
error UnknownLabel
error Unsupported
type Error = BadInput | UnknownLabel | Unsupported | TooLarge
encoding.Base64EncodeTo(dst: mut []u8, data: []u8, alphabet: Alphabet): int
encoding.Base64Len(n: int, alphabet: Alphabet): int
encoding.HexEncode(a: &Arena, data: []u8): OutOfMemory!str from a
encoding.Lookup(label: str): encoding.UnknownLabel|encoding.Unsupported!Encoding
Encoding.Name(self): str
Encoding.Decode(self, a: &Arena, bytes: []u8): OutOfMemory!str from a
Encoding.DecodeStrict(self, a: &Arena, bytes: []u8): encoding.BadInput|OutOfMemory!str from a
Encoding.Encode(self, a: &Arena, s: str): encoding.BadInput|encoding.Unsupported|OutOfMemory![]u8 from a
Encoding.Decoder(self): Decoder
Decoder.Push(mut self, a: &Arena, chunk: []u8, last: bool): OutOfMemory!str from a
// std.json, for the case generator. Value and Error below are json.Value and json.Error
error Mismatch(offset: int)
type Error = Syntax | Mismatch | Depth | OutOfMemory
json.Parse(a: &Arena, text: str into a): json.Error!Value from a, text
Value.Get(&self, key: str): ?&Value
Value.Str(&self): ?str
enum Value {
```
