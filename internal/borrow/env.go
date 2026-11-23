// Why: the driver, eval and codegen use the borrow checker through one small
// surface, and every input the checker may read must be named there so a
// body can be checked as an independent job.
//
// What: Env (the signatures, facts and items a check may read), Result,
// Facts (what later stages read: OnerrRuns, GuardDecisions) and Check.
//
// How: Check runs the move pass and the loan pass over one body; when a call
// may reach the self-reach remnant (a store the self-reach filter cannot
// see, defined at call.go remnantReach) it checks the body again with one
// loan set per root.

package borrow

import (
	"time"

	"mk/internal/diag"
	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

// Env is everything Check reads besides the body. Every field is read-only
// and may be shared by concurrent Checks.
type Env struct {
	// Types is the session's type table; its Items() gives declarations
	// (StructDecl, EnumDecl fields with Exported) for the type walks (typewalk.go).
	Types *types.Table
	// Known names the library items the rules key on (Arena, Alloc, Heap,
	// Cell, Mutex, Chan, Scope, Io). A nil Known, or a NoItem entry, makes
	// the rule that needs it inert.
	Known *types.KnownItems
	// Sig is the signature of the body's item: parameters, Errors, Flows
	// (final after stage 5: declared, or the automatic answer). For a const
	// initializer, test or contract body it is the synthesized signature lower
	// used for local 0 and the parameters.
	Sig *types.Signature
	// Module is the module that declares the body's item (the unsafe
	// permission of ends primitives). Nil reads as a module that is not
	// unsafe-permitted.
	Module *types.Module
	// Callee returns the signature of a directly called function, as written
	// (before substitution). Never nil for a CallDirect callee.
	Callee func(types.ItemID) *types.Signature
	// Method returns the signature of method number m of interface iface as
	// declared, with Self abstract.
	Method func(iface types.ItemID, m uint32) *types.Signature
	// Facts returns the ownership facts of any type that occurs in the body,
	// and of the declared parameter types of every signature the body calls,
	// generic ones included: a KParam not bounded by Plain is Linear and a
	// carrier; one bounded by Plain is Plain and a carrier; KSelf likewise.
	// Interior is true when the type holds inline a Cell, Mutex, Chan, Arena,
	// raw.Atomic or Scope.
	Facts func(types.TypeID) types.Facts
	// File returns a source file by ID, read only for the text of the
	// mechanical fixes (E0506 copies the temporary's expression). Nil means no
	// fix needs text, and those fixes are omitted.
	File func(source.FileID) *source.File
	// Timing asks Check to measure itself into Result.Elapsed (for
	// timing reports; never part of a cached result).
	Timing bool
	// FieldClause returns E0501's `from p.f` fix data for a directly called
	// function (fix.go): check's (*Checker).FieldClause. Nil
	// means the fix is omitted.
	FieldClause func(callee types.ItemID) (FieldClause, bool)
}

// FieldClause is a callee's `from p.f` clause that its body shows: the text,
// the zero-length span after its result type where it goes, and whether the
// callee is exported (the fix is proven for a private callee, compiles for
// an exported one).
type FieldClause struct {
	Clause   string
	At       source.Span
	Exported bool
}

// Result is the outcome of checking one body.
type Result struct {
	// Diags are the body's diagnostics, ordered by (span, code, message) and
	// deduplicated on (code, primary span); nil when accepted.
	Diags []*diag.Diagnostic
	// Facts are meaningful only when Diags holds no error.
	Facts Facts
	// Reads lists what the check read, by stable key, for the function's read
	// set: callee and interface-method signatures (ReadSignature), the shape
	// of every named type whose Facts it consulted (ReadShape), and ReadFields
	// with Name "all" for every struct or enum whose declaration a type walk
	// read, private fields included.
	Reads types.ReadSet
	// Elapsed is wall time when Env.Timing, else 0. Never cached, never in
	// --json.
	Elapsed time.Duration
}

// Facts is what eval, codegen and the driver read from a checked body. It
// holds no loan data: loans never reach code generation.
type Facts struct {
	// OnerrRuns has one entry per block whose terminator is TermOnerrGuard,
	// in BlockID order. In an accepted body every guard is decided
	// statically: Run says the onerr copy at Yes runs.
	OnerrRuns []OnerrRun
}

// OnerrRun is the decision for one guard.
type OnerrRun struct {
	Block mir.BlockID
	Run   bool
}

// Runs reports the decision for guard block b; an ICE if b is not a guard.
func (f *Facts) Runs(b mir.BlockID) bool {
	for _, r := range f.OnerrRuns {
		if r.Block == b {
			return r.Run
		}
	}
	source.ICEf(source.Span{}, "borrow.Facts.Runs: block %d is not an onerr guard", b)
	return false
}

// GuardDecisions returns OnerrRuns as a slice of bool indexed by BlockID
// (false for non-guard blocks), for eval, which does not import borrow.
func (f *Facts) GuardDecisions(numBlocks int) []bool {
	out := make([]bool, numBlocks)
	for _, r := range f.OnerrRuns {
		if int(r.Block) < numBlocks {
			out[r.Block] = r.Run
		}
	}
	return out
}

// Check runs the move pass and the loan pass over one body. The body must
// satisfy mir.(*Body).Validate and the shapes lower produces (Copy only of
// a plain whole local); a violated assumption is an ICE
// (raised with source.ICEf at the body's span), which the driver's
// source.Guard turns into E0000. Check never reads another body.
func Check(body *mir.Body, env *Env) *Result {
	var start time.Time
	if env.Timing {
		start = time.Now()
	}
	c := newChecker(body, env)
	res := c.run()
	if c.remnant {
		// A body with a call that may reach the self-reach remnant is
		// checked again with one set per root (call.go remnantReach).
		c = newChecker(body, env)
		c.whole = true
		res = c.run()
	}
	if env.Timing {
		res.Elapsed = time.Since(start)
	}
	return res
}
