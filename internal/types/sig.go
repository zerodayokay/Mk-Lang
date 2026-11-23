// Why: a function's signature is the contract every caller, the loan pass
// and the interface read, and its flows (from, into, ends, stores) must be
// data so that no caller needs the body.
//
// What: ParamIndex, Receiver, Param, Source, FieldSources, Store, Flows,
// Contract, Bound, GenericParam and Signature (Param, ParamByName,
// ReturnType, FnType).
//
// How: parameters are numbered from 1, equal to their MIR locals. Flows are
// final after stage 5, declared or automatic, and the interface stores them.
// A contract's predicate is the MIR body of an item of its own.

package types

import "mk/internal/source"

// ParamIndex names a parameter by position, 1-based, receiver first: the
// receiver (or the first parameter) is ParamIndex(1). It is the same number
// as the parameter's MIR local. The zero ParamIndex is none.
type ParamIndex uint32

// NoParam is the zero ParamIndex.
const NoParam ParamIndex = 0

// Receiver is how a method receives self: the only spellings are
// self, &self and mut self.
type Receiver uint8

const (
	// NoReceiver: a function, not a method.
	NoReceiver Receiver = iota
	// ByValue is self.
	ByValue
	// ByShared is &self.
	ByShared
	// ByMut is mut self.
	ByMut
)

// Param is one parameter of a signature. For a receiver, Name is SymSelf and
// Type is the full type: Self for self, &Self for &self, mut Self for mut self
// (gap 15).
type Param struct {
	Name source.Symbol
	Type TypeID
	// Const is true for const parameters; the argument arrives as a ConstArg.
	Const bool
	// Var is true for var parameters, which the body may assign.
	Var bool
	// Into is the parameter the value is stored into (x: T into p), NoParam
	// when absent. It is always written, never inferred.
	Into ParamIndex
	Span source.Span
}

// FieldStep is one field step of from p.f.g: the field's index in the
// declaring struct's Fields, and the type after the step.
type FieldStep struct {
	Field uint32
	Type  TypeID
}

// Source is one result flow. from p is Held with an empty path: the
// result holds H(p) and p stays borrowed. from p.f is Behind with path f: the
// result holds B(p) and p is free. A private field named in a path is part of
// the public signature.
type Source struct {
	Param  ParamIndex
	Behind bool
	Path   []FieldStep
}

// FieldSources is one named field of a per-field clause `from (f: a, g:
// b.x)`: Field is the field's index in the declared struct's or record's
// Fields (through ?T and E!T, before substitution; a record's canonical
// order), Sources its sources, each as a flat clause's Source.
type FieldSources struct {
	Field   uint32
	Sources []Source
}

// Store is *From into Into: From's contents are written into everything
// writable from Into. From may equal Into (*self into self).
type Store struct {
	From ParamIndex
	Into ParamIndex
}

// Flows are a signature's flows as data. Sources are final after stage 5:
// declared, or the automatic answer, which the interface stores; Declared
// says which (E0601 needs to know).
type Flows struct {
	Sources []Source
	// Fields is a per-field clause: the named fields only, sorted by
	// Field, one entry each; nil for a flat clause or none. Sources then
	// holds the union of the clause's sources, so a reader that names no
	// field reads the union, and a field the clause leaves out holds
	// Sources.
	Fields   []FieldSources
	Declared bool
	Stores   []Store
	// Ends lists the parameters whose loans the call ends (ends p).
	Ends []ParamIndex
	// Ambiguous is set by check when the automatic answer is ambiguous
	// (E0601): callers and iface see that the boundary check of the body
	// was skipped.
	Ambiguous bool
	// ClauseError is set by check when a declared clause got E0607; borrow
	// skips the body's boundary check, as for Ambiguous.
	ClauseError bool
}

// ContractKind tells requires from ensures.
type ContractKind uint8

const (
	Requires ContractKind = iota
	Ensures
)

