// Why: a C import is translated silently during a build; a user binding a C
// library needs to see what the translation produced and what it skipped.
//
// What: runCImport, the `mk cimport [flags] [path]` command, and
// writeCImport, which prints one block per import.
//
// How: the package is checked as mk check does, which translates its C
// imports; each block names the view gen/<target>/c/<name>.mk, the share of
// the headers' declarations bound with no glue, and the skipped ones by
// reason. Exit 0, 1 (diagnostics, printed as mk check prints them), 2 (usage
// or I/O), 3 (internal error).

package main

import (
	"fmt"
	"io"
	"sort"

	"mk/internal/driver"
)

func init() {
	register(command{"cimport", "cimport [flags] [path]         translate the package's C imports and print what each binds", runCImport})
}

func runCImport(args []string, stdout, stderr io.Writer) int {
	var cf compileFlags
	fs := newFlagSet("cimport", stderr, &cf)
	skipped := fs.Bool("skipped", false, "list every skipped C declaration and why")
	why := fs.String("why", "", "explain what happened to one C name")
	path, _, err := parseArgs(fs, args, false)
	if err != nil {
		return usageErr(stderr, fs, err)
	}
	if *skipped && *why != "" {
		fmt.Fprintln(stderr, "mk cimport: --skipped and --why cannot be used together")
		return 2
	}
	cfg, ok := cf.config(path, stderr)
	if !ok {
		return 2
	}
	s, err := driver.NewSession(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "mk cimport: %v\n", err)
		return 2
	}
	res := s.Check()
	if len(res.Report.Diags) > 0 {
		if err := res.WriteReport(stderr, cf.json); err != nil {
			fmt.Fprintf(stderr, "mk cimport: %v\n", err)
			return 2
		}
		return driver.ExitStatus(res.Report)
	}
	infos := s.CImports()
	if len(infos) == 0 {
		fmt.Fprintln(stdout, "no C imports")
		return 0
	}
	if *why != "" {
		exps := s.CImportWhy(*why)
		if len(exps) == 0 {
			fmt.Fprintf(stdout, "%s: not found in the package's C imports\n", *why)
			return 0
		}
		for _, e := range exps {
			fmt.Fprintln(stdout, e)
		}
		return 0
	}
	for _, c := range infos {
		writeCImport(stdout, c, *skipped)
	}
	return 0
}

// writeCImport prints one import's block.
func writeCImport(w io.Writer, c driver.CImportInfo, showSkipped bool) {
	bound := c.Direct + c.Generated
	share := 0.0
	if c.Decls > 0 {
		share = 100 * float64(bound) / float64(c.Decls)
	}
	hit := ""
	if c.CacheHit {
		hit = " (cached)"
	}
	fmt.Fprintf(w, "c.%s for %s%s\n  view     %s\n  bound    %d of %d declarations (%.1f%%): %d direct, %d through accessors or shims\n",
		c.Name, c.Target, hit, c.View, bound, c.Decls, share, c.Direct, c.Generated)
	if showSkipped {
		items := append([]driver.SkippedCDecl(nil), c.SkippedDecls...)
		sort.Slice(items, func(i, j int) bool {
			if items[i].Reason != items[j].Reason {
				return items[i].Reason < items[j].Reason
			}
			if items[i].Kind != items[j].Kind {
				return items[i].Kind < items[j].Kind
			}
			return items[i].Name < items[j].Name
		})
		for _, d := range items {
			fmt.Fprintf(w, "  skipped  %-7s  %-32s  %s\n", d.Kind, d.Name, d.Reason)
		}
		return
	}
	reasons := make([]string, 0, len(c.Skipped))
	for r := range c.Skipped {
		reasons = append(reasons, r)
	}
	sort.Slice(reasons, func(i, j int) bool {
		if c.Skipped[reasons[i]] != c.Skipped[reasons[j]] {
			return c.Skipped[reasons[i]] > c.Skipped[reasons[j]]
		}
		return reasons[i] < reasons[j]
	})
	for _, r := range reasons {
		fmt.Fprintf(w, "  skipped  %6d  %s\n", c.Skipped[r], r)
	}
}
