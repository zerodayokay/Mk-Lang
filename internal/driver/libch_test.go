// Why: two builds, in one process or in two, write std.libc's C headers
// into the same cache directory at once.
// What: TestWriteLibcHeaderConcurrent, of writeSameFile, the header writer.
// How: many goroutines write one header at once, with no lock between them,
// as between two processes, over many fresh directories; each write must
// succeed and leave the whole header, and no temporary may be left beside
// it.

package driver

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestWriteLibcHeaderConcurrent: writers of the same header at the same
// moment, as two builds of C-source programs in one process are, all
// succeed, and the header is whole.
func TestWriteLibcHeaderConcurrent(t *testing.T) {
	text := "/* std.libc */\n" + strings.Repeat("int mk_f(void *p, unsigned long n);\n", 2000)
	const writers, rounds = 16, 25
	for round := 0; round < rounds; round++ {
		dir := t.TempDir()
		p := filepath.Join(dir, "stdio.h")
		var start, done sync.WaitGroup
		start.Add(1)
		errs := make(chan error, writers)
		for w := 0; w < writers; w++ {
			done.Add(1)
			go func() {
				defer done.Done()
				start.Wait()
				_, err := writeSameFile(p, []byte(text))
				errs <- err
			}()
		}
		start.Done()
		done.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("round %d: %v", round, err)
			}
		}
		if got, err := os.ReadFile(p); err != nil || string(got) != text {
			t.Fatalf("round %d: the header holds %d bytes (%v), want %d", round, len(got), err, len(text))
		}
		if ents, _ := os.ReadDir(dir); len(ents) != 1 {
			var names []string
			for _, e := range ents {
				names = append(names, e.Name())
			}
			t.Fatalf("round %d: the directory holds %q, want the header alone", round, names)
		}
	}
}
