// Why: llvm-lib on a Unix host stores a member's full path, '/' and all,
// in the long-name table, and the reader once cut the name at its first '/'.
// What: TestArMembers_LongNames.
// How: a hand-made archive with a GNU entry and a COFF entry that both
// hold a path; both names must come back whole.

package driver

import (
	"fmt"
	"testing"
)

// arHeader is one 60-byte ar member header for name and size.
func arHeader(name string, size int) string {
	return fmt.Sprintf("%-16s%-12s%-6s%-6s%-8s%-10d`\n", name, "0", "0", "0", "644", size)
}

func TestArMembers_LongNames(t *testing.T) {
	for _, tc := range []struct {
		name, table string
	}{
		{"gnu", "/tmp/a/m0.o/\n"},
		{"coff", "/tmp/a/m0.o\x00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			table := tc.table
			if len(table)%2 == 1 {
				table += "\n"
			}
			b := "!<arch>\n" + arHeader("//", len(table)) + table + arHeader("/0", 2) + "ab"
			ms, err := arMembers([]byte(b))
			if err != nil {
				t.Fatal(err)
			}
			if len(ms) != 1 || ms[0].name != "/tmp/a/m0.o" || string(ms[0].data) != "ab" {
				t.Fatalf("members %+v, want one /tmp/a/m0.o holding \"ab\"", ms)
			}
		})
	}
}

func TestArMembers_Malformed(t *testing.T) {
	badMagic := "!<arch>\n" + arHeader("m0", 2)
	badMagic = badMagic[:len(badMagic)-2] + "XXab"
	hugeSize := "!<arch>\n" + arHeader("m0", 0)[:48] + "1844674407`\n" + "ab"
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"negative BSD name", []byte("!<arch>\n" + arHeader("#1/-1", 4) + "abcd")},
		{"oversized BSD name", []byte("!<arch>\n" + arHeader("#1/99", 4) + "abcd")},
		{"negative long name", []byte("!<arch>\n" + arHeader("//", 4) + "a/\nb" + arHeader("/-1", 2) + "ab")},
		{"oob long name", []byte("!<arch>\n" + arHeader("//", 4) + "a/\nb" + arHeader("/99", 2) + "ab")},
		{"uint64 overflow size", []byte(hugeSize)},
		{"truncated trailing header", []byte("!<arch>\n" + arHeader("m0", 2) + "ab" + "trunc")},
		{"bad header magic", []byte(badMagic)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := arMembers(tc.data); err == nil {
				t.Fatalf("arMembers accepted malformed archive (%s)", tc.name)
			}
		})
	}
}
