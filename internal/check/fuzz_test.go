// Why: the checker must never crash or hang, whatever text it is given.
// What: FuzzCheck and TestCorpusBlocksNoICE: arbitrary bytes, and every
// fenced mk block of doc/**/*.md and tests/programs, through syntax,
// resolve and check, with no ICE (no E0000, no escaped panic).
// How: each input runs on its own goroutine under a 2 s bound (proposed,
// unmeasured; asserted only with MK_TIMING=1 in the corpus scan).

package check

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"testing"
	"time"

	"mk/internal/diag"
	"mk/internal/resolve"
	"mk/internal/source"
	"mk/internal/types"
)

// languageDocBlocks reads every fenced mk block of the language documents
// (doc/**/*.md without the std module pages), keyed by "file:line" of its
// opening fence, in file and line order.
func languageDocBlocks(t testing.TB) (keys []string, texts map[string]string) {
	t.Helper()
	return docBlocks(t, languageDocs)
}

// allDocBlocks is languageDocBlocks with the std module pages' blocks too: the
// no-ICE property holds for any text.
func allDocBlocks(t testing.TB) (keys []string, texts map[string]string) {
	t.Helper()
	return docBlocks(t, docFiles)
}

// docBlocks reads the fenced mk blocks of the documents list names.
func docBlocks(t testing.TB, list func(string) ([]string, error)) (keys []string, texts map[string]string) {
	t.Helper()
	dir := filepath.Join("..", "..", "doc")
	files, err := list(dir)
	if err != nil || len(files) == 0 {
		t.Fatalf("no documents: %v", err)
	}
	texts = map[string]string{}
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(f)))
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
		for i := 0; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) != "```mk" {
				continue
			}
			start := i + 1
			j := start
			for j < len(lines) && strings.TrimSpace(lines[j]) != "```" {
				j++
			}
			key := f + ":" + itoa(i+1)
			keys = append(keys, key)
			texts[key] = strings.Join(lines[start:j], "\n") + "\n"
			i = j
		}
	}
	return keys, texts
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// checkNoICE runs src through the harness: parse (recovering), resolve and
// check every unit, each stage under source.Guard. It returns every
// diagnostic and fails the test on an ICE.
func checkNoICE(t *testing.T, src string) (*world, []*diag.Diagnostic) {
	t.Helper()
	w, _, ds := checkNoICEWith(t, opt{}, src)
	return w, ds
}

// checkNoICEWith is checkNoICE with module options (a test prelude) and
// work on the world before the module is added (stub modules); src is the
// module's last file.
func checkNoICEWith(t *testing.T, o opt, src string, before ...func(*world)) (*world, *built, []*diag.Diagnostic) {
	t.Helper()
	w := newWorldCore(t, true, o.core)
	for _, f := range before {
		f(w)
	}
	o.parseErrors = true
	var b *built
	if ie := source.Guard("module", source.Span{}, func() { b = w.add("m", o, src) }); ie != nil {
		t.Fatalf("ICE before stage 6: %v\n%s", ie, src)
	}
	ds := append([]*diag.Diagnostic(nil), b.diags()...)
	// The driver reports unknown imports during its import step. This
	// harness has only the fake prelude and explicitly supplied modules.
	for _, file := range b.files {
		for _, imp := range resolve.ImportsOf(file, w.in) {
			if _, ok := w.mods[imp.Path]; !ok {
				ds = append(ds, diag.New(diag.E0202, imp.PathSpan, "no module "+imp.Path))
			}
		}
	}
	for _, id := range b.m.Units() {
		it := w.items.Get(id)
		var body *Body
		if ie := source.Guard(it.Key.String(), it.Span, func() { body = w.c.Check(b.m, id) }); ie != nil {
			t.Fatalf("ICE in %s: %v\n%s", it.Key, ie, src)
		}
		ds = append(ds, body.Diags...)
	}
	for _, d := range ds {
		if d.Code == diag.E0000 {
			t.Fatalf("E0000 at %s: %s\n%s", w.lc(d.At), d.Message, src)
		}
	}
	// Lower and codegen lay out every closed declaration, and a Poison
	// without a diagnostic is lower's ICE: lay each out here, and with no
	// diagnostic at all no field may be Poison (`[-1]u8` and `[!0]u8` were
	// once such cases).
	for _, ii := range b.info.Items {
		it := w.items.Get(ii.Item)
		if it.Kind != types.KindStruct && it.Kind != types.KindEnum {
			continue
		}
		if len(ds) == 0 && b.parsed == 0 {
			if f, ok := poisonField(it.Decl); ok {
				t.Fatalf("%s: field %s is Poison with no diagnostic\n%s", it.Key, w.in.Str(f), src)
			}
		}
		if len(allGenerics(it.Decl)) > 0 {
			continue
		}
		nt := w.tab.Named(it.ID, nil)
		if ie := source.Guard(it.Key.String(), it.Span, func() { w.c.Layout(nt) }); ie != nil {
			t.Fatalf("ICE laying out %s: %v\n%s", it.Key, ie, src)
		}
	}
	return w, b, ds
}

