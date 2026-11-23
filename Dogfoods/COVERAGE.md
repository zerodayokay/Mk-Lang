# Coverage of reference section 18

Written by `go run ./tools/dogfood cover` on 2026-11-19, with mk 0.1.0 commit fc55f65b53f1, executing on x86_64-windows; do not edit by hand. A line is covered when some dogfood's own source references it (`mk query uses`; a site in generated code under `gen/` is shown as `gen:` and does not count) and, if it declares a function, some case executes it on every hosted target named above (an entry count above zero in a `--release --pgo-gen` build), or it is in `EXEMPT.md`.

- Lines: 1332. Covered: 1134. Exempt: 62. Gaps: 136.
- Static references: incomplete: mk query uses failed (see the errors below); a line counts as referenced when a case executes it, and cover fails.
- mk query uses error: notes: Dogfoods/notes: exit status 1: pkg.mk:16:27: E0202 unknown-module: C source ../../../Mk-evidence/vendor/sqlite/sqlite3.c does not exist (as spelled) | pkg.CSource(path: "../../../Mk-evidence/vendor/sqlite/sqlite3.c", flags: ["-DSQLITE_OS_OTHER=1", "-DSQLITE_THREADSAFE=0", "-DSQLITE_OMIT_LOCALTIME", "-DSQLITE_DEFAULT_MEMSTATUS=0", "-DSQLITE_TEMP_STORE=3", "-DSQLITE_WITHOUT_MSIZE", "-DSQLITE_DISABLE_INTRINSIC", "-DSQLITE_OMIT_SEH"]), | ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^ manifest: c_sources[0].path reason: invalid explain: mk explain E0202 1 error mk query: the package does not check far enough to answer uses

