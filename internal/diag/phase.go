// Why: a build that fails to parse should not also print a hundred type
// errors that follow from the parse error. The earliest-phase rule reports
// only the lowest phase that has errors, so every code needs a phase.
//
// What: Phase (internal, parse, imports, names, types, flow, native), its
// String spelling and IsValid, Phases, and DefaultPhase, the phase of
// each code's primary emitter.
//
// How: Phase numbers follow the order of the rule. DefaultPhase is a switch
// over the codes that sit outside their block's phase, then a switch over the
// code's hundreds block. E0000 belongs to every phase; E0001 comes after flow.

package diag

import "strconv"

// Phase is the report group a diagnostic belongs to. The earliest-phase rule of
// doc/tools.md keeps only the lowest Phase present, plus every
// PhaseInternal diagnostic and every cause a kept one names. The numbering
// is the order of the rule and is visible in JSON only through String.
type Phase uint8

const (
	// PhaseInternal holds E0000: a compiler fault, emitted whatever the phase.
	PhaseInternal Phase = iota
	// PhaseParse is the parse group: lexer, parser, habit recognizers.
	PhaseParse
	// PhaseImports is the imports group: module loading, import paths, the
	// import graph, the pkg.mk permission check.
	PhaseImports
	// PhaseNames is the names group: the resolver.
	PhaseNames
	// PhaseTypes is the types group: generators, signatures, shapes, kinds,
	// flows, bodies, MIR building, const evaluation, the instance limit.
	PhaseTypes
	// PhaseFlow is the flow group: moves, init states, loans, boundaries.
	PhaseFlow
	// PhaseNative holds E0001: the toolchain step after every group is clean.
	// It is not one of the five user phases; it is reached only when the parse
	// to flow groups produced nothing.
	PhaseNative

	numPhases
)

var phaseNames = [numPhases]string{
	PhaseInternal: "internal",
	PhaseParse:    "parse",
	PhaseImports:  "imports",
	PhaseNames:    "names",
	PhaseTypes:    "types",
	PhaseFlow:     "flow",
	PhaseNative:   "native",
}

// String is the phase's JSON name: internal, parse, imports, names, types,
// flow or native.
func (p Phase) String() string {
	if p < numPhases {
		return phaseNames[p]
	}
	return "phase(" + strconv.Itoa(int(p)) + ")"
}

// IsValid reports whether p is one of the declared phases.
func (p Phase) IsValid() bool { return p < numPhases }

// Phases lists every phase in rule order.
func Phases() []Phase {
	out := make([]Phase, numPhases)
	for i := range out {
		out[i] = Phase(i)
	}
	return out
}

// DefaultPhase is the group a code is emitted in by its primary emitter. A
// code that two passes emit takes the group of the emitter, which overrides
// the default with Diagnostic.In: E0101 by resolve in names for the forms it
// decides; E0204, E0206, E0207, E1002, E1102, E1204, E1221, E0905; E0409 and
// E0507 on a var global; E0507 at a *q into p clause by check in types; E0609
// for closure syntax and E1221 for macro forms by syntax in parse; E0609,
// whose default is types (check: a method named without a call, and the
// capture forms), by borrow in flow for a literal whose result could hold a
// loan on a capture. GroupForms is that list as data, one row per (code,
// group), and Groups gives a code's groups.
func DefaultPhase(c Code) Phase {
	switch c {
	case E0000:
		return PhaseInternal
	case E0001:
		return PhaseNative
	// Imports. E0214 to E0216 are the import lookup's and the
	// manifest step's, beside E0202.
	case E0202, E0205, E0213, E0214, E0215, E0216, E1102:
		return PhaseImports
	// Names.
	case E0201, E0204, E0206, E0207, E1209, E1221:
		return PhaseNames
	// Types, outside the E03xx, E07xx, E08xx, E09xx blocks. E0609 is here,
	// not in names; E1104 to E1108 are check's and mono's, both in types, and
	// E1109 is in types with them. E0217 is the checker's.
	case E0203, E0208, E0209, E0217, E0410, E0412, E0601, E0607, E0608, E0609, E1003, E1101, E1103,
		E1104, E1105, E1106, E1107, E1108, E1109, E1202:
		return PhaseTypes
	// Flow inside blocks that are otherwise types.
	case E0807:
		return PhaseFlow
	}
	switch c / 100 {
	case 1, 12:
		return PhaseParse
	case 3, 7, 8, 9:
		return PhaseTypes
	case 4, 5, 6, 10:
		return PhaseFlow
	}
	return PhaseTypes
}