// Contract is one requires or ensures clause: its predicate is the MIR body
// of the item Body (a KindFunc item owned by the function, ContractKey). Its
// signature has one parameter &P per function parameter the predicate names,
// in the function's parameter order, then, for ensures, result: &R, the ok
// value; it returns bool in local 0. An invariant item takes self: &Self.
type Contract struct {
	Kind ContractKind
	Body ItemID
	Span source.Span
	// Text is the clause's predicate as check spells it in stage 5, so E0706's
	// clause fact and the restated-clause comparison need no AST of the callee,
	// which a module loaded from the cache does not have; iface encodes it.
	// Data only: nothing in the contract parses or compares it.
	Text string
}

// BoundKind tags a Bound.
type BoundKind uint8

const (
	// BoundInterface is an interface applied to Args.
	BoundInterface BoundKind = iota
	BoundPlain
	BoundSend
	BoundShare
	// BoundFields is Fields[I] over interface Item with Args.
	BoundFields
)

// Bound is one bound of a type parameter, or what an impl implements.
type Bound struct {
	Kind BoundKind
	Item ItemID
	Args []GenericArg
	// Inner is read only when Kind is BoundFields: the kind of the bound
	// every field must meet. Its zero value, BoundInterface, means Item and
	// Args name that interface (Fields[Eq]); BoundPlain, BoundSend and
	// BoundShare are Fields[Plain], Fields[Send] and Fields[Share], with
	// Item NoItem and no Args. It is never BoundFields.
	Inner BoundKind
}

// GenericParam is one generic parameter: a type parameter with bounds, or a
// const parameter with its type.
type GenericParam struct {
	Const  bool
	Name   source.Symbol
	Bounds []Bound
	Type   TypeID
	Span   source.Span
}

// Signature is a checked function signature: the contract every caller, the
// loan pass and the interface read. Generics hold the owning type's
// parameters first, then the function's own.
type Signature struct {
	Item     ItemID
	Name     source.Symbol
	Receiver Receiver
	Generics []GenericParam
	// OwnGenerics is how many of Generics, at the end, are the function's own.
	OwnGenerics uint32
	// Params include the receiver as Params[0] when Receiver != NoReceiver.
	Params []Param
	// Result is the T of E!T, or Void.
	Result TypeID
	// Errors is EmptyErrors when the function cannot fail.
	Errors    ErrorSetID
	Flows     Flows
	Contracts []Contract
	// Variadic is true for a bodiless extern ending in "...".
	Variadic bool
	Exported bool
	// Extern is true for extern and export extern functions: C names, C ABI.
	Extern bool
	Span   source.Span
	// CPU is the cpu clause's feature set: CPUClosure of its names for the
	// build target's architecture, sorted; nil when absent. It is a public
	// fact: callers need it for the unsafe-call rule (E1101), so iface encodes
	// it.
	CPU []string
}

// Param returns the parameter at index p (1-based); NoParam or out of range
// is an ICE.
func (s *Signature) Param(p ParamIndex) *Param {
	if p == NoParam || int(p) > len(s.Params) {
		source.ICEf(s.Span, "Signature.Param: index %d of %d", p, len(s.Params))
	}
	return &s.Params[p-1]
}

// ParamByName returns the index of the parameter named n, or NoParam.
func (s *Signature) ParamByName(n source.Symbol) ParamIndex {
	for i := range s.Params {
		if s.Params[i].Name == n {
			return ParamIndex(i + 1)
		}
	}
	return NoParam
}

// ReturnType is the type of the function's MIR local 0: Errors!Result when
// the function can fail, else Result.
func (s *Signature) ReturnType(t *Table) TypeID { return t.ReturnType(s.Result, s.Errors) }

// FnType is the fn type of a non-generic function value of this signature:
// names dropped, flows positional.
func (s *Signature) FnType(t *Table) TypeID {
	ps := make([]FnParam, len(s.Params))
	for i, p := range s.Params {
		ps[i] = FnParam{Type: p.Type, Into: p.Into}
	}
	return t.Fn(FnType{Params: ps, Result: s.Result, Errors: s.Errors,
		Sources: s.Flows.Sources, Fields: s.Flows.Fields, Stores: s.Flows.Stores, Ends: s.Flows.Ends})
}
