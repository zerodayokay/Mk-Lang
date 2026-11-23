// Why: a capturing function literal takes its captures through a hidden
// trailing parameter, and the validator, borrow and codegen must all
// recognize that parameter's type the same way.
//
// What: IsEnvType, the test for an environment type.
//
// How: an environment type is either &R, where R is a non-empty record of
// shared references (&T) alone, or mut R, where R is a non-empty record of
// shared (&T) and exclusive (mut T, mut []T) borrows with at least one
// exclusive field.

package mir

import "mk/internal/types"

// IsEnvType reports whether t is an environment type:
//   - &R, where R is a non-empty record and every field is &T (read-only
//     captures), or
//   - mut R, where R is a non-empty record, every field is &T, mut T or
//     mut []T, and at least one field is mut T or mut []T (at least one
//     mutable or reborrowed capture).
//
// A literal that captures nothing has no environment.
//
// Locals len(Sig.Params)+1 .. ParamCount of a body (its hidden parameters)
// and the arguments of a call past its callee's declared parameters (its
// environment arguments) have such a type (invariants M1, M5).
func IsEnvType(tab *types.Table, t types.TypeID) bool {
	if t == types.NoType {
		return false
	}
	k := tab.Kind(t)
	if k != types.KRef && k != types.KMut {
		return false
	}
	r := tab.Get(tab.Get(t).Elem)
	if r.Kind != types.KRecord || len(r.Fields) == 0 {
		return false
	}
	hasMut := false
	for _, f := range r.Fields {
		switch tab.Kind(f.Type) {
		case types.KRef:
		case types.KMut, types.KMutSlice:
			hasMut = true
		default:
			return false
		}
	}
	return hasMut == (k == types.KMut)
}
