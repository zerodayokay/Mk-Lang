// Why: most codes are reported by one pass, but some are reported by two
// passes in different phases (E0101 by the parser and the resolver, E0609 by
// syntax, check and borrow). The earliest-phase rule and the corpus judge
// need to know every group a code can appear in.
//
// What: GroupForm and the table groupForms, one row per (code, group) for
// every code reported in more than one group, with GroupForms, Groups and
// MultiGroup to read it.
//
// How: a code with one group has no row; its group is DefaultPhase. Each row
// names which instances of the code take the group and which package emits
// them. E0905 takes the group of the error it wraps.

package diag

// GroupForm is one group a code is reported in, with the form of the code
// that takes it and the package that emits that form. A code placed in
// one group only has no GroupForm: its group is DefaultPhase.
type GroupForm struct {
	Code    Code
	Phase   Phase
	Form    string // which instances of the code take this group, in plain words
	Emitter string // the emitting package, by its directory name under internal/
}

// groupForms is the list of codes reported in more than one group, ordered by
// code, then by phase. Each code's DefaultPhase is one of its rows. Append a
// row in the same change as the new emitter, with Form saying which
// instances it reports.
var groupForms = [...]GroupForm{
	{E0101, PhaseParse, "lexer and parser forms", "syntax"},
	{E0101, PhaseNames, "forms only resolution decides", "resolve"},
	{E0101, PhaseTypes, "an index form for i, x in over an iterator, which only the source's type decides", "check"},
	{E0204, PhaseNames, "qualified item names", "resolve"},
	{E0204, PhaseTypes, "members, construction, destructuring", "check"},
	{E0206, PhaseImports, "case-only file names and two directories with one import path", "driver"},
	{E0206, PhaseNames, "declarations, imports and members with one folded name", "resolve"},
	{E0207, PhaseNames, "bare names, qualified names, declarations", "resolve"},
	{E0207, PhaseTypes, "members", "check"},
	{E0406, PhaseTypes, "a `become` with a `defer`, `onerr` or `scope` pending", "check"},
	{E0406, PhaseFlow, "a move of a value a `defer` uses", "borrow"},
	{E0507, PhaseTypes, "a *q into p clause with no writable path to p", "check"},
	{E0507, PhaseFlow, "writes through a shared step, a var global included", "borrow"},
	{E0609, PhaseParse, "closure syntax", "syntax"},
	{E0609, PhaseTypes, "a method named without a call; a capturing literal outside a const argument, or one that moves a capture or mutates one in a contract or invariant", "check"},
	{E0609, PhaseFlow, "a capturing literal whose result could hold a loan on a capture, that stores a capture loan or stores a non-static loan into a capture, and borrow's backstop", "borrow"},
	{E0905, PhaseParse, "wrapping a parse-group error in generated output", "gen"},
	{E0905, PhaseImports, "wrapping an imports-group error in generated output", "gen"},
	{E0905, PhaseNames, "wrapping a names-group error in generated output", "gen"},
	{E0905, PhaseTypes, "wrapping a types-group error in generated output", "gen"},
	{E0905, PhaseFlow, "wrapping a flow-group error in generated output", "gen"},
	{E1002, PhaseTypes, "var and const declaration types", "check"},
	{E1002, PhaseFlow, "s.go arguments", "check"},
	{E1102, PhaseImports, "unsafe blocks and unsafe impl outside unsafe_modules", "driver"},
	{E1102, PhaseFlow, "an ends primitive outside unsafe_modules", "borrow"},
	{E1204, PhaseParse, "prefix and declaration forms", "syntax"},
	{E1204, PhaseTypes, "postfix .await that fails member lookup", "check"},
	{E1221, PhaseParse, "macro forms such as println!", "syntax"},
	{E1221, PhaseNames, "type and value names", "resolve"},
	{E1221, PhaseTypes, "members", "check"},
}

// GroupForms returns the list of codes reported in more than one group:
// one entry per (code, group), ordered by code, then by phase. The slice is a
// copy; changing it changes nothing here.
func GroupForms() []GroupForm {
	out := make([]GroupForm, len(groupForms))
	copy(out, groupForms[:])
	return out
}

// Groups returns every group some pass reports c in, in rule order. For a
// code with one group it is exactly [DefaultPhase(c)]; for a code of
// GroupForms it is that code's phases, DefaultPhase(c) among them. Which of
// several groups a judge waits for (the latest, or the one a header names) is
// the judge's policy, not this table's.
func Groups(c Code) []Phase {
	var out []Phase
	for _, g := range groupForms {
		if g.Code == c {
			out = append(out, g.Phase)
		}
	}
	if out == nil {
		out = []Phase{DefaultPhase(c)}
	}
	return out
}

// MultiGroup reports whether c is reported in more than one group (it has
// rows in GroupForms).
func MultiGroup(c Code) bool {
	for _, g := range groupForms {
		if g.Code == c {
			return true
		}
	}
	return false
}
