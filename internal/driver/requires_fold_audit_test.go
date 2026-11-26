// Why: folded requires predicates and substituted SizeOf const arguments must
// invalidate a warm front cache whenever a private predicate or layout they
// read changes, while leaving unrelated private edits cached.
//
// What: TestFrontFoldedPrivateRequiresEdit, TestFrontFoldedPrivateLayoutEdit,
// TestFrontFoldedUnrelatedEditHits, TestFrontFoldedPrivateLayoutStep1Stored,
// TestFrontFoldedOpenGenericLayoutEdit, TestFrontFoldedErrorLayoutEdit, and
// TestFrontSubstSizeOfLayoutEdit.
//
// How: edits unexported predicates, private generic and error layouts, and
// unrelated private items across warm frontCheck runs and compares warm
// against cold results (plus ModulesFromCache on unrelated edits).

package driver

import (
	"bytes"
	"slices"
	"testing"
)

func TestFrontFoldedPrivateRequiresEdit(t *testing.T) {
	lib := func(limit string) string {
		return "Lane[const N: int](): int\n    requires N < " + limit + "\n{\n    return N\n}\n\nexport G[U](): int {\n    return Lane[SizeOf[[2]U]()]()\n}\n"
	}
	const app = "import lib\n\nconst K: int = lib.G[[16]u8]()\n"
	files := map[string]string{
		"pkg.mk":     manifest(nil),
		"lib/lib.mk": lib("64"),
		"app/app.mk": app,
	}
	warm, cold := warmAfter(t, files, "lib/lib.mk", func(string) string { return lib("16") })
	if got := codes(cold); !slices.Equal(got, []string{"E0901"}) {
		t.Fatalf("cold codes %q, want [E0901]\n%s", got, cold.Report.Text(cold.Sources))
	}
	if !bytes.Equal(jsonOf(t, warm), jsonOf(t, cold)) {
		t.Errorf("warm != cold after private predicate edit:\nwarm:\n%s\ncold:\n%s",
			jsonOf(t, warm), jsonOf(t, cold))
	}
}

func TestFrontFoldedPrivateLayoutEdit(t *testing.T) {
	lib := func(field string) string {
		return "struct Priv[T] {\n    a: T\n    b: " + field + "\n}\n\nLane[const N: int](): int\n    requires N == SizeOf[Priv[u8]]()\n{\n    return N\n}\n\nexport G[U](): int {\n    return Lane[SizeOf[[2]U]()]()\n}\n"
	}
	const app = "import lib\n\nconst K: int = lib.G[u8]()\n"
	files := map[string]string{
		"pkg.mk":     manifest(nil),
		"lib/lib.mk": lib("T"),
		"app/app.mk": app,
	}
	warm, cold := warmAfter(t, files, "lib/lib.mk", func(string) string { return lib("[2]T") })
	if got := codes(cold); !slices.Equal(got, []string{"E0901"}) {
		t.Fatalf("cold codes %q, want [E0901]\n%s", got, cold.Report.Text(cold.Sources))
	}
	if !bytes.Equal(jsonOf(t, warm), jsonOf(t, cold)) {
		t.Errorf("warm != cold after private generic layout edit:\nwarm:\n%s\ncold:\n%s",
			jsonOf(t, warm), jsonOf(t, cold))
	}
}

func TestFrontFoldedUnrelatedEditHits(t *testing.T) {
	lib := func(body, unusedField string) string {
		return "struct UnusedPriv[T] {\n    a: T\n    b: " + unusedField + "\n}\n\nLane[const N: int](): int\n    requires N < 64\n{\n    return N\n}\n\nexport G[U](): int {\n    return Lane[SizeOf[[2]U]()]()\n}\n\nUnused(): int {\n    return " + body + "\n}\n"
	}
	const app = "import lib\n\nconst K: int = lib.G[[16]u8]()\n"
	files := map[string]string{
		"pkg.mk":     manifest(nil),
		"lib/lib.mk": lib("1", "T"),
		"app/app.mk": app,
	}
	warm, _ := warmAfter(t, files, "lib/lib.mk", func(string) string { return lib("2", "[2]T") })
	if !slices.Contains(warm.Trace.ModulesFromCache, "app") {
		t.Errorf("ModulesFromCache = %v, resolved = %v, notStored = %v, evals = %v, want app to hit warm cache after unrelated edit in lib",
			warm.Trace.ModulesFromCache, warm.Trace.ModulesResolved, warm.Trace.NotStored, warm.Trace.Evaluations)
	}
}

