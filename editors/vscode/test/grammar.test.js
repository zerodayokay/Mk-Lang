// Why: the TextMate grammar is the only highlighting an editor has before
// mk lsp starts, and a regex slip shows up as a wrong color with no error.
// What: an offline test of syntaxes/mk.tmLanguage.json: every keyword,
// contextual word, universe name, primitive type, operator, literal and
// comment form, a few lines of real std files, and every .mk under std/.
// How: tokenizes with VS Code's own engine (vscode-textmate and
// vscode-oniguruma) and compares scopes. The word lists are read from
// doc/reference.md and from identKind in internal/syntax/lexer.go, so a new
// keyword fails here until the grammar knows it. Run: npm test.

'use strict';

const fs = require('fs');
const path = require('path');
const vsctm = require('vscode-textmate');
const oniguruma = require('vscode-oniguruma');

const repo = path.resolve(__dirname, '..', '..', '..');
const grammarPath = path.join(__dirname, '..', 'syntaxes', 'mk.tmLanguage.json');

let failures = 0;
let checks = 0;
function fail(msg) {
  failures++;
  console.error('FAIL ' + msg);
}

async function loadGrammar() {
  const wasm = fs.readFileSync(require.resolve('vscode-oniguruma/release/onig.wasm')).buffer;
  await oniguruma.loadWASM(wasm);
  const registry = new vsctm.Registry({
    onigLib: Promise.resolve({
      createOnigScanner: (p) => new oniguruma.OnigScanner(p),
      createOnigString: (s) => new oniguruma.OnigString(s),
    }),
    loadGrammar: async (scope) =>
      scope === 'source.mk' ? vsctm.parseRawGrammar(fs.readFileSync(grammarPath, 'utf8'), grammarPath) : null,
  });
  return registry.loadGrammar('source.mk');
}

// tokenize returns, per line, the tokens with their text and scopes.
function tokenize(grammar, text) {
  let state = vsctm.INITIAL;
  return text.split(/\r?\n/).map((line) => {
    const r = grammar.tokenizeLine(line, state);
    state = r.ruleStack;
    return r.tokens.map((t) => ({ text: line.slice(t.startIndex, t.endIndex), scopes: t.scopes }));
  });
}

// expect checks that the `skip`-th token whose trimmed text is `word`, on
// the first line of `src` that contains it, has an innermost scope starting
// with `want`. `src` may span lines; the grammar state carries over.
function expect(g, src, word, want, skip = 0) {
  checks++;
  const lines = tokenize(g, src);
  const hits = lines.flat().filter((t) => t.text.trim() === word);
  const t = hits[skip];
  const got = t ? t.scopes.at(-1) : `(no token; tokens: ${lines.flat().map((t) => t.text).join('|')})`;
  if (!got.startsWith(want)) fail(`${JSON.stringify(src)}: ${JSON.stringify(word)} is ${got}, want ${want}*`);
}

// referenceWords reads the word lists of doc/reference.md's lexical rules.
function referenceWords() {
  const ref = fs.readFileSync(path.join(repo, 'doc', 'reference.md'), 'utf8');
  const list = (label) => {
    const m = new RegExp(label + ':\\s*`([^`]+)`').exec(ref);
    if (!m) throw new Error(`no "${label}:" list in doc/reference.md`);
    return m[1].split(/\s+/);
  };
  return { keywords: list('Keywords'), contextual: list('Contextual'), universe: list('Universe names') };
}

// lexerKeywords reads the words identKind in internal/syntax/lexer.go turns
// into keyword tokens.
function lexerKeywords() {
  const src = fs.readFileSync(path.join(repo, 'internal', 'syntax', 'lexer.go'), 'utf8');
  const m = /^func identKind\([\s\S]*?^}/m.exec(src);
  if (!m) throw new Error('no identKind in internal/syntax/lexer.go');
  return [...m[0].matchAll(/"([a-z]+)"/g)].map((x) => x[1]);
}

