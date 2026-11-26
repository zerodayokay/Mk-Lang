// Why: each std module page (doc/std/<module>.md) embeds its module's API
// as mk doc writes it; a hand-kept copy goes stale with the first changed
// /// line.
// What: TestStdPages, the page generator: it renders every page's API
// section and, with -update, writes it between the page's
// `<!-- api:begin -->` and `<!-- api:end -->` markers; without -update it
// fails naming each stale page.
// How: a page documents the module the first word of its `# std.x` title
// names (`# std.core: the prelude` documents std.core's prelude names) and that
// module's public submodules (every std.x.y), the same set as
// `mk doc --std std.x --embed`. Renders are made for one fixed target,
// x86_64-linux, so the result does not depend on the host (an x86_64-windows
// render differs only in std.linux, which Windows lacks); std.free exists
// only on x86_64-freestanding and is rendered for it. std.raw is std-only,
// so its page is rendered from a session that browses std. A page whose
// module cannot be rendered from the repository alone is not checked and
// must hold no generated section: std.db.sqlite loads only in a package
// that declares SQLite's C source and C import, and a C import needs clang
// and SQLite's header, which the repository does not hold, so sqlite.md
// writes its API by hand. Public modules with no page are listed with
// t.Log.
//
// Regenerate: go test ./internal/driver -run TestStdPages -update
// (-stdpages <dir> reads and writes a copy of the pages instead).

package driver

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"mk/internal/types"
)

var stdPagesDir = flag.String("stdpages", "", "TestStdPages: the module pages' directory (default ../../doc/std)")

const (
	apiBegin = "<!-- api:begin -->"
	apiEnd   = "<!-- api:end -->"
)

// stdPageTargets are the modules rendered for a target other than
// x86_64-linux: std.free exists only there, and std.gpu and std.window
// import system views mk carries for x86_64-windows alone.
var stdPageTargets = map[string]string{
	"std.free":   "x86_64-freestanding",
	"std.gpu":    "x86_64-windows",
	"std.window": "x86_64-windows",
}

// stdPagesUnrendered are the modules whose page the test cannot render,
// with why.
var stdPagesUnrendered = map[string]string{
	"std.db.sqlite": "it loads only in a package that declares SQLite's C source and C import, which needs clang and SQLite's header",
}

func TestStdPages(t *testing.T) {
	dir := *stdPagesDir
	if dir == "" {
		dir = filepath.Join("..", "..", "doc", "std")
	}
	paths, err := filepath.Glob(filepath.Join(dir, "*.md"))
	// README.md is the library guide, not a module page.
	paths = slices.DeleteFunc(paths, func(p string) bool { return filepath.Base(p) == "README.md" })
	if err != nil || len(paths) == 0 {
		t.Fatalf("no module pages in %s: %v", dir, err)
	}
	slices.Sort(paths)
	sessions := map[string]*Session{}
	session := func(target string, browse bool) *Session {
		key := target
		if browse {
			key += "+browse"
		}
		if s := sessions[key]; s != nil {
			return s
		}
		tg, ok := types.TargetByName(target)
		if !ok {
			t.Fatalf("unknown target %s", target)
		}
		cfg := Config{Root: testPkg(t, map[string]string{"main.mk": "export Main() {\n}\n"}), CacheDir: t.TempDir(), Target: tg, BrowseStd: browse}
		cfg.TestHooks.NoRunner = true
		s, err := NewSession(cfg)
		if err != nil {
			t.Fatal(err)
		}
		sessions[key] = s
		return s
	}
	render := func(mod string) (*DocPackage, string) {
		target := "x86_64-linux"
		if tg, ok := stdPageTargets[mod]; ok {
			target = tg
		}
		dp, res, err := session(target, DocStdBrowse(mod)).Doc(DocOptions{Std: mod})
		if err != nil || dp == nil {
			t.Fatalf("mk doc --std %s (%s): %v\n%s", mod, target, err, res.Report.Text(res.Sources))
		}
		var b strings.Builder
		if err := dp.WriteEmbed(&b); err != nil {
			t.Fatal(err)
		}
		return dp, b.String()
	}
	var stale, titles []string
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		name := filepath.ToSlash(p)
		if *stdPagesDir == "" {
			name = "doc/std/" + filepath.Base(p)
		}
		first, _, _ := strings.Cut(text, "\n")
		// The title's first word names the module; a colon and the words
		// after it are the page's own ("# std.core: the prelude").
		title, ok := strings.CutPrefix(strings.TrimSpace(first), "# ")
		mod, _, _ := strings.Cut(title, " ")
		mod = strings.TrimSuffix(mod, ":")
		if !ok || !strings.HasPrefix(mod, "std.") || strings.ContainsAny(mod, "`") {
			t.Errorf("%s: the first line %q is not a `# std.x` title", name, first)
			continue
		}
		titles = append(titles, mod)
		if why, ok := stdPagesUnrendered[mod]; ok {
			if strings.Contains(text, apiBegin) {
				t.Errorf("%s: holds an %s section, which nothing fills: %s; write the API by hand and drop the markers", name, apiBegin, why)
			} else {
				t.Logf("%s: not checked: %s", name, why)
			}
			continue
		}
		b := strings.Index(text, apiBegin)
		e := strings.Index(text, apiEnd)
		if b < 0 || e < b {
			t.Errorf("%s: no %s ... %s section", name, apiBegin, apiEnd)
			continue
		}
		dp, api := render(mod)
		if len(dp.Modules) == 0 || dp.Modules[0].Path != mod {
			t.Errorf("%s: mk doc finds no module %s", name, mod)
			continue
		}
		want := text[:b] + apiBegin + "\n" + api + "\n" + text[e:]
		if want == text {
			continue
		}
		if *update {
			if err := os.WriteFile(p, []byte(want), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Logf("%s: API section rewritten", name)
			continue
		}
		stale = append(stale, name)
	}
	if len(stale) > 0 {
		what := fmt.Sprintf("%d module pages hold an API section", len(stale))
		if len(stale) == 1 {
			what = "1 module page holds an API section"
		}
		cmd := "go test ./internal/driver -run TestStdPages -update"
		if *stdPagesDir != "" {
			cmd += " -stdpages " + *stdPagesDir
		}
		t.Errorf("%s mk doc no longer writes:\n  %s\nrun: %s", what, strings.Join(stale, "\n  "), cmd)
	}
	// Public modules no page documents (the title's module or one under it).
	all, _ := render("std")
	free, _ := render("std.free")
	var missing []string
	for _, m := range append(all.Modules, free.Modules...) {
		covered := slices.ContainsFunc(titles, func(title string) bool { return m.Path == title || strings.HasPrefix(m.Path, title+".") })
		if !covered && !slices.Contains(missing, m.Path) {
			missing = append(missing, m.Path)
		}
	}
	if len(missing) > 0 {
		t.Logf("public modules with no page in %s: %s", dir, strings.Join(missing, " "))
	}
}
