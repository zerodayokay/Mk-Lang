// Why: C headers for the Nutshell targets must be read for the right triple.
// What: TestNutshellParseTriple.
// How: x86_64-nutshell and aarch64-nutshell read headers for their own ELF
// triples, which are their LLVM triples, as x86_64-freestanding's are.

package cimport

import "testing"

func TestNutshellParseTriple(t *testing.T) {
	for target, want := range map[string]string{
		"x86_64-nutshell":     "x86_64-unknown-nutshell-elf",
		"aarch64-nutshell":    "aarch64-unknown-nutshell-elf",
		"x86_64-freestanding": "x86_64-unknown-none-elf",
	} {
		if got := ParseTriple(target); got != want {
			t.Errorf("ParseTriple(%q) = %q, want %q", target, got, want)
		}
	}
}
