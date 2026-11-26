// Why: check needs compile-time evaluation, but the evaluator sits below it
// in the package DAG, so the two meet through an interface; that interface is
// the one place the stage order is visible.
//
// What: PanicCode (frozen numbering), Stage, EvalTarget, EvalRequest,
// Refusal, EvalErrorKind, EvalError, EvalResult and the Evaluator interface.
//
// How: a request names what to evaluate and at which stage; the result is a
// value or an error, with every read and the fuel used, so the cache can
// record it. Panic codes are numbered once and never renumbered.

package types

import (
	"strconv"

	"mk/internal/source"
)

// PanicCode is why a program panics. The numbering is frozen: the runtime's
// message table and the evaluator's trace use it, and it is append-only.
type PanicCode uint32

const (
	PanicNone PanicCode = iota
	// PanicOverflow: + - * or negation overflowed.
	PanicOverflow
	// PanicBounds: an index or slice bound out of range.
	PanicBounds
	// PanicDivideByZero: / or % by zero, or MIN / -1.
	PanicDivideByZero
	// PanicShiftAmount: << or >> by at least the bit width.
	PanicShiftAmount
	// PanicCast: Cast[T](x) does not fit.
	PanicCast
	// PanicMust: .Must() on none or an error.
	PanicMust
	// PanicAssert: Assert(false).
	PanicAssert
	// PanicExplicit: Panic(message).
	PanicExplicit
	// PanicStackLimit: the stack overflowed (guard page).
	PanicStackLimit
	// PanicStrBoundary: a str slice off a scalar boundary.
	PanicStrBoundary
	// PanicFreeNonEmpty: Free of a non-empty container of linear values.
	PanicFreeNonEmpty
	// PanicWrongAllocator: a heap container given the wrong allocator.
	PanicWrongAllocator
	// PanicEnumTag: an enum tag from C or raw memory out of range.
	PanicEnumTag
	// PanicContract: requires, ensures or invariant false in dev or test;
	// and, in every profile, a std.simd clause a generic does not restate,
	// false for the instance at the call.
	PanicContract
	// PanicUnreachable: MIR Unreachable reached, an internal error.
	PanicUnreachable
	// PanicSuspendInCallback: an Io operation in a Mk function called from C
	// on a system stack.
	PanicSuspendInCallback
	// PanicUnresolved: a call of a lazily bound function whose library or
	// symbol the loader cannot find.
	PanicUnresolved

	numPanicCodes
)

var panicNames = [numPanicCodes]string{"none", "overflow", "bounds", "divide by zero", "shift amount",
	"cast", "must", "assert", "explicit", "stack limit", "str boundary", "free non-empty",
	"wrong allocator", "enum tag", "contract", "unreachable", "suspend in callback", "unresolved function"}

// String is the panic's short name.
func (c PanicCode) String() string {
	if c < numPanicCodes {
		return panicNames[c]
	}
	return "panic(" + strconv.Itoa(int(c)) + ")"
}

// NumPanicCodes is one past the last code.
const NumPanicCodes = int(numPanicCodes)

// Stage is when an evaluation runs.
type Stage uint8

const (
	// StageSignatures (stage 5) may enter only bodies and layouts of modules
	// strictly below the evaluating one; anything else stops with E0908.
	StageSignatures Stage = iota
	// StageBodies (stage 6) may also enter same-module consts and bodies.
	StageBodies
)

// EvalTarget tags what an EvalRequest runs.
type EvalTarget uint8

const (
	// EvalConst runs the initializer of const or global Item.
	EvalConst EvalTarget = iota
	// EvalCall runs function Item with GenericArgs on Args.
	EvalCall
	// EvalContract runs contract Item (a requires clause on a const
	// parameter, E0706) on Args.
	EvalContract
	// EvalGen runs generator Item for a gen line; the gen package drives it.
	EvalGen
)

