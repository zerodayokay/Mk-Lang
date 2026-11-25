// Why: a function generic over an array length is called with an array, and
// the length must come from the argument's type as a type parameter does.
// What: TestInferConstGenericFromArrayLength.
// How: each case checks a body that calls a function with a const generic
// and no bracket arguments, and compares codes and positions.

package check

import "testing"

// TestInferConstGenericFromArrayLength: a const generic named by an array
// parameter's length is inferred from the argument. Such a call was an
// internal compiler error (types.Table.Intern(array): length kind 0): the
// parameter type with the length still open could not be interned. A length
// two arguments disagree on is E0301, and one no argument names is E0711.
func TestInferConstGenericFromArrayLength(t *testing.T) {
	const decls = "Count[const N: int](xs: &[N]int): int {\n\t_ = xs\n\treturn N\n}\nFirst[T: Plain, const N: int](xs: [N]T): T {\n\treturn xs[0]\n}\nSame[const N: int](a: &[N]int, b: &[N]int): bool {\n\treturn a[0] == b[0]\n}\nGrid[const R: int, const C: int](g: &[R][C]u8): int {\n\t_ = g\n\treturn R * C\n}\nZero[const N: int](): int {\n\treturn N\n}\n"
	runBodyCases(t, []bodyCase{
		{"from a reference to an array", decls + "F(): int {\n\ta := [1, 2, 3]\n\treturn Count(&a)\n}\n", ""},
		{"with a type parameter", decls + "F(): i32 {\n\tb: [2]i32 = [5, 6]\n\treturn First(b)\n}\n", ""},
		{"two lengths", decls + "F(): int {\n\tg: [2][5]u8 = [[0; 5]; 2]\n\treturn Grid(&g)\n}\n", ""},
		{"written in brackets", decls + "F(): int {\n\ta := [1, 2, 3]\n\treturn Count[3](&a) + Zero[4]()\n}\n", ""},
		{"arguments that disagree", decls + "F(): bool {\n\ta := [1, 2, 3]\n\tb := [5, 6]\n\treturn Same(&a, &b)\n}\n", "E0301@21:18"},
		{"no argument names it", decls + "F(): int {\n\treturn Zero()\n}\n", "E0711@19:9"},
		{"a slice has no length to give", decls + "F(s: []int): int {\n\treturn Count(&s)\n}\n", "E0301@19:15 E0711@19:9"},
	})
}