## prelude

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `struct System {` | procman, textkit | bigcalc, chat, cron and 13 more | - | covered (referenced) |
| `System.Stdout(&self): Writer` | textkit | bigcalc, chat, cron and 11 more | bigcalc/bench (windows), bigcalc/calc_arith (windows), bigcalc/calc_bits (windows) and 892 more | covered (executed) |
| `System.Stderr(&self): Writer` | procman, textkit | kvd, lox, mgrep and 4 more | bigcalc/cert_der_deep (windows), bigcalc/cert_show_garbage (windows), bigcalc/cert_show_missing (windows) and 825 more | covered (executed) |
| `System.Print[A: Fields[Format] + Plain](&self, const f: str, args: A): WriteError!void` | textkit | bigcalc, chat, cron and 11 more | bigcalc/cert_verify_ed25519 (windows), bigcalc/cert_verify_ip (windows), bigcalc/cert_verify_now (windows) and 280 more | covered (executed) |
| `System.PrintErr[A: Fields[Format] + Plain](&self, const f: str, args: A): WriteError!void` | procman, textkit | bigcalc, chat, cron and 12 more | bigcalc/cert_der_deep (windows), bigcalc/cert_show_garbage (windows), bigcalc/cert_show_missing (windows) and 481 more | covered (executed) |
| `System.Stdin(&self): Reader` | textkit | bigcalc, cron, fm and 4 more | bigcalc/decode_bad_base64 (windows), bigcalc/decode_base64url (windows), bigcalc/enc_open_128 (windows) and 255 more | covered (executed) |
| `System.Env(&self, name: str): ?str from self` | procman | cron, libprobe, procman | cron/bench (windows), cron/check-badzones (windows), cron/check-zones (windows) and 82 more | covered (executed) |
| `str.Len(self): int` | textkit | bigcalc, chat, cron and 14 more | - | covered (referenced; no instrumented body: compiled inline) |
| `str.Byte(self, i: int): u8` | textkit | bigcalc, chat, cron and 12 more | bigcalc/bench (windows), bigcalc/calc_arith (windows), bigcalc/calc_bits (windows) and 1030 more | covered (executed) |
| `str.AsBytes(self): []u8` | textkit | bigcalc, chat, cron and 13 more | - | covered (referenced; no instrumented body: compiled inline) |
| `str.Find(self, sub: str): ?int` | textkit | bigcalc, chat, cron and 14 more | bigcalc/bench (windows), bigcalc/calc_arith (windows), bigcalc/calc_bits (windows) and 884 more | covered (executed) |
| `str.Has(self, sub: str): bool` | textkit | chat, cron, fetch and 3 more | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 148 more | covered (executed) |
| `str.StartsWith(self, prefix: str): bool` | textkit | bigcalc, chat, cron and 14 more | bigcalc/bench (windows), bigcalc/calc_arith (windows), bigcalc/calc_bits (windows) and 766 more | covered (executed) |
| `str.EndsWith(self, suffix: str): bool` | textkit | chat, fetch, fm and 4 more | bigcalc/cert_der_pem (windows), bigcalc/cert_show_chain (windows), bigcalc/cert_show_der (windows) and 188 more | covered (executed) |
| `str.Trim(self): str` | textkit | bigcalc, chat, cron and 10 more | bigcalc/cert_der_deep (windows), bigcalc/cert_der_pem (windows), bigcalc/cert_show_chain (windows) and 315 more | covered (executed) |
| `str.Split(self, sep: str): Split from self, sep` | textkit | cron, fetch, imgproc and 6 more | cron/bench (windows), cron/check-badzones (windows), cron/check-errors (windows) and 159 more | covered (executed) |
| `str.Lines(self): Lines` | textkit | chat, cron, fetch and 9 more | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 256 more | covered (executed) |
| `str.Words(self): Words` | textkit | kernel, libprobe, lox and 4 more | libprobe/check-duplicate (windows), libprobe/check-missing (windows), libprobe/check-no-search (windows) and 58 more | covered (executed) |
| `str.Scalars(self): Scalars` | lox, textkit | fm, libprobe, lox and 1 more | bigcalc/cert_show_chain (windows), bigcalc/cert_show_der (windows), bigcalc/cert_verify_ed25519 (windows) and 126 more | covered (executed) |
| `str.ParseInt(self): ParseError!i64` | lox, textkit | bigcalc, chat, cron and 13 more | bigcalc/calc_bits (windows), bigcalc/enc_open_128 (windows), bigcalc/enc_open_prk (windows) and 468 more | covered (executed) |
| `str.ParseFloat(self): ParseError!f64` | lox, textkit | imgproc, lox, textkit and 1 more | imgproc/bench (windows), imgproc/extra-color-auto (windows), imgproc/extra-color-scalar (windows) and 81 more | covered (executed) |
| `str.ParseFloatPrefix(self): (value: f64, used: int)` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `str.From(bytes: []u8): Utf8Error!str` | textkit | bigcalc, chat, fetch and 9 more | bigcalc/bench (windows), bigcalc/calc_bits (windows), bigcalc/cert_der_deep (windows) and 405 more | covered (executed) |
| `str.FromC(p: *u8): str` | notes | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `str.ToLower(self, a: &Arena): OutOfMemory!str from a` | textkit | fetch, mgrep, textkit | fetch/bad-url (windows), fetch/bench (windows), fetch/cookies (windows) and 48 more | covered (executed) |
| `str.ToUpper(self, a: &Arena): OutOfMemory!str from a` | textkit | cron, textkit | cron/run-jobs (windows), cron/run-jobs-16 (windows), textkit/fold-upper (windows) | covered (executed) |
| `str.Replace(self, a: &Arena, old: str, new: str): OutOfMemory!str from a` | - | - | - | exempt (EXEMPT.md line 21) |
| `f64.Bits(self): u64` | lox, tracer | bigcalc, lox, tracer | bigcalc/calc_bits (windows), imgproc/ascii-ppm-blur (windows), imgproc/bad-kernel (windows) and 94 more | covered (executed) |
| `f64.FromBits(b: u64): f64` | lox, tracer | lox, tracer | bigcalc/calc_bits (windows), imgproc/ascii-ppm-blur (windows), imgproc/bench (windows) and 87 more | covered (executed) |
| `f32.Bits(self): u32` | imgproc | imgproc, tracer | imgproc/ascii-ppm-blur (windows), imgproc/bench (windows), imgproc/carriers-1 (windows) and 38 more | covered (executed) |
| `f32.FromBits(b: u32): f32` | imgproc, tracer | imgproc, tracer | imgproc/extra-color-scalar (windows), imgproc/extra-gray-scalar (windows), tracer/plot-every-f32 (windows) and 4 more | covered (executed) |
| `Split.Next(mut self): ?str from self.text` | textkit | cron, fetch, imgproc and 6 more | cron/bench (windows), cron/check-badzones (windows), cron/check-errors (windows) and 159 more | covered (executed) |
| `Lines.Next(mut self): ?str from self.text` | textkit | chat, cron, fetch and 9 more | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 256 more | covered (executed) |
| `Words.Next(mut self): ?str from self.text` | textkit | kernel, libprobe, lox and 4 more | libprobe/check-duplicate (windows), libprobe/check-missing (windows), libprobe/check-no-search (windows) and 58 more | covered (executed) |
| `Scalars.Next(mut self): ?char` | lox, textkit | fm, libprobe, lox and 1 more | bigcalc/cert_show_chain (windows), bigcalc/cert_show_der (windows), bigcalc/cert_verify_ed25519 (windows) and 126 more | covered (executed) |
| `Slice[T].Len(&self): int` | orderbook | bigcalc, chat, cron and 13 more | - | covered (referenced; no instrumented body: compiled inline) |
| `Slice[T].Get(&self, i: int): ?&T` | orderbook | cron, orderbook, raft | cron/bench (windows), cron/run-detach (windows), cron/run-interrupt (windows) and 51 more | covered (executed) |
| `Slice[T].GetMut(mut self, i: int): ?mut T` | orderbook | cron, orderbook, raft | cron/bench (windows), cron/run-detach (windows), cron/run-interrupt (windows) and 47 more | covered (executed) |
| `Slice[T].Sort(mut self, const less: fn(&T, &T): bool)` | lox, orderbook | bigcalc, fm, kvd and 6 more | bigcalc/run_script (windows), fetch/bench (windows), fetch/decoders (windows) and 94 more | covered (executed) |
| `Slice[T].SortStable(mut self, a: &Alloc, const less: fn(&T, &T): bool): OutOfMemory!void` | orderbook, textkit | orderbook, textkit | orderbook/basic (windows), textkit/sort-basic (windows) and more | covered (executed) |
| `Slice[T].SortStableInPlace(mut self, const less: fn(&T, &T): bool)` | lox, textkit | lox, textkit | lox/arith (windows), lox/bench (windows), lox/compile-errors (windows) and more | covered (executed) |
| `Slice[T].Search[K](&self, key: &K, const less: fn(&T, &K): bool): int` | lox, orderbook, textkit | lox, orderbook, textkit | lox/arith (windows), lox/bench (windows), lox/compile-errors (windows) and 40 more | covered (executed) |
| `Slice[T].Swap(mut self, i: int, j: int)` | orderbook | bigcalc, orderbook | bigcalc/calc_bits (windows), orderbook/account-missing (windows), orderbook/band (windows) and 17 more | covered (executed) |
| `Slice[T].Reverse(mut self)` | orderbook | orderbook | orderbook/band-full (windows), orderbook/basic (windows), orderbook/compact (windows) and 3 more | covered (executed) |
| `Slice[T].Fill(mut self, x: T into self)` | orderbook | fetch, orderbook, tracer | fetch/bench (windows), fetch/decoders (windows), fetch/no-decoder (windows) and 45 more | covered (executed) |
| `Slice[T].CopyFrom(mut self, src: []T) *src into self` | orderbook | bigcalc, chat, fetch and 7 more | bigcalc/bench (windows), bigcalc/calc_arith (windows), bigcalc/calc_bits (windows) and 458 more | covered (executed) |
| `Slice[T].Has(&self, x: &T): bool` | orderbook | bigcalc, fetch, fm and 6 more | bigcalc/cert_verify_ed25519 (windows), bigcalc/cert_verify_expired (windows), bigcalc/cert_verify_ip (windows) and 260 more | covered (executed) |
| `Slice[T].Index(&self, x: &T): ?int` | orderbook | orderbook, raft | bigcalc/cert_verify_ed25519 (windows), bigcalc/cert_verify_expired (windows), bigcalc/cert_verify_ip (windows) and 282 more | covered (executed) |
| `Slice[T].SplitAt(mut self, i: int): (left: mut []T, right: mut []T) from self` | imgproc, orderbook | imgproc, kvd, orderbook | bigcalc/bench (windows), bigcalc/calc_numtheory (windows), bigcalc/run_mem_limit (windows) and 41 more | covered (executed) |
| `Slice[T].GetPair(mut self, i: int, j: int): ?(a: mut T, b: mut T) from self` | orderbook | orderbook | orderbook/stops (windows) | covered (executed) |
| `Slice[T].Chunks(&self, n: int): Chunks[T] from self` | orderbook | orderbook | orderbook/account-missing (windows), orderbook/band (windows), orderbook/band-full (windows) and 16 more | covered (executed) |
| `Slice[T].ChunksMut(mut self, n: int): ChunksMut[T] from self` | imgproc, orderbook | fetch, imgproc, kvd and 2 more | fetch/bench (windows), fetch/parallel-1 (windows), fetch/parallel-16 (windows) and 148 more | covered (executed) |
| `Chunks[T].Next(mut self): ?[]T from self.xs` | orderbook | orderbook | orderbook/account-missing (windows), orderbook/band (windows), orderbook/band-full (windows) and 16 more | covered (executed) |
| `ChunksMut[T].Next(mut self): ?mut []T from self.ptr` | imgproc, orderbook | fetch, imgproc, kvd and 2 more | fetch/bench (windows), fetch/parallel-1 (windows), fetch/parallel-16 (windows) and 148 more | covered (executed) |
| `List[T].New(a: &Arena): List[T]` | lox, textkit | bigcalc, cron, fetch and 13 more | bigcalc/bench (windows), bigcalc/calc_arith (windows), bigcalc/calc_bits (windows) and 1138 more | covered (executed) |
| `List[T].Push(mut self, x: T into self): OutOfMemory!void` | lox, textkit | bigcalc, cron, fetch and 13 more | bigcalc/bench (windows), bigcalc/calc_arith (windows), bigcalc/calc_bits (windows) and 700 more | covered (executed) |
| `List[T].Pop(mut self): ?T` | lox | bigcalc, fm, lox and 2 more | bigcalc/bench (windows), bigcalc/calc_arith (windows), bigcalc/calc_bits (windows) and 116 more | covered (executed) |
| `List[T].Len(&self): int` | lox, textkit | bigcalc, cron, fetch and 11 more | bigcalc/bench (windows), bigcalc/calc_arith (windows), bigcalc/calc_bits (windows) and 561 more | covered (executed) |
| `List[T].Clear(mut self)` | lox, textkit | lox, textkit, tracer | fetch/url-base-set (windows), lox/arith (windows), lox/bench (windows) and 53 more | covered (executed) |
| `List[T].Truncate(mut self, n: int)` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `List[T].Insert(mut self, i: int, x: T into self): OutOfMemory!void` | lox, textkit | lox, textkit | lox/arith (windows), lox/bench (windows), lox/compile-errors (windows) and 18 more | covered (executed) |
| `List[T].Remove(mut self, i: int): T` | lox | bigcalc, fetch, lox | bigcalc/run_script (windows), lox/bench (windows), lox/compile-errors (windows) and 7 more | covered (executed) |
| `List[T].Done(self): mut []T` | textkit | bigcalc, cron, fetch and 10 more | bigcalc/calc_bits (windows), bigcalc/hash_stdin (windows), bigcalc/key_public_ed25519 (windows) and 306 more | covered (executed) |
| `Map[K, V].New(a: &Arena): Map[K, V]` | lox, orderbook | kvd, lox, orderbook and 1 more | kvd/admin-missing (windows), kvd/admin-usage (windows), kvd/bad-flag (windows) and 131 more | covered (executed) |
| `Map[K, V].NewKeyed(a: &Arena, key: HashKey): Map[K, V]` | orderbook | orderbook, pack | orderbook/account-missing (windows), orderbook/bad-id (windows), orderbook/bad-price (windows) and 31 more | covered (executed) |
| `Map[K, V].Get(&self, k: &K): ?&V` | lox, orderbook | kvd, lox, orderbook and 2 more | kvd/admin-missing (windows), kvd/admin-usage (windows), kvd/bench (windows) and 99 more | covered (executed) |
| `Map[K, V].GetMut(mut self, k: &K): ?mut V` | orderbook | orderbook | orderbook/account-missing (windows), orderbook/band (windows), orderbook/band-full (windows) and 16 more | covered (executed) |
| `Map[K, V].Set(mut self, k: K into self, v: V into self): OutOfMemory!void` | lox, orderbook | kvd, lox, orderbook and 2 more | kvd/admin-missing (windows), kvd/bench (windows), kvd/client-bad-addr (windows) and 96 more | covered (executed) |
| `Map[K, V].Replace(mut self, k: K into self, v: V into self): OutOfMemory!?V` | orderbook | orderbook | orderbook/account-missing (windows), orderbook/band (windows), orderbook/band-full (windows) and 14 more | covered (executed) |
| `Map[K, V].Ensure(mut self, k: K into self, v: V into self): OutOfMemory!mut V from self` | lox, orderbook | lox, orderbook, pack | lox/profile (windows), orderbook/account-missing (windows), orderbook/band (windows) and 19 more | covered (executed) |
| `Map[K, V].Has(&self, k: &K): bool` | orderbook | kvd, orderbook, tracer | kvd/bench (windows), kvd/demo (windows), kvd/demo-admin (windows) and 59 more | covered (executed) |
| `Map[K, V].Remove(mut self, k: &K): ?V` | orderbook | orderbook | orderbook/account-missing (windows), orderbook/band (windows), orderbook/band-full (windows) and 18 more | covered (executed) |
| `Map[K, V].Len(&self): int` | lox, orderbook | lox, orderbook, pack | lox/profile (windows), orderbook/account-missing (windows), orderbook/band (windows) and 20 more | covered (executed) |
| `Map[K, V].Iter(&self): MapIter[K, V]` | lox, orderbook | lox, orderbook | lox/profile (windows), orderbook/account-missing (windows), orderbook/band (windows) and 18 more | covered (executed) |
| `MapIter[K, V].Next(mut self): ?(key: &K, value: &V) from self.map` | kvd, lox, orderbook | kvd, lox, orderbook | kvd/demo-hold (windows), kvd/demo-hold-idle (windows), kvd/demo-many (windows) and 21 more | covered (executed) |
| `Set[T].New(a: &Arena): Set[T]` | lox, orderbook | bigcalc, fetch, lox and 2 more | bigcalc/run_script (windows), fetch/bad-url (windows), fetch/bench (windows) and 143 more | covered (executed) |
| `Set[T].NewKeyed(a: &Arena, key: HashKey): Set[T]` | orderbook | orderbook | orderbook/account-missing (windows), orderbook/bad-id (windows), orderbook/bad-price (windows) and 29 more | covered (executed) |
| `Set[T].Add(mut self, x: T into self): OutOfMemory!bool` | lox, orderbook | bigcalc, fetch, lox and 2 more | bigcalc/run_script (windows), fetch/decoders (windows), lox/gc-pressure (windows) and 46 more | covered (executed) |
| `Set[T].Has(&self, x: &T): bool` | lox, orderbook | lox, orderbook | lox/gc-pressure (windows), lox/gc-stress (windows), lox/lists (windows) and 31 more | covered (executed) |
| `Set[T].Remove(mut self, x: &T): bool` | lox, orderbook | lox, orderbook | lox/gc-pressure (windows), lox/gc-stress (windows), lox/lists (windows) and 31 more | covered (executed) |
| `Text.New(a: &Arena): Text` | lox, textkit | bigcalc, chat, cron and 10 more | bigcalc/cert_der_leaf (windows), bigcalc/cert_der_pem (windows), bigcalc/cert_show_chain (windows) and 471 more | covered (executed) |
| `Text.Push(mut self, s: str): OutOfMemory!void` | lox, textkit | bigcalc, cron, fetch and 9 more | bigcalc/cert_der_leaf (windows), bigcalc/cert_der_pem (windows), bigcalc/cert_show_chain (windows) and 282 more | covered (executed) |
| `Text.PushChar(mut self, c: char): OutOfMemory!void` | lox, textkit | fm, lox, textkit | bigcalc/cert_show_chain (windows), bigcalc/cert_show_der (windows), bigcalc/cert_verify_ed25519 (windows) and 28 more | covered (executed) |
| `Text.PushReplaced(mut self, s: str, old: str, new: str): OutOfMemory!void` | - | - | - | exempt (EXEMPT.md line 22) |
| `Text.Str(&self): str` | lox, textkit | bigcalc, chat, cron and 10 more | bigcalc/cert_show_chain (windows), bigcalc/cert_show_der (windows), bigcalc/cert_verify_ed25519 (windows) and 282 more | covered (executed) |
| `Text.Len(&self): int` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Text.Done(self): str` | textkit | bigcalc, cron, fetch and 6 more | bigcalc/cert_der_leaf (windows), bigcalc/cert_der_pem (windows), bigcalc/cert_show_chain (windows) and 367 more | covered (executed) |
| `Text.Clear(mut self)` | lox, textkit | cron, lox, procman and 1 more | cron/bench (windows), cron/check-badzones (windows), cron/check-errors (windows) and 76 more | covered (executed) |
| `Text.Drain(mut self, n: int)` | textkit | textkit | textkit/convert-labels (windows), textkit/convert-labels-stdin (windows), textkit/fold-full (windows) and 51 more | covered (executed) |
| `Text.Writer(mut self): Writer` | lox, textkit | chat, fetch, kvd and 5 more | bigcalc/cert_der_leaf (windows), bigcalc/cert_der_pem (windows), bigcalc/cert_show_chain (windows) and 366 more | covered (executed) |
| `HeapList[T].New(a: &Alloc): HeapList[T]` | cron, orderbook, textkit | bigcalc, chat, cron and 11 more | bigcalc/bench (windows), bigcalc/calc_arith (windows), bigcalc/calc_bits (windows) and 373 more | covered (executed) |
| `HeapList[T].Push(mut self, a: &Alloc, x: T into self): OutOfMemory!void` | cron, orderbook, textkit | bigcalc, chat, cron and 11 more | bigcalc/bench (windows), bigcalc/calc_arith (windows), bigcalc/calc_bits (windows) and 302 more | covered (executed) |
| `HeapList[T].Pop(mut self): ?T` | orderbook | kernel, orderbook | fm/batch (windows), fm/batch-raw-names (windows), fm/cp-ok (windows) and 23 more | covered (executed) |
| `HeapList[T].Len(&self): int` | orderbook, textkit | bigcalc, chat, fetch and 7 more | bigcalc/calc_arith (windows), bigcalc/calc_bits (windows), bigcalc/calc_numtheory (windows) and 124 more | covered (executed) |
| `HeapList[T].Remove(mut self, i: int): T` | orderbook | chat, kernel, orderbook | chat/basic (windows), chat/basic-one-carrier (windows), chat/errors (windows) and 29 more | covered (executed) |
| `HeapList[T].Insert(mut self, a: &Alloc, i: int, x: T into self): OutOfMemory!void` | orderbook | orderbook | orderbook/account-missing (windows), orderbook/band (windows), orderbook/band-full (windows) and 18 more | covered (executed) |
| `HeapList[T].Extend(mut self, a: &Alloc, xs: []T): OutOfMemory!void *xs into self` | bigcalc | bigcalc | bigcalc/bench (windows), bigcalc/calc_arith (windows), bigcalc/calc_bits (windows) and 18 more | covered (executed) |
| `HeapList[T].Clear(mut self)` | orderbook | bigcalc, orderbook | bigcalc/bench (windows), bigcalc/calc_arith (windows), bigcalc/calc_bits (windows) and 29 more | covered (executed) |
| `HeapList[T].Free(self, a: &Alloc)` | cron, orderbook, textkit | bigcalc, chat, cron and 11 more | bigcalc/bench (windows), bigcalc/calc_arith (windows), bigcalc/calc_bits (windows) and 371 more | covered (executed) |
| `HeapList[T].FreeEmpty(self, a: &Alloc)` | orderbook | orderbook | orderbook/account-missing (windows), orderbook/bad-id (windows), orderbook/bad-price (windows) and 29 more | covered (executed) |
| `HeapList[T].FreeEach(self, a: &Alloc, const release: fn(T))` | - | chat, raft | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 47 more | covered (executed) |
| `HeapMap[K, V].New(a: &Alloc): HeapMap[K, V]` | kvd | kvd, mgrep | kvd/bench (windows), kvd/demo (windows), kvd/demo-admin (windows) and 47 more | covered (executed) |
| `HeapMap[K, V].NewKeyed(a: &Alloc, key: HashKey): HeapMap[K, V]` | kvd | kvd | kvd/bench (windows) | covered (executed) |
| `HeapMap[K, V].Get(&self, k: &K): ?&V` | kvd | kvd | kvd/bench (windows) | covered (executed) |
| `HeapMap[K, V].GetMut(mut self, k: &K): ?mut V` | kvd | kvd | kvd/bench (windows), kvd/demo (windows), kvd/demo-hold (windows) and 9 more | covered (executed) |
| `HeapMap[K, V].Set(mut self, a: &Alloc, k: K into self, v: V into self): OutOfMemory!void` | kvd | kvd, mgrep | kvd/bench (windows), kvd/demo (windows), kvd/demo-hold (windows) and 29 more | covered (executed) |
| `HeapMap[K, V].Replace(mut self, a: &Alloc, k: K into self, v: V into self): OutOfMemory!?V` | kvd | kvd | kvd/bench (windows) | covered (executed) |
| `HeapMap[K, V].Ensure(mut self, a: &Alloc, k: K into self, v: V into self): OutOfMemory!mut V from self` | kvd | kvd | kvd/bench (windows) | covered (executed) |
| `HeapMap[K, V].Remove(mut self, k: &K): ?V` | kvd | kvd, mgrep | kvd/bench (windows), kvd/demo (windows), kvd/demo-hold (windows) and 45 more | covered (executed) |
| `HeapMap[K, V].Len(&self): int` | kvd | kvd | kvd/demo-hold (windows), kvd/demo-hold-idle (windows), kvd/demo-many (windows) | covered (executed) |
| `HeapMap[K, V].Iter(&self): MapIter[K, V]` | kvd | kvd | kvd/demo-hold (windows), kvd/demo-hold-idle (windows), kvd/demo-many (windows) | covered (executed) |
| `HeapMap[K, V].Free(self, a: &Alloc)` | kvd | kvd, mgrep | kvd/bench (windows), mgrep/before (windows), mgrep/bench (windows) and 34 more | covered (executed) |
| `HeapMap[K, V].FreeEmpty(self, a: &Alloc)` | kvd | kvd | kvd/bench (windows), kvd/demo (windows), kvd/demo-admin (windows) and 11 more | covered (executed) |
| `HeapMap[K, V].FreeEach(self, a: &Alloc, const release: fn(K, V))` | kvd | kvd | kvd/bench (windows) | covered (executed) |
| `TextMap[V].New(a: &Alloc): TextMap[V]` | kvd | bigcalc, kvd | bigcalc/bench (windows), bigcalc/calc_arith (windows), bigcalc/calc_bits (windows) and 36 more | covered (executed) |
| `TextMap[V].NewKeyed(a: &Alloc, key: HashKey): TextMap[V]` | kvd | kvd | kvd/bench (windows), kvd/demo (windows), kvd/demo-admin (windows) and 13 more | covered (executed) |
| `TextMap[V].Get(&self, k: str): ?&V` | kvd | bigcalc, kvd | bigcalc/bench (windows), bigcalc/calc_numtheory (windows), bigcalc/err_no_variable (windows) and 16 more | covered (executed) |
| `TextMap[V].GetMut(mut self, k: str): ?mut V` | kvd | kvd | kvd/bench (windows), kvd/demo (windows), kvd/demo-hold (windows) and 15 more | covered (executed) |
| `TextMap[V].Set(mut self, a: &Alloc, k: str, v: V into self): OutOfMemory!void` | kvd | bigcalc, kvd | bigcalc/bench (windows), bigcalc/calc_numtheory (windows), bigcalc/run_mem_within (windows) and 19 more | covered (executed) |
| `TextMap[V].Replace(mut self, a: &Alloc, k: str, v: V into self): OutOfMemory!?V` | kvd | kvd | kvd/bench (windows), kvd/demo (windows), kvd/demo-hold (windows) and 8 more | covered (executed) |
| `TextMap[V].Ensure(mut self, a: &Alloc, k: str, v: V into self): OutOfMemory!mut V from self` | kvd | kvd | kvd/bench (windows), kvd/demo (windows), kvd/demo-hold (windows) and 9 more | covered (executed) |
| `TextMap[V].Remove(mut self, a: &Alloc, k: str): ?V` | kvd | kvd | kvd/bench (windows), kvd/sim (windows), kvd/sim-carriers16 (windows) and 4 more | covered (executed) |
| `TextMap[V].Len(&self): int` | kvd | kvd | kvd/bench (windows), kvd/demo (windows), kvd/demo-hold (windows) and 12 more | covered (executed) |
| `TextMap[V].Iter(&self): TextMapIter[V]` | kvd | bigcalc, kvd | bigcalc/run_script (windows), kvd/bench (windows), kvd/demo (windows) and 15 more | covered (executed) |
| `TextMapIter[V].Next(mut self): ?(key: str, value: &V) from self.map` | kvd | bigcalc, kvd | bigcalc/run_script (windows), kvd/bench (windows), kvd/demo (windows) and 15 more | covered (executed) |
| `TextMap[V].Free(self, a: &Alloc)` | kvd | bigcalc, kvd | bigcalc/bench (windows), bigcalc/calc_arith (windows), bigcalc/calc_bits (windows) and 47 more | covered (executed) |
| `TextMap[V].FreeEmpty(self, a: &Alloc)` | kvd | kvd | kvd/bench (windows) | covered (executed) |
| `TextMap[V].FreeEach(self, a: &Alloc, const release: fn(V))` | kvd | kvd | kvd/bench (windows) | covered (executed) |
| `HeapText.New(a: &Alloc): HeapText` | kvd, textkit | chat, fetch, fm and 4 more | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 168 more | covered (executed) |
| `HeapText.Push(mut self, a: &Alloc, s: str): OutOfMemory!void` | kvd, textkit | chat, fetch, fm and 4 more | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 152 more | covered (executed) |
| `HeapText.Set(mut self, a: &Alloc, s: str): OutOfMemory!void` | kvd, textkit | fm, kvd, procman and 1 more | fm/interrupt (windows), fm/selftest (windows), fm/session (windows) and 31 more | covered (executed) |
| `HeapText.Clear(mut self)` | kvd | fm, kvd, mgrep and 1 more | fm/interrupt (windows), fm/session (windows), fm/shell-input (windows) and 20 more | covered (executed) |
| `HeapText.Len(&self): int` | kvd | fetch, fm, kvd | fetch/bad-url (windows), fetch/bench (windows), fetch/cookies (windows) and 65 more | covered (executed) |
| `HeapText.Str(&self): str` | kvd, textkit | chat, fetch, fm and 4 more | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 156 more | covered (executed) |
| `HeapText.Free(self, a: &Alloc)` | kvd, textkit | chat, fetch, fm and 4 more | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 168 more | covered (executed) |
| `Box[T].New(a: &Alloc, x: T): OutOfMemory!Box[T] from a, x` | kvd, orderbook | kvd, orderbook | chat/record-close (windows), chat/record-idle (windows), chat/web (windows) and 69 more | covered (executed) |
| `Box[T].Ref(&self): &T` | kvd, orderbook | kvd, orderbook | chat/web (windows), chat/web-errors (windows), chat/web-many-carriers (windows) and 46 more | covered (executed) |
| `Box[T].Mut(mut self): mut T` | kvd, orderbook | kvd, orderbook | chat/web (windows), chat/web-errors (windows), chat/web-many-carriers (windows) and 26 more | covered (executed) |
| `Box[T].Take(self, a: &Alloc): T` | kvd, orderbook | kvd, orderbook | chat/record-close (windows), chat/record-idle (windows), chat/web (windows) and 47 more | covered (executed) |
| `Box[T].Free(self, a: &Alloc)` | kvd, orderbook | kvd, orderbook | kvd/bench (windows), kvd/demo (windows), kvd/demo-hold (windows) and 23 more | covered (executed) |
| `Arena.New(h: &Heap): OutOfMemory!Arena` | lox, textkit | bigcalc, chat, cron and 10 more | bigcalc/bench (windows), bigcalc/calc_arith (windows), bigcalc/calc_bits (windows) and 1179 more | covered (executed) |
| `Arena.NewIn(a: &Alloc): OutOfMemory!Arena from a` | - | pack | pack/bench (windows), pack/create-budget-compress (windows), pack/create-budget-fits (windows) and 5 more | covered (executed) |
| `Arena.Put[T](&self, x: T into self): OutOfMemory!mut T from self` | lox | fetch, lox, textkit | chat/web (windows), chat/web-errors (windows), chat/web-many-carriers (windows) and 91 more | covered (executed) |
| `Arena.Slice[T](&self, n: int): OutOfMemory!mut []T` | imgproc, textkit | bigcalc, chat, cron and 12 more | bigcalc/bench (windows), bigcalc/calc_arith (windows), bigcalc/calc_bits (windows) and 767 more | covered (executed) |
| `Arena.SliceAligned[T](&self, n: int, align: int): OutOfMemory!mut []T` | - | - | - | exempt (EXEMPT.md line 19) |
| `Arena.Dup(&self, s: str): OutOfMemory!str from self` | textkit | bigcalc, chat, cron and 13 more | bigcalc/bench (windows), bigcalc/run_keep_going (windows), bigcalc/run_max_time (windows) and 393 more | covered (executed) |
| `Arena.Fmt[A: Fields[Format] + Plain](&self, const f: str, args: A): OutOfMemory!str from self` | textkit | bigcalc, chat, cron and 11 more | - | covered (referenced; no instrumented body: compiled inline) |
| `Arena.Sub(&self): OutOfMemory!Arena` | lox, notes, textkit | kvd, lox, orderbook and 5 more | bigcalc/bench (windows), bigcalc/cert_show_chain (windows), bigcalc/cert_show_der (windows) and 425 more | covered (executed) |
| `Arena.Alloc(&self): &Alloc` | kvd | kvd | bigcalc/bench (windows), bigcalc/calc_arith (windows), bigcalc/calc_bits (windows) and 863 more | covered (executed) |
| `Arena.Reset(mut self) ends self` | textkit | bigcalc, cron, fetch and 5 more | bigcalc/bench (windows), bigcalc/calc_arith (windows), bigcalc/calc_bits (windows) and 248 more | covered (executed) |
| `Arena.Free(self) ends self` | lox, textkit | bigcalc, chat, cron and 11 more | bigcalc/bench (windows), bigcalc/calc_arith (windows), bigcalc/calc_bits (windows) and 627 more | covered (executed) |
| `Heap.Alloc(&self): &Alloc` | kernel, textkit | bigcalc, chat, cron and 12 more | bigcalc/bench (windows), bigcalc/calc_arith (windows), bigcalc/calc_bits (windows) and 1179 more | covered (executed) |
| `Alloc.Bytes(&self, n: int, align: int): *u8` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Alloc.FreeBytes(&self, p: *u8, n: int, align: int)` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Print[A: Fields[Format] + Plain](w: mut Writer, const f: str, args: A): WriteError!void` | kernel, orderbook, textkit | bigcalc, chat, cron and 11 more | - | covered (referenced; no instrumented body: compiled inline) |
| `Swap[T](a: mut T, b: mut T) *a into b, *b into a` | - | imgproc, lox | bigcalc/bench (windows), bigcalc/calc_numtheory (windows), bigcalc/err_no_inverse (windows) and 119 more | covered (executed) |
| `Replace[T](dst: mut T, x: T into dst): T` | kvd | chat, imgproc, lox | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 160 more | covered (executed) |
| `Writer.Str(mut self, s: str): WriteError!void` | cron, kernel, textkit | bigcalc, chat, cron and 11 more | bigcalc/bench (windows), bigcalc/calc_arith (windows), bigcalc/calc_bits (windows) and 1009 more | covered (executed) |
| `Writer.Bytes(mut self, b: []u8): WriteError!void` | kernel, textkit | bigcalc, chat, fetch and 7 more | bigcalc/bench (windows), bigcalc/calc_arith (windows), bigcalc/calc_bits (windows) and 1177 more | covered (executed) |
| `Writer.Flush(mut self): WriteError!void` | cron, kernel, textkit | chat, cron, fetch and 8 more | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 184 more | covered (executed) |
| `Reader.Read(mut self, buf: mut []u8): ReadError!int` | mgrep, textkit | bigcalc, fetch, mgrep and 2 more | bigcalc/encode_base64 (windows), bigcalc/encode_base64url (windows), bigcalc/encode_hex (windows) and 266 more | covered (executed) |
| `Reader.ReadAll(mut self, buf: mut []u8): ReadError!int` | mgrep, textkit | bigcalc, mgrep, pack and 1 more | bigcalc/encode_base64 (windows), bigcalc/encode_base64url (windows), bigcalc/encode_hex (windows) and 57 more | covered (executed) |
| `Reader.Line(mut self, a: &Arena): ReadError\|OutOfMemory!?str from a` | cron, lox, mgrep and 1 more | bigcalc, cron, fm and 5 more | bigcalc/bench (windows), bigcalc/run_keep_going (windows), bigcalc/run_max_time (windows) and 45 more | covered (executed) |
| `Reader.Until(mut self, delim: u8, buf: mut []u8): ReadError!int` | mgrep, textkit | chat, kvd, mgrep and 1 more | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 78 more | covered (executed) |
| `Reader.Of(b: []u8): Reader from b` | - | - | - | exempt (EXEMPT.md line 20) |
| `interface Format {` | textkit | bigcalc, cron, fetch and 12 more | - | covered (referenced) |
| `interface Eq {` | - | bigcalc, chat, lox and 5 more | - | covered (referenced) |
| `interface Ord {` | - | bigcalc, orderbook, raft | - | covered (referenced) |
| `interface Hash {` | - | pack, raft, gen: lox and 1 more | - | covered (referenced) |
| `interface Free {` | kvd, textkit | bigcalc, chat, cron and 12 more | - | covered (referenced) |
| `Hasher.Bytes(mut self, b: []u8)` | pack | pack | pack/list-dups (windows), pack/list-dups-none (windows) | covered (executed) |
| `Hasher.U64(mut self, x: u64)` | - | raft, gen: lox | bigcalc/run_script (windows), lox/gc-pressure (windows), lox/gc-stress (windows) and 54 more | covered (executed) |
| `HashKey.New(io: Io): Canceled!HashKey` | kvd | kvd, pack | kvd/bench (windows), kvd/demo (windows), kvd/demo-admin (windows) and 15 more | covered (executed) |
| `HashKey.Of(k0: u64, k1: u64): HashKey` | kvd, orderbook | kvd, orderbook, pack | kvd/bench (windows), kvd/demo (windows), kvd/demo-hold (windows) and 37 more | covered (executed) |
| `interface Slice[T] {` | - | bigcalc, chat, cron and 15 more | - | covered (referenced) |
| `struct Cell[T: Plain] {` | lox | lox, orderbook | - | covered (referenced) |
| `Cell[T].New(x: T): Cell[T]` | lox | lox, orderbook | bigcalc/bench (windows), bigcalc/calc_arith (windows), bigcalc/calc_bits (windows) and 358 more | covered (executed) |
| `Cell[T].Load(&self): T` | lox | lox, orderbook | bigcalc/run_mem_limit (windows), bigcalc/run_mem_within (windows), chat/bad-hex (windows) and 235 more | covered (executed) |
| `Cell[T].Store(&self, x: T into self)` | lox | lox, orderbook | bigcalc/run_mem_limit (windows), bigcalc/run_mem_within (windows), chat/bad-hex (windows) and 285 more | covered (executed) |
| `Path.New(a: &Arena, bytes: []u8): OutOfMemory!Path from a` | mgrep | fm, mgrep | fm/batch (windows), fm/batch-raw-names (windows), fm/cp-ok (windows) and 55 more | covered (executed) |
| `Path.AsBytes(&self): []u8 from self.bytes` | mgrep | fm, mgrep | fm/batch-raw-names (windows), mgrep/bench (windows), mgrep/carriers1 (windows) and 14 more | covered (executed) |
| `Path.Str(&self): ?str from self.bytes` | mgrep | fm, mgrep, pack and 1 more | fm/batch (windows), fm/batch-raw-names (windows), fm/cp-ok (windows) and 55 more | covered (executed) |
| `Path.Join(&self, a: &Arena, part: str): OutOfMemory!Path from a` | mgrep | mgrep | mgrep/bench (windows), mgrep/carriers1 (windows), mgrep/carriers16 (windows) and 13 more | covered (executed) |
| `Path.Base(&self): Path from self.bytes` | mgrep | fm, mgrep | fm/batch-raw-names (windows), fm/interrupt (windows), fm/selftest (windows) and 38 more | covered (executed) |
| `Path.Parent(&self): ?Path from self.bytes` | mgrep | mgrep | mgrep/missing (windows) | covered (executed) |
| `Path.Format(&self, w: mut Writer): WriteError!void` | mgrep | fm, mgrep | fm/interrupt (windows), fm/selftest (windows), fm/session (windows) and 2 more | covered (executed) |
| `Io.Scope(self): Scope` | chat | bigcalc, chat, cron and 9 more | - | covered (referenced; no instrumented body: compiled inline) |
| `Io.ScopeOf[E](self): Scope[E]` | chat | chat | - | covered (referenced; no instrumented body: compiled inline) |
| `Io.ScopeWith(self, l: Limits): Scope` | chat, cron | chat, cron, imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Io.NoCancel(self): Io` | chat, cron | chat, cron, kvd and 1 more | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 160 more | covered (executed) |
| `Io.Sleep(self, d: time.Duration): Canceled!void` | chat, cron | chat, cron, fetch and 3 more | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 73 more | covered (executed) |
| `Io.Random(self, buf: mut []u8): Canceled!void` | - | - | bigcalc/enc_seal_ephemeral (windows), bigcalc/enc_seal_random_nonce (windows), chat/web (windows) and 86 more | exempt (reference; executed) (EXEMPT.md line 17) |
| `Io.Yield(self): Canceled!void` | chat | bigcalc, chat, imgproc and 1 more | bigcalc/err_rep_too_deep (windows), bigcalc/run_max_time (windows), bigcalc/run_script (windows) and 144 more | covered (executed) |
| `Scope.Cancel(&self)` | chat, cron | chat, cron, fetch and 5 more | - | covered (referenced; no instrumented body: compiled inline) |
| `Scope.CancelAfter(&self, d: time.Duration)` | chat, cron, fetch and 1 more | bigcalc, chat, cron and 4 more | - | covered (referenced; no instrumented body: compiled inline) |
| `Scope.Expired(&self): bool` | chat, cron, fetch and 1 more | bigcalc, chat, cron and 3 more | - | covered (referenced; no instrumented body: compiled inline) |
| `Scope.Remaining(&self): ?time.Duration` | fetch | fetch | - | covered (referenced; no instrumented body: compiled inline) |
| `struct Limits {` | chat, cron, imgproc | chat, cron, imgproc | - | covered (referenced) |
| `enum StackSize {` | chat, imgproc | chat, cron, imgproc | - | covered (referenced) |
| `struct TargetInfo {` | - | cron, fm, imgproc and 1 more | - | covered (referenced) |
| `enum Arch {` | imgproc | imgproc | - | covered (referenced) |
| `enum Os {` | - | cron, fm, mgrep | - | covered (referenced) |

## std.fs

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `error Os(code: i64)` | kvd, pack | fm, kvd, pack | - | covered (referenced) |
| `error Unsupported` | kvd, pack | kvd, pack | - | covered (referenced) |
| `type Error = NotFound \| Exists \| Denied \| IsDirectory \| NotDirectory \| NoSpace \| InvalidPath \| Utf8Error \| Os \| Canceled \| Unsupported` | kvd, mgrep, pack | bigcalc, chat, cron and 10 more | - | covered (referenced) |
| `fs.Rename(io: Io, src: str, dst: str): fs.Error!void` | kvd, pack, procman | bigcalc, fm, kvd and 5 more | bigcalc/enc_seal_ephemeral (windows), bigcalc/enc_seal_random_nonce (windows), fm/batch (windows) and 61 more | covered (executed) |
| `fs.Remove(io: Io, p: str): fs.Error!void` | kvd, pack, procman | kvd, pack, procman and 1 more | kvd/bench (windows), kvd/demo (windows), kvd/demo-admin (windows) and 26 more | covered (executed) |
| `fs.ReadText(a: &Arena, io: Io, p: str): fs.Error\|OutOfMemory!str from a` | cron, fetch, mgrep and 2 more | chat, cron, fetch and 10 more | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 260 more | covered (executed) |
| `fs.ReadBytes(a: &Arena, io: Io, p: str): fs.Error\|OutOfMemory![]u8 from a` | cron, fetch, mgrep and 1 more | bigcalc, cron, fetch and 7 more | bigcalc/cert_der_deep (windows), bigcalc/cert_der_leaf (windows), bigcalc/cert_der_pem (windows) and 537 more | covered (executed) |
| `fs.WriteAll(io: Io, p: str, data: []u8): fs.Error!void` | cron, fetch, kvd and 2 more | bigcalc, cron, fetch and 8 more | cron/run-jobs (windows), cron/run-jobs-16 (windows), fetch/cookies (windows) and 16 more | covered (executed) |
| `fs.Open(io: Io, p: str): fs.Error!File` | fetch, mgrep, pack and 1 more | bigcalc, fetch, fm and 4 more | bigcalc/bench (windows), bigcalc/encode_base64 (windows), bigcalc/encode_base64url (windows) and 291 more | covered (executed) |
| `fs.Create(io: Io, p: str): fs.Error!File` | kvd, pack | bigcalc, chat, fm and 6 more | bigcalc/enc_seal_ephemeral (windows), bigcalc/enc_seal_random_nonce (windows), chat/history-bad-dir (windows) and 54 more | covered (executed) |
| `File.Read(mut self, io: Io, buf: mut []u8): fs.Error!int` | fetch, mgrep, pack | bigcalc, fetch, mgrep and 1 more | bigcalc/hash_all (windows), bigcalc/hash_carriers1 (windows), bigcalc/hash_carriers16 (windows) and 47 more | covered (executed) |
| `File.Write(mut self, io: Io, data: []u8): fs.Error!int` | kvd, pack | kvd, pack | bigcalc/enc_seal_ephemeral (windows), bigcalc/enc_seal_random_nonce (windows), cron/bench (windows) and 87 more | covered (executed) |
| `File.ReadAt(&self, io: Io, pos: i64, buf: mut []u8): fs.Error!int` | kvd, mgrep, pack | kvd, mgrep, pack | kvd/demo-timeouts (windows), kvd/serve-corrupt-log (windows), kvd/sim (windows) and 60 more | covered (executed) |
| `File.WriteAt(&self, io: Io, pos: i64, data: []u8): fs.Error!int` | kvd, pack | kvd, pack | kvd/bench (windows), kvd/demo (windows), kvd/demo-admin (windows) and 18 more | covered (executed) |
| `File.WriteAll(mut self, io: Io, data: []u8): fs.Error!void` | fetch, kvd, pack | bigcalc, fetch, kvd and 4 more | bigcalc/enc_seal_ephemeral (windows), bigcalc/enc_seal_random_nonce (windows), cron/bench (windows) and 81 more | covered (executed) |
| `File.Sync(mut self, io: Io): fs.Error!void` | kvd, pack | bigcalc, chat, kvd and 5 more | bigcalc/enc_seal_ephemeral (windows), bigcalc/enc_seal_random_nonce (windows), chat/operator (windows) and 60 more | covered (executed) |
| `File.Writer(mut self, io: Io, buf: mut []u8): Writer from self, buf` | kvd, pack | chat, fm, kvd and 3 more | chat/operator (windows), fm/interrupt (windows), fm/session (windows) and 44 more | covered (executed) |
| `File.Reader(mut self, io: Io, buf: mut []u8): Reader from self, buf` | fetch, mgrep, pack and 1 more | bigcalc, fetch, fm and 4 more | bigcalc/bench (windows), bigcalc/encode_base64 (windows), bigcalc/encode_base64url (windows) and 228 more | covered (executed) |
| `File.Close(self, io: Io): fs.Error!void` | cron, fetch, kvd and 3 more | bigcalc, chat, cron and 10 more | bigcalc/bench (windows), bigcalc/enc_seal_ephemeral (windows), bigcalc/enc_seal_random_nonce (windows) and 314 more | covered (executed) |
| `File.Abandon(self)` | cron, fetch, kvd and 3 more | bigcalc, chat, cron and 10 more | cron/run-second-instance (windows), fetch/upload-not-replayable (windows), fm/selftest (windows) and 69 more | covered (executed) |
| `File.Adopt(p: rt.Passed): File` | - | - | - | exempt (EXEMPT.md line 83) |
| `fs.MakeDir(io: Io, p: str): fs.Error!void` | kvd, pack, procman and 1 more | kvd, pack, procman and 1 more | fm/batch (windows), fm/batch-fail (windows), fm/batch-raw-names (windows) and 51 more | covered (executed) |
| `fs.Stat(io: Io, p: str): fs.Error!Info` | cron, fetch, mgrep and 2 more | cron, fetch, fm and 4 more | cron/run-jobs (windows), cron/run-jobs-16 (windows), cron/run-timeout (windows) and 139 more | covered (executed) |
| `Dir.Open(io: Io, p: str): fs.Error!Dir` | mgrep, pack, procman | mgrep, pack, procman | mgrep/bench (windows), mgrep/carriers1 (windows), mgrep/carriers16 (windows) and 26 more | covered (executed) |
| `Dir.Next(mut self, a: &Arena, io: Io): fs.Error\|OutOfMemory!?Entry from a` | mgrep, pack, procman | mgrep, pack, procman | mgrep/bench (windows), mgrep/carriers1 (windows), mgrep/carriers16 (windows) and 25 more | covered (executed) |
| `Dir.Close(self)` | mgrep, pack, procman | mgrep, pack, procman | mgrep/bench (windows), mgrep/carriers1 (windows), mgrep/carriers16 (windows) and 25 more | covered (executed) |
| `fs.OpenWith(io: Io, p: str, o: Options): fs.Error!File` | cron, fetch, kvd | cron, fetch, kvd | cron/bench (windows), cron/run-detach (windows), cron/run-interrupt (windows) and 61 more | covered (executed) |
| `fs.OpenPath(io: Io, p: &Path, o: Options): fs.Error!File` | fm | fm | fm/batch-raw-names (windows) | covered (executed) |
| `File.Seek(mut self, io: Io, pos: i64, from: Whence): fs.Error!i64` | kvd | kvd | kvd/bench (windows), kvd/demo (windows), kvd/demo-admin (windows) and 11 more | covered (executed) |
| `File.Size(&self, io: Io): fs.Error!i64` | fetch, kvd | fetch, kvd | fetch/upload (windows), fetch/upload-chunked (windows), fetch/upload-not-replayable (windows) and 12 more | covered (executed) |
| `File.Truncate(mut self, io: Io, size: i64): fs.Error!void` | kvd | kvd | kvd/bench (windows), kvd/demo (windows), kvd/demo-admin (windows) and 11 more | covered (executed) |
| `File.Lock(&self, io: Io, mode: LockMode): fs.Error!void` | cron, kvd | cron, kvd | cron/run-second-instance (windows), kvd/bench (windows), kvd/demo (windows) and 13 more | covered (executed) |
| `File.TryLock(&self, io: Io, mode: LockMode): fs.Error!bool` | cron, kvd | cron, kvd | cron/bench (windows), cron/run-detach (windows), cron/run-interrupt (windows) and 30 more | covered (executed) |
| `File.Unlock(&self, io: Io): fs.Error!void` | cron, kvd | cron, kvd | cron/bench (windows), cron/run-detach (windows), cron/run-interrupt (windows) and 23 more | covered (executed) |
| `fs.Map(io: Io, f: &File, offset: i64, len: int): fs.Error!Mapping` | kvd | kvd | kvd/demo-timeouts (windows), kvd/serve-damaged-snap (windows), kvd/sim (windows) and 8 more | covered (executed) |
| `Mapping.Bytes(&self): []u8 from self` | kvd | kvd | kvd/demo-timeouts (windows), kvd/serve-damaged-snap (windows), kvd/snap-damaged (windows) and 4 more | covered (executed) |
| `Mapping.Unmap(self)` | kvd | kvd | kvd/demo-timeouts (windows), kvd/serve-damaged-snap (windows), kvd/snap-damaged (windows) and 4 more | covered (executed) |
| `fs.Walk(h: &Heap, io: Io, root: str): fs.Error\|OutOfMemory!Walker from h` | fm | fm | fm/batch (windows), fm/batch-raw-names (windows), fm/cp-ok (windows) and 8 more | covered (executed) |
| `Walker.Next(mut self, a: &Arena, io: Io): fs.Error\|OutOfMemory!?WalkEntry from a` | fm | fm | fm/batch (windows), fm/batch-raw-names (windows), fm/cp-ok (windows) and 7 more | covered (executed) |
| `Walker.SkipDir(mut self)` | fm | fm | fm/batch (windows), fm/interrupt (windows), fm/selftest (windows) and 2 more | covered (executed) |
| `Walker.Close(self)` | fm | fm | fm/batch (windows), fm/batch-raw-names (windows), fm/cp-ok (windows) and 7 more | covered (executed) |
| `fs.MakeDirAll(io: Io, p: str): fs.Error!void` | fm | fm, pack | fm/batch (windows), fm/batch-fail (windows), fm/batch-raw-names (windows) and 9 more | covered (executed) |
| `fs.RemoveAll(io: Io, p: str): fs.Error!void` | fm | fm | fm/batch (windows), fm/batch-raw-names (windows), fm/selftest (windows) and 1 more | covered (executed) |
| `fs.Copy(io: Io, src: str, dst: str): fs.Error!i64` | fm | fm | fm/batch (windows), fm/cp-ok (windows), fm/selftest (windows) and 1 more | covered (executed) |
| `fs.CreateTemp(a: &Arena, io: Io, dir: str, prefix: str): fs.Error\|OutOfMemory!(file: File, path: str) from a` | pack | pack | pack/bench (windows), pack/create-budget-fits (windows), pack/create-gzip-stats (windows) and 18 more | covered (executed) |
| `fs.TempDir(a: &Arena, io: Io): fs.Error\|OutOfMemory!str from a` | fm | fm | fm/session (windows) | covered (executed) |
| `fs.Symlink(io: Io, target: str, link: str): fs.Error!void` | - | fm | fm/batch (windows), fm/session (windows) | covered (executed) |
| `fs.ReadLink(a: &Arena, io: Io, p: str): fs.Error\|OutOfMemory!str from a` | - | fm | fm/batch (windows) | covered (executed) |
| `fs.StatLink(io: Io, p: str): fs.Error!Info` | fm | fm | fm/batch (windows), fm/batch-raw-names (windows), fm/cp-into-itself (windows) and 10 more | covered (executed) |
| `fs.StatPath(io: Io, p: &Path): fs.Error!Info` | fm | fm | fm/batch-raw-names (windows) | covered (executed) |
| `fs.SetModified(io: Io, p: str, t: time.Time): fs.Error!void` | pack | fm, pack | fm/batch (windows), fm/batch-raw-names (windows), pack/untar-pax-extract (windows) and 3 more | covered (executed) |
| `fs.SetPermissions(io: Io, p: str, perm: Permissions): fs.Error!void` | pack | pack | pack/untar-pax-extract (windows), pack/untar-zst-extract (windows), pack/untar-zz-cut-extract (windows) and 1 more | covered (executed) |
| `fs.Cwd(a: &Arena, io: Io): fs.Error\|OutOfMemory!str from a` | fm | fm | fm/pwd (windows) | covered (executed) |
| `fs.Absolute(a: &Arena, io: Io, p: str): fs.Error\|OutOfMemory!str from a` | fm | fm | fm/batch (windows), fm/cp-into-itself (windows), fm/cp-ok (windows) and 4 more | covered (executed) |
| `struct Options {` | cron, kvd | cron, fetch, fm and 1 more | - | covered (referenced) |
| `enum Whence {` | kvd | kvd | - | covered (referenced) |
| `enum LockMode {` | cron, kvd | cron, kvd | - | covered (referenced) |
| `enum Kind {` | fm, pack | fm, mgrep, orderbook and 2 more | - | covered (referenced) |
| `struct Permissions {` | pack | fm, pack | - | covered (referenced) |
| `struct WalkEntry {` | fm | fm | - | covered (referenced) |
| `struct Entry {` | mgrep, pack, procman | mgrep, pack, procman | - | covered (referenced) |
| `struct Info {` | fm, mgrep, pack and 1 more | fetch, fm, mgrep and 3 more | - | covered (referenced) |

## std.net

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `error Os(code: i64)` | kvd | kvd | - | covered (referenced) |
| `error Unsupported` | kvd | fetch, kvd | - | covered (referenced) |
| `error SecureFailed(why: SecureFailure)` | - | fetch | - | covered (referenced) |
| `type Error = Refused \| Reset \| AddrInUse \| Unreachable \| InvalidAddress \| Os \| Canceled \| Closed \| time.TimedOut \| Unsupported \| SecureFailed \| OutOfMemory` | kvd | chat, kvd, procman and 1 more | - | covered (referenced) |
| `net.Listen(io: Io, addr: str): net.Error!Listener` | chat, fetch, kvd | chat, fetch, kvd and 1 more | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 97 more | covered (executed) |
| `net.Dial(io: Io, addr: str): net.Error!Stream` | chat, fetch, kvd | chat, fetch, kvd and 1 more | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 100 more | covered (executed) |
| `Listener.Accept(&self, io: Io): net.Error!Stream` | chat, fetch, kvd | chat, fetch, kvd and 1 more | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 101 more | covered (executed) |
| `Listener.Port(&self): int` | chat, fetch, kvd | chat, fetch, kvd and 1 more | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 105 more | covered (executed) |
| `Listener.Close(self)` | chat, fetch, kvd | chat, fetch, kvd and 1 more | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 105 more | covered (executed) |
| `Stream.Read(&self, io: Io, buf: mut []u8): net.Error!int` | chat, kvd | chat, kvd, procman | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 21 more | covered (executed) |
| `Stream.Write(&self, io: Io, data: []u8): net.Error!int` | chat, kvd | chat, kvd | chat/slow-consumer (windows), kvd/bench (windows), kvd/record (windows) | covered (executed) |
| `Stream.WriteAll(&self, io: Io, data: []u8): net.Error!void` | chat, kvd | chat, kvd, procman | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 26 more | covered (executed) |
| `Stream.Reader(&self, io: Io, buf: mut []u8): Reader from self, buf` | chat, kvd | chat, kvd, procman | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 25 more | covered (executed) |
| `Stream.Writer(&self, io: Io, buf: mut []u8): Writer from self, buf` | chat, kvd | chat, kvd | chat/web (windows), chat/web-many-carriers (windows), chat/web-one-carrier (windows) and 15 more | covered (executed) |
| `Stream.Close(self)` | chat, kvd | chat, kvd, procman | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 37 more | covered (executed) |
| `net.ParseAddr(s: str): net.Error!Addr` | kvd, raft | kvd, raft | kvd/client-bad-addr (windows), kvd/client-refused (windows), kvd/client-resolve (windows) and 30 more | covered (executed) |
| `net.Resolve(a: &Arena, io: Io, host: str): net.Error\|OutOfMemory!List[Addr] from a` | kvd, raft | kvd, raft | chat/web (windows), chat/web-errors (windows), chat/web-many-carriers (windows) and 64 more | covered (executed) |
| `Addr.Format(&self, w: mut Writer): WriteError!void` | kvd, raft | kvd, raft | chat/web (windows), chat/web-errors (windows), chat/web-many-carriers (windows) and 83 more | covered (executed) |
| `net.Bind(io: Io, addr: str): net.Error!Datagram` | raft | raft | raft/bench (windows), raft/crash-seeds (windows), raft/crash-verbose (windows) and 19 more | covered (executed) |
| `Datagram.Send(&self, io: Io, data: []u8, to: Addr): net.Error!void` | raft | raft | raft/bench (windows), raft/crash-seeds (windows), raft/crash-verbose (windows) and 18 more | covered (executed) |
| `Datagram.Recv(&self, io: Io, buf: mut []u8): net.Error!(n: int, peer: Addr)` | raft | raft | raft/bench (windows), raft/crash-seeds (windows), raft/crash-verbose (windows) and 18 more | covered (executed) |
| `Datagram.Port(&self): int` | raft | raft | raft/bench (windows), raft/crash-seeds (windows), raft/crash-verbose (windows) and 19 more | covered (executed) |
| `Datagram.Close(self)` | raft | raft | raft/bench (windows), raft/crash-seeds (windows), raft/crash-verbose (windows) and 19 more | covered (executed) |
| `Stream.SetNoDelay(&self, on: bool): net.Error!void` | kvd | kvd | kvd/bench (windows), kvd/demo (windows), kvd/demo-hold (windows) and 10 more | covered (executed) |
| `Stream.SetKeepAlive(&self, idle: ?time.Duration): net.Error!void` | kvd | kvd | kvd/bench (windows), kvd/demo (windows), kvd/demo-hold (windows) and 5 more | covered (executed) |
| `Stream.Shutdown(&self, io: Io, how: Shutdown): net.Error!void` | chat | chat | chat/record-close (windows), chat/web (windows), chat/web-many-carriers (windows) and 1 more | covered (executed) |
| `Stream.LocalAddr(&self): Addr` | - | chat | chat/record-close (windows), chat/record-idle (windows) | covered (executed) |
| `Stream.PeerAddr(&self): Addr` | - | chat | chat/record-close (windows), chat/record-idle (windows) | covered (executed) |
| `Stream.Conn(self): Conn` | chat, fetch | chat, fetch | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 81 more | covered (executed) |
| `net.ListenWith(io: Io, addr: str, o: ListenOptions): net.Error!Listener` | kvd | kvd | kvd/demo (windows), kvd/demo-admin (windows), kvd/demo-admin-bad (windows) and 5 more | covered (executed) |
| `net.ListenUnix(io: Io, path: str): net.Error!Listener` | kvd | kvd | kvd/demo-admin (windows), kvd/demo-admin-bad (windows) | covered (executed) |
| `net.DialUnix(io: Io, path: str): net.Error!Stream` | kvd | kvd | kvd/admin-missing (windows) | covered (executed) |
| `Stream.SendHandles(&self, io: Io, data: []u8, handles: []Pass): net.Error!void` | - | - | - | exempt (EXEMPT.md line 74) |
| `Stream.RecvHandles(&self, a: &Alloc, io: Io, buf: mut []u8, into: mut HeapList[Passed]): net.Error!int` | - | - | - | exempt (EXEMPT.md line 75) |
| `Passed.Close(self)` | - | - | - | exempt (EXEMPT.md line 76) |
| `Passed.Adopt(p: rt.Passed): Passed` | - | - | - | exempt (EXEMPT.md line 77) |
| `Conn.Read(&self, io: Io, buf: mut []u8): net.Error!int` | chat | chat | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 81 more | covered (executed) |
| `Conn.WriteAll(&self, io: Io, data: []u8): net.Error!void` | chat | chat | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 81 more | covered (executed) |
| `Conn.Shutdown(&self, io: Io, how: Shutdown): net.Error!void` | chat | chat | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 29 more | covered (executed) |
| `Conn.Reader(&self, io: Io, buf: mut []u8): Reader from self, buf` | fetch | fetch | fetch/bad-url (windows), fetch/bench (windows), fetch/cookies (windows) and 45 more | covered (executed) |
| `Conn.Writer(&self, io: Io, buf: mut []u8): Writer from self, buf` | chat, fetch | chat, fetch | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 74 more | covered (executed) |
| `Conn.PeerAddr(&self): ?Addr` | chat | chat | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 23 more | covered (executed) |
| `Conn.Close(self, io: Io): net.Error!void` | chat, fetch | chat, fetch | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 71 more | covered (executed) |
| `Conn.Abandon(self)` | chat, fetch | chat, fetch | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 81 more | covered (executed) |
| `net.NewConn[T: Transport + Send + Share](h: &Heap, t: T): OutOfMemory!Conn from h, t` | chat | chat | chat/web (windows), chat/web-errors (windows), chat/web-many-carriers (windows) and 4 more | covered (executed) |
| `net.ReserveConn[T: Transport + Send + Share](h: &Heap): OutOfMemory!ConnSlot[T] from h` | chat | chat | chat/record-close (windows), chat/record-idle (windows), chat/web (windows) and 15 more | covered (executed) |
| `ConnSlot[T].Fill(self, t: T): Conn from self, t` | chat | chat | chat/record-close (windows), chat/record-idle (windows), chat/web (windows) and 10 more | covered (executed) |
| `ConnSlot[T].Free(self, a: &Alloc)` | chat | chat | chat/record-close (windows), chat/record-idle (windows), fetch/tls-expired (windows) and 4 more | covered (executed) |
| `interface Transport {` | chat | chat | - | covered (referenced) |
| `Secure.Of[T: Share](c: core.ErasedCall): Secure from c` | - | - | fetch/bad-url (windows), fetch/bench (windows), fetch/cookies (windows) and 63 more | exempt (reference; executed) (EXEMPT.md line 73) |
| `Secure.Connect(&self, h: &Heap into conn, io: Io, conn: Conn, host: str): net.Error!Conn from h, conn` | fetch | fetch | fetch/handshake (windows), fetch/tls (windows), fetch/tls-expired (windows) and 6 more | covered (executed) |
| `struct ListenOptions {` | kvd | kvd | - | covered (referenced) |
| `enum Shutdown {` | - | chat | - | covered (referenced) |
| `enum Pass {` | - | - | - | exempt (EXEMPT.md line 78) |
| `enum Passed {` | - | - | - | exempt (EXEMPT.md line 79) |
| `enum SecureFailure {` | fetch | fetch | - | covered (referenced) |
| `struct Addr {` | kvd, raft | chat, fetch, kvd and 1 more | - | covered (referenced) |

## std.proc

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `proc.WaitSignal(io: Io, s: Signal): Canceled!void` | cron, kvd, procman | chat, cron, kvd and 1 more | cron/run-detach (windows), cron/run-interrupt (windows), cron/run-jobs (windows) and 32 more | covered (executed) |
| `proc.Exit(code: int): never` | fetch, procman, raft | fetch, fm, imgproc and 2 more | fetch/bad-decoder (windows), fetch/bad-option (windows), fetch/bad-url (windows) and 193 more | covered (executed) |
| `enum Signal {` | cron, kvd, procman | chat, cron, kvd and 1 more | - | covered (referenced) |
| `error NotFound` | procman | procman | - | covered (referenced) |
| `error Denied` | procman | procman | - | covered (referenced) |
| `error TooLarge` | procman | procman | - | covered (referenced) |
| `error Os(code: i64)` | procman | procman | - | covered (referenced) |
| `error Unsupported` | procman | procman | - | covered (referenced) |
| `type Error = NotFound \| Denied \| TooLarge \| Os \| Canceled \| OutOfMemory \| Unsupported` | procman | fm, procman | - | covered (referenced) |
| `proc.Run(a: &Arena, io: Io, args: []str, input: []u8, limit: int): proc.Error!Output from a` | procman | procman | procman/bench (windows), procman/run-crash (windows), procman/run-fail (windows) and 18 more | covered (executed) |
| `proc.Supervise[E](sys: &System, workers: int, l: &net.Listener, const worker: fn(&System, &net.Listener): E!void): E\|proc.Error!void` | procman | procman | procman/status-pool (windows), procman/status-pool-carriers (windows) | covered (executed) |
| `proc.IsWorker(sys: &System): bool` | procman | procman | procman/bench (windows), procman/procfile-missing (windows), procman/start-binary (windows) and 17 more | covered (executed) |
| `proc.Pipe(io: Io): proc.Error!(r: PipeReader, w: PipeWriter)` | cron, procman | cron | cron/run-jobs (windows), cron/run-jobs-16 (windows) | covered (executed) |
| `PipeReader.Read(&self, io: Io, buf: mut []u8): proc.Error!int` | cron, procman | cron | cron/run-interrupt (windows), cron/run-jobs (windows), cron/run-jobs-16 (windows) and 3 more | covered (executed) |
| `PipeReader.Reader(&self, io: Io, buf: mut []u8): Reader from self, buf` | cron, procman | cron | cron/run-interrupt (windows), cron/run-jobs (windows), cron/run-jobs-16 (windows) and 3 more | covered (executed) |
| `PipeReader.Close(self)` | cron, procman | cron, fm | cron/run-interrupt (windows), cron/run-jobs (windows), cron/run-jobs-16 (windows) and 3 more | covered (executed) |
| `PipeWriter.WriteAll(&self, io: Io, data: []u8): proc.Error!void` | cron, procman | cron | cron/run-jobs (windows), cron/run-jobs-16 (windows) | covered (executed) |
| `PipeWriter.Writer(&self, io: Io, buf: mut []u8): Writer from self, buf` | cron, procman | cron | cron/run-jobs (windows), cron/run-jobs-16 (windows) | covered (executed) |
| `PipeWriter.Close(self)` | cron, procman | cron, fm | cron/run-jobs (windows), cron/run-jobs-16 (windows) | covered (executed) |
| `proc.Spawn(a: &Arena, io: Io, c: &Command): proc.Error!(child: Child, stdin: ?PipeWriter, stdout: ?PipeReader, stderr: ?PipeReader)` | cron, procman | cron, fm | cron/run-detach (windows), cron/run-interrupt (windows), cron/run-jobs (windows) and 27 more | covered (executed) |
| `proc.Inherited(a: &Alloc, io: Io, into: mut HeapList[net.Passed]): proc.Error!int` | procman | - | - | exempt (EXEMPT.md line 87) |
| `Child.Wait(self, io: Io): proc.Error!Status` | cron, procman | cron, fm | cron/run-detach (windows), cron/run-interrupt (windows), cron/run-jobs (windows) and 6 more | covered (executed) |
| `Child.Signal(&self, s: Signal): proc.Error!void` | cron, procman | cron | cron/run-interrupt (windows) | covered (executed) |
| `Child.Kill(&self): proc.Error!void` | cron, procman | cron | cron/run-interrupt (windows), cron/run-timeout (windows) | covered (executed) |
| `Child.Pid(&self): i64` | cron, procman | cron | cron/run-detach (windows), cron/run-interrupt (windows), cron/run-jobs (windows) and 4 more | covered (executed) |
| `Child.Detach(self)` | cron, procman | cron, fm | cron/run-detach (windows) | covered (executed) |
| `Pty.Open(io: Io, cols: int, rows: int): proc.Error!Pty` | fm, procman | fm | fm/session (windows), fm/shell-input (windows) | covered (executed) |
| `Pty.Resize(&self, io: Io, cols: int, rows: int): proc.Error!void` | fm, procman | fm | fm/shell-input (windows) | covered (executed) |
| `Pty.Read(&self, io: Io, buf: mut []u8): proc.Error!int` | fm, procman | fm | fm/session (windows), fm/shell-input (windows) | covered (executed) |
| `Pty.WriteAll(&self, io: Io, data: []u8): proc.Error!void` | fm, procman | fm | fm/shell-input (windows) | covered (executed) |
| `Pty.Close(self)` | fm, procman | fm | fm/session (windows), fm/shell-input (windows) | covered (executed) |
| `struct Command {` | cron, fm, procman | cron, fm | - | covered (referenced) |
| `enum Pass {` | procman | - | - | exempt (EXEMPT.md line 88) |
| `enum Env {` | cron, procman | cron | - | covered (referenced) |
| `enum Stdio {` | cron, procman | cron | - | covered (referenced) |
| `struct Status {` | cron, procman | cron, fm | - | covered (referenced) |
| `struct Output {` | procman | procman | - | covered (referenced) |

## std.sync

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `Shared[T].New(h: &Heap, x: T): OutOfMemory!Shared[T] from h, x` | chat, kvd | chat, kvd | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 39 more | covered (executed) |
| `Shared[T].Clone(&self): Shared[T] from self.ptr` | chat, kvd | chat, kvd | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 35 more | covered (executed) |
| `Shared[T].Ref(&self): &T` | chat, kvd | chat, kvd | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 35 more | covered (executed) |
| `Shared[T].Free(self, h: &Heap)` | chat, kvd | chat, kvd | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 39 more | covered (executed) |
| `Mutex[T].New(x: T): Mutex[T]` | chat, cron, kvd | chat, cron, kvd | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 119 more | covered (executed) |
| `Mutex[T].Lock(&self, io: Io): Canceled!Guard[T]` | chat, cron, kvd | chat, cron, kvd | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 109 more | covered (executed) |
| `Mutex[T].Into(self): T` | chat, cron, kvd | chat, cron, kvd | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 116 more | covered (executed) |
| `Guard[T].Ref(&self): &T` | chat, kvd | chat, kvd | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 88 more | covered (executed) |
| `Guard[T].Mut(mut self): mut T` | chat, cron, kvd | chat, cron, kvd | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 105 more | covered (executed) |
| `Guard[T].Store(mut self, x: T into self)` | chat, kvd | chat, kvd | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 35 more | covered (executed) |
| `Guard[T].Unlock(self)` | chat, cron, kvd | chat, cron, kvd | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 109 more | covered (executed) |
| `Chan[T].New(a: &Alloc, capacity: int): OutOfMemory!Chan[T]` | chat, mgrep | chat, kvd, mgrep and 2 more | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 114 more | covered (executed) |
| `Chan[T].Send(&self, io: Io, x: T into self): Canceled\|Closed!void` | chat, mgrep | chat, kvd, mgrep and 2 more | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 93 more | covered (executed) |
| `Chan[T].TrySend(&self, x: T into self): Closed!?T` | chat | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Chan[T].Recv(&self, io: Io): Canceled\|Closed!T` | chat, mgrep | chat, kvd, mgrep and 2 more | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 93 more | covered (executed) |
| `Chan[T].Close(&self)` | chat, mgrep | chat, kvd, mgrep and 1 more | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 89 more | covered (executed) |
| `Chan[T].Free(self, a: &Alloc)` | chat, mgrep | chat, kvd, mgrep and 2 more | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 113 more | covered (executed) |
| `Chan[T].FreeEmpty(self, a: &Alloc)` | chat | chat | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 26 more | covered (executed) |
| `Chan[T].FreeEach(self, a: &Alloc, const release: fn(T))` | chat | chat | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 25 more | covered (executed) |
| `RwLock[T].New(x: T): RwLock[T]` | chat, kvd | chat, kvd | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 38 more | covered (executed) |
| `RwLock[T].Lock(&self, io: Io): Canceled!Guard[T]` | chat, kvd | chat, kvd | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 36 more | covered (executed) |
| `RwLock[T].LockRead(&self, io: Io): Canceled!ReadGuard[T]` | chat, kvd | chat, kvd | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 34 more | covered (executed) |
| `RwLock[T].Into(self): T` | chat, kvd | chat, kvd | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 37 more | covered (executed) |
| `ReadGuard[T].Ref(&self): &T` | chat, kvd | chat, kvd | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 33 more | covered (executed) |
| `ReadGuard[T].Unlock(self)` | chat, kvd | chat, kvd | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 34 more | covered (executed) |
| `sync.Select[R: Fields[Selectable] + Plain](io: Io, chans: R): Canceled!int` | chat, raft | chat, raft | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 27 more | covered (executed) |
| `Semaphore.New(permits: int): Semaphore` | chat, mgrep | chat, mgrep | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 70 more | covered (executed) |
| `Semaphore.Acquire(&self, io: Io, n: int): Canceled!void` | chat, mgrep | chat, mgrep | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 33 more | covered (executed) |
| `Semaphore.TryAcquire(&self, n: int): bool` | chat, mgrep | chat, mgrep | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 55 more | covered (executed) |
| `Semaphore.Release(&self, n: int)` | chat, mgrep | chat, mgrep | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 66 more | covered (executed) |
| `Notify.New(): Notify` | chat, kvd | chat, kvd | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 40 more | covered (executed) |
| `Notify.NotifyOne(&self)` | chat, kvd | chat, kvd | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 37 more | covered (executed) |
| `Notify.NotifyAll(&self)` | chat, kvd | chat, kvd | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 35 more | covered (executed) |
| `Notify.Wait(&self, io: Io): Canceled!void` | chat, kvd | chat, kvd | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 38 more | covered (executed) |
| `Barrier.New(n: int): Barrier` | chat, raft | chat, kvd, raft | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 55 more | covered (executed) |
| `Barrier.Wait(&self, io: Io): Canceled!bool` | chat, raft | chat, kvd, raft | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 35 more | covered (executed) |
| `Once[T].New(): Once[T]` | chat, raft | chat, raft | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 46 more | covered (executed) |
| `Once[T].Get(&self, io: Io, const init: fn(): T): Canceled!&T from self` | chat, raft | chat, raft | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 26 more | covered (executed) |
| `Once[T].Into(self): ?T` | chat, raft | chat, raft | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 45 more | covered (executed) |
| `interface Selectable {` | chat | chat | - | covered (referenced) |

## std.atomic

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `AtomicInt.New(n: i64): AtomicInt` | chat, fetch, kernel and 1 more | chat, fetch, kernel and 2 more | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 56 more | covered (executed) |
| `AtomicInt.Add(&self, n: i64): i64` | chat, kernel, kvd | chat, kernel, kvd and 1 more | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 51 more | covered (executed) |
| `AtomicInt.Load(&self): i64` | chat, fetch, kernel and 1 more | chat, fetch, kernel and 1 more | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 41 more | covered (executed) |
| `AtomicInt.Store(&self, n: i64)` | chat, fetch, kvd | chat, fetch, kvd | chat/drill (windows), fetch/deny-loopback (windows), fetch/deny-other (windows) and 11 more | covered (executed) |
| `AtomicBool.New(b: bool): AtomicBool` | chat, kernel | chat, fm, kernel | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 28 more | covered (executed) |
| `AtomicBool.Load(&self): bool` | chat, kernel | chat, fm, kernel | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 27 more | covered (executed) |
| `AtomicBool.Store(&self, b: bool)` | chat, kernel | chat, fm, kernel | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 28 more | covered (executed) |
| `AtomicBool.Swap(&self, b: bool): bool` | chat | chat | chat/slow-consumer (windows), chat/web (windows), chat/web-errors (windows) and 5 more | covered (executed) |
| `Atomic[T].New(x: T): Atomic[T]` | chat, kernel, raft | chat, kernel, procman and 1 more | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 64 more | covered (executed) |
| `Atomic[T].Load(&self, const o: Order): T` | chat, kernel, raft | chat, kernel, procman and 1 more | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 63 more | covered (executed) |
| `Atomic[T].Store(&self, x: T, const o: Order)` | chat, kernel, raft | chat, kernel, raft | chat/drill (windows), raft/bench (windows), raft/crash-seeds (windows) and 18 more | covered (executed) |
| `Atomic[T].Swap(&self, x: T, const o: Order): T` | chat, raft | chat, raft | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 44 more | covered (executed) |
| `Atomic[T].CompareExchange(&self, old: T, new: T, const success: Order, const failure: Order): (ok: bool, value: T)` | chat, raft | chat, procman, raft | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 61 more | covered (executed) |
| `Atomic[T].CompareExchangeWeak(&self, old: T, new: T, const success: Order, const failure: Order): (ok: bool, value: T)` | chat, raft | chat, raft | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 44 more | covered (executed) |
| `Atomic[T].FetchAdd(&self, x: T, const o: Order): T` | chat, raft | chat, raft | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 45 more | covered (executed) |
| `Atomic[T].FetchSub(&self, x: T, const o: Order): T` | chat, raft | chat, raft | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 27 more | covered (executed) |
| `Atomic[T].FetchAnd(&self, x: T, const o: Order): T` | chat, raft | chat, raft | chat/drill (windows), raft/bench (windows), raft/crash-seeds (windows) and 19 more | covered (executed) |
| `Atomic[T].FetchOr(&self, x: T, const o: Order): T` | chat, raft | chat, raft | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 44 more | covered (executed) |
| `Atomic[T].FetchXor(&self, x: T, const o: Order): T` | chat, raft | chat, raft | chat/drill (windows), raft/bench (windows), raft/crash-seeds (windows) and 19 more | covered (executed) |
| `Atomic[T].Wait(&self, io: Io, expected: T): Canceled!void` | chat | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Atomic[T].Wake(&self, all: bool)` | chat | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Spin[T].New(x: T): Spin[T]` | chat, kernel | chat, kernel | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 26 more | covered (executed) |
| `Spin[T].Lock(&self): SpinGuard[T]` | chat, kernel | chat, kernel | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 18 more | covered (executed) |
| `Spin[T].Into(self): T` | chat, kernel | chat, kernel | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 26 more | covered (executed) |
| `SpinGuard[T].Ref(&self): &T` | chat, kernel | chat, kernel | chat/bench (windows), chat/drill (windows), chat/shutdown-connected (windows) and 2 more | covered (executed) |
| `SpinGuard[T].Mut(mut self): mut T` | chat, kernel | chat, kernel | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 18 more | covered (executed) |
| `SpinGuard[T].Store(mut self, x: T into self)` | chat, kernel | chat, kernel | chat/drill (windows) | covered (executed) |
| `SpinGuard[T].Unlock(self)` | chat, kernel | chat, kernel | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 18 more | covered (executed) |
| `const Relaxed: Order = Order(v: 0)` | chat, kernel, raft | chat, kernel, raft | - | covered (referenced) |
| `const Acquire: Order = Order(v: 1)` | chat, kernel, raft | chat, kernel, raft | - | covered (referenced) |
| `const Release: Order = Order(v: 2)` | chat, kernel, raft | chat, kernel, raft | - | covered (referenced) |
| `const AcqRel: Order = Order(v: 3)` | chat, raft | chat, raft | - | covered (referenced) |
| `const SeqCst: Order = Order(v: 4)` | chat, raft | chat, procman, raft | - | covered (referenced) |
| `Order.CanLoad(self): bool` | chat, raft | chat, raft | chat/drill (windows) | covered (executed) |
| `Order.CanStore(self): bool` | chat, raft | chat, raft | chat/drill (windows) | covered (executed) |
| `Order.CanFence(self): bool` | chat, raft | chat, raft | chat/drill (windows) | covered (executed) |
| `Order.CanModify(self): bool` | chat | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `atomic.Fence(const o: Order)` | chat, kernel, raft | chat, kernel, raft | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 47 more | covered (executed) |
| `const Unordered: Order = Order(v: 5)` | chat | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `atomic.Pause()` | chat | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |

## std.time

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `time.Seconds(n: i64): Duration` | cron, fetch, kvd and 2 more | chat, cron, fetch and 3 more | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 166 more | covered (executed) |
| `time.Millis(n: i64): Duration` | cron, kvd, procman and 1 more | bigcalc, chat, cron and 5 more | bigcalc/run_max_time (windows), chat/bad-hex (windows), chat/bad-script (windows) and 73 more | covered (executed) |
| `Duration.Nanos(self): i64` | cron, fetch, kvd and 2 more | chat, cron, fetch and 4 more | bigcalc/run_max_time (windows), chat/bad-hex (windows), chat/bad-script (windows) and 171 more | covered (executed) |
| `time.Now(io: Io): Instant` | kvd, procman, raft | imgproc, kvd, procman and 1 more | chat/web (windows), chat/web-errors (windows), chat/web-many-carriers (windows) and 219 more | covered (executed) |
| `Instant.Since(self, earlier: Instant): Duration` | kvd, procman, raft | imgproc, kvd, procman and 1 more | chat/web (windows), chat/web-many-carriers (windows), chat/web-one-carrier (windows) and 65 more | covered (executed) |
| `time.Minutes(n: i64): Duration` | cron, procman, raft | cron | cron/zone-berlin (windows), cron/zone-lord-howe (windows), cron/zone-new-york-2007 (windows) | covered (executed) |
| `time.Hours(n: i64): Duration` | cron, fetch, procman and 1 more | cron, fetch | cron/bench (windows), cron/run-detach (windows), cron/run-interrupt (windows) and 19 more | covered (executed) |
| `time.Micros(n: i64): Duration` | kvd, procman, raft | kvd | kvd/bench (windows) | covered (executed) |
| `time.Nanos(n: i64): Duration` | cron, procman, raft | chat, cron | cron/run-second-instance (windows), cron/run-timeout (windows) | covered (executed) |
| `Duration.Format(&self, w: mut Writer): WriteError!void` | cron, procman, raft | cron | cron/check-zones (windows), cron/run-timeout (windows) | covered (executed) |
| `time.ParseDuration(s: str): ParseError!Duration` | cron, fetch, procman and 1 more | cron, fetch | cron/check-errors (windows), cron/check-zones (windows), cron/next-autumn (windows) and 22 more | covered (executed) |
| `time.Wall(io: Io): Time` | cron, fetch, kvd and 2 more | bigcalc, cron, fetch and 1 more | bigcalc/cert_verify_now (windows), cron/bench (windows), cron/check-errors (windows) and 103 more | covered (executed) |
| `time.FromUnix(seconds: i64, nanos: i64): Time` | cron, fetch, kvd and 2 more | cron, fetch, kvd and 1 more | bigcalc/cert_show_chain (windows), bigcalc/cert_show_der (windows), bigcalc/cert_verify_ed25519 (windows) and 187 more | covered (executed) |
| `Time.Unix(self): i64` | fetch, procman, raft | cron, fetch | cron/check-errors (windows), cron/run-local-zone (windows), cron/run-log-unwritable (windows) and 20 more | covered (executed) |
| `Time.UnixNanos(self): i64` | cron, kvd, procman and 1 more | cron, kvd | bigcalc/cert_verify_ed25519 (windows), bigcalc/cert_verify_expired (windows), bigcalc/cert_verify_ip (windows) and 53 more | covered (executed) |
| `Time.Add(self, d: Duration): Time` | cron, fetch, procman and 1 more | cron, fetch | cron/bench (windows), cron/run-detach (windows), cron/run-interrupt (windows) and 12 more | covered (executed) |
| `Time.Sub(self, t: Time): Duration` | cron, procman, raft | cron | cron/bench (windows), cron/run-second-instance (windows), cron/run-timeout (windows) and 3 more | covered (executed) |
| `Time.In(self, z: &Zone): Date` | cron, fm, procman and 1 more | cron, fm | cron/bench (windows), cron/next-autumn (windows), cron/next-spring (windows) and 20 more | covered (executed) |
| `Time.Format(&self, w: mut Writer): WriteError!void` | cron, kvd, procman and 1 more | bigcalc, cron, fetch and 2 more | bigcalc/cert_show_chain (windows), bigcalc/cert_show_der (windows), cron/bench (windows) and 37 more | covered (executed) |
| `Date.Time(self): Time` | cron, procman, raft | cron | bigcalc/cert_show_chain (windows), bigcalc/cert_show_der (windows), bigcalc/cert_verify_ed25519 (windows) and 43 more | covered (executed) |
| `time.FromDate(year: i32, month: int, day: int, hour: int, minute: int, second: int, nanos: int, z: &Zone): time.Error!Time` | cron, procman, raft | cron | cron/bench (windows), cron/next-autumn (windows), cron/next-spring (windows) and 4 more | covered (executed) |
| `Zone.Utc(): Zone` | cron, procman, raft | cron, fm | cron/zone-berlin (windows), cron/zone-lord-howe (windows), cron/zone-new-york-2007 (windows) and 3 more | covered (executed) |
| `Zone.Fixed(offset_seconds: i32): Zone` | procman, raft | fm | fm/batch (windows) | covered (executed) |
| `Zone.FromTzif(a: &Arena, name: str, data: []u8): time.Error\|OutOfMemory!Zone from a` | cron, procman, raft | cron | cron/bench (windows), cron/check-badzones (windows), cron/check-zones (windows) and 24 more | covered (executed) |
| `Zone.Load(a: &Arena, io: Io, name: str, embedded: ?Database): time.Error\|OutOfMemory!Zone from a` | cron, procman, raft | cron | cron/bench (windows), cron/check-badzones (windows), cron/check-zones (windows) and 25 more | covered (executed) |
| `Zone.Local(a: &Arena, io: Io): time.Error\|OutOfMemory!Zone from a` | cron, fm, procman and 1 more | cron, fm | cron/check-errors (windows), cron/run-local-zone (windows), cron/run-log-unwritable (windows) and 16 more | covered (executed) |
| `time.LocalName(a: &Arena, io: Io): ?str from a` | cron, procman, raft | cron | cron/check-errors (windows), cron/run-local-zone (windows), cron/run-log-unwritable (windows) | covered (executed) |
| `time.ParseRfc3339(s: str): ParseError!Time` | cron, procman, raft | bigcalc, cron, fm | bigcalc/cert_verify_ed25519 (windows), bigcalc/cert_verify_expired (windows), bigcalc/cert_verify_ip (windows) and 11 more | covered (executed) |
| `time.ParseHttpDate(s: str): ParseError!Time` | fetch, procman, raft | fetch | fetch/bad-url (windows), fetch/bench (windows), fetch/cert-at (windows) and 62 more | covered (executed) |
| `time.FormatHttpDate(a: &Arena, t: Time): OutOfMemory!str from a` | fetch, procman, raft | fetch | fetch/time-cond-304 (windows), fetch/time-cond-older (windows) | covered (executed) |
| `error TimedOut` | kvd, procman, raft | chat, fetch, fm and 1 more | - | covered (referenced) |
| `error BadName` | cron, procman, raft | cron | - | covered (referenced) |
| `error BadData` | cron, procman, raft | cron | - | covered (referenced) |
| `error NotFound` | cron, procman, raft | cron | - | covered (referenced) |
| `error NoDatabase` | cron, procman, raft | cron | - | covered (referenced) |
| `error Skipped` | cron, procman, raft | cron | - | covered (referenced) |
| `error Repeated` | cron, procman, raft | cron | - | covered (referenced) |
| `error OutOfRange` | cron, procman, raft | cron | - | covered (referenced) |
| `type Error = BadName \| BadData \| NotFound \| NoDatabase \| Skipped \| Repeated \| OutOfRange \| Canceled` | procman, raft | cron, fm | - | covered (referenced) |
| `struct Date {` | cron, fm, procman and 1 more | cron, fm | - | covered (referenced) |
| `struct Database {` | cron, procman, raft | cron | - | covered (referenced) |

## std.mmio

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `mmio.Load[T: Plain](p: *T): T` | kernel | kernel | - | exempt (execution) (EXEMPT.md line 28) |
| `mmio.Store[T: Plain](p: *T, x: T)` | kernel | kernel | - | exempt (execution) (EXEMPT.md line 29) |
| `mmio.Fence()` | kernel | kernel | - | exempt (execution) (EXEMPT.md line 30) |
| `Reg[T].New(addr: uint): Reg[T]` | kernel | kernel | - | exempt (execution) (EXEMPT.md line 31) |
| `Reg[T].Read(&self): T` | kernel | kernel | - | exempt (execution) (EXEMPT.md line 32) |
| `Reg[T].Write(&self, x: T)` | kernel | kernel | - | exempt (execution) (EXEMPT.md line 33) |

## std.math

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `math.AddOverflow[T: Integer](a: T, b: T): (value: T, overflow: bool)` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `math.SubOverflow[T: Integer](a: T, b: T): (value: T, overflow: bool)` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `math.MulOverflow[T: Integer](a: T, b: T): (value: T, overflow: bool)` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `math.WrapI32(x: f64): i32` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `math.WrapU32(x: f64): u32` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `math.CastSat[T: Integer](x: f64): T` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `math.CastSatF32[T: Integer](x: f32): T` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `math.MulWide(a: u64, b: u64): (hi: u64, lo: u64)` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `math.DivWide(hi: u64, lo: u64, d: u64): (q: u64, r: u64)` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `math.AddCarry(x: u64, y: u64, c: u64): (sum: u64, carry: u64)` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `math.SubBorrow(x: u64, y: u64, b: u64): (diff: u64, borrow: u64)` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `math.Min[T: Ord + Plain](a: T, b: T): T` | imgproc | imgproc, raft, tracer | imgproc/ascii-ppm-blur (windows), imgproc/bench (windows), imgproc/binary-stdout (windows) and 133 more | covered (executed) |
| `math.Max[T: Ord + Plain](a: T, b: T): T` | imgproc | imgproc, raft, tracer | imgproc/ascii-pgm-invert (windows), imgproc/ascii-ppm-blur (windows), imgproc/bench (windows) and 150 more | covered (executed) |
| `math.Abs(x: f64): f64` | imgproc | imgproc, tracer | imgproc/ascii-ppm-blur (windows), imgproc/bench (windows), imgproc/carriers-1 (windows) and 46 more | covered (executed) |
| `math.Sqrt(x: f64): f64` | imgproc | imgproc, tracer | imgproc/ascii-ppm-blur (windows), imgproc/bad-kernel (windows), imgproc/bench (windows) and 62 more | covered (executed) |
| `Rng.New(seed: u64): Rng` | raft | imgproc, raft | imgproc/bench (windows), raft/bench (windows), raft/crash-seeds (windows) and 20 more | covered (executed) |
| `Rng.FromIo(io: Io): Canceled!Rng` | raft | raft | raft/bench (windows), raft/crash-seeds (windows), raft/crash-verbose (windows) and 19 more | covered (executed) |
| `Rng.U64(mut self): u64` | raft | raft | imgproc/bench (windows), raft/bench (windows), raft/crash-seeds (windows) and 20 more | covered (executed) |
| `Rng.Below(mut self, n: u64): u64` | raft | imgproc, raft | imgproc/bench (windows), raft/bench (windows), raft/crash-seeds (windows) and 20 more | covered (executed) |
| `Rng.Float(mut self): f64` | raft | raft | raft/bench (windows), raft/crash-seeds (windows), raft/faults (windows) and 13 more | covered (executed) |
| `Rng.Fill(mut self, buf: mut []u8)` | raft | raft | raft/faults (windows), raft/tasks-verbose (windows) | covered (executed) |
| `math.Floor(x: f64): f64` | tracer | tracer | tracer/plot-every (windows), tracer/table-f64 (windows) | covered (executed) |
| `math.Ceil(x: f64): f64` | tracer | tracer | tracer/plot-every (windows), tracer/table-f64 (windows) | covered (executed) |
| `math.Round(x: f64): f64` | tracer | tracer | tracer/plot-every (windows), tracer/table-f64 (windows) | covered (executed) |
| `math.RoundEven(x: f64): f64` | tracer | tracer | tracer/plot-every (windows), tracer/table-f64 (windows) | covered (executed) |
| `math.RoundToZero(x: f64): f64` | tracer | tracer | tracer/plot-every (windows), tracer/table-f64 (windows) | covered (executed) |
| `math.Fract(x: f64): f64` | tracer | tracer | tracer/bench (windows), tracer/carriers-1 (windows), tracer/carriers-16 (windows) and 9 more | covered (executed) |
| `math.Mod(x: f64, y: f64): f64` | tracer | tracer | tracer/missing-dir (windows), tracer/plot-ascii (windows), tracer/render-16 (windows) and 2 more | covered (executed) |
| `math.Pow(x: f64, y: f64): f64` | tracer | tracer | tracer/bench (windows), tracer/carriers-1 (windows), tracer/carriers-16 (windows) and 16 more | covered (executed) |
| `math.Exp(x: f64): f64` | tracer | tracer | tracer/plot-f64 (windows), tracer/table-f32 (windows), tracer/table-f64 (windows) | covered (executed) |
| `math.Exp2(x: f64): f64` | tracer | tracer | tracer/plot-every (windows), tracer/plot-every-f32 (windows), tracer/table-f32 (windows) and 1 more | covered (executed) |
| `math.Expm1(x: f64): f64` | tracer | tracer | tracer/plot-every (windows), tracer/plot-every-f32 (windows), tracer/table-f32 (windows) and 1 more | covered (executed) |
| `math.Log(x: f64): f64` | tracer | tracer | tracer/plot-nan (windows), tracer/table-f32 (windows), tracer/table-f64 (windows) | covered (executed) |
| `math.Log2(x: f64): f64` | tracer | tracer | tracer/table-f32 (windows), tracer/table-f64 (windows) | covered (executed) |
| `math.Log10(x: f64): f64` | tracer | tracer | tracer/plot-ascii (windows), tracer/plot-every (windows), tracer/plot-every-f32 (windows) and 6 more | covered (executed) |
| `math.Log1p(x: f64): f64` | tracer | tracer | tracer/plot-every (windows), tracer/plot-every-f32 (windows), tracer/table-f32 (windows) and 1 more | covered (executed) |
| `math.Sin(x: f64): f64` | tracer | tracer | tracer/bad-face (windows), tracer/bad-grid (windows), tracer/bench (windows) and 14 more | covered (executed) |
| `math.Cos(x: f64): f64` | tracer | tracer | tracer/bad-face (windows), tracer/bad-grid (windows), tracer/bench (windows) and 13 more | covered (executed) |
| `math.Tan(x: f64): f64` | tracer | tracer | tracer/bench (windows), tracer/carriers-1 (windows), tracer/carriers-16 (windows) and 11 more | covered (executed) |
| `math.Asin(x: f64): f64` | tracer | tracer | tracer/plot-every (windows), tracer/plot-every-f32 (windows), tracer/table-f32 (windows) and 1 more | covered (executed) |
| `math.Acos(x: f64): f64` | tracer | tracer | tracer/bench (windows), tracer/carriers-1 (windows), tracer/carriers-16 (windows) and 11 more | covered (executed) |
| `math.Atan(x: f64): f64` | tracer | tracer | tracer/plot-every (windows), tracer/plot-every-f32 (windows), tracer/table-f32 (windows) and 1 more | covered (executed) |
| `math.Atan2(y: f64, x: f64): f64` | tracer | tracer | tracer/bench (windows), tracer/carriers-1 (windows), tracer/carriers-16 (windows) and 11 more | covered (executed) |
| `math.Sinh(x: f64): f64` | tracer | tracer | tracer/plot-every (windows), tracer/plot-every-f32 (windows), tracer/table-f32 (windows) and 1 more | covered (executed) |
| `math.Cosh(x: f64): f64` | tracer | tracer | tracer/plot-every (windows), tracer/plot-every-f32 (windows), tracer/table-f32 (windows) and 1 more | covered (executed) |
| `math.Tanh(x: f64): f64` | tracer | tracer | tracer/plot-every (windows), tracer/plot-every-f32 (windows), tracer/table-f32 (windows) and 1 more | covered (executed) |
| `math.Asinh(x: f64): f64` | tracer | tracer | tracer/plot-every (windows), tracer/plot-every-f32 (windows), tracer/table-f32 (windows) and 1 more | covered (executed) |
| `math.Acosh(x: f64): f64` | tracer | tracer | tracer/plot-every (windows), tracer/plot-every-f32 (windows), tracer/table-f32 (windows) and 1 more | covered (executed) |
| `math.Atanh(x: f64): f64` | tracer | tracer | tracer/plot-every (windows), tracer/plot-every-f32 (windows), tracer/table-f32 (windows) and 1 more | covered (executed) |
| `math.Hypot(x: f64, y: f64): f64` | tracer | tracer | tracer/bad-camera (windows), tracer/bench (windows), tracer/carriers-1 (windows) and 14 more | covered (executed) |
| `math.Cbrt(x: f64): f64` | tracer | tracer | tracer/bench (windows), tracer/carriers-1 (windows), tracer/carriers-16 (windows) and 9 more | covered (executed) |
| `math.Fma(x: f64, y: f64, z: f64): f64` | tracer | tracer | tracer/plot-ascii (windows), tracer/table-f64 (windows), tracer/table-some (windows) | covered (executed) |
| `math.CopySign(x: f64, sign: f64): f64` | tracer | tracer | tracer/bench (windows), tracer/carriers-1 (windows), tracer/carriers-16 (windows) and 11 more | covered (executed) |
| `math.FrExp(x: f64): (frac: f64, exp: int)` | tracer | tracer | tracer/plot-every (windows), tracer/table-f64 (windows), tracer/table-some (windows) | covered (executed) |
| `math.LdExp(frac: f64, exp: int): f64` | tracer | tracer | tracer/bench (windows), tracer/carriers-1 (windows), tracer/carriers-16 (windows) and 12 more | covered (executed) |
| `math.NextAfter(x: f64, toward: f64): f64` | tracer | tracer | tracer/plot-every (windows), tracer/table-f64 (windows) | covered (executed) |
| `math.IsNan(x: f64): bool` | tracer | tracer | tracer/bench (windows), tracer/carriers-1 (windows), tracer/carriers-16 (windows) and 13 more | covered (executed) |
| `math.IsInf(x: f64): bool` | tracer | tracer | tracer/plot-every (windows), tracer/table-f64 (windows) | covered (executed) |
| `math.IsFinite(x: f64): bool` | tracer | tracer | tracer/bad-camera (windows), tracer/bad-face (windows), tracer/bad-grid (windows) and 29 more | covered (executed) |
| `math.SqrtF32(x: f32): f32` | tracer | tracer | tracer/table-f32 (windows) | covered (executed) |
| `math.FloorF32(x: f32): f32` | tracer | tracer | tracer/plot-every-f32 (windows), tracer/table-f32 (windows) | covered (executed) |
| `math.CeilF32(x: f32): f32` | tracer | tracer | tracer/plot-every-f32 (windows), tracer/table-f32 (windows) | covered (executed) |
| `math.RoundF32(x: f32): f32` | tracer | tracer | tracer/plot-every-f32 (windows), tracer/table-f32 (windows) | covered (executed) |
| `math.RoundEvenF32(x: f32): f32` | tracer | tracer | tracer/plot-every-f32 (windows), tracer/table-f32 (windows) | covered (executed) |
| `math.RoundToZeroF32(x: f32): f32` | tracer | tracer | tracer/plot-every-f32 (windows), tracer/table-f32 (windows) | covered (executed) |
| `math.FractF32(x: f32): f32` | tracer | tracer | tracer/plot-every-f32 (windows), tracer/table-f32 (windows) | covered (executed) |
| `math.ModF32(x: f32, y: f32): f32` | tracer | tracer | tracer/table-f32 (windows) | covered (executed) |
| `math.PowF32(x: f32, y: f32): f32` | tracer | tracer | tracer/plot-every-f32 (windows), tracer/table-f32 (windows) | covered (executed) |
| `math.ExpF32(x: f32): f32` | tracer | tracer | tracer/table-f32 (windows) | covered (executed) |
| `math.Exp2F32(x: f32): f32` | tracer | tracer | tracer/plot-every-f32 (windows), tracer/table-f32 (windows) | covered (executed) |
| `math.Expm1F32(x: f32): f32` | tracer | tracer | tracer/plot-every-f32 (windows), tracer/table-f32 (windows) | covered (executed) |
| `math.LogF32(x: f32): f32` | tracer | tracer | tracer/table-f32 (windows) | covered (executed) |
| `math.Log2F32(x: f32): f32` | tracer | tracer | tracer/table-f32 (windows) | covered (executed) |
| `math.Log10F32(x: f32): f32` | tracer | tracer | tracer/plot-every-f32 (windows), tracer/table-f32 (windows) | covered (executed) |
| `math.Log1pF32(x: f32): f32` | tracer | tracer | tracer/plot-every-f32 (windows), tracer/table-f32 (windows) | covered (executed) |
| `math.SinF32(x: f32): f32` | tracer | tracer | tracer/table-f32 (windows), tracer/table-some (windows) | covered (executed) |
| `math.CosF32(x: f32): f32` | tracer | tracer | tracer/plot-every-f32 (windows), tracer/table-f32 (windows) | covered (executed) |
| `math.TanF32(x: f32): f32` | tracer | tracer | tracer/plot-f32 (windows), tracer/table-f32 (windows) | covered (executed) |
| `math.AsinF32(x: f32): f32` | tracer | tracer | tracer/plot-every-f32 (windows), tracer/table-f32 (windows) | covered (executed) |
| `math.AcosF32(x: f32): f32` | tracer | tracer | tracer/plot-every-f32 (windows), tracer/table-f32 (windows) | covered (executed) |
| `math.AtanF32(x: f32): f32` | tracer | tracer | tracer/plot-every-f32 (windows), tracer/table-f32 (windows) | covered (executed) |
| `math.Atan2F32(y: f32, x: f32): f32` | tracer | tracer | tracer/plot-every-f32 (windows), tracer/table-f32 (windows) | covered (executed) |
| `math.SinhF32(x: f32): f32` | tracer | tracer | tracer/plot-every-f32 (windows), tracer/table-f32 (windows) | covered (executed) |
| `math.CoshF32(x: f32): f32` | tracer | tracer | tracer/plot-every-f32 (windows), tracer/table-f32 (windows) | covered (executed) |
| `math.TanhF32(x: f32): f32` | tracer | tracer | tracer/plot-every-f32 (windows), tracer/table-f32 (windows) | covered (executed) |
| `math.AsinhF32(x: f32): f32` | tracer | tracer | tracer/plot-every-f32 (windows), tracer/table-f32 (windows) | covered (executed) |
| `math.AcoshF32(x: f32): f32` | tracer | tracer | tracer/plot-every-f32 (windows), tracer/table-f32 (windows) | covered (executed) |
| `math.AtanhF32(x: f32): f32` | tracer | tracer | tracer/plot-every-f32 (windows), tracer/table-f32 (windows) | covered (executed) |
| `math.HypotF32(x: f32, y: f32): f32` | tracer | tracer | tracer/table-f32 (windows) | covered (executed) |
| `math.CbrtF32(x: f32): f32` | tracer | tracer | tracer/plot-every-f32 (windows), tracer/table-f32 (windows) | covered (executed) |
| `math.FmaF32(x: f32, y: f32, z: f32): f32` | tracer | tracer | tracer/table-f32 (windows), tracer/table-some (windows) | covered (executed) |
| `math.CopySignF32(x: f32, sign: f32): f32` | tracer | tracer | tracer/plot-every-f32 (windows), tracer/table-f32 (windows) | covered (executed) |
| `math.FrExpF32(x: f32): (frac: f32, exp: int)` | tracer | tracer | tracer/plot-every-f32 (windows), tracer/table-f32 (windows), tracer/table-some (windows) | covered (executed) |
| `math.LdExpF32(frac: f32, exp: int): f32` | tracer | tracer | tracer/plot-every-f32 (windows), tracer/table-f32 (windows), tracer/table-some (windows) | covered (executed) |
| `math.NextAfterF32(x: f32, toward: f32): f32` | tracer | tracer | tracer/plot-every-f32 (windows), tracer/table-f32 (windows) | covered (executed) |
| `math.IsNanF32(x: f32): bool` | tracer | tracer | tracer/plot-every-f32 (windows), tracer/table-f32 (windows), tracer/table-some (windows) | covered (executed) |
| `math.IsInfF32(x: f32): bool` | tracer | tracer | tracer/plot-every-f32 (windows), tracer/table-f32 (windows) | covered (executed) |
| `math.IsFiniteF32(x: f32): bool` | tracer | tracer | tracer/plot-every-f32 (windows), tracer/table-f32 (windows) | covered (executed) |
| `math.LeadingZeros64(x: u64): int` | tracer | tracer | tracer/bench (windows), tracer/bits (windows), tracer/carriers-1 (windows) and 10 more | covered (executed) |
| `math.LeadingZeros32(x: u32): int` | tracer | tracer | tracer/bits (windows) | covered (executed) |
| `math.LeadingZeros16(x: u16): int` | tracer | tracer | tracer/bits (windows) | covered (executed) |
| `math.LeadingZeros8(x: u8): int` | tracer | tracer | tracer/bits (windows) | covered (executed) |
| `math.TrailingZeros64(x: u64): int` | tracer | tracer | tracer/bench (windows), tracer/bits (windows), tracer/carriers-1 (windows) and 10 more | covered (executed) |
| `math.TrailingZeros32(x: u32): int` | tracer | tracer | tracer/bits (windows) | covered (executed) |
| `math.TrailingZeros16(x: u16): int` | tracer | tracer | tracer/bits (windows) | covered (executed) |
| `math.TrailingZeros8(x: u8): int` | tracer | tracer | tracer/bits (windows) | covered (executed) |
| `math.OnesCount64(x: u64): int` | tracer | tracer | tracer/bits (windows) | covered (executed) |
| `math.OnesCount32(x: u32): int` | tracer | tracer | tracer/bits (windows) | covered (executed) |
| `math.OnesCount16(x: u16): int` | tracer | tracer | tracer/bits (windows) | covered (executed) |
| `math.OnesCount8(x: u8): int` | tracer | tracer | tracer/bits (windows) | covered (executed) |
| `math.RotateLeft64(x: u64, n: int): u64` | tracer | tracer | tracer/bench (windows), tracer/bits (windows), tracer/carriers-1 (windows) and 9 more | covered (executed) |
| `math.RotateLeft32(x: u32, n: int): u32` | tracer | tracer | tracer/bench (windows), tracer/bits (windows), tracer/carriers-1 (windows) and 8 more | covered (executed) |
| `math.RotateLeft16(x: u16, n: int): u16` | tracer | tracer | tracer/bits (windows) | covered (executed) |
| `math.RotateLeft8(x: u8, n: int): u8` | tracer | tracer | tracer/bits (windows) | covered (executed) |
| `math.RotateRight64(x: u64, n: int): u64` | tracer | tracer | tracer/bits (windows) | covered (executed) |
| `math.RotateRight32(x: u32, n: int): u32` | tracer | tracer | tracer/bench (windows), tracer/bits (windows), tracer/carriers-1 (windows) and 8 more | covered (executed) |
| `math.RotateRight16(x: u16, n: int): u16` | tracer | tracer | tracer/bits (windows) | covered (executed) |
| `math.RotateRight8(x: u8, n: int): u8` | tracer | tracer | tracer/bits (windows) | covered (executed) |
| `math.ReverseBits64(x: u64): u64` | tracer | tracer | tracer/bits (windows) | covered (executed) |
| `math.ReverseBits32(x: u32): u32` | tracer | tracer | tracer/bench (windows), tracer/bits (windows), tracer/carriers-1 (windows) and 8 more | covered (executed) |
| `math.ReverseBits16(x: u16): u16` | tracer | tracer | tracer/bits (windows) | covered (executed) |
| `math.ReverseBits8(x: u8): u8` | tracer | tracer | tracer/bits (windows) | covered (executed) |
| `math.ReverseBytes64(x: u64): u64` | tracer | tracer | tracer/bits (windows) | covered (executed) |
| `math.ReverseBytes32(x: u32): u32` | tracer | tracer | tracer/bench (windows), tracer/bits (windows), tracer/carriers-1 (windows) and 8 more | covered (executed) |
| `math.ReverseBytes16(x: u16): u16` | tracer | tracer | tracer/bits (windows), tracer/render-16 (windows) | covered (executed) |
| `math.Gcd(a: u64, b: u64): u64` | tracer | tracer | tracer/bench (windows), tracer/bits (windows), tracer/carriers-1 (windows) and 15 more | covered (executed) |
| `math.CheckedPow(x: i64, n: u32): ?i64` | tracer | tracer | tracer/bad-grid (windows), tracer/bench (windows), tracer/bits (windows) and 11 more | covered (executed) |
| `math.CheckedPowU64(x: u64, n: u32): ?u64` | tracer | tracer | tracer/bad-grid (windows), tracer/bench (windows), tracer/bits (windows) and 11 more | covered (executed) |
| `math.ShortestDigits(x: f64): Digits` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `math.ExactDigits(x: f64, n: int, mode: DigitMode): Digits` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `struct Digits {` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `enum DigitMode {` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `interface Integer {` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |

## std.simd

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `Vec[T, N].Splat(x: T): Vec[T, N] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc, gen: mgrep | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].Zero(): Vec[T, N] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].Iota(): Vec[T, N] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].FromArray(a: [N]T): Vec[T, N] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].ToArray(self): [N]T requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].FromSlice(xs: []T, i: int): Vec[T, N] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc, gen: mgrep | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].FromSliceOr(xs: []T, i: int, fill: Vec[T, N]): Vec[T, N] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].CopyTo(self, xs: mut []T, i: int) requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].CopyToMasked(self, xs: mut []T, i: int, m: Mask[T, N]) requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].Gather[I: Lane](xs: []T, idx: Vec[I, N]): Vec[T, N] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].Lane(self, const i: int): T requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64 requires i >= 0 && i < N` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].With(self, const i: int, x: T): Vec[T, N] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64 requires i >= 0 && i < N` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].Shuffle(self, b: Vec[T, N], const idx: [N]int): Vec[T, N] from self, b requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].Reverse(self): Vec[T, N] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].Broadcast(self, const i: int): Vec[T, N] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64 requires i >= 0 && i < N` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].Rotate(self, const k: int): Vec[T, N] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64 requires k >= 0 && k < N` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].Slide(self, prev: Vec[T, N], const k: int): Vec[T, N] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64 requires k >= 0 && k <= N` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].InterleaveLo(self, b: Vec[T, N]): Vec[T, N] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].InterleaveHi(self, b: Vec[T, N]): Vec[T, N] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].DeinterleaveEven(self, b: Vec[T, N]): Vec[T, N] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].DeinterleaveOdd(self, b: Vec[T, N]): Vec[T, N] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].ReduceSum(self): T requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].ReduceSumWrap(self): T requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].ReduceSumWide(self): i64 requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].ReduceMin(self): T requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].ReduceMax(self): T requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].ReduceAnd(self): T requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].ReduceOr(self): T requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].ReduceXor(self): T requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].Min(self, b: Vec[T, N]): Vec[T, N] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].Max(self, b: Vec[T, N]): Vec[T, N] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].Abs(self): Vec[T, N] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].AbsWrap(self): Vec[T, N] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].AddSat(self, b: Vec[T, N]): Vec[T, N] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].SubSat(self, b: Vec[T, N]): Vec[T, N] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].MulHigh(self, b: Vec[T, N]): Vec[T, N] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].DivTrunc(self, b: Vec[T, N]): Vec[T, N] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].ShlEach(self, k: Vec[T, N]): Vec[T, N] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].ShrEach(self, k: Vec[T, N]): Vec[T, N] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].Fma(self, b: Vec[T, N], c: Vec[T, N]): Vec[T, N] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].Sqrt(self): Vec[T, N] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].Floor(self): Vec[T, N] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].Ceil(self): Vec[T, N] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].RoundEven(self): Vec[T, N] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].RoundToZero(self): Vec[T, N] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].WidenLo[U: Lane, const M: int](self): Vec[U, M] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64 requires M * 2 == N && M >= 2 && SizeOf[U]() == 2 * SizeOf[T]()` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].WidenHi[U: Lane, const M: int](self): Vec[U, M] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64 requires M * 2 == N && M >= 2 && SizeOf[U]() == 2 * SizeOf[T]()` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].MulWidenLo[U: Lane, const M: int](self, b: Vec[T, N]): Vec[U, M] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64 requires M * 2 == N && M >= 2 && SizeOf[U]() == 2 * SizeOf[T]()` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].MulWidenHi[U: Lane, const M: int](self, b: Vec[T, N]): Vec[U, M] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64 requires M * 2 == N && M >= 2 && SizeOf[U]() == 2 * SizeOf[T]()` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].NarrowSat[U: Lane, const M: int](self, b: Vec[T, N]): Vec[U, M] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64 requires M == 2 * N && M <= 64 && 2 * SizeOf[U]() == SizeOf[T]()` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].Cast[U: Lane](self): Vec[U, N] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].Trunc[U: Lane](self): Vec[U, N] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].CastSat[U: Lane](self): Vec[U, N] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Vec[T, N].Reinterpret[U: Lane, const M: int](self): Vec[U, M] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64 requires M * SizeOf[U]() == N * SizeOf[T]() && M >= 2 && M <= 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Mask[T, N].FromBits(b: u64): Mask[T, N] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Mask[T, N].Bits(self): u64 requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc, gen: mgrep | - | covered (referenced; no instrumented body: compiled inline) |
| `Mask[T, N].Any(self): bool requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Mask[T, N].All(self): bool requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Mask[T, N].CountSet(self): int requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Mask[T, N].FirstSet(self): ?int requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Mask[T, N].Select(self, a: Vec[T, N], b: Vec[T, N]): Vec[T, N] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `Mask[T, N].Cast[U: Lane](self): Mask[U, N] requires N == 2 \|\| N == 4 \|\| N == 8 \|\| N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `simd.Lookup16[const N: int](idx: Vec[u8, N], table: Vec[u8, 16]): Vec[u8, N] requires N == 16 \|\| N == 32 \|\| N == 64` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `simd.Lanes[T: Lane](): int` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `simd.Cpu(): CpuFeatures` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `CpuFeatures.Of(const names: str): CpuFeatures` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `CpuFeatures.Has(&self, const name: str): bool` | imgproc | imgproc | - | covered (referenced; no instrumented body: compiled inline) |
| `CpuFeatures.HasAll(&self, other: &CpuFeatures): bool` | imgproc | imgproc | imgproc/ascii-pgm-invert (windows), imgproc/ascii-ppm-blur (windows), imgproc/bad-header (windows) and 71 more | covered (executed) |
| `simd.Dispatch[F: Plain](choices: []Choice[F], base: F): F` | imgproc | imgproc | imgproc/ascii-pgm-invert (windows), imgproc/ascii-ppm-blur (windows), imgproc/bad-header (windows) and 60 more | covered (executed) |
| `simd.IndexByte(s: []u8, b: u8): ?int` | mgrep | mgrep, gen: orderbook | bigcalc/bench (windows), bigcalc/run_script (windows), chat/bad-hex (windows) and 298 more | covered (executed) |
| `simd.IndexAny(s: []u8, set: []u8): ?int` | mgrep | mgrep, gen: orderbook | mgrep/byteclass (windows), mgrep/fold (windows), orderbook/csv-book (windows) and 2 more | covered (executed) |
| `simd.Count(s: []u8, b: u8): int` | mgrep | mgrep | mgrep/before (windows), mgrep/bench (windows), mgrep/big (windows) and 35 more | covered (executed) |
| `simd.Equal(a: []u8, b: []u8): bool` | imgproc, mgrep | imgproc, mgrep | chat/shutdown-connected (windows), fm/batch (windows), fm/cp-into-itself (windows) and 33 more | covered (executed) |
| `simd.Compare(a: []u8, b: []u8): int` | imgproc, mgrep | imgproc, mgrep | imgproc/compare-differ (windows), mgrep/bench (windows), mgrep/carriers1 (windows) and 95 more | covered (executed) |
| `simd.ValidUtf8(s: []u8): bool` | mgrep | mgrep | bigcalc/calc_bits (windows), bigcalc/cert_der_leaf (windows), bigcalc/cert_der_pem (windows) and 460 more | covered (executed) |
| `simd.ValidWtf8(s: []u8): bool` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `simd.IsAscii(s: []u8): bool` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `simd.Utf16Len(s: str): int` | - | - | textkit/utf16-encode (windows), textkit/utf16-encode-big-bom (windows), textkit/utf16-encode-hex (windows) | covered (executed) |
| `simd.LastIndexByte(s: []u8, b: u8): ?int` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `simd.Dot[T: Lane](a: []T, b: []T): T` | imgproc | imgproc | imgproc/ascii-ppm-blur (windows), imgproc/bench (windows), imgproc/carriers-1 (windows) and 25 more | covered (executed) |
| `simd.Sum[T: Lane](xs: []T): T` | imgproc | imgproc | imgproc/chain-color-auto (windows), imgproc/chain-color-scalar (windows), imgproc/chain-color-v128 (windows) and 18 more | covered (executed) |
| `interface Lane {` | imgproc | imgproc | - | covered (referenced) |
| `struct Choice[F] {` | imgproc | imgproc | - | covered (referenced) |

