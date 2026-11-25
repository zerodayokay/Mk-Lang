# Mk grammar

This is Mk's grammar in EBNF, for people who write tools for Mk or want to see exactly what the parser accepts. [The reference](reference.md) says what each form means; this page says only how it is written. The productions come first, then the rules they cannot express, then examples of the harder cases.

## Notation

| Form | Meaning |
|---|---|
| `{ x }` | `x` repeated zero or more times |
| `[ x ]` | `x` is optional |
| `x \| y` | `x` or `y` |
| `"x"` | the token `x` |
| `Term` | a statement terminator, produced by the newline rule of [the reference's lexical rules](reference.md#2-lexical-rules) |

## Productions

```ebnf
File        = { Item Term } .
Item        = Import | Gen | Test | [ "export" ] Decl .
Import      = "import" [ Name "=" ] Path .
Path        = Name { "." Name } .
Gen         = "gen" Path "(" [ TypeOrExpr { "," TypeOrExpr } [ "," ] ] ")" [ "fuel" Expr ] .
Test        = "test" String [ "(" Param ")" ] Block .

Decl        = Struct | Enum | ErrorDecl | Interface | Impl | Alias | Const | Global | Func | Extern .
Struct      = { "linear" | [ "mut" | "plain" ] "view" | Packed | Aligned } "struct" Name [ TypeParams ] "{" { Member Term } "}" .
Packed      = "packed" [ "(" Int ")" ] .
Aligned     = "align" "(" Int ")" .
Member      = [ "export" ] Name ":" Type | "invariant" Expr .
Enum        = [ "open" ] "enum" Name [ TypeParams ] [ ":" Type ] "{" { Variant Term } "}" .
Variant     = Name [ "(" Fields ")" ] [ "=" Expr ] .
Fields      = Field { "," Field } [ "," ] .
Field       = Name ":" Type .
ErrorDecl   = "error" Name [ "(" Fields ")" ] .
Interface   = "interface" Name [ TypeParams ] "{" { Signature Term } "}" .
Impl        = [ "unsafe" ] "impl" [ TypeParams ] Type "for" Type "{" { Func Term } "}" .
Alias       = [ "open" ] "type" Name [ TypeParams ] "=" Type .
Const       = "const" Name [ ":" Type ] "=" Expr [ "fuel" Expr ] [ Section ] .
Global      = "var" Name ":" Type "=" Expr [ Section ] .
Extern      = "extern" ( [ "naked" ] Signature [ Block ] | "var" Name ":" Type ) .

Func        = [ "naked" ] Signature Block | Signature .
Signature   = FuncName [ TypeParams ] "(" [ Params ] ")" [ ":" Type ] [ Flow { [ "," ] Flow } ] { Contract } [ Cpu ] [ Section ] .
Cpu         = "cpu" String .
Section     = "section" String .
FuncName    = Name [ TypeArgs ] "." Name | Name .
Params      = [ SelfParam "," ] Param { "," Param } [ "," "..." ] [ "," ] | SelfParam [ "," ] .
SelfParam   = [ "&" | "mut" ] "self" [ ParamFlow ] .
Param       = [ "const" | "var" ] Name ":" Type [ ParamFlow ] .
ParamFlow   = "into" Name .
Flow        = "from" ( Sources | "(" FieldSrc { "," FieldSrc } [ "," ] ")" ) | "ends" Name | "*" Name "into" Name .
FieldSrc    = Name ":" Sources .
Sources     = Source { "," Source } .
Source      = Name { "." Name } .
Contract    = "requires" Expr | "ensures" Expr .
TypeParams  = "[" TypeParam { "," TypeParam } [ "," ] "]" .
TypeParam   = Name [ ":" Bounds ] | "const" Name ":" Type .
Bounds      = Type { "+" Type } .
TypeArgs    = "[" TypeOrExpr { "," TypeOrExpr } [ "," ] "]" .

Type        = ErrorType .
ErrorType   = UnionType [ "!" Type ] .
UnionType   = PrefixType { "|" PrefixType } .
PrefixType  = "&" PrefixType | "mut" "[" "]" PrefixType | "mut" PrefixType | "?" PrefixType | "*" PrefixType
            | "[" "]" PrefixType | "[" Expr "]" PrefixType | "fn" "(" [ FnParams ] ")" [ ":" Type ] { [ "," ] Flow }
            | Record | "(" Type ")" | Path [ TypeArgs ] .
FnParams    = FnParam { "," FnParam } [ "," ] .
FnParam     = [ Name ":" ] Type [ ParamFlow ] .
Record      = "(" Fields ")" .
TypeOrExpr  = Type | Expr .

Block       = "{" { Stmt Term } "}" .
Stmt        = Bind | Assign | If | For | Match | Return | Fail | "break" [ Name ] | "continue" [ Name ]
            | Defer | Scope | Unsafe | Asm | Become | Expr | Name ":" For .
Asm         = "asm" "(" String { "," AsmOperand } [ "," ] ")" .
AsmOperand  = [ Name ":" ] AsmDir "(" ( Name | String ) ")" ( Expr | "_" ) | [ Name ":" ] "const" Expr | [ Name ":" ] "sym" Path
            | "clobber_abi" "(" String ")" | "options" "(" Name { "," Name } ")" .
AsmDir      = "in" | "out" | "lateout" | "inout" | "inlateout" .
Bind        = [ "var" ] Name ( ":=" Value | ":" Type [ "=" Value ] ) | [ "var" ] ( RecordPat | ArrayPat ) ":=" Value | "_" "=" Value .
RecordPat   = [ Path ] "(" PatField { "," PatField } [ "," ".." ] [ "," ] ")" .
ArrayPat    = "[" Name { "," Name } [ "," ] "]" .
PatField    = Name [ ":" Pattern ] .
Assign      = Expr AssignOp Value .
AssignOp    = "=" | "+=" | "-=" | "*=" | "/=" | "%=" | "+%=" | "-%=" | "*%=" | "<<=" | ">>=" | "&=" | "|=" | "^=" .
If          = "if" Cond Block [ "else" ( If | Block ) ] .
Cond        = Name ":=" Expr | Name "is" Type | Expr .
For         = "for" [ ForHead ] Block .
ForHead     = [ Name "," ] ( Name | RecordPat ) "in" ForSource | Cond .
ForSource   = Expr ".." Expr | [ "mut" | "&" ] Expr | "fields" "(" Expr ")" .
Match       = "match" Expr "{" { Arm Term } "}" .
Arm         = Pattern Block .
Pattern     = Alt { "|" Alt } .
Alt         = "." Name [ "(" PatFields ")" ] | Path [ "(" PatFields ")" ] | Literal | "-" Int | "_"
            | "some" "(" Pattern ")" | "none" | "ok" "(" Pattern ")" | "err" "(" Pattern ")"
            | Name | ( [ "-" ] Int | Char ) ".." [ [ "-" ] Int | Char ] | ".." ( [ "-" ] Int | Char ) .
PatFields   = PatField { "," PatField } [ "," ] .
Return      = "return" [ Value ] .
Fail        = "fail" Expr .
Become      = "become" Postfix .
Defer       = ( "defer" | "onerr" ) ( Block | Expr ) .
Scope       = [ "try" ] "scope" Name ":=" Expr Block .
Unsafe      = "unsafe" String Block .

Value       = Expr .
Expr        = Fallback .
Fallback    = Or [ "else" [ Name ] Block ] .
Or          = And { "||" And } .
And         = Compare { "&&" Compare } .
Compare     = Add [ CompareOp Add ] .
CompareOp   = "==" | "!=" | "<" | "<=" | ">" | ">=" .
Add         = Mul { AddOp Mul } .
AddOp       = "+" | "-" | "+%" | "-%" | "|" | "^" .
Mul         = Unary { MulOp Unary } .
MulOp       = "*" | "/" | "%" | "*%" | "<<" | ">>" | "&" .
Unary       = UnaryOp Unary | Postfix .
UnaryOp     = "-" | "!" | "~" | "&" | "mut" | "*" | "try" .
Postfix     = Primary { Suffix } .
Suffix      = "." Name | "." "go" Postfix | TypeArgs | "[" [ Expr ] ".." [ Expr ] "]"
            | "(" [ Args ] ")" .
Args        = Arg { "," Arg } [ "," ] .
Arg         = [ Name ":" ] Value .
Primary     = Literal | Name | "(" Expr ")" | RecordLit | ArrayLit | FnLit | Quote | If | Match .
RecordLit   = "(" [ RecField { "," RecField } [ "," ] ] ")" .
RecField    = Name ":" Value | Name .
ArrayLit    = "[" [ Value { "," Value } [ "," ] ] "]" | "[" Expr ";" Expr "]" .
FnLit       = "fn" "(" [ Params ] ")" [ ":" Type ] Block .
Quote       = "quote" "{" Tokens "}" .
```

## Rules the productions do not show

### Value position

`Value` marks value position: the right side of a binding or assignment, an argument, a field or an element, a `return`. It is an `Expr`; it differs only in where a brace literal habit is recognised. An `If` or `Match` is a `Primary`, so it produces a value wherever an expression stands, and suffixes and operators may follow its closing brace: `1 + if c { 2 } else { 3 }`, `xs[if c { 0 } else { 1 }]`, `match k { .. }.Len()`.

- An `If` used as a value must have an `else` (E0311).
- The last `Stmt` of each arm, and of a `Fallback`'s `Block`, is the arm's value. An arm whose last line is not a value is E0310, or E0803 for an `else` block.
- At the start of a statement an `If` or `Match` is the `Stmt`, not an expression, so nothing may follow its closing brace on that line (E0101). Wrap it in parentheses to use it as an expression statement: `(if c { a } else { b }).Close()`.

### Lookahead and covers

The parser needs at most three tokens of lookahead (LL(3)): at `Name ":" For`, and where a `Bind` must be told from an expression. It never backtracks. Where two forms start alike, it parses a cover and decides afterwards:

| Cover | Decided at | Readings |
|---|---|---|
| `Point(x, y) := p`, `(a, b) := e` | the `:=` | a call or record literal, or a `RecordPat` |
| `[a, b] := e` | the `:=` | an array literal, or an `ArrayPat` |
| `for (a, b) in xs` against `for (c) { ... }` | the `in` | a `RecordPat`, or a parenthesised condition |
| The brackets after a receiver in `FuncName` | the closing `]` | type arguments of the receiver, or `TypeParams` |
| `[N]T` | the closing `]` | an array type, or an array literal `[N]` |
| A bracket argument in `TypeArgs` and `Gen` | the closing `]` | a type or an expression (`TypeOrExpr`) |

Inside brackets, `fn(` always begins a `fn` type, so a `fn` literal cannot be a bracket argument (E0101 at its `{`). In bracket arguments `mut` is parsed as the unary operator, and name resolution decides whether it is a type. `F[T]` and `Type[T].M` without a call are a `Postfix` with a `TypeArgs` suffix, the same tree as an index; name resolution decides that they name an instance as a value.

A parenthesised lone name, such as `(i)`, is always a one-field `RecordLit`, never a grouping.

### Flows in signatures and `fn` types

A `fn` type's flows are greedy: every flow after `fn(...)` belongs to it. Inside a comma list (`Params`, `FnParams`, `Fields`, `TypeArgs`, `Gen` arguments), a `,` after a `fn` type's flow continues that flow only as more `Sources`: after a `Source`, when the next token is a `Name` that is not followed by `:`, `[`, `(`, `|` or `!`. Every other `,` ends the `fn` type and separates the list.

```text
f: fn(a: mut A, b: mut B) *a into b *b into a, n: int     // two flows: no comma between them
f: (fn(a: mut A, b: mut B) *a into b, *b into a), n: int  // or parenthesise the fn type
g: fn(f: fn(a: mut A, b: mut B) *a into b, *u8): int      // `, *u8` is the next parameter
```

- When a signature's result is a `fn` type, the flows after it belong to that type. A flow of the signature itself needs parentheses: `: (fn(&A): &A) from a`.
- Contracts always belong to the signature.
- After `from` sources, a `,` followed by `*`, `from` or `ends` starts a new flow, and a `,` followed by a `Name` continues `Sources`.
- Inside the parentheses of a per-field clause, a `,` followed by a `Name` and `:` starts the next `FieldSrc`, a `,` followed by `)` is the trailing comma, and any other `,` continues the current `Sources`. The `)` closes the clause, so a `,` after it starts a new flow or, inside a comma list, ends the `fn` type.
- A signature may have any number of `*` flows, but at most one `from` and one `ends`.

### Newlines inside a signature

A signature may continue on the next line before a line that begins with `from`, `ends`, `*`, `requires` or `ensures`, or with `cpu` or `section` followed by a string. The parser, not the lexer, skips the `Term` there, because `from := 0` must stay a statement. It skips a `Term` before a line that begins with `{` only when the signature has already continued that way, so an Allman-style `{` after a one-line signature is E0101. The newline rule also ends a statement before `}`.

### Other parse rules

- `T is X` is accepted by the parser; name resolution rejects it when `T` is not a type parameter.
- A `RecordPat` in a `ForHead` takes no `Path` and no `..`, and its `ForSource` takes no `&`, `mut`, range or `fields` (E0101). The loop variables take the elements by copy.
- `"[" Expr ";" Expr "]"` is the only place `;` appears.
- `fields` is a universe name and a plain name to the lexer. The parser reads it as a keyword only when it starts a `ForSource`.
- `...` is allowed only on a bodiless `extern` with at least one named parameter, else E0101.
- A float literal in a pattern is E0308.
- A `.go` suffix is a whole statement, and its operand is a call.
- `become` is a keyword. The `Postfix` after it must end in a call suffix, else E0101.
- A capturing `fn` literal is an ordinary `Arg`. Whether it stands in a `const` position, and what it captures, is decided after parsing.
- `Print(w, "done\n")` is a call with two `Args`.

### Range patterns

A range `Alt` has at least one end. Each end is an `Int` or a `Char` literal, with a `-` only before an `Int`. `lo..` and `..hi` reach the scrutinee type's limits. After a literal, `..` in a pattern starts a range; a lone `..` is the rest marker of a `RecordPat`.

### Words read by position

Some words are keywords only where the grammar expects them, and plain names elsewhere.

| Word | Read as the keyword when |
|---|---|
| `cpu`, `section` | After a signature (or, for `section`, a `const` or `var` initializer) and before a string literal. `cpu := simd.Cpu()` is still a binding. An expression cannot continue with a name, so `section` after an initializer ends it |
| `naked` | At an item start followed by a `FuncName`, or after `extern` |
| `align` | `align ( Int )` followed by `struct`, `linear`, `mut view`, `plain view`, `view` or `packed`. Any other `align (` is a call |
| `plain` | Before `view`, `struct`, `linear`, `mut`, `packed` or `align` at struct item position (`plain view struct`). Elsewhere `plain` is an ordinary name |
| `open` | At an item start, after `export` if present, followed by `enum` or `type` |
| `out`, `lateout`, `inout`, `inlateout`, `sym`, `clobber_abi`, `options` | Inside `asm(...)`, as an operand's first word |

`asm` itself is a keyword, because `asm(` would otherwise parse as a call. Inside `asm(` the first argument must be a string literal (E0101). An operand is recognised by its first one or two tokens: `Name ":"`, a direction word and `(`, `const`, `sym`, `clobber_abi (` or `options (`. `in` is already a keyword.

The `asm` template is kept as written. A hole is `{name}` or `{name:mod}`, naming an operand's label or the local it passes. `{{` and `}}` are literal braces, so AVX-512's `{k1}` is written `{{k1}}`.

`simd.Vec`, `simd.Mask` and their operators need no production: they are generic types of a module, and their operators are the usual ones.

### Lexical rules

- Escapes are `\n \r \t \0 \\ \' \"`, `\xHH`, and `\u{H}` with one to six hex digits naming a Unicode scalar value. Outside byte literals, `\xHH` is at most `7F`.
- A raw string `r"..."` is one line and holds no `"`.
- Byte literals `b'a'` and `b"..."` are ASCII.
- A `"""` text starts on the line after the opening quotes. The closing line's indentation is removed from every line, byte for byte, and the last newline is kept.
- Integer prefixes `0x`, `0o` and `0b` are lowercase. A decimal literal of more than one digit does not start with `0`: `007` is E0101; write `0o7`.
- `_` stands only between digits or after a prefix. An integer literal is at most 2^64-1. No literal takes a suffix.
- A float has digits on both sides of the `.`.
- A `char` literal is one Unicode scalar value.
- A doc comment is exactly `///`.
- A byte order mark after the start of a file, a lone carriage return, a form feed or a vertical tab is E0101, and so is a syntax tree nested more than 1,000 levels deep.

## Examples

Commas after the flows of a `fn` type. `Raw` is the case that would need four tokens of lookahead if a `,` before `*` could continue the inner type's flows.

```mk
// verdict: parse
// commas after the flows of a fn type
Exchange[T](a: mut T, b: mut T) *a into b, *b into a {
}

Both(f: fn(a: mut A, b: mut B) *a into b *b into a, n: int) {
}

Grouped(f: (fn(a: mut A, b: mut B) *a into b, *b into a), n: int) {
}

Last(f: fn(x: &A) ends x, n: int) {
}

Pick(f: fn(x: &A, y: &B): &A from x, y, n: int) {
}

Raw(g: fn(f: fn(a: mut A, b: mut B) *a into b, *u8): int) {
}

Pair(p: Two[fn(a: mut A, b: mut B) *a into b, int]) {
}

Make(): fn(a: mut A, b: mut B) *a into b, *b into a {
}
```

A per-field clause is closed by its own parentheses:

```mk
// verdict: parse
// per-field sources, several sources for one field, a path, a trailing comma
Parse(a: &Arena, src: str): OutOfMemory!Parsed from (nums: a, name: src) {
}

Tree(a: &Arena, src: str): Parsed from (tree: a, src, name: src,) {
}

Lexer.Take(&self): Token from (text: self.src) {
}

Both(f: fn(x: &A, y: &B): P from (a: x, b: y), n: int) {
}
```

`Split(f: fn(a: mut A, b: mut B) *a into b, *b into a)` is E0101: the `,` ends the `fn` type, and `*b` cannot start a `Param`.

An `if` that starts a statement is the statement, and a destructured loop variable takes its elements by copy:

```mk
// verdict: reject E0101
// an if that starts a statement is the statement: nothing follows its brace
Show(c: bool, a: str, b: str) {
    if c { a } else { b }.Len() // <- E0101
}
```

```mk
// verdict: reject E0101
// a destructured loop variable takes no & or mut source
SumPairs(xs: [](a: int, b: int)): int {
    var n := 0
    for (a, b) in &xs { // <- E0101
        n += a + b
    }
    return n
}
```