async function main() {
  const g = await loadGrammar();
  const words = referenceWords();
  const lexer = lexerKeywords();

  // 1. Every keyword, from both lists, as a statement head.
  for (const kw of new Set([...words.keywords, ...lexer])) {
    checks++;
    const t = tokenize(g, `    ${kw} x`)[0].find((t) => t.text === kw);
    const s = t ? t.scopes.at(-1) : '(none)';
    if (!/^(keyword|storage)\./.test(s)) fail(`keyword ${kw}: ${s}`);
  }
  for (const kw of lexer) {
    if (!words.keywords.includes(kw)) fail(`lexer keyword ${kw} is not in doc/reference.md's Keywords list`);
  }

  // 2. Each contextual word in its position, and as a plain name elsewhere.
  const contextual = {
    from: ['Pick(a: &T, b: &T): &T from a {', 'keyword.other.flow'],
    into: ['Push(l: mut List, x: &T) *x into l {', 'keyword.other.flow'],
    ends: ['Release(self) ends self {', 'keyword.other.flow'],
    fuel: ['const T = Build() fuel 100_000_000', 'keyword.other.fuel'],
    packed: ['export packed struct Header {', 'storage.modifier'],
    open: ['export open type Error = A | B', 'storage.modifier'],
    self: ['    return self.v', 'variable.language.self'],
    Self: ['    return Self(v: 1)', 'variable.language.self'],
    result: ['    ensures result >= 0', 'variable.language.result'],
  };
  for (const w of words.contextual) {
    if (!contextual[w]) {
      fail(`contextual word ${w} has no case in this test`);
      continue;
    }
    expect(g, contextual[w][0], w, contextual[w][1]);
  }
  for (const w of ['from', 'into', 'ends', 'fuel', 'packed', 'open', 'result']) {
    expect(g, `    ${w} := 3`, w, 'source.mk');
  }
  // The systems words, at their positions.
  const systems = [
    ['export extern naked Entry() {', 'naked', 'storage.modifier'],
    ['export naked Entry() {', 'naked', 'storage.modifier'],
    ['export extern naked Entry() {', 'Entry', 'entity.name.function'],
    ['export AesRound(s: [16]u8): [16]u8 cpu "aes"', 'cpu', 'storage.modifier'],
    ['    cpu "sha, sha2"', 'cpu', 'storage.modifier'],
    ['const Header: [3]u32 = [1, 0, 2] section ".multiboot"', 'section', 'storage.modifier'],
    ['align(4096) struct Page {', 'align', 'storage.modifier'],
    ['packed(2) struct Pair {', 'packed', 'storage.modifier'],
    ['    cpu := 1', 'cpu', 'source.mk'],
    ['    section := 1', 'section', 'source.mk'],
  ];
  for (const [src, w, want] of systems) expect(g, src, w, want);
  // asm's operand and option words: keywords inside asm(...), names outside.
  const asmWords = 'in out inout lateout inlateout const sym options clobber_abi att raw nomem nostack preserves_flags pure readonly noreturn reg reg_byte kreg xmm_reg ymm_reg zmm_reg'.split(' ');
  const asmSrc =
    '        asm("mov {t:r}, rbx\\ncpuid", t: out(reg) b, in(reg_byte) x, inout("eax") a, lateout(xmm_reg) c, inlateout(ymm_reg) d,\n' +
    '            out(zmm_reg) e, out(kreg) f, const 4, sym KernelMain, clobber_abi("C"),\n' +
    '            options(att, raw, nomem, nostack, preserves_flags, pure, readonly, noreturn))\n' +
    '        out := 1';
  for (const w of asmWords) expect(g, asmSrc, w, 'keyword.other.asm');
  expect(g, asmSrc, 'asm', 'storage.modifier.unsafe');
  expect(g, asmSrc, '{t:r}', 'constant.other.placeholder');
  expect(g, asmSrc, 'out', 'source.mk', 3);
  expect(g, '    x := options(1)', 'options', 'entity.name.function');

  // 3. Universe names and primitive types.
  for (const u of words.universe) {
    checks++;
    const t = tokenize(g, `    _ = ${u}(x)`)[0].find((t) => t.text === u);
    const s = t ? t.scopes.at(-1) : '(none)';
    if (!/^(constant|support)\./.test(s)) fail(`universe name ${u}: ${s}`);
  }
  const prims = 'i8 i16 i32 i64 u8 u16 u32 u64 int uint f32 f64 bool char void never str'.split(' ');
  for (const p of prims) expect(g, `    x: ${p} = y`, p, 'storage.type.primitive');

  // internal/lsp/keywords_table.go's words: each is in a list above.
  const table = fs.readFileSync(path.join(repo, 'internal', 'lsp', 'keywords_table.go'), 'utf8');
  const known = new Set([...words.keywords, ...words.contextual, ...words.universe, ...prims, ...asmWords, 'naked', 'cpu', 'section', 'align', 'Scope', 'like']);
  for (const m of table.matchAll(/^\t"([A-Za-z_]+)": \{/gm)) {
    if (!known.has(m[1])) fail(`keywords_table.go word ${m[1]} is in no list the grammar covers`);
  }

  // 4. Operators and punctuation.
  const ops = [
    ['x := y', ':=', 'keyword.operator.assignment'],
    ['x = y', '=', 'keyword.operator.assignment'],
    ...['+=', '-=', '*=', '/=', '%=', '+%=', '-%=', '*%=', '<<=', '>>=', '&=', '|=', '^='].map((o) => [`x ${o} y`, o, 'keyword.operator.assignment']),
    ...['==', '!=', '<', '<=', '>', '>='].map((o) => [`b := x ${o} y`, o, 'keyword.operator.comparison']),
    ...['&&', '||'].map((o) => [`b := x ${o} y`, o, 'keyword.operator.logical']),
    ['b := !x', '!', 'keyword.operator.logical'],
    ...['+', '-', '*', '/', '%', '+%', '-%', '*%', '<<', '>>', '&', '|', '^'].map((o) => [`n := x ${o} y`, o, 'keyword.operator.arithmetic']),
    ['n := ~x', '~', 'keyword.operator.arithmetic'],
    ['    for i in 0..n {', '..', 'keyword.operator.range'],
    ['extern printf(f: *u8, ...): i32', '...', 'keyword.operator.variadic'],
    ['    v := x?', '?', 'keyword.operator.optional'],
    ['F(): E!T {', '!', 'keyword.operator.error-union'],
    ['    x := a.b', '.', 'punctuation.accessor'],
    ['    F(a, b)', ',', 'punctuation.separator.comma'],
    ['    xs := [0; 8]', ';', 'punctuation.terminator'],
    ['        $arms', '$arms', 'variable.other.splice'],
    ['        if x.$(f.Name()) {', '$', 'keyword.operator.splice'],
  ];
  for (const [src, w, want] of ops) expect(g, src, w, want);

  // 5. Literal and comment forms.
  const lit = [
    ['    x := "a\\n"', 'a', 'string.quoted.double'],
    ['    x := "a\\n"', '\\n', 'constant.character.escape'],
    ['    x := "\\x41\\u{1F600}"', '\\x41', 'constant.character.escape'],
    ['    x := "\\x41\\u{1F600}"', '\\u{1F600}', 'constant.character.escape'],
    ['    x := "{name:8} {{"', '{name:8}', 'constant.other.placeholder'],
    ['    x := "{name:8} {{"', '{{', 'constant.character.escape'],
    ['    x := r"C:\\no\\escape"', 'C:\\no\\escape', 'string.quoted.raw'],
    ['    x := r"C:\\no\\escape"', 'r', 'storage.type.string'],
    ['    x := b"bytes"', 'bytes', 'string.quoted.double'],
    ['    x := b"bytes"', 'b', 'storage.type.string'],
    ["    c := 'a'", 'a', 'string.quoted.single'],
    ["    c := b'\\n'", '\\n', 'constant.character.escape'],
    ['    n := 1_000', '1_000', 'constant.numeric.integer'],
    ['    n := 0xFF_FF', '0xFF_FF', 'constant.numeric.hex'],
    ['    n := 0o17', '0o17', 'constant.numeric.octal'],
    ['    n := 0b101', '0b101', 'constant.numeric.binary'],
    ['    f := 1.5', '1.5', 'constant.numeric.float'],
    ['    f := 1e9', '1e9', 'constant.numeric.float'],
    ['    f := 1.5e-9', '1.5e-9', 'constant.numeric.float'],
    ['    b := true', 'true', 'constant.language'],
    ['    // a comment', 'a comment', 'comment.line.double-slash'],
    ['    // a comment', '//', 'punctuation.definition.comment'],
    ['/// The doc of the next item', 'The doc of the next item', 'comment.line.documentation'],
    ['/// The doc of the next item', '///', 'punctuation.definition.comment'],
    ['/// Returns `x`.', '`x`', 'markup.inline.raw'],
    ['    n++ // <- E0101', 'E0101', 'constant.other.error-code'],
    ['    x := 1 // a "quote', 'a "quote', 'comment.line.double-slash'],
    ['    u := "http://x"', 'http://x', 'string.quoted.double'],
  ];
  for (const [src, w, want] of lit) expect(g, src, w, want);
  // A """ text spans lines, keeps escapes and holes, and ends.
  const ml = tokenize(g, 'x := """\n  two {n}\n  """\ny := 1');
  checks++;
  if (!ml[1].every((t) => t.scopes.includes('string.quoted.triple.mk'))) fail('""" text: line 2 left the string');
  if (ml[3].some((t) => t.scopes.includes('string.quoted.triple.mk'))) fail('""" text: did not end');
  expect(g, 'x := """\n  two {n}\n  """', '{n}', 'constant.other.placeholder');

  // 6. Declarations.
  const decls = [
    ['export struct Order {', 'struct', 'storage.type.struct'],
    ['export struct Order {', 'Order', 'entity.name.type'],
    ['export struct Order {', 'export', 'storage.modifier.export'],
    ['export open type Error = A | B', 'type', 'storage.type.type'],
    ['export open type Error = A | B', 'Error', 'entity.name.type'],
    ['export open enum Kind {', 'enum', 'storage.type.enum'],
    ['export error Unknown(arg: int)', 'error', 'storage.type.error'],
    ['export error Unknown(arg: int)', 'Unknown', 'entity.name.type'],
    ['export interface Reader {', 'interface', 'storage.type.interface'],
    ['export view struct List[T] {', 'view', 'storage.modifier'],
    ['export mut view struct Spin[T] {', 'mut', 'storage.modifier'],
    ['export mut view struct Spin[T] {', 'view', 'storage.modifier'],
    ['export mut view struct Spin[T] {', 'T', 'entity.name.type.parameter'],
    ['export linear mut view struct Guard[T] {', 'linear', 'storage.modifier'],
    ['export linear mut view struct Guard[T] {', 'mut', 'storage.modifier'],
    ['export linear mut view struct Guard[T] {', 'Guard', 'entity.name.type'],
    ['export Fence(const o: Order)', 'Fence', 'entity.name.function'],
    ['export Order.CanLoad(self): bool {', 'Order', 'entity.name.type'],
    ['export Order.CanLoad(self): bool {', 'CanLoad', 'entity.name.function'],
    ['export extern strlen(s: *u8): uint {', 'extern', 'storage.type.extern'],
    ['export extern strlen(s: *u8): uint {', 'strlen', 'entity.name.function'],
    ['export Main(sys: &System): WriteError!void {', 'System', 'entity.name.type'],
    ['export Main(sys: &System): WriteError!void {', 'WriteError', 'entity.name.type'],
    ['impl Eq for Point {', 'impl', 'storage.type'],
    ['    requires o.CanFence() {', 'CanFence', 'entity.name.function'],
  ];
  for (const [src, w, want] of decls) expect(g, src, w, want);

  // 7. Lines of real files, as they stand.
  const real = {
    'std/atomic/order.mk': [
      ['export struct Order {', 'Order', 'entity.name.type'],
      ['export Order.CanLoad(self): bool {', 'CanLoad', 'entity.name.function'],
      ['    requires o.CanFence() {', 'requires', 'keyword.other.contract'],
      ['import std.raw', 'import', 'keyword.other.import'],
    ],
    'std/cli/args.mk': [
      ['export open type Error = Unknown', 'open', 'storage.modifier'],
      ['    g.Emit(quote {', 'quote', 'storage.type'],
    ],
    'tests/programs/hello.mk': [
      ['    try sys.Print(', 'try', 'keyword.control'],
      ['    try sys.Print(', '{name}', 'constant.other.placeholder'],
    ],
  };
  for (const [file, cases] of Object.entries(real)) {
    const text = fs.readFileSync(path.join(repo, file), 'utf8');
    const lines = text.split(/\r?\n/);
    const toks = tokenize(g, text);
    for (const [line, word, want] of cases) {
      checks++;
      const i = lines.findIndex((l) => l.includes(line));
      if (i < 0) {
        fail(`${file}: no line containing ${JSON.stringify(line)}`);
        continue;
      }
      const t = toks[i].find((t) => t.text.trim() === word);
      const s = t ? t.scopes.at(-1) : '(none)';
      if (!s.startsWith(want)) fail(`${file}:${i + 1}: ${JSON.stringify(word)} is ${s}, want ${want}*`);
    }
  }

  // 8. Every .mk under std/: the grammar never throws and never leaves a
  // string, comment or asm(...) open at the end of a file.
  let files = 0;
  const walk = (dir) => {
    for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
      const p = path.join(dir, e.name);
      if (e.isDirectory()) {
        if (!e.name.startsWith('.')) walk(p);
      } else if (e.name.endsWith('.mk')) {
        files++;
        const lines = tokenize(g, fs.readFileSync(p, 'utf8') + '\nx := 1');
        const last = lines[lines.length - 1];
        if (last.some((t) => t.scopes.length > 1 && t.scopes.at(-1) !== 'keyword.operator.assignment.mk' && t.scopes.at(-1) !== 'constant.numeric.integer.mk')) {
          fail(`${path.relative(repo, p)}: a string, comment or region is still open at the end: ${last.map((t) => t.scopes.join(' ')).join(' | ')}`);
        }
      }
    }
  };
  walk(path.join(repo, 'std'));

  if (failures) {
    console.error(`${failures} failures`);
    process.exit(1);
  }
  console.log(`grammar ok: ${checks} checks, ${files} std files tokenized`);
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