## std.free

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `free.Heap(): &Heap` | kernel | kernel | - | exempt (execution) (EXEMPT.md line 37) |
| `struct PanicInfo {` | kernel | kernel | - | covered (referenced) |

## std.mem

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `type AllocTable = core.AllocTable` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `mem.OffsetOf[T](field: str): int` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `mem.Prefetch[T](p: &T, write: bool)` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Fixed.New(buf: mut []u8): Fixed` | cron, imgproc, kernel and 1 more | bigcalc, chat, cron and 11 more | bigcalc/bench (windows), bigcalc/calc_arith (windows), bigcalc/calc_bits (windows) and 239 more | covered (executed) |
| `Fixed.Alloc(&self): &Alloc from self` | cron, imgproc, kernel and 1 more | bigcalc, chat, cron and 11 more | bigcalc/run_mem_limit (windows), bigcalc/run_mem_within (windows), chat/bad-hex (windows) and 168 more | covered (executed) |
| `Fixed.Reset(mut self) ends self` | imgproc, kernel | imgproc, kernel | imgproc/ascii-pgm-invert (windows), imgproc/ascii-ppm-blur (windows), imgproc/bench (windows) and 105 more | covered (executed) |
| `Counting.New(inner: &Alloc): Counting` | orderbook, pack | chat, kvd, orderbook and 2 more | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 146 more | covered (executed) |
| `Counting.Alloc(&self): &Alloc from self` | orderbook, pack | chat, kvd, orderbook and 2 more | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 146 more | covered (executed) |
| `Counting.Bytes(&self): int` | orderbook, pack | orderbook, pack | orderbook/band (windows), orderbook/band-full (windows), orderbook/basic (windows) and 22 more | covered (executed) |
| `Counting.Live(&self): int` | orderbook, pack | chat, orderbook, pack and 1 more | chat/basic (windows), chat/basic-one-carrier (windows), chat/many-carriers (windows) and 40 more | covered (executed) |
| `Counting.Peak(&self): int` | orderbook, pack | chat, kvd, orderbook and 2 more | chat/basic (windows), chat/basic-one-carrier (windows), chat/many-carriers (windows) and 43 more | covered (executed) |
| `mem.Null(): &Alloc` | pack | pack | pack/fingerprint-bad (windows), pack/fingerprint-bad-base64 (windows), pack/fingerprint-base64 (windows) and 55 more | covered (executed) |
| `mem.Custom(ctx: *u8, table: &AllocTable): Alloc from table` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Pool[T].New(a: &Arena): Pool[T]` | lox, orderbook | lox, orderbook | lox/arith (windows), lox/bench (windows), lox/compile-errors (windows) and 54 more | covered (executed) |
| `Pool[T].Add(mut self, x: T into self): OutOfMemory!Handle` | lox, orderbook | lox, orderbook | lox/arith (windows), lox/bench (windows), lox/compile-errors (windows) and 52 more | covered (executed) |
| `Pool[T].Get(&self, h: Handle): ?&T` | lox, orderbook | lox, orderbook | lox/arith (windows), lox/bench (windows), lox/control (windows) and 45 more | covered (executed) |
| `Pool[T].GetMut(mut self, h: Handle): ?mut T` | lox, orderbook | lox, orderbook | lox/bench (windows), lox/gc-limit (windows), lox/gc-log (windows) and 21 more | covered (executed) |
| `Pool[T].Free(mut self, h: Handle): bool` | orderbook | orderbook | orderbook/budget (windows), orderbook/carriers-1 (windows), orderbook/carriers-16 (windows) and 1 more | covered (executed) |
| `Pool[T].Len(&self): int` | lox, orderbook | lox, orderbook | lox/arith (windows), lox/bench (windows), lox/control (windows) and 37 more | covered (executed) |
| `Pool[T].Iter(&self): PoolIter[T] from self` | lox, orderbook | lox, orderbook | lox/bench (windows), lox/gc-limit (windows), lox/gc-log (windows) and 11 more | covered (executed) |
| `PoolIter[T].Next(mut self): ?(handle: Handle, value: &T) from self.pool` | lox, orderbook | lox, orderbook | lox/bench (windows), lox/gc-limit (windows), lox/gc-log (windows) and 11 more | covered (executed) |
| `Pool[T].Sweep(mut self, const keep: fn(Handle, &T): bool): int` | lox, orderbook | lox, orderbook | lox/bench (windows), lox/gc-limit (windows), lox/gc-log (windows) and 6 more | covered (executed) |
| `Pool[T].GetPair(mut self, h: Handle, k: Handle): ?(a: mut T, b: mut T) from self` | lox, orderbook | lox, orderbook | lox/lists (windows), orderbook/account-missing (windows), orderbook/band (windows) and 19 more | covered (executed) |

