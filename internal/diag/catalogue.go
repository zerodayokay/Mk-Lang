// Why: every error code has one name and one fix class, and the compiler,
// the docs and the editor must agree on them. Keeping the catalogue as data
// in one file lets a test compare it with doc/errors.md row by row.
//
// What: a named constant for every code (E0000 internal-error, E0001
// toolchain, E0101 upward), Entry, and the catalogue table of names and
// fix-class text.
//
// How: written by hand from the table in doc/errors.md;
// TestCatalogueMatchesDocs compares every row with it, so the two cannot
// drift. Retired numbers have no constant and are never reused.

package diag

// Every code of the catalogue, as a named constant, so a grep for a code finds
// every site that emits it. Numbers never assigned, or retired (E0210 to E0212,
// E0407, E0606, E0710, E1201, E1218), have no constant and are never reused.
const (
	E0000 Code = 0    // internal-error
	E0001 Code = 1    // toolchain
	E0101 Code = 101  // syntax
	E0102 Code = 102  // reserved-word
	E0201 Code = 201  // unknown-name
	E0202 Code = 202  // unknown-module
	E0203 Code = 203  // unknown-member
	E0204 Code = 204  // not-exported
	E0205 Code = 205  // import-cycle
	E0206 Code = 206  // duplicate-name
	E0207 Code = 207  // naming
	E0208 Code = 208  // unused
	E0209 Code = 209  // unused-result
	E0213 Code = 213  // target-api-mismatch
	E0214 Code = 214  // internal-module
	E0215 Code = 215  // grant-missing
	E0216 Code = 216  // mk-too-old
	E0217 Code = 217  // test-dep-outside-test
	E0301 Code = 301  // type-mismatch
	E0302 Code = 302  // no-conversion
	E0303 Code = 303  // lossy-conversion
	E0304 Code = 304  // argument-count
	E0305 Code = 305  // field-names
	E0306 Code = 306  // non-exhaustive
	E0307 Code = 307  // not-optional
	E0308 Code = 308  // no-operator
	E0309 Code = 309  // record-names
	E0310 Code = 310  // arm-without-value
	E0311 Code = 311  // value-if-without-else
	E0312 Code = 312  // tail-call-signature
	E0313 Code = 313  // tail-call-argument
	E0314 Code = 314  // tail-call-target
	E0315 Code = 315  // empty-range
	E0401 Code = 401  // use-after-move
	E0402 Code = 402  // not-consumed
	E0403 Code = 403  // use-before-assign
	E0404 Code = 404  // assign-immutable
	E0405 Code = 405  // moved-in-loop
	E0406 Code = 406  // reserved-by-defer
	E0408 Code = 408  // not-plain
	E0409 Code = 409  // move-out-of-reference
	E0410 Code = 410  // must-declare-kind
	E0411 Code = 411  // moved-before-failure
	E0412 Code = 412  // private-in-signature
	E0501 Code = 501  // exclusive-conflict
	E0502 Code = 502  // write-while-borrowed
	E0503 Code = 503  // move-while-borrowed
	E0504 Code = 504  // access-while-exclusive
	E0505 Code = 505  // outlives-owner
	E0506 Code = 506  // temporary-held
	E0507 Code = 507  // mut-through-shared
	E0508 Code = 508  // mut-of-immutable
	E0601 Code = 601  // source-required
	E0602 Code = 602  // source-undeclared
	E0603 Code = 603  // returns-local-borrow
	E0604 Code = 604  // store-undeclared
	E0605 Code = 605  // loan-into-global
	E0607 Code = 607  // from-own-bytes
	E0608 Code = 608  // flow-mismatch
	E0609 Code = 609  // literal-captures
	E0701 Code = 701  // bound-unmet
	E0702 Code = 702  // no-impl
	E0703 Code = 703  // needs-bound
	E0704 Code = 704  // impl-conflict
	E0705 Code = 705  // not-const
	E0706 Code = 706  // requires-failed
	E0707 Code = 707  // impl-signature
	E0708 Code = 708  // instantiation-limit
	E0709 Code = 709  // const-arithmetic
	E0711 Code = 711  // cannot-infer
	E0801 Code = 801  // error-not-in-set
	E0802 Code = 802  // error-unhandled
	E0803 Code = 803  // else-falls-through
	E0804 Code = 804  // try-on-non-error
	E0805 Code = 805  // missing-return
	E0806 Code = 806  // try-in-defer
	E0807 Code = 807  // onerr-two-values
	E0901 Code = 901  // const-failed
	E0902 Code = 902  // fuel-exhausted
	E0903 Code = 903  // generator-error
	E0904 Code = 904  // generated-edited
	E0905 Code = 905  // generated-invalid
	E0906 Code = 906  // forbidden-input
	E0907 Code = 907  // layout-cycle
	E0908 Code = 908  // stage-violation
	E0909 Code = 909  // const-cycle
	E0910 Code = 910  // file-input
	E1001 Code = 1001 // not-send
	E1002 Code = 1002 // not-share
	E1003 Code = 1003 // scope-escape
	E1101 Code = 1101 // needs-unsafe
	E1102 Code = 1102 // unsafe-not-permitted
	E1103 Code = 1103 // raw-outside-unsafe
	E1104 Code = 1104 // asm-operand
	E1105 Code = 1105 // naked-body
	E1106 Code = 1106 // cpu-feature
	E1107 Code = 1107 // vector-abi
	E1108 Code = 1108 // general-regs-only
	E1109 Code = 1109 // no-c-form
	E1202 Code = 1202 // iterator-chain
	E1203 Code = 1203 // lifetime-syntax
	E1204 Code = 1204 // async-syntax
	E1205 Code = 1205 // fn-keyword
	E1206 Code = 1206 // semicolon
	E1207 Code = 1207 // let-keyword
	E1208 Code = 1208 // postfix-question
	E1209 Code = 1209 // null-word
	E1210 Code = 1210 // exception-words
	E1211 Code = 1211 // arrow-return
	E1212 Code = 1212 // ampersand-mut
	E1213 Code = 1213 // double-colon
	E1214 Code = 1214 // fat-arrow
	E1215 Code = 1215 // pub-keyword
	E1216 Code = 1216 // loop-keywords
	E1217 Code = 1217 // if-let
	E1219 Code = 1219 // impl-block
	E1220 Code = 1220 // inclusive-range
	E1221 Code = 1221 // foreign-name
	E1222 Code = 1222 // as-cast
	E1223 Code = 1223 // assert-statement
)

