// Why: std/core's layers and std's unsafe budget must hold, checked without
// importing any compiler package.
// What: TestLayerZeroTextScan and TestUnsafeBudget.
// How: text scans of the embedded .mk sources: layer-0 bodies calling layer-1
// or layer-2 names, and unsafe blocks per KLOC against a ratchet ceiling.

package std

import (
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// mkFile is one std source file, CRLF normalized.
type mkFile struct {
	path string // relative to std/
	text string
}

// mkFiles returns every embedded .mk file under dir ("." for all), sorted.
func mkFiles(t *testing.T, dir string) []mkFile {
	t.Helper()
	var out []mkFile
	err := fs.WalkDir(FS, dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".mk") {
			return err
		}
		b, err := FS.ReadFile(p)
		if err != nil {
			return err
		}
		out = append(out, mkFile{p, strings.ReplaceAll(string(b), "\r\n", "\n")})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// coreLayer is the layer of a std/core file by prefix: core_* 0,
// mem_*, coll_* and text_* 1, io_* and sync_* 2, -1 for none (tests_*).
func coreLayer(file string) int {
	b := path.Base(file)
	switch {
	case strings.HasPrefix(b, "core_"):
		return 0
	case strings.HasPrefix(b, "mem_"), strings.HasPrefix(b, "coll_"), strings.HasPrefix(b, "text_"):
		return 1
	case strings.HasPrefix(b, "io_"), strings.HasPrefix(b, "sync_"):
		return 2
	}
	return -1
}

// declLine matches a function declaration line: free (group 2) or a method
// (group 1 the receiver type, group 2 the name); group 3 is the rest.
var declLine = regexp.MustCompile(`^(?:export )?(?:([A-Z]\w*)(?:\[[^\]]*\])?\.)?([A-Za-z]\w*)(?:\[[^\]]*\])?\((.*)$`)

// TestLayerZeroTextScan: a body in a core_* file (layer 0) calls no
// free function declared only in a layer-1 or layer-2 file and no method
// whose name only layer-1 or layer-2 files declare, and takes no &Arena,
// &Alloc, &Heap or Text parameter, which it could use only through layer 1
// (a body may call only its own layer and below). Reader.Line(a: &Arena)
// copies into a, so it lives in text_text.mk. Calls through function values
// (WriterTable, ReaderTable, FmtArg.f) are not visible to a text scan;
// FmtRun's call of FmtArg.f is outside the layering rule as written.
func TestLayerZeroTextScan(t *testing.T) {
	files := mkFiles(t, "core")
	freeAt := map[string][]int{}   // free function name -> layers declaring it
	methodAt := map[string][]int{} // method name -> layers declaring it
	for _, f := range files {
		l := coreLayer(f.path)
		if l < 0 {
			continue
		}
		for _, line := range strings.Split(f.text, "\n") {
			m := declLine.FindStringSubmatch(line)
			if m == nil || strings.HasPrefix(line, " ") {
				continue
			}
			if m[1] == "" {
				freeAt[m[2]] = append(freeAt[m[2]], l)
			} else {
				methodAt[m[2]] = append(methodAt[m[2]], l)
			}
		}
	}
	upper := func(ls []int) bool { return len(ls) > 0 && !slices.Contains(ls, 0) }
	callRe := regexp.MustCompile(`(\.?)\b([A-Za-z]\w*)(?:\[[^\]()]*\])?\(`)
	allocParam := regexp.MustCompile(`\b\w+: (&Arena|&Alloc|&Heap|mut Text|&Text)\b`)
	for _, f := range files {
		if coreLayer(f.path) != 0 {
			continue
		}
		var fn string
		for i, line := range strings.Split(f.text, "\n") {
			if m := declLine.FindStringSubmatch(line); m != nil && !strings.HasPrefix(line, " ") {
				fn = strings.TrimPrefix(strings.Join([]string{m[1], m[2]}, "."), ".")
				if strings.HasSuffix(line, "{") && allocParam.MatchString(m[3]) {
					t.Errorf("std/%s:%d: layer-0 body %s takes %s", f.path, i+1, fn, allocParam.FindString(m[3]))
				}
				continue
			}
			if !strings.HasPrefix(line, " ") {
				continue
			}
			code, _, _ := strings.Cut(line, "//")
			code = strings.ReplaceAll(code, "raw.", "raw_") // intrinsics are layer 0
			for _, c := range callRe.FindAllStringSubmatch(code, -1) {
				if c[1] == "" && upper(freeAt[c[2]]) || c[1] == "." && upper(methodAt[c[2]]) {
					t.Errorf("std/%s:%d: layer-0 body %s calls %s%s, declared in layer 1 or 2", f.path, i+1, fn, c[1], c[2])
				}
			}
		}
	}
}

// unsafeBlock is the text of an unsafe block.
var unsafeBlock = regexp.MustCompile(`\bunsafe "`)

// unsafeCeiling is the number of unsafe blocks outside rt, sys, mem and task
// counted by go test ./std on 2026-11-03 (Windows 11): 23 in 4,393 lines
// (5.2 per KLOC), up from 14 in 2,238 lines (6.3 per KLOC) before more
// bodies were written. Raised once, then: the container slot operations
// moved into mem_* files (coll_list went from 7 blocks to 0), and the 9 new blocks are raw access a mem_*
// helper cannot take, because the layering rule forbids a layer-0 body
// (core_rw, core_utf8, core_slice) to call layer 1, and the process
// streams (io_stream, io_system) are reached through raw pointers that
// other tasks share. The budget is 3 per KLOC; until std meets
// it, the count may only go down (a ratchet).
const unsafeCeiling = 23

// TestUnsafeBudget counts unsafe blocks per KLOC outside rt, sys,
// mem and task, attributing each block by module directory and, in
// std/core, by file prefix (mem_* files count as mem), and logs the
// per-file table. It passes at 3 per KLOC or below; above, it fails when
// the count exceeds unsafeCeiling.
func TestUnsafeBudget(t *testing.T) {
	exempt := func(p string) bool {
		// simd, free, mmio and libc are the boundary the language puts
		// behind unsafe (a cpu call, the hardware, the C ABI), as rt and
		// sys are the OS's.
		for _, d := range []string{"rt/", "sys/", "task/", "simd/", "free/", "mmio/", "libc/", "gpu/dx/"} {
			if strings.HasPrefix(p, d) {
				return true
			}
		}
		return strings.HasPrefix(path.Base(p), "mem_") || strings.HasPrefix(p, "mem/")
	}
	lines, blocks := 0, 0
	var table strings.Builder
	for _, f := range mkFiles(t, ".") {
		if exempt(f.path) {
			continue
		}
		n := strings.Count(f.text, "\n")
		k := len(unsafeBlock.FindAllStringIndex(f.text, -1))
		lines += n
		blocks += k
		if k > 0 {
			fmt.Fprintf(&table, "\n  %-24s %3d blocks %5d lines", f.path, k, n)
		}
	}
	density := float64(blocks) * 1000 / float64(lines)
	t.Logf("unsafe outside rt, sys, mem, task, simd, free, mmio, libc: %d blocks in %d lines, %.1f per KLOC (budget 3)%s", blocks, lines, density, table.String())
	if density > 3 && blocks > unsafeCeiling {
		t.Errorf("%d unsafe blocks outside rt, sys, mem, task (%.1f per KLOC, budget 3, ceiling %d): concentrate the new one in a mem_* helper or remove one", blocks, density, unsafeCeiling)
	}
}