// poisonField is the first field of a struct or enum declaration whose
// type is Poison.
func poisonField(d types.Decl) (source.Symbol, bool) {
	var fs []types.FieldDecl
	switch d := d.(type) {
	case *types.StructDecl:
		fs = d.Fields
	case *types.EnumDecl:
		for _, v := range d.Variants {
			fs = append(fs, v.Fields...)
		}
	}
	for _, f := range fs {
		if f.Type == types.Poison {
			return f.Name, true
		}
	}
	return 0, false
}

// TestCorpusBlocksNoICE runs every doc block through check with the fake
// prelude: no block may draw an ICE (FuzzCheck's property on its seeds;
// the blocks' verdicts are judged by internal/corpus).
func TestCorpusBlocksNoICE(t *testing.T) {
	keys, texts := allDocBlocks(t)
	progs, _ := filepath.Glob(filepath.Join("..", "..", "tests", "programs", "*.mk"))
	sort.Strings(progs)
	for _, p := range progs {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		k := "programs/" + filepath.Base(p)
		keys = append(keys, k)
		texts[k] = strings.ReplaceAll(string(data), "\r\n", "\n")
	}
	for _, k := range keys {
		src := texts[k]
		t.Run(k, func(t *testing.T) {
			t.Parallel()
			start := time.Now()
			checkNoICE(t, src)
			if d := time.Since(start); !timingRun() {
				t.Logf("%s: the 2 s bound is not asserted (took %v)", timingOff, d)
			} else if d > 2*time.Second {
				t.Errorf("took %v, over the 2 s bound", d)
			}
		})
	}
}

// fuzzLimit is FuzzCheck's per-input bound (2 s, proposed and
// unmeasured).
const fuzzLimit = 2 * time.Second

// withinLimit runs f on its own goroutine and fails t from the test
// goroutine when f panics or has not returned after fuzzLimit, so a hang is
// a failure rather than a fuzz worker that never answers. f may call
// t.Fatalf: FailNow ends f's goroutine, whose deferred close still runs. A
// hung f is left running; the fuzz worker reports the input and saves it.
func withinLimit(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	var caught any
	var stack []byte
	go func() {
		defer close(done)
		defer func() {
			if p := recover(); p != nil {
				caught, stack = p, debug.Stack()
			}
		}()
		f()
	}()
	select {
	case <-done:
		if stack != nil {
			t.Fatalf("%s panicked: %v\n%s", what, caught, stack)
		}
	case <-time.After(fuzzLimit):
		t.Fatalf("%s did not return within %v", what, fuzzLimit)
	}
}

func FuzzCheck(f *testing.F) {
	keys, texts := allDocBlocks(f)
	for _, k := range keys {
		f.Add(texts[k])
	}
	for _, s := range captureSeeds() {
		f.Add(s) // start from capturing literals
	}
	// A compiler-provided Format, an error-set field, a .Format call on a
	// set.
	f.Add("error A(n: int)\nerror B\ntype S = A | B\nerror W(s: S, k: int)\nF(w: mut Writer, e: S, x: W): WriteError!void {\n\ttry e.Format(w)\n\ttry Print(w, \"{x} {e}\", (x, e))\n}\n")
	f.Fuzz(func(t *testing.T, src string) {
		withinLimit(t, "check", func() { checkNoICE(t, src) })
	})
}

// docFiles lists the Markdown files under dir, doc/std's pages included, as
// paths relative to dir with '/', in byte order: "reference.md",
// "std/fs.md".
func docFiles(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil || !e.Type().IsRegular() || !strings.HasSuffix(p, ".md") {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		out = append(out, filepath.ToSlash(rel))
		return err
	})
	sort.Strings(out)
	return out, err
}

// languageDocs is docFiles without the std module pages (doc/std/): their
// examples call the real std, which this package's stand-in std does not
// declare, so mk corpus judges them instead.
func languageDocs(dir string) ([]string, error) {
	all, err := docFiles(dir)
	var out []string
	for _, f := range all {
		if !strings.HasPrefix(f, "std/") {
			out = append(out, f)
		}
	}
	return out, err
}