// Entry is one row of the catalogue: the code, its kebab-case name exactly as
// doc/errors.md spells it, and the text of its fix-class
// column (the class word first: None, Proven, Compiles or Alternatives).
type Entry struct {
	Code     Code
	Name     string
	FixClass string
}

// catalogue holds the rows of doc/errors.md in catalogue order.
// It is never mutated.
var catalogue = [...]Entry{
	{E0000, "internal-error", "None"},
	{E0001, "toolchain", "Alternatives"},
	{E0101, "syntax", "Alternatives"},
	{E0102, "reserved-word", "Proven: rename, for a keyword-named local or parameter; alternatives otherwise"},
	{E0201, "unknown-name", "Proven if one export matches: add import"},
	{E0202, "unknown-module", "Alternatives"},
	{E0203, "unknown-member", "Alternatives"},
	{E0204, "not-exported", "Alternatives"},
	{E0205, "import-cycle", "Alternatives"},
	{E0206, "duplicate-name", "Proven: rename; two imports of one last segment: alternatives `import <name> = <path>`"},
	{E0207, "naming", "Proven: recase, and `e else { x }` for a literal, name or field default; alternatives for a `match` pattern's name; compiles otherwise"},
	{E0208, "unused", "Proven: remove"},
	{E0209, "unused-result", "Proven: `_ =`"},
	{E0213, "target-api-mismatch", "Alternatives"},
	{E0214, "internal-module", "Alternatives"},
	{E0215, "grant-missing", "Alternatives: the grant line the message gives"},
	{E0216, "mk-too-old", "Alternatives: `mk upgrade`"},
	{E0217, "test-dep-outside-test", "Alternatives"},
	{E0301, "type-mismatch", "Proven: `&*p` for a `mut T` where a `&T` is expected, when it is the whole operand of `return` in a body with no `defer` or `onerr` and reads through a parameter or is a call or `try` result; compiles: the same elsewhere, `&x` for a loan `mut x`, and `xs[..]` for a `mut []T` where a `[]T` is expected"},
	{E0302, "no-conversion", "Alternatives"},
	{E0303, "lossy-conversion", "Compiles: `Cast`, or `Trunc` between integers"},
	{E0304, "argument-count", "Proven if each missing parameter has one in-scope candidate"},
	{E0305, "field-names", "Compiles: stubs (a `std.pkg` field a manifest leaves out is supplied empty)"},
	{E0306, "non-exhaustive", "Compiles: stubs; proven for `reason: open`, a `_` arm that panics"},
	{E0307, "not-optional", "Alternatives"},
	{E0308, "no-operator", "Proven for listed mappings; compiles for the vector mappings, which choose wrapping"},
	{E0309, "record-names", "Proven: rename"},
	{E0310, "arm-without-value", "Alternatives: the value on the arm's last line, or the statement form"},
	{E0311, "value-if-without-else", "Alternatives: add the `else`, or the statement form"},
	{E0312, "tail-call-signature", "Alternatives: `return` the call"},
	{E0313, "tail-call-argument", "Alternatives: pass a reference"},
	{E0314, "tail-call-target", "Alternatives: `return` the call"},
	{E0315, "empty-range", "Alternatives"},
	{E0401, "use-after-move", "Alternatives: reborrow, copy"},
	{E0402, "not-consumed", "Proven only when one void release exists, its arguments are unique in scope, and the value does not escape"},
	{E0403, "use-before-assign", "Alternatives"},
	{E0404, "assign-immutable", "Proven: add `var`"},
	{E0405, "moved-in-loop", "Alternatives"},
	{E0406, "reserved-by-defer", "Alternatives"},
	{E0408, "not-plain", "Alternatives"},
	{E0409, "move-out-of-reference", "Compiles: destructure"},
	{E0410, "must-declare-kind", "Proven: add the words"},
	{E0411, "moved-before-failure", "Alternatives: move after the last failure point, or split the function"},
	{E0412, "private-in-signature", "Alternatives"},
	{E0501, "exclusive-conflict", "Alternatives; `from self.f` when the callee body shows it, proven for a private function and compiles for an exported one"},
	{E0502, "write-while-borrowed", "Alternatives"},
	{E0503, "move-while-borrowed", "Alternatives"},
	{E0504, "access-while-exclusive", "Alternatives"},
	{E0505, "outlives-owner", "Alternatives"},
	{E0506, "temporary-held", "Proven: bind a local"},
	{E0507, "mut-through-shared", "Alternatives"},
	{E0508, "mut-of-immutable", "Proven: add `var`"},
	{E0601, "source-required", "Minimal clause from the body: proven for a private function, compiles for an exported one"},
	{E0602, "source-undeclared", "Clause from the body: proven for a private function, compiles for an exported one"},
	{E0603, "returns-local-borrow", "Alternatives"},
	{E0604, "store-undeclared", "Proven for a private function; compiles for an exported one, whose new clause can move errors to callers"},
	{E0605, "loan-into-global", "Alternatives"},
	{E0607, "from-own-bytes", "Proven: use `from p`, or drop the field from the clause"},
	{E0608, "flow-mismatch", "Compiles"},
	{E0609, "literal-captures", "Alternatives: struct plus `fn`, or a loop"},
	{E0701, "bound-unmet", "Compiles: add `impl` stub; `FreeEmpty` for a container's `Free` whose elements are closed, linear and not `Free`"},
	{E0702, "no-impl", "Compiles"},
	{E0703, "needs-bound", "Compiles: add bound"},
	{E0704, "impl-conflict", "Alternatives"},
	{E0705, "not-const", "Alternatives"},
	{E0706, "requires-failed", "Proven: the restated `requires` of a `const` passed on, when the clause keeps no name of the callee; alternatives otherwise"},
	{E0707, "impl-signature", "Proven: copy signature"},
	{E0708, "instantiation-limit", "Alternatives"},
	{E0709, "const-arithmetic", "Alternatives: use a slice"},
	{E0711, "cannot-infer", "Proven when one type fits"},
	{E0801, "error-not-in-set", "Alternatives: widen or handle"},
	{E0802, "error-unhandled", "Compiles: add `try`"},
	{E0803, "else-falls-through", "Alternatives: the value on its last line, or `return`"},
	{E0804, "try-on-non-error", "Proven: remove"},
	{E0805, "missing-return", "Proven for a tail expression; Compiles for a last `if` or `match` whose arms end in values: `return` before it"},
	{E0806, "try-in-defer", "Alternatives"},
	{E0807, "onerr-two-values", "Proven: split it"},
	{E0901, "const-failed", "Alternatives"},
	{E0902, "fuel-exhausted", "Compiles: raise `fuel`"},
	{E0903, "generator-error", "Alternatives"},
	{E0904, "generated-edited", "Proven: regenerate"},
	{E0905, "generated-invalid", "Alternatives"},
	{E0906, "forbidden-input", "Alternatives"},
	{E0907, "layout-cycle", "Alternatives"},
	{E0908, "stage-violation", "Compiles: move the helper to a submodule"},
	{E0909, "const-cycle", "Alternatives"},
	{E0910, "file-input", "Alternatives"},
	{E1001, "not-send", "Alternatives"},
	{E1002, "not-share", "Alternatives"},
	{E1003, "scope-escape", "Alternatives"},
	{E1101, "needs-unsafe", "Alternatives"},
	{E1102, "unsafe-not-permitted", "Alternatives"},
	{E1103, "raw-outside-unsafe", "Alternatives"},
	{E1104, "asm-operand", "Alternatives: `raw.Of(r)` or `raw.Data(xs)` for a reference or a slice"},
	{E1105, "naked-body", "Alternatives"},
	{E1106, "cpu-feature", "Alternatives"},
	{E1107, "vector-abi", "Alternatives"},
	{E1108, "general-regs-only", "Alternatives: a `cpu \"sse2\"` function that saves the FPU state, or `cpu: \"baseline\"` on the bin"},
	{E1109, "no-c-form", "Alternatives"},
	{E1202, "iterator-chain", "Compiles when the output type is stated"},
	{E1203, "lifetime-syntax", "Proven: delete or replace"},
	{E1204, "async-syntax", "Proven: delete"},
	{E1205, "fn-keyword", "Proven"},
	{E1206, "semicolon", "Proven"},
	{E1207, "let-keyword", "Proven"},
	{E1208, "postfix-question", "Proven after a call; alternatives otherwise"},
	{E1209, "null-word", "Proven"},
	{E1210, "exception-words", "Alternatives"},
	{E1211, "arrow-return", "Proven"},
	{E1212, "ampersand-mut", "Proven"},
	{E1213, "double-colon", "Proven"},
	{E1214, "fat-arrow", "Proven"},
	{E1215, "pub-keyword", "Proven"},
	{E1216, "loop-keywords", "Proven"},
	{E1217, "if-let", "Proven"},
	{E1219, "impl-block", "Proven"},
	{E1220, "inclusive-range", "Proven, or alternatives when the high end may be the type's maximum"},
	{E1221, "foreign-name", "Proven if the arguments line up, else alternatives"},
	{E1222, "as-cast", "Compiles: `T(x)` or `Cast`"},
	{E1223, "assert-statement", "Proven: `Assert(c)`"},
}
