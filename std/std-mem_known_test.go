// Why: the compiler looks up std items by key, so each key must name a real
// declaration in std.
// What: TestStdKnownItemsResolve and knownCompilerMade.
// How: reads entity.go's KnownItems and resolve/known.go's knownTable as text
// and finds each key's declaration in std's text; std.core is one text for
// every target, and std.target holds TargetInfo, Arch and Os.

package std

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// knownCompilerMade are the KnownItems the driver adds itself: Target is the
// per-build const of type TargetInfo (internal/driver/target.go). std must
// not declare them.
var knownCompilerMade = map[string]bool{"Target": true}

// knownItemsFields is every field of types.KnownItems, in order.
func knownItemsFields(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "internal", "types", "entity.go"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "type KnownItems struct {")
	if i < 0 {
		t.Fatal("internal/types/entity.go: no KnownItems")
	}
	body := src[i:]
	body = body[strings.Index(body, "{")+1 : strings.Index(body, "\n}")]
	var fields []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		names, ty, ok := strings.Cut(line, " ItemID")
		if !ok || strings.TrimSpace(ty) != "" {
			t.Fatalf("KnownItems: unexpected line %q", line)
		}
		for _, n := range strings.Split(names, ",") {
			fields = append(fields, strings.TrimSpace(n))
		}
	}
	return fields
}

type knownKey struct{ field, module, owner, name string }

var knownRow = regexp.MustCompile(`return &k\.(\w+) \}, (knownRaw|knownSimd|knownTarget|"[\w.]*"), "(\w*)", "(\w+)"\}`)

// knownTableKeys is resolve's knownTable: field, module ("" for the
// prelude), owner and name.
func knownTableKeys(t *testing.T) []knownKey {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "internal", "resolve", "known.go"))
	if err != nil {
		t.Fatal(err)
	}
	var keys []knownKey
	for _, m := range knownRow.FindAllStringSubmatch(string(b), -1) {
		mod := strings.Trim(m[2], `"`)
		switch m[2] {
		case "knownRaw":
			mod = "std.raw"
		case "knownSimd":
			mod = "std.simd" // the four SIMD items
		case "knownTarget":
			mod = "std.target" // TargetInfo, Arch, Os and Target
		}
		keys = append(keys, knownKey{m[1], mod, m[3], m[4]})
	}
	return keys
}

// moduleText is every .mk file of a std module directory, joined.
func moduleText(t *testing.T, dir string) string {
	t.Helper()
	ms, err := filepath.Glob(filepath.Join(dir, "*.mk"))
	if err != nil || len(ms) == 0 {
		t.Fatalf("%s: no .mk files (%v)", dir, err)
	}
	var b strings.Builder
	for _, m := range ms {
		x, err := os.ReadFile(m)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(x)
		b.WriteByte('\n')
	}
	return b.String()
}

// declares reports whether text declares the exported item name, or the
// exported member owner.name, at the top level.
func declares(text, owner, name string) bool {
	var re *regexp.Regexp
	if owner != "" {
		re = regexp.MustCompile(`(?m)^export ` + regexp.QuoteMeta(owner) + `(\[[^\]]*\])?\.` + regexp.QuoteMeta(name) + `[\[(]`)
	} else {
		re = regexp.MustCompile(`(?m)^export (` + declarationKind + `(struct|enum|interface|error|type|const) ` + regexp.QuoteMeta(name) + `\b|` + regexp.QuoteMeta(name) + `[\[(])`)
	}
	return re.MatchString(text)
}

// TestStdKnownItemsResolve: every KnownItems field has exactly one key in
// resolve's table, and every key but the compiler-made ones names an
// exported declaration in std.core (the prelude), std.raw, std.simd or
// std.target.
func TestStdKnownItemsResolve(t *testing.T) {
	fields := knownItemsFields(t)
	keys := knownTableKeys(t)
	if len(fields) < 40 || len(keys) < 40 {
		t.Fatalf("read %d fields and %d keys: the parse is wrong", len(fields), len(keys))
	}
	seen := map[string]int{}
	for _, k := range keys {
		seen[k.field]++
	}
	for _, f := range fields {
		if seen[f] != 1 {
			t.Errorf("KnownItems.%s has %d keys in internal/resolve/known.go, want 1", f, seen[f])
		}
	}
	if len(keys) != len(fields) {
		t.Errorf("%d keys for %d fields", len(keys), len(fields))
	}
	texts := map[string]string{"": moduleText(t, "core"), "std.raw": moduleText(t, "raw"), "std.simd": moduleText(t, "simd"), "std.target": moduleText(t, "target")}
	for _, k := range keys {
		text, ok := texts[k.module]
		if !ok {
			t.Errorf("KnownItems.%s: module %q is neither the prelude, std.raw, std.simd nor std.target", k.field, k.module)
			continue
		}
		got := declares(text, k.owner, k.name)
		if knownCompilerMade[k.field] {
			if got {
				t.Errorf("KnownItems.%s: std declares %s, which the driver makes", k.field, k.name)
			}
			continue
		}
		if !got {
			what := k.name
			if k.owner != "" {
				what = k.owner + "." + k.name
			}
			mod := k.module
			if mod == "" {
				mod = "std.core"
			}
			t.Errorf("KnownItems.%s: no exported %s in %s", k.field, what, mod)
		}
		// The prelude still offers std.target's declarations by name
		// (std.core's aliases in core_target.mk).
		if k.module == "std.target" && !declares(texts[""], "", k.name) {
			t.Errorf("KnownItems.%s: std.core exports no %s, which the prelude keeps", k.field, k.name)
		}
	}
}

// TestStdKnownItemsResolve_SeesMissing: the scan finds a planted absence.
func TestStdKnownItemsResolve_SeesMissing(t *testing.T) {
	core := moduleText(t, "core")
	for _, c := range []struct{ owner, name string }{{"", "Arena"}, {"", "Scope"}, {"Arena", "Fmt"}, {"str", "FromC"}, {"", "OutOfMemory"}, {"", "Print"}, {"", "Slice"}} {
		if !declares(core, c.owner, c.name) {
			t.Errorf("declares(%q, %q) is false on std.core", c.owner, c.name)
		}
	}
	planted := strings.Replace(core, "export Arena.Fmt[", "Arena.Fmt[", 1)
	if declares(planted, "Arena", "Fmt") {
		t.Error("an unexported Arena.Fmt still counts")
	}
	for _, head := range []string{"linear mut view struct Arena", "export mut linear view struct Arena", "export linear mut view struct Arenas"} {
		if declares(head+" {\n", "", "Arena") {
			t.Errorf("%q counts as an exported Arena", head)
		}
	}
	if declares(core, "", "NoSuchKnownItem") {
		t.Error("a missing name counts")
	}
}