// EvalRequest is one evaluation. It names what to run by entity, so this
// package needs no MIR: the evaluator fetches the body itself, applying the
// stage rule to every body and layout it enters.
type EvalRequest struct {
	What        EvalTarget
	Item        ItemID
	GenericArgs []GenericArg
	// Args: for EvalCall one value per parameter; for EvalContract one value
	// per predicate parameter, the named const parameters in parameter
	// order, passed by reference (Item is the predicate item); for EvalConst
	// none.
	Args []ConstID
	Fuel uint64
	// Stage is StageSignatures for every request check issues, the requires
	// of a call inside a body included; StageBodies only for the driver's
	// declaration evaluation of a const or global after the module's
	// stage 7.
	Stage Stage
	// Module is the module the evaluation runs for: the stage rule's "same
	// module".
	Module ModuleID
	// Site is where the evaluation is written: diagnostics anchor there.
	Site   source.Span
	Target Target
}

// Refusal is an operation the evaluator will not perform, each E0901
// with the fact refused.
type Refusal uint8

const (
	// RefuseIo: an Io operation.
	RefuseIo Refusal = iota + 1
	// RefuseSyscall: sys.Syscall.
	RefuseSyscall
	// RefuseExtern: a call to a bodiless extern (or a bodiless non-intrinsic).
	RefuseExtern
	// RefusePointerBytes: reading a pointer's bytes as an integer, or an
	// integer's as a pointer.
	RefusePointerBytes
	// RefuseRawAccess: raw-pointer access outside the evaluation's own
	// allocations.
	RefuseRawAccess
	// RefuseGlobal: reading or writing a var global's memory.
	RefuseGlobal
	// RefuseAsm: an asm statement, or a call of a naked function.
	RefuseAsm
	// RefuseVolatile: a std.mmio access.
	RefuseVolatile
)

// String is the fact value: io, syscall, extern, pointer-bytes, raw-access,
// global, asm, volatile.
func (r Refusal) String() string {
	switch r {
	case RefuseAsm:
		return "asm"
	case RefuseVolatile:
		return "volatile"
	case RefuseIo:
		return "io"
	case RefuseSyscall:
		return "syscall"
	case RefuseExtern:
		return "extern"
	case RefusePointerBytes:
		return "pointer-bytes"
	case RefuseRawAccess:
		return "raw-access"
	case RefuseGlobal:
		return "global"
	}
	return "refusal(" + strconv.Itoa(int(r)) + ")"
}

// EvalErrorKind tags an EvalError. The checker emits, at the request's site:
// EvalFuel as E0902, EvalPanic and EvalRefused as E0901, EvalStage as E0908,
// EvalCycle as E0909, EvalDataFile as E0910, with the trace as facts.
type EvalErrorKind uint8

const (
	EvalFuel EvalErrorKind = iota + 1
	EvalPanic
	EvalRefused
	EvalStage
	EvalCycle
	// EvalDataFile: a build.File or Gen.Input read failed; File and Path say
	// why and what.
	EvalDataFile
)

// EvalError is why an evaluation produced no value.
type EvalError struct {
	Kind EvalErrorKind
	// Steps is the fuel used when it ran out.
	Steps uint64
	// Fuel is, for EvalFuel, the fuel that ran out: the stated or default
	// one, or more for a const's or global's large value (eval's
	// startConst); 0 when unknown, and the renderer computes it.
	Fuel uint64
	// Panic and Message describe an EvalPanic.
	Panic   PanicCode
	Message string
	Refusal Refusal
	// At is where in the evaluated code it stopped.
	At source.Span
	// Trace and Items are parallel for every kind: the call chain from the
	// request's site inward, outermost first, the last span equal to At. For
	// EvalCycle, Items is the canonical cycle. A zero span with NoItem marks
	// frames elided past 64.
	Trace []source.Span
	Items []ItemID
	// File and Path describe an EvalDataFile: the reason and the path as
	// written.
	File DataFileError
	Path string
}

// EvalResult is an evaluation's outcome: a value or an error, and in both
// cases everything it read and the steps it used. Value NoConst with Err nil
// is poison (an input already had a diagnostic): the caller uses Poison and
// reports nothing.
type EvalResult struct {
	Value ConstID
	Err   *EvalError
	Reads ReadSet
	Steps uint64
}

// Evaluator runs compile-time evaluations. check calls it only through this
// interface; the driver implements it with eval and gives it the bodies,
// layouts and consts the stage rule allows. Eval is safe for concurrent use.
type Evaluator interface {
	Eval(req *EvalRequest) EvalResult
}