## std.coll

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `Deque[T].New(a: &Arena): Deque[T]` | lox, mgrep, orderbook | lox, mgrep, orderbook | lox/arith (windows), lox/bench (windows), lox/compile-errors (windows) and 91 more | covered (executed) |
| `Deque[T].Push(mut self, x: T into self): OutOfMemory!void` | lox, mgrep, orderbook | lox, mgrep, orderbook | lox/bench (windows), lox/deep-recursion (windows), lox/deep-recursion-limit (windows) and 39 more | covered (executed) |
| `Deque[T].PushFront(mut self, x: T into self): OutOfMemory!void` | orderbook | orderbook | orderbook/account-missing (windows), orderbook/band (windows), orderbook/band-full (windows) and 15 more | covered (executed) |
| `Deque[T].Pop(mut self): ?T` | lox, mgrep, orderbook | lox, mgrep, orderbook | lox/arith (windows), lox/bench (windows), lox/control (windows) and 30 more | covered (executed) |
| `Deque[T].PopFront(mut self): ?T` | mgrep, orderbook | mgrep, orderbook | mgrep/before (windows), mgrep/carriers1 (windows), mgrep/carriers16 (windows) and 31 more | covered (executed) |
| `Deque[T].Get(&self, i: int): ?&T` | lox, mgrep, orderbook | lox, mgrep, orderbook | lox/deep-recursion (windows), lox/deep-recursion-limit (windows), lox/repl (windows) and 11 more | covered (executed) |
| `Deque[T].GetMut(mut self, i: int): ?mut T` | orderbook | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Deque[T].Len(&self): int` | lox, mgrep, orderbook | lox, mgrep, orderbook | lox/arith (windows), lox/bench (windows), lox/control (windows) and 58 more | covered (executed) |
| `HeapDeque[T].New(a: &Alloc): HeapDeque[T]` | orderbook, raft | orderbook, raft | orderbook/account-missing (windows), orderbook/bad-record (windows), orderbook/bad-side (windows) and 49 more | covered (executed) |
| `HeapDeque[T].Push(mut self, a: &Alloc, x: T into self): OutOfMemory!void` | orderbook, raft | orderbook, raft | orderbook/account-missing (windows), orderbook/bad-record (windows), orderbook/bad-side (windows) and 48 more | covered (executed) |
| `HeapDeque[T].PushFront(mut self, a: &Alloc, x: T into self): OutOfMemory!void` | orderbook, raft | orderbook, raft | orderbook/account-missing (windows), orderbook/band (windows), orderbook/band-full (windows) and 20 more | covered (executed) |
| `HeapDeque[T].Pop(mut self): ?T` | orderbook, raft | orderbook, raft | orderbook/account-missing (windows), orderbook/band (windows), orderbook/band-full (windows) and 13 more | covered (executed) |
| `HeapDeque[T].PopFront(mut self): ?T` | orderbook, raft | orderbook, raft | orderbook/account-missing (windows), orderbook/band (windows), orderbook/band-full (windows) and 39 more | covered (executed) |
| `HeapDeque[T].Get(&self, i: int): ?&T` | orderbook, raft | orderbook, raft | orderbook/account-missing (windows), orderbook/band (windows), orderbook/band-full (windows) and 14 more | covered (executed) |
| `HeapDeque[T].Len(&self): int` | orderbook, raft | orderbook, raft | orderbook/account-missing (windows), orderbook/band (windows), orderbook/band-full (windows) and 34 more | covered (executed) |
| `HeapDeque[T].Free(self, a: &Alloc)` | orderbook, raft | orderbook, raft | orderbook/account-missing (windows), orderbook/bad-record (windows), orderbook/bad-side (windows) and 48 more | covered (executed) |
| `HeapDeque[T].FreeEmpty(self, a: &Alloc)` | orderbook, raft | raft | raft/bench (windows), raft/crash-seeds (windows), raft/crash-verbose (windows) and 17 more | covered (executed) |
| `HeapDeque[T].FreeEach(self, a: &Alloc, const release: fn(T))` | orderbook, raft | orderbook, raft | orderbook/band (windows), orderbook/band-full (windows), orderbook/basic (windows) and 35 more | covered (executed) |
| `PriorityQueue[T].New(a: &Arena): PriorityQueue[T]` | orderbook, raft | orderbook, raft | orderbook/account-missing (windows), orderbook/bad-id (windows), orderbook/bad-price (windows) and 51 more | covered (executed) |
| `PriorityQueue[T].Push(mut self, x: T into self): OutOfMemory!void` | orderbook, raft | orderbook, raft | orderbook/account-missing (windows), orderbook/band (windows), orderbook/band-full (windows) and 37 more | covered (executed) |
| `PriorityQueue[T].Pop(mut self): ?T` | orderbook, raft | orderbook, raft | orderbook/account-missing (windows), orderbook/band (windows), orderbook/band-full (windows) and 35 more | covered (executed) |
| `PriorityQueue[T].Peek(&self): ?&T` | orderbook, raft | orderbook, raft | orderbook/account-missing (windows), orderbook/band (windows), orderbook/band-full (windows) and 39 more | covered (executed) |
| `PriorityQueue[T].Len(&self): int` | orderbook, raft | orderbook, raft | orderbook/account-missing (windows), orderbook/band (windows), orderbook/band-full (windows) and 40 more | covered (executed) |
| `BTree[K, V].New(a: &Arena): BTree[K, V]` | lox, orderbook | lox, orderbook | lox/arith (windows), lox/bench (windows), lox/compile-errors (windows) and 54 more | covered (executed) |
| `BTree[K, V].Get(&self, k: &K): ?&V` | lox, orderbook | lox, orderbook | lox/bench (windows), lox/control (windows), lox/deep-recursion (windows) and 30 more | covered (executed) |
| `BTree[K, V].GetMut(mut self, k: &K): ?mut V` | lox, orderbook | lox, orderbook | lox/bench (windows), lox/control (windows), lox/gc-limit (windows) and 22 more | covered (executed) |
| `BTree[K, V].Set(mut self, k: K into self, v: V into self): OutOfMemory!void` | lox, orderbook | lox, orderbook | lox/arith (windows), lox/bench (windows), lox/compile-errors (windows) and 43 more | covered (executed) |
| `BTree[K, V].Replace(mut self, k: K into self, v: V into self): OutOfMemory!?V` | lox, orderbook | lox, orderbook | lox/define (windows), orderbook/account-missing (windows), orderbook/band (windows) and 13 more | covered (executed) |
| `BTree[K, V].Has(&self, k: &K): bool` | lox, orderbook | lox, orderbook | lox/repl (windows), orderbook/account-missing (windows), orderbook/band (windows) and 19 more | covered (executed) |
| `BTree[K, V].Remove(mut self, k: &K): ?V` | lox, orderbook | lox, orderbook | lox/repl (windows), orderbook/rejects (windows) | covered (executed) |
| `BTree[K, V].Len(&self): int` | lox, orderbook | lox, orderbook | lox/repl (windows), orderbook/band-full (windows), orderbook/basic (windows) and 5 more | covered (executed) |
| `BTree[K, V].Iter(&self): BTreeIter[K, V]` | lox, orderbook | lox, orderbook | lox/bench (windows), lox/gc-limit (windows), lox/gc-log (windows) and 10 more | covered (executed) |
| `BTree[K, V].Range(&self, lo: &K, hi: &K): BTreeIter[K, V] from self` | lox, orderbook | lox, orderbook | lox/repl (windows), orderbook/band-full (windows) | covered (executed) |
| `BTreeIter[K, V].Next(mut self): ?(key: &K, value: &V) from self.tree` | lox, orderbook | lox, orderbook | lox/bench (windows), lox/gc-limit (windows), lox/gc-log (windows) and 33 more | covered (executed) |
| `HeapBTree[K, V].New(a: &Alloc): HeapBTree[K, V]` | orderbook | orderbook | orderbook/account-missing (windows), orderbook/bad-id (windows), orderbook/bad-price (windows) and 29 more | covered (executed) |
| `HeapBTree[K, V].Get(&self, k: &K): ?&V` | orderbook | orderbook | orderbook/bench (windows), orderbook/budget (windows), orderbook/carriers-1 (windows) and 4 more | covered (executed) |
| `HeapBTree[K, V].GetMut(mut self, k: &K): ?mut V` | orderbook | orderbook | orderbook/account-missing (windows), orderbook/bad-record (windows), orderbook/bad-side (windows) and 27 more | covered (executed) |
| `HeapBTree[K, V].Set(mut self, a: &Alloc, k: K into self, v: V into self): OutOfMemory!void` | orderbook | orderbook | orderbook/account-missing (windows), orderbook/bad-record (windows), orderbook/bad-side (windows) and 27 more | covered (executed) |
| `HeapBTree[K, V].Replace(mut self, a: &Alloc, k: K into self, v: V into self): OutOfMemory!?V` | orderbook | orderbook | orderbook/compact (windows) | covered (executed) |
| `HeapBTree[K, V].Has(&self, k: &K): bool` | orderbook | orderbook | orderbook/account-missing (windows), orderbook/band (windows), orderbook/band-full (windows) and 13 more | covered (executed) |
| `HeapBTree[K, V].Remove(mut self, k: &K): ?V` | orderbook | orderbook | orderbook/account-missing (windows), orderbook/band (windows), orderbook/band-full (windows) and 16 more | covered (executed) |
| `HeapBTree[K, V].Len(&self): int` | orderbook | orderbook | orderbook/account-missing (windows), orderbook/band (windows), orderbook/band-full (windows) and 19 more | covered (executed) |
| `HeapBTree[K, V].Iter(&self): BTreeIter[K, V]` | orderbook | orderbook | orderbook/account-missing (windows), orderbook/bad-record (windows), orderbook/bad-side (windows) and 27 more | covered (executed) |
| `HeapBTree[K, V].Range(&self, lo: &K, hi: &K): BTreeIter[K, V] from self` | orderbook | orderbook | orderbook/band-full (windows) | covered (executed) |
| `HeapBTree[K, V].Free(self, a: &Alloc)` | orderbook | orderbook | orderbook/account-missing (windows), orderbook/bad-id (windows), orderbook/bad-price (windows) and 11 more | covered (executed) |
| `HeapBTree[K, V].FreeEmpty(self, a: &Alloc)` | orderbook | orderbook | orderbook/compact (windows), orderbook/rejects (windows) | covered (executed) |
| `HeapBTree[K, V].FreeEach(self, a: &Alloc, const release: fn(K, V))` | orderbook | orderbook | orderbook/band (windows), orderbook/band-full (windows), orderbook/basic (windows) and 15 more | covered (executed) |
| `Bitset.New(a: &Arena, bits: int): OutOfMemory!Bitset` | orderbook, raft | orderbook, raft | orderbook/account-missing (windows), orderbook/bad-id (windows), orderbook/bad-price (windows) and 51 more | covered (executed) |
| `Bitset.Set(mut self, i: int)` | orderbook, raft | orderbook, raft | orderbook/account-missing (windows), orderbook/bad-record (windows), orderbook/bad-side (windows) and 47 more | covered (executed) |
| `Bitset.Remove(mut self, i: int)` | orderbook, raft | orderbook, raft | orderbook/account-missing (windows), orderbook/band (windows), orderbook/band-full (windows) and 38 more | covered (executed) |
| `Bitset.Has(&self, i: int): bool` | orderbook, raft | orderbook, raft | orderbook/account-missing (windows), orderbook/bad-record (windows), orderbook/bad-side (windows) and 46 more | covered (executed) |
| `Bitset.Count(&self): int` | orderbook, raft | orderbook, raft | orderbook/account-missing (windows), orderbook/band (windows), orderbook/band-full (windows) and 39 more | covered (executed) |
| `Bitset.Len(&self): int` | orderbook, raft | orderbook, raft | orderbook/account-missing (windows), orderbook/band (windows), orderbook/band-full (windows) and 39 more | covered (executed) |

## std.text

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `text.Graphemes(s: str): Clusters` | textkit | fm, textkit | fm/bench (windows), fm/interrupt (windows), fm/selftest (windows) and 11 more | covered (executed) |
| `Clusters.Next(mut self): ?str from self.text` | textkit | fm, textkit | fm/bench (windows), fm/interrupt (windows), fm/selftest (windows) and 11 more | covered (executed) |
| `text.Normalize(a: &Arena, s: str, form: Form): OutOfMemory!str from a` | textkit | textkit | fetch/url-idna (windows), textkit/norm-check (windows), textkit/norm-hex-bad (windows) and 9 more | covered (executed) |
| `text.FoldCase(a: &Arena, s: str): OutOfMemory!str from a` | textkit | textkit | textkit/fold-full (windows), textkit/fold-hex (windows), textkit/grep-fixed (windows) and 5 more | covered (executed) |
| `text.Width(s: str): int` | mgrep, textkit | fm, mgrep, textkit | fm/bench (windows), fm/interrupt (windows), fm/selftest (windows) and 46 more | covered (executed) |
| `text.ToUtf16(a: &Arena, s: str): OutOfMemory![]u16 from a` | textkit | textkit | textkit/utf16-encode (windows), textkit/utf16-encode-big-bom (windows), textkit/utf16-encode-hex (windows) | covered (executed) |
| `text.FromUtf16(a: &Arena, units: []u16): Utf8Error\|OutOfMemory!str from a` | textkit | textkit | textkit/utf16-decode-big (windows), textkit/utf16-decode-bom (windows), textkit/utf16-decode-hex (windows) and 1 more | covered (executed) |
| `text.WordBounds(s: str): Bounds` | fm, textkit | fm, textkit | fm/bench (windows), fm/session (windows), fm/view-budget (windows) and 10 more | covered (executed) |
| `text.SentenceBounds(s: str): Bounds` | textkit | textkit | textkit/sentences-basic (windows), textkit/sentences-from (windows), textkit/sentences-marks (windows) and 3 more | covered (executed) |
| `Bounds.Next(mut self): ?int` | fm, textkit | fm, textkit | fm/bench (windows), fm/session (windows), fm/view-budget (windows) and 16 more | covered (executed) |
| `text.ParagraphBounds(s: str): Bounds` | textkit | - | - | exempt (EXEMPT.md line 92) |
| `text.BidiLevels(a: &Arena, s: str, dir: Direction): OutOfMemory!Levels from a, s` | textkit | - | - | exempt (EXEMPT.md line 93) |
| `Levels.Base(&self): u8` | textkit | - | - | exempt (EXEMPT.md line 94) |
| `Levels.At(&self, i: int): u8` | textkit | - | - | exempt (EXEMPT.md line 95) |
| `Levels.Line(&self, a: &Arena, start: int, end: int): OutOfMemory![]Run from a` | textkit | - | - | exempt (EXEMPT.md line 96) |
| `text.Mirror(c: char): ?char` | textkit | - | - | exempt (EXEMPT.md line 97) |
| `text.LineBreaks(s: str): Breaks` | textkit | - | - | exempt (EXEMPT.md line 98) |
| `Breaks.Next(mut self): ?(at: int, mandatory: bool)` | textkit | - | - | exempt (EXEMPT.md line 99) |
| `Capped.New(buf: mut []u8): Capped from buf` | textkit | - | chat/record-close (windows), chat/record-idle (windows), chat/web-bad-addr (windows) and 16 more | exempt (EXEMPT.md line 45) |
| `Capped.Writer(mut self): Writer from self` | textkit | - | chat/record-close (windows), chat/record-idle (windows), chat/web-bad-addr (windows) and 16 more | exempt (EXEMPT.md line 46) |
| `Capped.Bytes(&self): []u8 from self` | textkit | - | chat/record-close (windows), chat/record-idle (windows), chat/web-bad-addr (windows) and 16 more | exempt (EXEMPT.md line 47) |
| `Capped.Dropped(&self): int` | textkit | - | chat/record-close (windows), chat/record-idle (windows), chat/web-bad-addr (windows) and 16 more | exempt (EXEMPT.md line 48) |
| `Capped.Reset(mut self)` | textkit | - | - | exempt (EXEMPT.md line 49) |
| `Sink[S].New(write: fn(mut S, []u8): WriteError!void): Sink[S]` | kernel, textkit | kernel | - | exempt (execution) (EXEMPT.md line 43) |
| `Sink[S].Writer(&self, s: mut S, buf: mut []u8): Writer from self, s, buf` | kernel, textkit | kernel | - | exempt (execution) (EXEMPT.md line 44) |
| `enum Form {` | textkit | textkit | - | covered (referenced) |
| `enum Direction {` | textkit | - | - | exempt (EXEMPT.md line 100) |
| `struct Run {` | textkit | - | - | exempt (EXEMPT.md line 101) |
| `text.CategoryOf(c: char): Category` | textkit | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `text.ScriptOf(c: char): Script` | textkit | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `text.ScriptExtensions(c: char): []Script` | textkit | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `text.Has(c: char, p: Property): bool` | textkit | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `text.UnicodeVersion(): str` | textkit | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |

## std.hash

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `hash.Xx(data: []u8): u64` | kvd | kvd, raft | kvd/demo (windows), kvd/demo-hold (windows), kvd/demo-hold-idle (windows) and 22 more | covered (executed) |
| `hash.Xx64(data: []u8, seed: u64): u64` | kvd | kvd | kvd/demo (windows), kvd/demo-hold (windows), kvd/demo-hold-idle (windows) and 33 more | covered (executed) |
| `hash.Crc32(data: []u8): u32` | fetch, kvd | fetch, kvd, raft | fetch/bench (windows), fetch/decoders (windows), fetch/no-decoder (windows) and 55 more | covered (executed) |
| `hash.Sip13(data: []u8, key: HashKey): u64` | kvd | kvd | kvd/demo (windows), kvd/demo-hold (windows), kvd/demo-hold-idle (windows) and 4 more | covered (executed) |
| `hash.Sha256(data: []u8): [32]u8` | pack | bigcalc, pack | bigcalc/cert_show_chain (windows), bigcalc/cert_show_der (windows), bigcalc/cert_verify_ip (windows) and 44 more | covered (executed) |
| `hash.Sha512(data: []u8): [64]u8` | pack | bigcalc, pack | bigcalc/hash_stdin (windows), bigcalc/key_public_ed25519 (windows), bigcalc/key_show_ed25519 (windows) and 27 more | covered (executed) |
| `hash.Blake3(data: []u8): [32]u8` | pack | pack | pack/bench (windows), pack/create-budget-fits (windows), pack/create-gzip-stats (windows) and 20 more | covered (executed) |
| `Crc32State.New(): Crc32State` | fetch, kvd | fetch, kvd | fetch/decoders (windows), fetch/download (windows), fetch/resume (windows) and 39 more | covered (executed) |
| `Crc32State.Bytes(mut self, data: []u8)` | fetch, kvd | fetch, kvd | fetch/decoders (windows), fetch/download (windows), fetch/resume (windows) and 18 more | covered (executed) |
| `Crc32State.Sum(self): u32` | fetch, kvd | fetch, kvd | fetch/decoders (windows), fetch/download (windows), fetch/resume (windows) and 19 more | covered (executed) |
| `Xx64State.New(seed: u64): Xx64State` | kvd | kvd | kvd/demo (windows), kvd/demo-hold (windows), kvd/demo-hold-idle (windows) and 10 more | covered (executed) |
| `Xx64State.Bytes(mut self, data: []u8)` | kvd | kvd | kvd/demo (windows), kvd/demo-hold (windows), kvd/demo-hold-idle (windows) and 10 more | covered (executed) |
| `Xx64State.Sum(self): u64` | kvd | kvd | kvd/demo (windows), kvd/demo-hold (windows), kvd/demo-hold-idle (windows) and 10 more | covered (executed) |
| `Sha256State.New(): Sha256State` | pack | bigcalc, pack | bigcalc/cert_show_chain (windows), bigcalc/cert_show_der (windows), bigcalc/cert_verify_ip (windows) and 80 more | covered (executed) |
| `Sha256State.Bytes(mut self, data: []u8)` | pack | bigcalc, pack | bigcalc/cert_show_chain (windows), bigcalc/cert_show_der (windows), bigcalc/cert_verify_ip (windows) and 74 more | covered (executed) |
| `Sha256State.Sum(self): [32]u8` | pack | bigcalc, pack | bigcalc/cert_show_chain (windows), bigcalc/cert_show_der (windows), bigcalc/cert_verify_ip (windows) and 78 more | covered (executed) |
| `Sha512State.New(): Sha512State` | pack | bigcalc, pack | bigcalc/hash_all (windows), bigcalc/hash_carriers1 (windows), bigcalc/hash_carriers16 (windows) and 48 more | covered (executed) |
| `Sha512State.Bytes(mut self, data: []u8)` | pack | bigcalc, pack | bigcalc/cert_verify_ed25519 (windows), bigcalc/cert_verify_ip (windows), bigcalc/cert_verify_now (windows) and 67 more | covered (executed) |
| `Sha512State.Sum(self): [64]u8` | pack | bigcalc, pack | bigcalc/cert_verify_ed25519 (windows), bigcalc/cert_verify_ip (windows), bigcalc/cert_verify_now (windows) and 57 more | covered (executed) |
| `Blake3State.New(): Blake3State` | pack | pack | pack/bench (windows), pack/create-budget (windows), pack/create-budget-compress (windows) and 17 more | covered (executed) |
| `Blake3State.Bytes(mut self, data: []u8)` | pack | pack | pack/bench (windows), pack/create-budget (windows), pack/create-budget-compress (windows) and 17 more | covered (executed) |
| `Blake3State.Sum(self): [32]u8` | pack | pack | pack/bench (windows), pack/create-budget (windows), pack/create-budget-compress (windows) and 17 more | covered (executed) |
| `hash.Sha1(data: []u8): [20]u8` | bigcalc, fetch | bigcalc, fetch | bigcalc/cert_show_chain (windows), bigcalc/cert_show_der (windows), bigcalc/hash_stdin (windows) and 5 more | covered (executed) |
| `hash.Sha384(data: []u8): [48]u8` | bigcalc | bigcalc | bigcalc/cert_verify_ed25519 (windows), bigcalc/cert_verify_ip (windows), bigcalc/cert_verify_now (windows) and 6 more | covered (executed) |
| `Sha1State.New(): Sha1State` | bigcalc | bigcalc | bigcalc/cert_show_chain (windows), bigcalc/cert_show_der (windows), bigcalc/hash_all (windows) and 21 more | covered (executed) |
| `Sha1State.Bytes(mut self, data: []u8)` | bigcalc | bigcalc | bigcalc/cert_show_chain (windows), bigcalc/cert_show_der (windows), bigcalc/hash_all (windows) and 18 more | covered (executed) |
| `Sha1State.Sum(self): [20]u8` | bigcalc | bigcalc | bigcalc/cert_show_chain (windows), bigcalc/cert_show_der (windows), bigcalc/hash_all (windows) and 21 more | covered (executed) |
| `Sha384State.New(): Sha384State` | bigcalc | bigcalc | bigcalc/cert_verify_ed25519 (windows), bigcalc/cert_verify_ip (windows), bigcalc/cert_verify_now (windows) and 24 more | covered (executed) |
| `Sha384State.Bytes(mut self, data: []u8)` | bigcalc | bigcalc | bigcalc/cert_verify_ed25519 (windows), bigcalc/cert_verify_ip (windows), bigcalc/cert_verify_now (windows) and 22 more | covered (executed) |
| `Sha384State.Sum(self): [48]u8` | bigcalc | bigcalc | bigcalc/cert_verify_ed25519 (windows), bigcalc/cert_verify_ip (windows), bigcalc/cert_verify_now (windows) and 12 more | covered (executed) |

## std.json

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `error Syntax(offset: int)` | notes | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `error Mismatch(offset: int)` | notes, textkit | textkit, gen: orderbook | - | covered (referenced) |
| `error Depth(offset: int)` | notes | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `type Error = Syntax \| Mismatch \| Depth \| OutOfMemory` | notes, textkit | textkit, gen: orderbook | - | covered (referenced) |
| `json.Parse(a: &Arena, text: str into a): json.Error!Value from a, text` | notes, textkit | orderbook, textkit | textkit/ucdcases-excerpt (windows) | covered (executed) |
| `json.Codec(g: mut Gen, t: Type)` | notes, orderbook | orderbook | - | exempt (execution) (EXEMPT.md line 53) |
| `Value.Get(&self, key: str): ?&Value` | notes, textkit | orderbook, textkit | textkit/ucdcases-excerpt (windows) | covered (executed) |
| `Value.Int(&self): ?i64` | notes | orderbook | - | covered (referenced; no instrumented body: compiled inline) |
| `Value.Float(&self): ?f64` | notes | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Value.Str(&self): ?str` | notes, textkit | textkit | textkit/ucdcases-excerpt (windows) | covered (executed) |
| `Value.Format(&self, w: mut Writer): WriteError!void` | notes | gen: orderbook | orderbook/basic-json (windows), orderbook/carriers-1 (windows), orderbook/carriers-16 (windows) and 2 more | covered (executed) |
| `enum Value {` | notes, textkit | textkit, gen: orderbook | - | covered (referenced) |
| `struct Member {` | notes | gen: orderbook | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `interface Json {` | notes, orderbook | orderbook | - | covered (referenced) |

## std.http

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `error BadRequest(offset: int)` | notes | fetch | - | covered (referenced) |
| `error TooLarge` | notes | fetch | - | covered (referenced) |
| `error Status(code: int)` | notes | fetch | - | covered (referenced) |
| `error NoSecureTransport` | notes | fetch | - | covered (referenced) |
| `error BodyNotReplayable` | fetch, notes | fetch | - | covered (referenced) |
| `error TooManyRedirects` | notes | fetch | - | covered (referenced) |
| `error BadUrl` | notes | fetch | - | covered (referenced) |
| `error ProxyRefused(code: int)` | notes | - | - | exempt (EXEMPT.md line 105) |
| `type Error = BadRequest \| TooLarge \| Status \| net.Error \| OutOfMemory \| ReadError \| WriteError \| NoSecureTransport \| BodyNotReplayable \| TooManyRedirects \| BadUrl \| ProxyRefused` | notes | chat, fetch | - | covered (referenced) |
| `http.ReadRequest(a: &Arena, io: Io, r: mut Reader, limit: int): http.Error!Request from a` | notes | fetch | fetch/bad-url (windows), fetch/bench (windows), fetch/cookies (windows) and 47 more | covered (executed) |
| `http.Serve[C: Share](sys: &System, addr: str, limit: int, c: &C, handle: (fn(c: &C, a: &Arena, io: Io, req: &Request, res: mut Response): http.Error!void *res into res)): http.Error!void` | notes | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `http.Fetch(a: &Arena, io: Io, method: str, url: str, headers: []Header, body: []u8, limit: int): http.Error!Reply from a` | notes | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Request.Header(&self, name: str): ?str from self` | fetch, notes | fetch | chat/web (windows), chat/web-errors (windows), chat/web-many-carriers (windows) and 30 more | covered (executed) |
| `Request.Param(&self, name: str): ?str from self` | notes | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Request.ParamInt(&self, name: str): ?i64` | notes | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Response.New(a: &Arena): Response` | notes | - | chat/web (windows), chat/web-errors (windows), chat/web-many-carriers (windows) and 5 more | covered (executed) |
| `Response.Status(mut self, code: int)` | notes | chat | chat/web (windows), chat/web-errors (windows), chat/web-many-carriers (windows) and 3 more | covered (executed) |
| `Response.Header(mut self, name: str, value: str): OutOfMemory!void *self into self` | notes | chat | chat/web (windows), chat/web-errors (windows), chat/web-many-carriers (windows) and 2 more | covered (executed) |
| `Response.Body(mut self, data: []u8): OutOfMemory!void *self into self` | notes | chat | chat/web (windows), chat/web-errors (windows), chat/web-many-carriers (windows) and 2 more | covered (executed) |
| `Response.Writer(mut self): Writer from self` | notes | chat | chat/web (windows), chat/web-many-carriers (windows), chat/web-one-carrier (windows) and 1 more | covered (executed) |
| `Response.Send(&self, io: Io, w: mut Writer): http.Error!void` | notes | - | chat/web (windows), chat/web-errors (windows), chat/web-many-carriers (windows) and 2 more | covered (executed) |
| `http.Dispatch[C](a: &Arena, io: Io, routes: []Route[C], c: &C, req: &Request, res: mut Response): http.Error!void *res into res, *routes into a` | notes | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Client.New(h: &Heap, l: ClientLimits): Client from h, l` | fetch, notes | chat, fetch | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 86 more | covered (executed) |
| `Client.Do(&self, a: &Arena, io: Io, req: &ClientRequest): http.Error!Exchange from a, self` | notes | fetch | chat/web (windows), chat/web-errors (windows), chat/web-many-carriers (windows) and 47 more | covered (executed) |
| `Client.Fetch(&self, a: &Arena, io: Io, req: &ClientRequest): http.Error!Reply from a` | fetch, notes | chat, fetch | chat/web (windows), chat/web-errors (windows), chat/web-many-carriers (windows) and 40 more | covered (executed) |
| `Client.DoStreaming(&self, a: &Arena, io: Io, req: &ClientRequest, body: mut Reader, len: ?i64): http.Error!Exchange from a, self` | notes | fetch | fetch/upload (windows), fetch/upload-chunked (windows), fetch/upload-not-replayable (windows) | covered (executed) |
| `Client.Close(self)` | fetch, notes | chat, fetch | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 86 more | covered (executed) |
| `Exchange.Body(mut self, buf: mut []u8): Reader from self, buf` | fetch, notes | fetch | chat/web (windows), chat/web-errors (windows), chat/web-many-carriers (windows) and 37 more | covered (executed) |
| `Exchange.Close(self, io: Io): http.Error!void` | fetch, notes | fetch | chat/web (windows), chat/web-errors (windows), chat/web-many-carriers (windows) and 35 more | covered (executed) |
| `Exchange.Abandon(self)` | fetch, notes | fetch | fetch/cut-download (windows), fetch/cut-fetch (windows), fetch/fail-download (windows) and 6 more | covered (executed) |
| `http.ServeWith[C: Share](sys: &System, addr: str, o: &ServerOptions, c: &C, handle: (fn(c: &C, a: &Arena, io: Io, req: &Request, res: mut Response, link: &Link): http.Error!void *res into res)): http.Error!void` | chat, notes | chat | chat/web (windows), chat/web-bad-addr (windows), chat/web-errors (windows) and 6 more | covered (executed) |
| `http.ServeOn[C: Share](h: &Heap, io: Io, l: &net.Listener, o: &ServerOptions, c: &C, handle: (fn(c: &C, a: &Arena, io: Io, req: &Request, res: mut Response, link: &Link): http.Error!void *res into res)): http.Error!void` | notes | - | chat/web (windows), chat/web-errors (windows), chat/web-many-carriers (windows) and 5 more | exempt (reference; executed) (EXEMPT.md line 106) |
| `http.ServeChan[C: Share, T: Send](h: &Heap into ch, io: Io, l: &net.Listener, o: &ServerOptions, c: &C, ch: &Chan[T], handle: (fn(c: &C, h: &Heap into ch, ch: &Chan[T], a: &Arena, io: Io, req: &Request, res: mut Response, link: &Link): http.Error!void *res into res)): http.Error!void` | notes | - | - | exempt (EXEMPT.md line 107) |
| `Response.Flush(mut self, io: Io, link: &Link): http.Error!void` | chat, notes | chat | chat/web (windows), chat/web-many-carriers (windows), chat/web-one-carrier (windows) and 1 more | covered (executed) |
| `Link.Takeover(&self): ?Taken` | chat, notes | chat | chat/web (windows), chat/web-errors (windows), chat/web-many-carriers (windows) and 5 more | covered (executed) |
| `Taken.Abandon(self)` | chat, notes | chat | chat/web (windows), chat/web-many-carriers (windows), chat/web-one-carrier (windows) and 2 more | covered (executed) |
| `struct ClientRequest {` | fetch, notes | chat, fetch | - | covered (referenced) |
| `linear struct Exchange {` | fetch, notes | fetch | - | covered (referenced) |
| `struct ClientLimits {` | fetch, notes | fetch | - | covered (referenced) |
| `struct ServerOptions {` | chat, notes | chat | - | covered (referenced) |
| `struct Header {` | fetch, notes | chat, fetch | - | covered (referenced) |
| `struct Request {` | fetch, notes | chat, fetch | - | covered (referenced) |
| `struct Reply {` | fetch, notes | chat, fetch | - | covered (referenced) |
| `linear struct Taken {` | chat, notes | chat | - | covered (referenced) |
| `struct Route[C] {` | notes | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |

## std.regex

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `error Syntax(offset: int)` | mgrep, textkit | mgrep, textkit | - | covered (referenced) |
| `error TooLarge` | mgrep | mgrep, textkit | - | covered (referenced) |
| `type Error = Syntax \| TooLarge \| OutOfMemory` | mgrep, textkit | mgrep, textkit | - | covered (referenced) |
| `regex.Compile(a: &Arena, pattern: str): regex.Error!Regex from a` | mgrep, textkit | mgrep, textkit | mgrep/bench (windows), mgrep/column (windows), mgrep/count-regex (windows) and 24 more | covered (executed) |
| `regex.Matcher(g: mut Gen, t: Type, pattern: str)` | mgrep | mgrep | - | exempt (execution) (EXEMPT.md line 57) |
| `Regex.IsMatch(&self, s: str): bool` | mgrep, textkit | mgrep, textkit | mgrep/bench (windows), mgrep/column (windows), mgrep/count-regex (windows) and 17 more | covered (executed) |
| `Regex.Find(&self, s: str): ?Match` | mgrep, textkit | mgrep, textkit | mgrep/column (windows), mgrep/heading (windows), mgrep/patterns (windows) and 3 more | covered (executed) |
| `Regex.FindAll(&self, a: &Arena, s: str): OutOfMemory!List[Match] from a` | mgrep, textkit | mgrep, textkit | mgrep/bench (windows), mgrep/column (windows), mgrep/count-regex (windows) and 8 more | covered (executed) |
| `Regex.Group(&self, s: str, m: Match, i: int): ?str from s` | mgrep, textkit | mgrep, textkit | mgrep/group (windows), textkit/grep-group (windows) | covered (executed) |
| `Regex.Replace(&self, a: &Arena, s: str, with: str): OutOfMemory!str from a` | mgrep, textkit | mgrep, textkit | mgrep/replace (windows), textkit/grep-replace (windows) | covered (executed) |
| `struct Match {` | mgrep, textkit | mgrep, textkit | - | covered (referenced) |

## std.crypto

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `error BadTag` | bigcalc, pack | bigcalc, pack | - | covered (referenced) |
| `error BadLength` | bigcalc, pack | bigcalc, pack | - | covered (referenced) |
| `error BadKey` | bigcalc | bigcalc | - | covered (referenced) |
| `error Encrypted` | bigcalc | bigcalc | - | covered (referenced) |
| `error BadEncoding` | bigcalc | bigcalc | - | covered (referenced) |
| `error BadCost` | - | - | - | exempt (EXEMPT.md line 111) |
| `type Error = BadTag \| BadLength \| BadKey \| Encrypted \| BadEncoding \| BadCost` | - | bigcalc, fetch, pack | - | covered (referenced) |
| `crypto.Hmac256(key: []u8, data: []u8): [32]u8` | pack | bigcalc, pack | bigcalc/enc_open_128 (windows), bigcalc/enc_open_256_armor (windows), bigcalc/enc_open_ephemeral (windows) and 39 more | covered (executed) |
| `crypto.X25519(secret: &[32]u8, point: &[32]u8): [32]u8` | pack | pack | fetch/handshake (windows), fetch/handshake-timeout (windows), fetch/handshake-unknown-root (windows) and 32 more | covered (executed) |
| `crypto.Random(io: Io, buf: mut []u8): Canceled!void` | pack | bigcalc, pack | bigcalc/enc_seal_ephemeral (windows), bigcalc/enc_seal_random_nonce (windows), fetch/handshake (windows) and 14 more | covered (executed) |
| `crypto.Equal(a: []u8, b: []u8): bool` | pack | bigcalc, pack | bigcalc/cert_verify_ip (windows), bigcalc/cert_verify_now (windows), bigcalc/cert_verify_ok (windows) and 67 more | covered (executed) |
| `ChaCha20Poly1305.Seal(key: &[32]u8, nonce: &[12]u8, aad: []u8, plain: []u8, out: mut []u8): crypto.Error!void` | pack | pack | pack/bench (windows), pack/create-sealed (windows) | covered (executed) |
| `ChaCha20Poly1305.Open(key: &[32]u8, nonce: &[12]u8, aad: []u8, sealed: []u8, out: mut []u8): crypto.Error!void` | pack | pack | pack/extract-sealed (windows), pack/list-sealed (windows), pack/sum-sealed (windows) and 4 more | covered (executed) |
| `Ed25519.Keypair(io: Io): Canceled![64]u8` | pack | pack | pack/keygen-random (windows) | covered (executed) |
| `Ed25519.FromSeed(seed: &[32]u8): [64]u8` | pack | bigcalc, pack | bigcalc/key_public_ed25519 (windows), bigcalc/key_show_ed25519 (windows), bigcalc/sign_ed25519 (windows) and 26 more | covered (executed) |
| `Ed25519.Sign(secret: &[64]u8, msg: []u8): [64]u8` | pack | bigcalc, pack | bigcalc/sign_ed25519 (windows), pack/bench (windows), pack/create-sealed (windows) and 2 more | covered (executed) |
| `Ed25519.Verify(public: &[32]u8, msg: []u8, sig: &[64]u8): bool` | pack | bigcalc, pack | bigcalc/verify_ed25519 (windows), bigcalc/verify_ed25519_tampered (windows), pack/extract-bad-data (windows) and 18 more | covered (executed) |
| `crypto.Hmac384(key: []u8, data: []u8): [48]u8` | bigcalc | bigcalc | bigcalc/mac_sha384 (windows) | covered (executed) |
| `crypto.Hmac512(key: []u8, data: []u8): [64]u8` | bigcalc | bigcalc | bigcalc/mac_sha512_expect (windows) | covered (executed) |
| `crypto.HkdfExtract(salt: []u8, ikm: []u8, h: HashKind): Prk` | bigcalc, pack | bigcalc, pack | bigcalc/enc_open_128 (windows), bigcalc/enc_open_256_armor (windows), bigcalc/enc_open_ephemeral (windows) and 16 more | covered (executed) |
| `crypto.HkdfExpand(prk: &Prk, info: []u8, out: mut []u8): crypto.Error!void` | bigcalc, pack | bigcalc, pack | bigcalc/enc_open_128 (windows), bigcalc/enc_open_256_armor (windows), bigcalc/enc_open_ephemeral (windows) and 28 more | covered (executed) |
| `Prk.FromBytes(h: HashKind, key: []u8): crypto.Error!Prk` | bigcalc | bigcalc | bigcalc/enc_open_prk (windows), fetch/handshake (windows), fetch/handshake-timeout (windows) and 9 more | covered (executed) |
| `Prk.Wipe(mut self)` | bigcalc, pack | bigcalc, pack | bigcalc/enc_open_128 (windows), bigcalc/enc_open_256_armor (windows), bigcalc/enc_open_ephemeral (windows) and 28 more | covered (executed) |
| `AesGcm.New(key: []u8): crypto.Error!AesGcm` | bigcalc, pack | bigcalc, pack | bigcalc/enc_open_128 (windows), bigcalc/enc_open_256_armor (windows), bigcalc/enc_open_ephemeral (windows) and 24 more | covered (executed) |
| `AesGcm.Seal(&self, nonce: &[12]u8, aad: []u8, plain: []u8, out: mut []u8): crypto.Error!int` | bigcalc, pack | bigcalc, pack | bigcalc/enc_seal_128 (windows), bigcalc/enc_seal_ecdh (windows), bigcalc/enc_seal_ephemeral (windows) and 13 more | covered (executed) |
| `AesGcm.Open(&self, nonce: &[12]u8, aad: []u8, sealed: []u8, out: mut []u8): crypto.Error!int` | bigcalc, pack | bigcalc, pack | bigcalc/enc_open_128 (windows), bigcalc/enc_open_256_armor (windows), bigcalc/enc_open_ephemeral (windows) and 19 more | covered (executed) |
| `AesGcm.Wipe(mut self)` | bigcalc, pack | bigcalc, pack | bigcalc/enc_open_128 (windows), bigcalc/enc_open_256_armor (windows), bigcalc/enc_open_ephemeral (windows) and 24 more | covered (executed) |
| `EcSecret.Generate(io: Io, curve: Curve): Canceled\|crypto.Error!EcSecret` | bigcalc | bigcalc | bigcalc/enc_seal_ephemeral (windows) | covered (executed) |
| `EcSecret.FromBytes(curve: Curve, b: []u8): crypto.Error!EcSecret` | bigcalc | bigcalc | bigcalc/enc_open_ephemeral (windows), bigcalc/enc_seal_ecdh (windows), bigcalc/key_public_p256 (windows) and 20 more | covered (executed) |
| `EcSecret.Public(&self, out: mut []u8): crypto.Error!int` | bigcalc | bigcalc | bigcalc/enc_seal_ephemeral (windows), bigcalc/key_public_p256 (windows), bigcalc/key_show_der (windows) and 3 more | covered (executed) |
| `EcSecret.Wipe(mut self)` | bigcalc | bigcalc | bigcalc/enc_open_ephemeral (windows), bigcalc/enc_seal_ecdh (windows), bigcalc/enc_seal_ephemeral (windows) and 7 more | covered (executed) |
| `crypto.EcdhShared(curve: Curve, secret: &EcSecret, peer: []u8, out: mut []u8): crypto.Error!void` | bigcalc | bigcalc | bigcalc/enc_open_ephemeral (windows), bigcalc/enc_seal_ecdh (windows), bigcalc/enc_seal_ephemeral (windows) | covered (executed) |
| `crypto.EcdsaSign(secret: &EcSecret, digest: []u8, format: SigFormat, out: mut []u8): crypto.Error!int` | bigcalc | bigcalc | bigcalc/sign_rfc6979_der (windows), bigcalc/sign_rfc6979_fixed (windows), bigcalc/sign_rfc6979_sha384 (windows) and 11 more | covered (executed) |
| `crypto.EcdsaVerify(public: []u8, curve: Curve, digest: []u8, format: SigFormat, sig: []u8): bool` | bigcalc | bigcalc | bigcalc/cert_verify_ed25519 (windows), bigcalc/cert_verify_ip (windows), bigcalc/cert_verify_now (windows) and 13 more | covered (executed) |
| `RsaPublic.New(modulus: []u8, exponent: u64): crypto.Error!RsaPublic from modulus` | bigcalc | bigcalc | bigcalc/cert_show_chain (windows), bigcalc/cert_verify_ip (windows), bigcalc/cert_verify_now (windows) and 12 more | covered (executed) |
| `crypto.RsaVerifyPkcs1(public: &RsaPublic, h: HashKind, digest: []u8, sig: []u8): bool` | bigcalc | bigcalc | bigcalc/cert_verify_ip (windows), bigcalc/cert_verify_now (windows), bigcalc/cert_verify_ok (windows) and 5 more | covered (executed) |
| `crypto.RsaVerifyPss(public: &RsaPublic, h: HashKind, digest: []u8, sig: []u8): bool` | bigcalc | bigcalc | bigcalc/verify_rsa_pss (windows), bigcalc/verify_rsa_pss_max_salt (windows) | covered (executed) |
| `crypto.ParsePublicKey(der: []u8): crypto.Error!PublicKey from der` | bigcalc | bigcalc | bigcalc/cert_show_chain (windows), bigcalc/cert_show_der (windows), bigcalc/cert_verify_ed25519 (windows) and 32 more | covered (executed) |
| `crypto.ParsePrivateKey(der: []u8): crypto.Error!PrivateKey` | bigcalc, fetch | bigcalc, fetch | bigcalc/enc_open_ephemeral (windows), bigcalc/enc_seal_ecdh (windows), bigcalc/key_public_ed25519 (windows) and 25 more | covered (executed) |
| `crypto.Wipe(buf: mut []u8)` | bigcalc, pack | bigcalc, pack | bigcalc/enc_open_128 (windows), bigcalc/enc_open_256_armor (windows), bigcalc/enc_open_ephemeral (windows) and 67 more | covered (executed) |
| `crypto.Argon2id(h: &Heap, c: Argon2Cost, password: []u8, salt: []u8, secret: []u8, ad: []u8, out: mut []u8): crypto.BadLength\|crypto.BadCost\|OutOfMemory!void` | - | - | - | exempt (EXEMPT.md line 112) |
| `crypto.Argon2idVerify(h: &Heap, c: Argon2Cost, password: []u8, salt: []u8, secret: []u8, ad: []u8, tag: []u8): crypto.BadLength\|crypto.BadCost\|OutOfMemory!bool` | - | - | - | exempt (EXEMPT.md line 113) |
| `struct Argon2Cost {` | - | - | - | exempt (EXEMPT.md line 114) |
| `enum Curve {` | bigcalc | bigcalc | - | covered (referenced) |
| `enum HashKind {` | bigcalc, pack | bigcalc, pack | - | covered (referenced) |
| `enum SigFormat {` | bigcalc | bigcalc | - | covered (referenced) |
| `enum PublicKey {` | bigcalc | bigcalc | - | covered (referenced) |
| `enum PrivateKey {` | bigcalc | bigcalc | - | covered (referenced) |

## std.compress

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `error Corrupt(offset: int)` | pack | pack | - | covered (referenced) |
| `error Truncated` | pack | pack | - | covered (referenced) |
| `error TooLarge` | pack | pack | - | covered (referenced) |
| `type Error = Corrupt \| Truncated \| TooLarge \| OutOfMemory` | pack | pack | - | covered (referenced) |
| `compress.Inflate(a: &Arena, data: []u8, limit: int): compress.Error![]u8 from a` | pack | pack | pack/extract-sealed (windows), pack/extract-sealed-aes (windows), pack/sum-sealed (windows) and 4 more | covered (executed) |
| `compress.Gunzip(a: &Arena, data: []u8, limit: int): compress.Error![]u8 from a` | notes, pack | pack | pack/verify-bad-digest (windows), pack/verify-gzip (windows) | covered (executed) |
| `compress.Deflate(a: &Arena, data: []u8): OutOfMemory![]u8 from a` | pack | pack | pack/bench (windows), pack/create-budget-compress (windows), pack/create-budget-fits (windows) and 4 more | covered (executed) |
| `compress.Gzip(a: &Arena, data: []u8): OutOfMemory![]u8 from a` | fetch, notes, pack | fetch, pack | fetch/bench (windows), fetch/decoders (windows), fetch/no-decoder (windows) and 1 more | covered (executed) |
| `Inflater.New(a: &Arena): Inflater` | pack | pack | pack/extract-bad-data (windows), pack/extract-cut-data (windows), pack/extract-signed (windows) and 2 more | covered (executed) |
| `Inflater.NewGzip(a: &Arena): Inflater` | pack | pack | fetch/decoders (windows), pack/extract-bad-digest (windows), pack/extract-gzip-here (windows) and 2 more | covered (executed) |
| `Inflater.Read(mut self, r: mut Reader, buf: mut []u8): compress.Error\|ReadError!int *self into self` | pack | pack | pack/extract-bad-data (windows), pack/extract-bad-digest (windows), pack/extract-cut-data (windows) and 10 more | covered (executed) |
| `Inflater.NewZlib(a: &Arena): Inflater` | pack | pack | fetch/decoders (windows), pack/untar-zz (windows), pack/untar-zz-cut (windows) and 8 more | covered (executed) |
| `Inflater.Reader(mut self, r: mut Reader, buf: mut []u8): Reader from self, r, buf` | pack | pack | fetch/decoders (windows), pack/untar-gz (windows), pack/untar-zz (windows) and 9 more | covered (executed) |
| `Inflater.Cause(&self): ?compress.Error` | pack | pack | fetch/decoders (windows), pack/unzlib-corrupt (windows), pack/unzlib-cut-stream (windows) and 1 more | covered (executed) |
| `compress.Zlib(a: &Arena, data: []u8): OutOfMemory![]u8 from a` | fetch, pack | fetch, pack | fetch/decoders (windows), pack/zlib-whole (windows) | covered (executed) |
| `compress.Unzlib(a: &Arena, data: []u8, limit: int): compress.Error![]u8 from a` | pack | pack | pack/unzlib-cut-whole (windows), pack/unzlib-max-whole (windows), pack/unzlib-whole (windows) | covered (executed) |
| `compress.Adler32(data: []u8): u32` | pack | pack | fetch/decoders (windows), pack/unzlib-corrupt (windows), pack/unzlib-cut-stream (windows) and 8 more | covered (executed) |
| `Adler32State.New(): Adler32State` | pack | - | - | exempt (EXEMPT.md line 118) |
| `Adler32State.Bytes(mut self, data: []u8)` | pack | - | - | exempt (EXEMPT.md line 119) |
| `Adler32State.Sum(self): u32` | pack | - | - | exempt (EXEMPT.md line 120) |
| `Encoder.New(a: &Arena, framing: Framing, level: int): OutOfMemory!Encoder from a` | pack | pack | pack/zlib-several (windows), pack/zlib-several-missing (windows), pack/zlib-store (windows) and 1 more | covered (executed) |
| `Encoder.Write(mut self, w: mut Writer, data: []u8): WriteError!void` | pack | pack | pack/zlib-several (windows), pack/zlib-several-missing (windows), pack/zlib-store (windows) and 1 more | covered (executed) |
| `Encoder.Flush(mut self, w: mut Writer): WriteError!void` | pack | pack | pack/zlib-stream (windows) | covered (executed) |
| `Encoder.Finish(self, w: mut Writer): WriteError!void` | pack | pack | pack/zlib-several (windows), pack/zlib-store (windows), pack/zlib-stream (windows) | covered (executed) |
| `Encoder.Abandon(self)` | pack | pack | pack/zlib-several-missing (windows) | covered (executed) |
| `ZstdDecoder.New(a: &Arena, window_limit: int): ZstdDecoder` | pack | pack | pack/untar-skippable (windows), pack/untar-window-limit (windows), pack/untar-zst (windows) and 6 more | covered (executed) |
| `ZstdDecoder.NewLazy(a: &Arena, window_limit: int): ZstdDecoder` | pack | - | fetch/decoders (windows) | exempt (reference; executed) (EXEMPT.md line 121) |
| `ZstdDecoder.Read(mut self, r: mut Reader, buf: mut []u8): compress.Error\|ReadError!int *self into self` | pack | pack | pack/untar-skippable (windows), pack/untar-zst (windows), pack/untar-zst-checksum (windows) and 2 more | covered (executed) |
| `ZstdDecoder.Reader(mut self, r: mut Reader, buf: mut []u8): Reader from self, r, buf` | pack | pack | pack/untar-skippable (windows), pack/untar-window-limit (windows), pack/untar-zst (windows) and 6 more | covered (executed) |
| `ZstdDecoder.ReaderLazy(mut self, r: mut Reader, buf: mut []u8): Reader from self, r, buf *self into self` | pack | - | fetch/decoders (windows) | exempt (reference; executed) (EXEMPT.md line 122) |
| `ZstdDecoder.Cause(&self): ?compress.Error` | pack | pack | fetch/decoders (windows), pack/untar-window-limit (windows), pack/untar-zst-block (windows) and 2 more | covered (executed) |
| `enum Framing {` | pack | pack | - | covered (referenced) |

## std.cli

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `error Unknown(arg: int)` | mgrep, notes, pack and 2 more | cron, imgproc, lox and 6 more | - | covered (referenced) |
| `error Missing(field: int)` | mgrep, notes, pack and 2 more | imgproc, lox, mgrep and 3 more | - | covered (referenced) |
| `error Invalid(arg: int)` | mgrep, notes, pack and 2 more | cron, imgproc, lox and 6 more | - | covered (referenced) |
| `type Error = Unknown \| Missing \| Invalid \| OutOfMemory` | mgrep, notes, pack and 2 more | cron, imgproc, mgrep and 6 more | - | covered (referenced) |
| `cli.Args(g: mut Gen, t: Type)` | cron, mgrep, notes and 3 more | chat, cron, imgproc and 6 more | - | exempt (execution) (EXEMPT.md line 61) |

## std.log

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `Logger.New(w: mut Writer, level: Level): Logger from w` | kvd, notes, procman | kvd, procman | kvd/bench (windows), kvd/record (windows), procman/bench (windows) and 37 more | covered (executed) |
| `Logger.NewTimed(io: Io, w: mut Writer, level: Level): Logger from w` | kvd, notes, procman | kvd, procman | kvd/sim (windows), kvd/sim-carriers16 (windows), kvd/sim-seed (windows) and 17 more | covered (executed) |
| `Logger.Log[A: Fields[Format] + Plain](mut self, level: Level, const f: str, args: A): WriteError!void` | kvd, notes, procman | kvd, procman | kvd/bench (windows), kvd/record (windows), kvd/sim (windows) and 30 more | covered (executed) |
| `Logger.Debug[A: Fields[Format] + Plain](mut self, const f: str, args: A): WriteError!void` | notes, procman | procman | procman/bench (windows), procman/check (windows), procman/procfile-from-env (windows) and 24 more | covered (executed) |
| `Logger.Info[A: Fields[Format] + Plain](mut self, const f: str, args: A): WriteError!void` | kvd, notes, procman | kvd, procman | kvd/bench (windows), kvd/record (windows), kvd/sim (windows) and 19 more | covered (executed) |
| `Logger.Warn[A: Fields[Format] + Plain](mut self, const f: str, args: A): WriteError!void` | kvd, notes, procman | kvd, procman | kvd/sim (windows), kvd/sim-carriers16 (windows), kvd/sim-seed (windows) and 5 more | covered (executed) |
| `Logger.Error[A: Fields[Format] + Plain](mut self, const f: str, args: A): WriteError!void` | kvd, notes, procman | kvd, procman | kvd/sim (windows), kvd/sim-carriers16 (windows), kvd/sim-seed (windows) and 6 more | covered (executed) |
| `log.Stderr(level: Level): Sink` | chat, cron, kvd and 2 more | chat, cron, kvd | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 43 more | covered (executed) |
| `log.Stdout(level: Level): Sink` | kvd, notes, procman | kvd | kvd/demo-timeouts (windows) | covered (executed) |
| `log.ToFile(f: &Mutex[fs.File], level: Level): Sink from f` | cron, notes, procman | cron | cron/bench (windows), cron/run-detach (windows), cron/run-interrupt (windows) and 8 more | covered (executed) |
| `struct Sink {` | cron, kvd, notes and 1 more | chat, cron, kvd | - | covered (referenced) |
| `Sink.Log[A: Fields[Format] + Plain](&self, io: Io, level: Level, const f: str, args: A): WriteError!void` | kvd, notes, procman | kvd | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 43 more | covered (executed) |
| `Sink.Debug[A: Fields[Format] + Plain](&self, io: Io, const f: str, args: A): WriteError!void` | kvd, notes, procman | kvd | kvd/demo (windows), kvd/demo-hold (windows), kvd/demo-hold-idle (windows) and 3 more | covered (executed) |
| `Sink.Info[A: Fields[Format] + Plain](&self, io: Io, const f: str, args: A): WriteError!void` | chat, cron, kvd and 2 more | chat, cron, kvd | chat/bad-hex (windows), chat/bad-script (windows), chat/basic (windows) and 40 more | covered (executed) |
| `Sink.Warn[A: Fields[Format] + Plain](&self, io: Io, const f: str, args: A): WriteError!void` | chat, cron, kvd and 2 more | chat, cron, kvd | chat/web-origin (windows), chat/web-wait (windows), cron/run-interrupt (windows) and 5 more | covered (executed) |
| `Sink.Error[A: Fields[Format] + Plain](&self, io: Io, const f: str, args: A): WriteError!void` | chat, cron, kvd and 2 more | chat, cron, kvd | chat/web-bad-addr (windows), cron/sim-log-fault (windows), kvd/demo-admin (windows) and 1 more | covered (executed) |
| `enum Level {` | cron, kvd, notes and 1 more | chat, cron, kvd and 1 more | - | covered (referenced) |

## std.dyn

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `dyn.Of(g: mut Gen, i: Type)` | lox | lox | - | exempt (execution) (EXEMPT.md line 65) |
| `Ref.Of[T](x: mut T): Ref from x` | lox | lox | lox/arith (windows), lox/bench (windows), lox/compile-errors (windows) and 22 more | covered (executed) |
| `Ref.Mut[T](mut self): mut T from self` | lox | lox | lox/bench (windows), lox/control (windows), lox/define (windows) and 9 more | covered (executed) |
| `Ref.Get[T](&self): &T from self` | lox | lox | lox/bench (windows), lox/control (windows), lox/define (windows) and 9 more | covered (executed) |
| `Ref.Is[T](&self): bool` | lox | lox | lox/gc-pressure (windows), lox/gc-stress (windows), lox/profile (windows) | covered (executed) |

## std.dyn.erased

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `Key[T](): u64` | lox | lox | - | covered (referenced; no instrumented body: compiled inline) (EXEMPT.md line 69) |

## std.sim

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `Sim.New(a: &Arena, seed: u64): OutOfMemory!Sim from a` | raft | bigcalc, chat, cron and 11 more | cron/bench (windows), cron/sim-bad-days (windows), cron/sim-bad-fault (windows) and 43 more | covered (executed) |
| `Sim.Io(&self): Io` | cron, fetch, kvd and 1 more | bigcalc, chat, cron and 11 more | cron/bench (windows), cron/sim-lock-fault (windows), cron/sim-log-fault (windows) and 41 more | covered (executed) |
| `Sim.Arena(&self): &Arena from self` | kvd, raft | kvd, raft | kvd/sim (windows), kvd/sim-carriers16 (windows), kvd/sim-seed (windows) and 22 more | covered (executed) |
| `Sim.Advance(mut self, d: time.Duration)` | kvd, raft | kvd, raft | kvd/sim (windows), kvd/sim-carriers16 (windows), kvd/sim-seed (windows) and 3 more | covered (executed) |
| `Sim.Run(mut self)` | kvd, raft | kvd, raft | kvd/sim (windows), kvd/sim-carriers16 (windows), kvd/sim-seed (windows) and 21 more | covered (executed) |
| `Sim.CrashAfterRandomOps(mut self)` | kvd, raft | bigcalc, kvd, orderbook and 4 more | kvd/sim (windows), kvd/sim-carriers16 (windows), kvd/sim-seed (windows) and 4 more | covered (executed) |
| `Sim.Restart(mut self): Io` | kvd, raft | bigcalc, kvd, orderbook and 4 more | kvd/sim (windows), kvd/sim-carriers16 (windows), kvd/sim-seed (windows) and 4 more | covered (executed) |
| `Sim.FailNext(mut self, op: Op, f: Fault)` | cron, fetch, kvd and 1 more | bigcalc, chat, cron and 11 more | cron/sim-lock-fault (windows), cron/sim-log-fault (windows), fetch/sim-connect-refused (windows) and 14 more | covered (executed) |
| `Sim.TermInput(mut self, data: []u8, after: time.Duration): OutOfMemory!void` | raft | fm | fm/selftest (windows) | covered (executed) |
| `Sim.Free(self)` | cron, fetch, kvd and 1 more | bigcalc, chat, cron and 11 more | cron/bench (windows), cron/sim-bad-days (windows), cron/sim-bad-fault (windows) and 43 more | covered (executed) |
| `Recorder.New(inner: Io): OutOfMemory!Recorder` | kvd, raft | kvd, raft | kvd/record (windows), raft/bench (windows), raft/crash-seeds (windows) and 17 more | covered (executed) |
| `Recorder.Io(&self): Io` | kvd, raft | kvd, raft | kvd/record (windows), raft/bench (windows), raft/crash-seeds (windows) and 17 more | covered (executed) |
| `Recorder.Log(&self, a: &Arena): OutOfMemory![]u8 from a` | kvd, raft | kvd, raft | kvd/record (windows), raft/bench (windows), raft/crash-seeds (windows) and 16 more | covered (executed) |
| `Recorder.Free(self)` | kvd, raft | kvd, raft | kvd/record (windows), raft/bench (windows), raft/crash-seeds (windows) and 17 more | covered (executed) |
| `Replayer.New(log: []u8): OutOfMemory!Replayer` | kvd, raft | kvd, raft | kvd/record (windows), kvd/replay (windows), raft/bench (windows) and 20 more | covered (executed) |
| `Replayer.Io(&self): Io` | kvd, raft | kvd, raft | kvd/record (windows), kvd/replay (windows), raft/bench (windows) and 20 more | covered (executed) |
| `Replayer.Diverged(&self): bool` | kvd, raft | kvd, raft | kvd/record (windows), kvd/replay (windows), raft/bench (windows) and 19 more | covered (executed) |
| `Replayer.Done(&self): bool` | kvd, raft | kvd, raft | kvd/record (windows), kvd/replay (windows), raft/bench (windows) and 19 more | covered (executed) |
| `Replayer.Free(self)` | kvd, raft | kvd, raft | kvd/record (windows), kvd/replay (windows), raft/bench (windows) and 20 more | covered (executed) |
| `enum Op {` | cron, fetch, kvd and 1 more | bigcalc, chat, cron and 11 more | - | covered (referenced) |
| `enum Fault {` | cron, fetch, kvd and 1 more | bigcalc, chat, cron and 11 more | - | covered (referenced) |

## std.db.sqlite

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `error Sqlite(code: i32)` | notes | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `error Busy` | notes | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `error Misuse` | notes | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `type Error = Sqlite \| Busy \| Misuse \| OutOfMemory \| Canceled` | notes | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `sqlite.Open(a: &Arena, io: Io, path: str): sqlite.Error!Db` | notes | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Db.Prepare(&self, a: &Arena, io: Io, sql: str): sqlite.Error!Stmt from self, a` | notes | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Db.Exec(&self, io: Io, sql: str): sqlite.Error!void` | notes | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Db.Close(self)` | notes | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Stmt.BindInt(mut self, i: int, v: i64): sqlite.Error!void` | notes | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Stmt.BindText(mut self, i: int, s: str): sqlite.Error!void` | notes | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Stmt.Step(mut self, io: Io): sqlite.Error!bool` | notes | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Stmt.Int(&self, col: int): i64` | notes | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Stmt.Str(&self, col: int): str from self` | notes | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Stmt.Reset(mut self)` | notes | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Stmt.Close(self)` | notes | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |

## std.big

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `error TooLarge` | bigcalc | bigcalc | - | covered (referenced) |
| `type Error = ParseError \| TooLarge \| OutOfMemory` | bigcalc | bigcalc | - | covered (referenced) |
| `big.FromI64(a: &Arena, x: i64): OutOfMemory!Int from a` | bigcalc | bigcalc | bigcalc/bench (windows), bigcalc/calc_arith (windows), bigcalc/calc_bits (windows) and 5 more | covered (executed) |
| `big.FromBytes(a: &Arena, b: []u8): OutOfMemory!Int from a` | bigcalc | bigcalc | bigcalc/calc_bits (windows), bigcalc/cert_der_leaf (windows), bigcalc/cert_show_chain (windows) and 4 more | covered (executed) |
| `big.FromWords(words: []u64, negative: bool): Int from words` | bigcalc | bigcalc | bigcalc/bench (windows), bigcalc/calc_arith (windows), bigcalc/calc_bits (windows) and 8 more | covered (executed) |
| `big.Parse(a: &Arena, s: str, base: int, max_digits: int): big.Error!Int from a` | bigcalc | bigcalc | bigcalc/bench (windows), bigcalc/calc_arith (windows), bigcalc/calc_bits (windows) and 21 more | covered (executed) |
| `Int.Clone(&self, a: &Arena): OutOfMemory!Int from a` | bigcalc | bigcalc | bigcalc/bench (windows), bigcalc/calc_arith (windows), bigcalc/calc_bits (windows) and 9 more | covered (executed) |
| `Int.Words(&self): []u64 from self` | bigcalc | bigcalc | bigcalc/bench (windows), bigcalc/calc_arith (windows), bigcalc/calc_bits (windows) and 9 more | covered (executed) |
| `Int.Add(&self, a: &Arena, y: &Int): OutOfMemory!Int from a` | bigcalc | bigcalc | bigcalc/bench (windows), bigcalc/calc_arith (windows), bigcalc/calc_bits (windows) and 6 more | covered (executed) |
| `Int.Sub(&self, a: &Arena, y: &Int): OutOfMemory!Int from a` | bigcalc | bigcalc | bigcalc/bench (windows), bigcalc/calc_arith (windows), bigcalc/calc_bits (windows) and 2 more | covered (executed) |
| `Int.Mul(&self, a: &Arena, y: &Int): OutOfMemory!Int from a` | bigcalc | bigcalc | bigcalc/bench (windows), bigcalc/calc_arith (windows), bigcalc/calc_bits (windows) and 6 more | covered (executed) |
| `Int.DivMod(&self, a: &Arena, y: &Int): OutOfMemory!(q: Int, r: Int) from a` | bigcalc | bigcalc | bigcalc/calc_arith (windows) | covered (executed) |
| `Int.Mod(&self, a: &Arena, m: &Int): OutOfMemory!Int from a` | bigcalc | bigcalc | bigcalc/bench (windows), bigcalc/calc_arith (windows) | covered (executed) |
| `Int.ModPow(&self, a: &Arena, e: &Int, m: &Int): OutOfMemory!Int from a` | bigcalc | bigcalc | bigcalc/bench (windows), bigcalc/calc_numtheory (windows), bigcalc/run_script (windows) | covered (executed) |
| `Int.ModInverse(&self, a: &Arena, m: &Int): OutOfMemory!?Int from a` | bigcalc | bigcalc | bigcalc/bench (windows), bigcalc/calc_numtheory (windows), bigcalc/err_no_inverse (windows) and 1 more | covered (executed) |
| `Int.Gcd(&self, a: &Arena, y: &Int): OutOfMemory!Int from a` | bigcalc | bigcalc | bigcalc/bench (windows), bigcalc/calc_numtheory (windows) | covered (executed) |
| `Int.Sqrt(&self, a: &Arena): OutOfMemory!Int from a` | bigcalc | bigcalc | bigcalc/bench (windows), bigcalc/calc_numtheory (windows) | covered (executed) |
| `Int.Shl(&self, a: &Arena, n: int): OutOfMemory!Int from a` | bigcalc | bigcalc | bigcalc/calc_bits (windows) | covered (executed) |
| `Int.Shr(&self, a: &Arena, n: int): OutOfMemory!Int from a` | bigcalc | bigcalc | bigcalc/calc_bits (windows) | covered (executed) |
| `Int.Neg(&self, a: &Arena): OutOfMemory!Int from a` | bigcalc | bigcalc | bigcalc/calc_arith (windows), bigcalc/calc_bits (windows), bigcalc/calc_numtheory (windows) and 2 more | covered (executed) |
| `Int.Cmp(&self, y: &Int): int` | bigcalc | bigcalc | bigcalc/run_script (windows) | covered (executed) |
| `Int.Sign(&self): int` | bigcalc | bigcalc | bigcalc/bench (windows), bigcalc/calc_arith (windows), bigcalc/calc_bits (windows) and 12 more | covered (executed) |
| `Int.BitLen(&self): int` | bigcalc | bigcalc | bigcalc/bench (windows), bigcalc/calc_arith (windows), bigcalc/calc_bits (windows) and 14 more | covered (executed) |
| `Int.Bit(&self, i: int): bool` | bigcalc | bigcalc | bigcalc/calc_bits (windows) | covered (executed) |
| `Int.ToI64(&self): ?i64` | bigcalc | bigcalc | bigcalc/bench (windows), bigcalc/calc_arith (windows), bigcalc/calc_bits (windows) and 14 more | covered (executed) |
| `Int.ToF64(&self): f64` | bigcalc | bigcalc | bigcalc/calc_bits (windows) | covered (executed) |
| `Int.Bytes(&self, a: &Arena): OutOfMemory![]u8 from a` | bigcalc | bigcalc | bigcalc/calc_bits (windows) | covered (executed) |
| `Int.Text(&self, a: &Arena, base: int): OutOfMemory!str from a` | bigcalc | bigcalc | bigcalc/calc_bits (windows), bigcalc/cert_der_leaf (windows), bigcalc/cert_show_chain (windows) and 5 more | covered (executed) |
| `Int.Format(&self, w: mut Writer): WriteError!void` | bigcalc | bigcalc | bigcalc/bench (windows), bigcalc/calc_arith (windows), bigcalc/calc_bits (windows) and 5 more | covered (executed) |

## std.encoding

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `error BadInput(offset: int)` | bigcalc, textkit | bigcalc, textkit | - | covered (referenced) |
| `error UnknownLabel` | textkit | textkit | - | covered (referenced) |
| `error Unsupported` | textkit | textkit | - | covered (referenced) |
| `error TooLarge` | bigcalc | bigcalc | - | covered (referenced) |
| `type Error = BadInput \| UnknownLabel \| Unsupported \| TooLarge` | bigcalc, textkit | bigcalc, fetch | - | covered (referenced) |
| `encoding.Base64Encode(a: &Arena, data: []u8, alphabet: Alphabet): OutOfMemory!str from a` | bigcalc, fetch, pack | bigcalc, fetch, pack | bigcalc/calc_bits (windows), bigcalc/enc_seal_128 (windows), bigcalc/enc_seal_random_nonce (windows) and 11 more | covered (executed) |
| `encoding.Base64Decode(a: &Arena, s: str, alphabet: Alphabet): encoding.Error\|OutOfMemory![]u8 from a` | bigcalc | bigcalc | bigcalc/calc_bits (windows), bigcalc/enc_open_256_armor (windows), bigcalc/enc_open_wrong_aad (windows) and 1 more | covered (executed) |
| `encoding.Base64EncodeTo(dst: mut []u8, data: []u8, alphabet: Alphabet): int` | bigcalc, pack, textkit | bigcalc, pack, textkit | bigcalc/calc_bits (windows), bigcalc/enc_seal_128 (windows), bigcalc/enc_seal_random_nonce (windows) and 30 more | covered (executed) |
| `encoding.Base64DecodeTo(dst: mut []u8, s: str, alphabet: Alphabet): encoding.Error!int` | bigcalc, pack | bigcalc, pack | bigcalc/decode_bad_base64 (windows), bigcalc/decode_base64url (windows), chat/web (windows) and 19 more | covered (executed) |
| `encoding.Base64Len(n: int, alphabet: Alphabet): int` | bigcalc, pack, textkit | bigcalc, pack, textkit | bigcalc/calc_bits (windows), bigcalc/enc_seal_128 (windows), bigcalc/enc_seal_random_nonce (windows) and 42 more | covered (executed) |
| `encoding.HexEncode(a: &Arena, data: []u8): OutOfMemory!str from a` | bigcalc, fetch, textkit | bigcalc, fetch, textkit | bigcalc/calc_bits (windows), bigcalc/cert_der_leaf (windows), bigcalc/cert_der_pem (windows) and 26 more | covered (executed) |
| `encoding.HexDecode(a: &Arena, s: str): encoding.Error\|OutOfMemory![]u8 from a` | bigcalc | bigcalc | bigcalc/calc_bits (windows), bigcalc/enc_open_128 (windows), bigcalc/enc_open_256_armor (windows) and 13 more | covered (executed) |
| `encoding.PercentEncode(a: &Arena, s: []u8, set: PercentSet): OutOfMemory!str from a` | fetch | fetch | fetch/url-escape (windows) | covered (executed) |
| `encoding.PercentDecode(a: &Arena, s: str): OutOfMemory![]u8 from a` | fetch | fetch | fetch/url-escape (windows) | covered (executed) |
| `PercentSet.Has(self, b: u8): bool` | - | - | - | exempt (EXEMPT.md line 126) |
| `encoding.Lookup(label: str): encoding.UnknownLabel\|encoding.Unsupported!Encoding` | textkit | textkit | textkit/convert-bad-dump (windows), textkit/convert-base64 (windows), textkit/convert-base64-tail (windows) and 78 more | covered (executed) |
| `Encoding.Name(self): str` | textkit | textkit | textkit/convert-bad-dump (windows), textkit/convert-base64 (windows), textkit/convert-base64-tail (windows) and 73 more | covered (executed) |
| `Encoding.Decode(self, a: &Arena, bytes: []u8): OutOfMemory!str from a` | textkit | textkit | textkit/sentences-from (windows) | covered (executed) |
| `Encoding.DecodeStrict(self, a: &Arena, bytes: []u8): encoding.BadInput\|OutOfMemory!str from a` | textkit | textkit | textkit/convert-strict (windows), textkit/convert-strict-ok (windows) | covered (executed) |
| `Encoding.DecodeKeepBom(self, a: &Arena, bytes: []u8): OutOfMemory!str from a` | - | - | - | exempt (EXEMPT.md line 127) |
| `Encoding.Encode(self, a: &Arena, s: str): encoding.BadInput\|encoding.Unsupported\|OutOfMemory![]u8 from a` | textkit | textkit | textkit/convert-base64 (windows), textkit/convert-base64-tail (windows), textkit/convert-hex-stream (windows) and 68 more | covered (executed) |
| `Encoding.Decoder(self): Decoder` | textkit | textkit | textkit/convert-base64 (windows), textkit/convert-base64-tail (windows), textkit/convert-hex-stream (windows) and 67 more | covered (executed) |
| `Decoder.Push(mut self, a: &Arena, chunk: []u8, last: bool): OutOfMemory!str from a` | textkit | textkit | textkit/convert-base64 (windows), textkit/convert-base64-tail (windows), textkit/convert-hex-stream (windows) and 67 more | covered (executed) |
| `Encoding.Of(codec: Codec): Encoding` | - | - | - | exempt (EXEMPT.md line 128) |
| `encoding.ParsePem(a: &Arena, text: str into a): encoding.Error\|OutOfMemory!List[PemBlock] from a, text` | bigcalc, fetch | bigcalc, fetch | bigcalc/cert_der_pem (windows), bigcalc/cert_show_chain (windows), bigcalc/cert_show_der (windows) and 59 more | covered (executed) |
| `encoding.FormatPem(w: mut Writer, label: str, der: []u8): WriteError!void` | bigcalc | bigcalc | bigcalc/key_public_ed25519 (windows), bigcalc/key_public_p256 (windows), bigcalc/key_public_rsa (windows) | covered (executed) |
| `encoding.DerRead(data: []u8): encoding.Error!(value: DerValue, rest: []u8) from data` | bigcalc | bigcalc | bigcalc/cert_der_deep (windows), bigcalc/cert_der_leaf (windows), bigcalc/cert_der_pem (windows) and 63 more | covered (executed) |
| `DerValue.Children(&self): DerIter from self.content` | bigcalc | bigcalc | bigcalc/cert_show_chain (windows), bigcalc/cert_show_der (windows), bigcalc/cert_verify_ed25519 (windows) and 52 more | covered (executed) |
| `DerIter.Next(mut self): encoding.Error!?DerValue from self.data` | bigcalc | bigcalc | bigcalc/cert_show_chain (windows), bigcalc/cert_show_der (windows), bigcalc/cert_verify_ed25519 (windows) and 52 more | covered (executed) |
| `DerValue.Integer(&self): encoding.Error![]u8 from self.content` | bigcalc | bigcalc | bigcalc/cert_der_leaf (windows), bigcalc/cert_show_chain (windows), bigcalc/cert_show_der (windows) and 47 more | covered (executed) |
| `DerValue.BitString(&self): encoding.Error![]u8 from self.content` | bigcalc | bigcalc | bigcalc/cert_show_chain (windows), bigcalc/key_show_rsa1024 (windows) | covered (executed) |
| `DerValue.Boolean(&self): encoding.Error!bool` | bigcalc | bigcalc | bigcalc/cert_der_leaf (windows), bigcalc/cert_show_chain (windows), bigcalc/verify_p384_tampered (windows) and 1 more | covered (executed) |
| `DerWriter.New(a: &Arena): DerWriter from a` | bigcalc | bigcalc | bigcalc/calc_bits (windows), bigcalc/key_public_ed25519 (windows), bigcalc/key_public_p256 (windows) | covered (executed) |
| `DerWriter.Begin(mut self, tag: u32): OutOfMemory!void` | bigcalc | bigcalc | bigcalc/key_public_ed25519 (windows), bigcalc/key_public_p256 (windows) | covered (executed) |
| `DerWriter.End(mut self): encoding.Error!void` | bigcalc | bigcalc | bigcalc/key_public_ed25519 (windows), bigcalc/key_public_p256 (windows) | covered (executed) |
| `DerWriter.Primitive(mut self, tag: u32, content: []u8): OutOfMemory!void` | bigcalc | bigcalc | bigcalc/key_public_ed25519 (windows), bigcalc/key_public_p256 (windows) | covered (executed) |
| `DerWriter.Integer(mut self, magnitude: []u8): OutOfMemory!void` | bigcalc | bigcalc | bigcalc/calc_bits (windows) | covered (executed) |
| `DerWriter.Bytes(self): encoding.Error![]u8` | bigcalc | bigcalc | bigcalc/calc_bits (windows), bigcalc/key_public_ed25519 (windows), bigcalc/key_public_p256 (windows) | covered (executed) |
| `enum PercentSet {` | - | fetch | - | covered (referenced) |
| `struct PemBlock {` | bigcalc | bigcalc, fetch | - | covered (referenced) |
| `struct DerValue {` | bigcalc | bigcalc | - | covered (referenced) |
| `struct Codec {` | - | - | - | exempt (EXEMPT.md line 129) |

## std.encoding.cjk

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `cjk.Lookup(label: str): encoding.UnknownLabel!encoding.Encoding` | - | - | - | exempt (EXEMPT.md line 133) |

## std.url

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `error BadUrl(offset: int)` | fetch | fetch | - | covered (referenced) |
| `error BadHost` | fetch | fetch | - | covered (referenced) |
| `error IdnaUnsupported` | fetch | fetch | - | covered (referenced) |
| `type Error = BadUrl \| BadHost \| IdnaUnsupported` | fetch | chat, fetch | - | covered (referenced) |
| `url.Parse(a: &Arena, input: str into a, base: ?&Url): url.Error\|OutOfMemory!Url from a, input` | chat, fetch | chat, fetch | chat/web (windows), chat/web-bad-addr (windows), chat/web-errors (windows) and 74 more | covered (executed) |
| `Url.Href(&self): str from self` | fetch | fetch | fetch/url-base-set (windows), fetch/url-idna (windows), fetch/url-ipv6 (windows) and 2 more | covered (executed) |
| `Url.Scheme(&self): str from self` | fetch | fetch | chat/web (windows), chat/web-errors (windows), chat/web-many-carriers (windows) and 71 more | covered (executed) |
| `Url.Username(&self): str from self` | fetch | fetch | fetch/url-base-set (windows), fetch/url-idna (windows), fetch/url-ipv6 (windows) and 2 more | covered (executed) |
| `Url.Password(&self): str from self` | fetch | fetch | fetch/url-base-set (windows), fetch/url-idna (windows), fetch/url-ipv6 (windows) and 2 more | covered (executed) |
| `Url.Host(&self): Host from self` | fetch | fetch | fetch/url-base-set (windows), fetch/url-idna (windows), fetch/url-ipv6 (windows) and 2 more | covered (executed) |
| `Url.Hostname(&self): str from self` | fetch | fetch | chat/web (windows), chat/web-errors (windows), chat/web-many-carriers (windows) and 71 more | covered (executed) |
| `Url.Port(&self): ?u16` | fetch | fetch | chat/web (windows), chat/web-errors (windows), chat/web-many-carriers (windows) and 69 more | covered (executed) |
| `Url.Path(&self): str from self` | fetch | fetch | chat/web (windows), chat/web-errors (windows), chat/web-many-carriers (windows) and 71 more | covered (executed) |
| `Url.Query(&self): str from self` | fetch | fetch | chat/web (windows), chat/web-errors (windows), chat/web-many-carriers (windows) and 66 more | covered (executed) |
| `Url.Fragment(&self): str from self` | fetch | fetch | fetch/url-base-set (windows), fetch/url-idna (windows), fetch/url-ipv6 (windows) and 2 more | covered (executed) |
| `Url.Origin(&self, a: &Arena): OutOfMemory!str from a` | fetch | fetch | fetch/url-base-set (windows), fetch/url-idna (windows), fetch/url-ipv6 (windows) and 2 more | covered (executed) |
| `Url.Set(&self, a: &Arena, part: Part, value: str): url.Error\|OutOfMemory!Url from a` | fetch | fetch | fetch/echo-request (windows), fetch/url-base-set (windows) | covered (executed) |
| `Url.Format(&self, w: mut Writer): WriteError!void` | fetch | fetch | fetch/url-base-set (windows), fetch/url-idna (windows), fetch/url-ipv6 (windows) and 2 more | covered (executed) |
| `url.ParseQuery(a: &Arena, s: str): OutOfMemory!List[(name: str, value: str)] from a` | chat, fetch | chat, fetch | chat/web (windows), chat/web-errors (windows), chat/web-many-carriers (windows) and 10 more | covered (executed) |
| `url.FormatQuery(a: &Arena, pairs: [](name: str, value: str)): OutOfMemory!str from a` | fetch | fetch | fetch/bad-url (windows), fetch/bench (windows), fetch/cookies (windows) and 60 more | covered (executed) |
| `url.DomainToAscii(a: &Arena, domain: str, strict: bool): url.Error\|OutOfMemory!str from a` | fetch | fetch | fetch/url-idna (windows) | covered (executed) |
| `url.DomainToUnicode(a: &Arena, domain: str): OutOfMemory!str from a` | fetch | fetch | fetch/url-idna (windows) | covered (executed) |
| `enum Host {` | fetch | fetch | - | covered (referenced) |
| `enum Part {` | fetch | fetch | - | covered (referenced) |

## std.websocket

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `error Protocol(code: u16)` | fetch | fetch | - | covered (referenced) |
| `error TooLarge` | fetch | fetch | - | covered (referenced) |
| `error PeerClosed(code: u16)` | fetch | chat, fetch | - | covered (referenced) |
| `error Handshake(status: int)` | fetch | fetch | - | covered (referenced) |
| `type Error = Protocol \| TooLarge \| PeerClosed \| Handshake \| net.Error \| http.NoSecureTransport \| OutOfMemory` | fetch | chat, fetch | - | covered (referenced) |
| `websocket.Accept(h: &Heap, io: Io, req: &http.Request, t: http.Taken, protocols: []str, const allow: fn(origin: str): bool): websocket.Error!Conn from h, t` | chat | chat | chat/web (windows), chat/web-errors (windows), chat/web-many-carriers (windows) and 5 more | covered (executed) |
| `websocket.Dial(h: &Heap, a: &Arena, io: Io, u: &url.Url, o: &Options): websocket.Error!Conn from h` | chat, fetch | chat, fetch | chat/web (windows), chat/web-errors (windows), chat/web-many-carriers (windows) and 13 more | covered (executed) |
| `Conn.Send(&self, io: Io, kind: Kind, data: []u8): websocket.Error!void` | chat, fetch | chat, fetch | chat/web (windows), chat/web-errors (windows), chat/web-many-carriers (windows) and 10 more | covered (executed) |
| `Conn.Recv(&self, a: &Arena, io: Io): websocket.Error!Message from a` | chat, fetch | chat, fetch | chat/web (windows), chat/web-errors (windows), chat/web-many-carriers (windows) and 10 more | covered (executed) |
| `Conn.Ping(&self, io: Io, data: []u8): websocket.Error!void` | fetch | fetch | fetch/websocket (windows) | covered (executed) |
| `Conn.Protocol(&self): ?str from self` | fetch | chat, fetch | chat/web (windows), chat/web-errors (windows), chat/web-many-carriers (windows) and 10 more | covered (executed) |
| `Conn.Close(self, io: Io, code: u16, reason: str): websocket.Error!void` | chat, fetch | chat, fetch | chat/web (windows), chat/web-errors (windows), chat/web-many-carriers (windows) and 9 more | covered (executed) |
| `Conn.Abandon(self)` | chat, fetch | chat, fetch | chat/web-errors (windows), chat/web-shutdown (windows), chat/web-wait (windows) and 3 more | covered (executed) |
| `enum Kind {` | fetch | chat, fetch | - | covered (referenced) |
| `struct Message {` | fetch | chat, fetch | - | covered (referenced) |
| `struct Options {` | fetch | chat, fetch | - | covered (referenced) |

## std.term

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `error NotTerminal` | fm | fm | - | covered (referenced) |
| `error Device(code: i64)` | fm | fm | - | covered (referenced) |
| `error Unsupported` | fm | fm | - | covered (referenced) |
| `type Error = NotTerminal \| Device \| Unsupported \| Canceled \| OutOfMemory` | fm | fm | - | covered (referenced) |
| `term.Open(io: Io): term.Error!Term` | fm | fm | fm/interrupt (windows), fm/selftest (windows), fm/session (windows) and 1 more | covered (executed) |
| `term.IsTerminal(io: Io, s: Std): bool` | fm | fm | fm/interrupt (windows), fm/no-terminal (windows), fm/session (windows) and 1 more | covered (executed) |
| `term.UseColor(io: Io, s: Std): bool` | fm | fm | fm/interrupt (windows), fm/selftest (windows), fm/session (windows) and 1 more | covered (executed) |
| `Term.Raw(mut self, io: Io): term.Error!void` | fm | fm | fm/interrupt (windows), fm/selftest (windows), fm/session (windows) and 1 more | covered (executed) |
| `Term.Restore(mut self, io: Io): term.Error!void` | fm | fm | fm/interrupt (windows), fm/selftest (windows), fm/session (windows) and 1 more | covered (executed) |
| `Term.Size(&self, io: Io): term.Error!(cols: int, rows: int)` | fm | fm | fm/interrupt (windows), fm/selftest (windows), fm/session (windows) and 1 more | covered (executed) |
| `Term.Read(mut self, a: &Arena, io: Io): term.Error!Event from a` | fm | fm | fm/interrupt (windows), fm/selftest (windows), fm/session (windows) and 1 more | covered (executed) |
| `Term.ReadSecret(mut self, a: &Arena, io: Io): term.Error!str from a` | fm | fm | fm/selftest (windows), fm/session (windows) | covered (executed) |
| `Term.Writer(mut self, io: Io, buf: mut []u8): Writer from self, buf` | fm | fm | fm/interrupt (windows), fm/selftest (windows), fm/session (windows) and 1 more | covered (executed) |
| `Term.Close(self, io: Io)` | fm | fm | fm/interrupt (windows), fm/selftest (windows), fm/session (windows) and 1 more | covered (executed) |
| `Style.Format(&self, w: mut Writer): WriteError!void` | fm | fm | fm/session (windows), fm/shell-input (windows) | covered (executed) |
| `term.Sanitize(w: mut Writer, s: str): WriteError!void` | fm | fm | fm/interrupt (windows), fm/selftest (windows), fm/session (windows) and 1 more | covered (executed) |
| `term.MoveTo(w: mut Writer, row: int, col: int): WriteError!void` | fm | fm | fm/interrupt (windows), fm/selftest (windows), fm/session (windows) and 1 more | covered (executed) |
| `term.Clear(w: mut Writer): WriteError!void` | fm | fm | fm/interrupt (windows), fm/selftest (windows), fm/session (windows) and 1 more | covered (executed) |
| `term.AltScreen(w: mut Writer, on: bool): WriteError!void` | fm | fm | fm/interrupt (windows), fm/selftest (windows), fm/session (windows) and 1 more | covered (executed) |
| `term.ShowCursor(w: mut Writer, on: bool): WriteError!void` | fm | fm | fm/interrupt (windows), fm/selftest (windows), fm/session (windows) and 1 more | covered (executed) |
| `term.BracketedPaste(w: mut Writer, on: bool): WriteError!void` | fm | fm | fm/interrupt (windows), fm/selftest (windows), fm/session (windows) and 1 more | covered (executed) |
| `term.MouseCapture(w: mut Writer, on: bool): WriteError!void` | fm | fm | fm/interrupt (windows), fm/selftest (windows), fm/session (windows) and 1 more | covered (executed) |
| `term.KittyKeys(w: mut Writer, on: bool): WriteError!void` | fm | fm | fm/interrupt (windows), fm/selftest (windows), fm/session (windows) and 1 more | covered (executed) |
| `enum Std {` | fm | fm | - | covered (referenced) |
| `enum Event {` | fm | fm | - | covered (referenced) |
| `enum Key {` | fm | fm | - | covered (referenced) |
| `struct Mods {` | fm | fm | - | covered (referenced) |
| `struct Style {` | fm | fm | - | covered (referenced) |
| `enum Color {` | fm | fm | - | covered (referenced) |

## std.x509

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `error Malformed(offset: int)` | bigcalc | bigcalc | - | covered (referenced) |
| `error UnknownAuthority` | bigcalc | bigcalc, fetch | - | covered (referenced) |
| `error Expired` | bigcalc | bigcalc, fetch | - | covered (referenced) |
| `error NameMismatch` | bigcalc | bigcalc, fetch | - | covered (referenced) |
| `error BadSignature` | bigcalc | bigcalc, fetch | - | covered (referenced) |
| `error BadUsage` | bigcalc | bigcalc, fetch | - | covered (referenced) |
| `error TooComplex` | bigcalc | bigcalc | - | covered (referenced) |
| `error Unsupported` | bigcalc | bigcalc | - | covered (referenced) |
| `type Error = Malformed \| UnknownAuthority \| Expired \| NameMismatch \| BadSignature \| BadUsage \| TooComplex \| Unsupported \| Canceled \| OutOfMemory` | bigcalc | bigcalc, fetch | - | covered (referenced) |
| `x509.Parse(a: &Arena, der: []u8): x509.Error!Certificate from a, der` | bigcalc | bigcalc | bigcalc/cert_show_der (windows), bigcalc/key_show_not_a_key (windows), bigcalc/verify_ed25519_tampered (windows) and 14 more | covered (executed) |
| `x509.ParsePem(a: &Arena, pem: str): x509.Error!List[Certificate] from a` | bigcalc, fetch | bigcalc, fetch | bigcalc/cert_show_chain (windows), bigcalc/cert_show_der (windows), bigcalc/cert_show_garbage (windows) and 27 more | covered (executed) |
| `x509.SystemRoots(a: &Arena, io: Io): x509.Error!Pool from a` | fetch | fetch | fetch/cert-system-roots (windows), fetch/handshake-unknown-root (windows), fetch/tls-unknown-root (windows) and 1 more | covered (executed) |
| `x509.SystemPool(h: &Heap, io: Io): x509.Error!&Pool` | - | - | fetch/handshake-unknown-root (windows), fetch/tls-unknown-root (windows), fetch/websocket-tls-unknown-root (windows) | exempt (reference; executed) (EXEMPT.md line 137) |
| `x509.Verify(a: &Arena, io: Io, chain: []Certificate, roots: &Pool, now: time.Time, host: str): x509.Error!void` | bigcalc, fetch | bigcalc, fetch | bigcalc/cert_verify_ed25519 (windows), bigcalc/cert_verify_expired (windows), bigcalc/cert_verify_ip (windows) and 22 more | covered (executed) |
| `Pool.New(certs: []Certificate): Pool from certs` | bigcalc, fetch | bigcalc, fetch | bigcalc/cert_verify_ed25519 (windows), bigcalc/cert_verify_expired (windows), bigcalc/cert_verify_ip (windows) and 79 more | covered (executed) |
| `Certificate.Subject(&self, a: &Arena): OutOfMemory!str from a` | bigcalc, fetch | bigcalc, fetch | bigcalc/cert_show_chain (windows), bigcalc/cert_show_der (windows), bigcalc/cert_verify_ed25519 (windows) and 8 more | covered (executed) |
| `Certificate.Issuer(&self, a: &Arena): OutOfMemory!str from a` | bigcalc, fetch | bigcalc, fetch | bigcalc/cert_show_chain (windows), bigcalc/cert_show_der (windows), fetch/cert-at (windows) and 4 more | covered (executed) |
| `Certificate.Names(&self, a: &Arena): OutOfMemory!List[str] from a, self *self into a` | bigcalc, fetch | bigcalc, fetch | bigcalc/cert_show_chain (windows), bigcalc/cert_show_der (windows), fetch/cert-at (windows) and 4 more | covered (executed) |
| `Certificate.NotBefore(&self): time.Time` | bigcalc, fetch | bigcalc, fetch | bigcalc/cert_show_chain (windows), bigcalc/cert_show_der (windows), fetch/cert-at (windows) and 4 more | covered (executed) |
| `Certificate.NotAfter(&self): time.Time` | bigcalc, fetch | bigcalc, fetch | bigcalc/cert_show_chain (windows), bigcalc/cert_show_der (windows), fetch/cert-at (windows) and 4 more | covered (executed) |
| `Certificate.Der(&self): []u8 from self` | bigcalc, fetch | bigcalc, fetch | bigcalc/cert_show_chain (windows), bigcalc/cert_show_der (windows), fetch/cert-at (windows) and 15 more | covered (executed) |
| `Certificate.PublicKey(&self): crypto.Error!crypto.PublicKey from self` | bigcalc | bigcalc | bigcalc/cert_show_chain (windows), bigcalc/cert_show_der (windows), fetch/handshake (windows) and 4 more | covered (executed) |
| `struct Certificate {` | bigcalc | bigcalc, fetch | - | covered (referenced) |
| `struct Pool {` | bigcalc | bigcalc, fetch | - | covered (referenced) |

## std.tls

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `error Alert(code: u8)` | fetch | fetch | - | covered (referenced) |
| `error Protocol` | fetch | fetch | - | covered (referenced) |
| `error Unsupported` | fetch | fetch | - | covered (referenced) |
| `type Error = Alert \| Protocol \| Unsupported \| x509.Error \| net.Error` | fetch | fetch | - | covered (referenced) |
| `tls.Client(h: &Heap, io: Io, conn: net.Conn, c: &Config, host: str): tls.Error!Conn from h, conn` | fetch | fetch | fetch/handshake (windows), fetch/handshake-timeout (windows), fetch/handshake-unknown-root (windows) and 8 more | covered (executed) |
| `tls.Connector(c: &Config): net.Secure from c` | fetch | fetch | fetch/bad-url (windows), fetch/bench (windows), fetch/cookies (windows) and 63 more | covered (executed) |
| `Conn.Read(&self, io: Io, buf: mut []u8): tls.Error!int` | fetch | fetch | fetch/handshake (windows), fetch/handshake-timeout (windows), fetch/tls (windows) and 2 more | covered (executed) |
| `Conn.WriteAll(&self, io: Io, data: []u8): tls.Error!void` | fetch | fetch | fetch/handshake (windows), fetch/handshake-timeout (windows), fetch/tls (windows) and 2 more | covered (executed) |
| `Conn.Reader(&self, io: Io, buf: mut []u8): Reader from self, buf` | fetch | fetch | fetch/handshake (windows), fetch/handshake-timeout (windows), fetch/tls (windows) and 2 more | covered (executed) |
| `Conn.Writer(&self, io: Io, buf: mut []u8): Writer from self, buf` | fetch | fetch | fetch/handshake (windows), fetch/handshake-timeout (windows), fetch/tls (windows) and 2 more | covered (executed) |
| `Conn.Alpn(&self): ?str from self` | fetch | fetch | fetch/handshake (windows) | covered (executed) |
| `Conn.Close(self, io: Io): tls.Error!void` | fetch | fetch | fetch/handshake (windows), fetch/handshake-timeout (windows), fetch/tls (windows) and 2 more | covered (executed) |
| `Conn.Abandon(self)` | fetch | fetch | fetch/handshake (windows), fetch/handshake-timeout (windows), fetch/tls (windows) and 2 more | covered (executed) |
| `tls.Server(h: &Heap, io: Io, conn: net.Conn, c: &Config): tls.Error!Conn from h, conn` | fetch | fetch | fetch/handshake (windows), fetch/handshake-timeout (windows), fetch/handshake-unknown-root (windows) and 9 more | covered (executed) |
| `tls.Acceptor(c: &Config): net.Secure from c` | fetch | fetch | fetch/handshake (windows), fetch/handshake-timeout (windows), fetch/handshake-unknown-root (windows) and 9 more | covered (executed) |
| `struct Config {` | fetch | fetch | - | covered (referenced) |
| `struct Identity {` | fetch | fetch | - | covered (referenced) |
| `enum Version {` | fetch | fetch | - | covered (referenced) |

## std.csv

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `error Quote(line: i64, column: int)` | orderbook | orderbook | - | covered (referenced) |
| `error TooLarge(line: i64)` | orderbook | orderbook | - | covered (referenced) |
| `type Error = Quote \| TooLarge \| ReadError` | orderbook | orderbook | - | covered (referenced) |
| `Records.New(r: mut Reader, buf: mut []u8, o: &Options): Records from r, buf` | orderbook | orderbook | orderbook/account-missing (windows), orderbook/bad-id (windows), orderbook/bad-price (windows) and 30 more | covered (executed) |
| `Records.Next(mut self): csv.Error!?Record from self` | orderbook | orderbook | orderbook/account-missing (windows), orderbook/bad-id (windows), orderbook/bad-price (windows) and 30 more | covered (executed) |
| `Record.Len(&self): int` | orderbook | orderbook | orderbook/account-missing (windows), orderbook/bad-id (windows), orderbook/bad-price (windows) and 30 more | covered (executed) |
| `Record.Field(&self, i: int): ?str from self` | orderbook | orderbook | orderbook/account-missing (windows), orderbook/bad-id (windows), orderbook/bad-price (windows) and 29 more | covered (executed) |
| `Record.Line(&self): i64` | orderbook | orderbook | orderbook/account-missing (windows), orderbook/bad-id (windows), orderbook/bad-price (windows) and 28 more | covered (executed) |
| `csv.WriteRecord(w: mut Writer, values: []str, o: &Options): WriteError!void` | orderbook | orderbook | orderbook/csv-book (windows), orderbook/csv-files (windows) | covered (executed) |
| `csv.Codec(g: mut Gen, t: Type)` | orderbook | orderbook | - | covered (referenced; no instrumented body: compiled inline) |
| `struct Options {` | orderbook | orderbook | - | covered (referenced) |

## std.dl

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `error NotFound` | libprobe | libprobe | - | covered (referenced) |
| `error Invalid` | libprobe | libprobe | - | covered (referenced) |
| `error InitFailed` | libprobe | libprobe | - | covered (referenced) |
| `error BadName` | libprobe | libprobe | - | covered (referenced) |
| `error Unsupported` | libprobe | libprobe | - | covered (referenced) |
| `error Os(code: i64)` | libprobe | libprobe | - | covered (referenced) |
| `type Error = NotFound \| Invalid \| InitFailed \| BadName \| Unsupported \| Os` | libprobe | libprobe | - | covered (referenced) |
| `dl.Open(io: Io, name: str, search: Search): dl.Error!Library` | libprobe | libprobe | libprobe/call-long-text (windows), libprobe/call-no-symbol (windows), libprobe/call-none (windows) and 17 more | covered (executed) |
| `Library.Symbol(&self, name: str): ?*u8` | libprobe | libprobe | libprobe/call-long-text (windows), libprobe/call-no-symbol (windows), libprobe/call-none (windows) and 7 more | covered (executed) |
| `Library.Close(self, io: Io)` | libprobe | libprobe | libprobe/call-long-text (windows), libprobe/call-no-symbol (windows), libprobe/call-none (windows) and 8 more | covered (executed) |
| `Library.Detach(self)` | libprobe | libprobe | libprobe/open-keep (windows) | covered (executed) |
| `enum Search {` | libprobe | libprobe | - | covered (referenced) |

## std.runtime

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `runtime.Processors(): int` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `runtime.Carriers(): int` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `runtime.StackBounds(): (lo: uint, hi: uint)` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `runtime.StackRemaining(): uint` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `runtime.Blocking[T: Send](io: Io, state: T, f: fn(T): T): Canceled!T` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Waker.New(): Waker` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Waker.Wait(&self, io: Io): Canceled!void` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Waker.Wake(&self)` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `runtime.AddFaultFilter(f: fn(&FaultContext): FaultAction): FilterId` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `FaultContext.Kind(&self): FaultKind` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `FaultContext.Addr(&self): uint` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `FaultContext.Pc(&self): uint` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `FaultContext.Sp(&self): uint` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `FaultContext.Reg(&self, n: int): u64` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `FaultContext.SetPc(mut self, pc: uint)` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `FaultContext.SetSp(mut self, sp: uint)` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `FaultContext.SetReg(mut self, n: int, v: u64)` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `runtime.ForeignIo(ctx: *u8, table: &IoTable): Foreign` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Foreign.Io(&self): Io` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Foreign.Release(self)` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `runtime.ForeignContext(io: Io): *u8` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `runtime.Dedicated(io: Io): Io` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `runtime.Thread(io: Io, f: fn(Io)): Joined` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Joined.Join(self, io: Io): Canceled!void` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `runtime.TaskIo(): ?Io` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `runtime.Cycles(): u64` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `error Unsupported` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `error OsError(code: i64)` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `type Error = Unsupported \| OsError` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `runtime.WriteDump(path: str): runtime.Error!void` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `enum FaultKind {` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `enum FaultAction {` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `struct PanicInfo {` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |

## std.vm

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `error Misaligned` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `error Unsupported` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `error Os(code: i64)` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `type Error = OutOfMemory \| Misaligned \| Unsupported \| Os` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `vm.PageSize(): int` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `vm.Reserve(size: int, align: int): vm.Error!Region` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Region.Base(&self): uint` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Region.Size(&self): int` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Region.Commit(&self, offset: int, len: int): vm.Error!void` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Region.Decommit(&self, offset: int, len: int)` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Region.Protect(&self, offset: int, len: int, p: Prot): vm.Error!void` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Region.Release(self)` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `CodeRegion.New(size: int): vm.Error!CodeRegion` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `CodeRegion.Reserve(size: int): vm.Error!CodeRegion` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `CodeRegion.Size(&self): int` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `CodeRegion.Writable(&self): mut []u8 from self` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `CodeRegion.Entry(&self, offset: int): uint` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `CodeRegion.Commit(&self, offset: int, len: int): vm.Error!void` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `CodeRegion.Seal(&self): vm.Error!void` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `CodeRegion.Unseal(&self): vm.Error!void` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `CodeRegion.Flush(&self, offset: int, len: int)` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `CodeRegion.Release(self)` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `enum Prot {` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |

## std.linux

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `linux.Syscall(nr: uint, a0: uint, a1: uint, a2: uint, a3: uint, a4: uint, a5: uint): (r: uint, errno: int)` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |

## std.testing

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `testing.Fuzz(f: fn([]u8))` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `testing.Bench(f: fn(n: int))` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |

## std.build

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `build.File(path: str): []u8` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `enum ProfileKind {` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |

## std.core

| Line | Owned by | Referenced by | Executed by | Verdict |
|---|---|---|---|---|
| `Type.Methods(&self): []Method` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Method.Flows(&self): []Flow` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Method.HasFlows(&self): bool` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Flow.Kind(&self): FlowKind` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Flow.Param(&self): str` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Flow.Path(&self): str` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Flow.Field(&self): str` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `Gen.Input(&self, path: str): []u8` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
| `enum FlowKind {` | - | - | - | gap: not executed, and references are unknown (mk query uses: see Static references above) |