func TestFrontFoldedPrivateLayoutStep1Stored(t *testing.T) {
	lib := func(field string) string {
		return "struct Priv[T] {\n    a: T\n    b: " + field + "\n}\n\nLane[const N: int](): int\n    requires N == SizeOf[Priv[u8]]()\n{\n    return N\n}\n\nexport G[U](): int {\n    return Lane[SizeOf[[2]U]()]()\n}\n\nUnused(): int {\n    return 1\n}\n"
	}
	const app = "import lib\n\nconst K: int = lib.G[u8]()\n"
	files := map[string]string{
		"pkg.mk":     manifest(nil),
		"lib/lib.mk": lib("T"),
		"app/app.mk": app,
	}
	// Editing only Unused() must let app hit cache, proving step 1 actually stored app.
	warm, _ := warmAfter(t, files, "lib/lib.mk", func(s string) string {
		return string(bytes.ReplaceAll([]byte(s), []byte("return 1"), []byte("return 2")))
	})
	if !slices.Contains(warm.Trace.ModulesFromCache, "app") {
		t.Errorf("ModulesFromCache = %v, notStored = %v, evals = %v (step 1 did not store app or unrelated edit missed!)",
			warm.Trace.ModulesFromCache, warm.Trace.NotStored, warm.Trace.Evaluations)
	}
}

func TestFrontFoldedOpenGenericLayoutEdit(t *testing.T) {
	lib := func(field string) string {
		return "struct Priv[T] {\n    a: T\n    b: " + field + "\n}\n\nLane[T, const N: int](): int\n    requires N == SizeOf[Priv[T]]()\n{\n    return N\n}\n\nexport G[U](): int {\n    return Lane[U, SizeOf[[2]U]()]()\n}\n"
	}
	const app = "import lib\n\nconst K: int = lib.G[u8]()\n"
	files := map[string]string{
		"pkg.mk":     manifest(nil),
		"lib/lib.mk": lib("T"),
		"app/app.mk": app,
	}
	warm, cold := warmAfter(t, files, "lib/lib.mk", func(string) string { return lib("[2]T") })
	if got := codes(cold); !slices.Equal(got, []string{"E0901"}) {
		t.Fatalf("cold codes %q, want [E0901]\n%s", got, cold.Report.Text(cold.Sources))
	}
	if !bytes.Equal(jsonOf(t, warm), jsonOf(t, cold)) {
		t.Errorf("warm != cold after open generic private layout edit:\nwarm:\n%s\ncold:\n%s",
			jsonOf(t, warm), jsonOf(t, cold))
	}
}

func TestFrontFoldedErrorLayoutEdit(t *testing.T) {
	lib := func(payload string) string {
		return "error E(x: " + payload + ")\n\nLane[T, const N: int](): int\n    requires N == 2 && SizeOf[E!T]() < 24\n{\n    return N\n}\n\nexport G[U](): int {\n    return Lane[U, SizeOf[[2]U]()]()\n}\n"
	}
	const app = "import lib\n\nconst K: int = lib.G[u8]()\n"
	files := map[string]string{
		"pkg.mk":     manifest(nil),
		"lib/lib.mk": lib("u8"),
		"app/app.mk": app,
	}
	warm, cold := warmAfter(t, files, "lib/lib.mk", func(string) string { return lib("[32]u8") })
	if got := codes(cold); !slices.Equal(got, []string{"E0901"}) {
		t.Fatalf("cold codes %q, want [E0901]\n%s", got, cold.Report.Text(cold.Sources))
	}
	if !bytes.Equal(jsonOf(t, warm), jsonOf(t, cold)) {
		t.Errorf("warm != cold after error layout edit:\nwarm:\n%s\ncold:\n%s",
			jsonOf(t, warm), jsonOf(t, cold))
	}
}

func TestFrontSubstSizeOfLayoutEdit(t *testing.T) {
	lib := func(field string) string {
		return "struct Priv[T] {\n    a: T\n    b: " + field + "\n}\n\nLane[const N: int](): int\n    requires N == 2\n{\n    return N\n}\n\nexport G[U](): int {\n    return Lane[SizeOf[Priv[U]]()]()\n}\n"
	}
	const app = "import lib\n\nconst K: int = lib.G[u8]()\n"
	files := map[string]string{
		"pkg.mk":     manifest(nil),
		"lib/lib.mk": lib("T"),
		"app/app.mk": app,
	}
	warm, cold := warmAfter(t, files, "lib/lib.mk", func(string) string { return lib("[2]T") })
	if got := codes(cold); !slices.Equal(got, []string{"E0901"}) {
		t.Fatalf("cold codes %q, want [E0901]\n%s", got, cold.Report.Text(cold.Sources))
	}
	if !bytes.Equal(jsonOf(t, warm), jsonOf(t, cold)) {
		t.Errorf("warm != cold after Subst SizeOf layout edit:\nwarm:\n%s\ncold:\n%s",
			jsonOf(t, warm), jsonOf(t, cold))
	}
}
