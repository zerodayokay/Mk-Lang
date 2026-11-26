// Why: editors and coding agents need short, exact answers about a package
// (a signature, a type, the panic sites) without parsing compiler output.
//
// What: runQuery, the `mk query <query> [args] [--json] [--root path]`
// command, and queryMember for `mk query panics --member name` and
// `mk query sig --member name`.
//
// How: one process start, one Check of the package (a warm run is
// a cache hit), one answer capped near 200 tokens. Exit 0 with an answer
// (one from declarations alone, when the package does not check as far as
// types, ends with a "(partial: ...)" line), 1 when the subject is not found
// or there is nothing to answer from (its diagnostics on stderr), 2 on a
// usage error. In a package that does not parse, find and members answer
// from std alone and say so.

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"mk/internal/driver"
)

func init() {
	register(command{"query", "query   <query> [args] [--json] [--root path]   ask the checked package", runQuery})
}

func runQuery(args []string, stdout, stderr io.Writer) int {
	var cf compileFlags
	fs := newFlagSet("query", stderr, &cf)
	root := fs.String("root", "", "the package root, a directory or a .mk file (default: the working directory)")
	member := fs.String("member", "", "panics and sig only: every panic site, or every signature, of the named workspace member, never capped")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: mk query <query> [args] [--json] [--root path]; queries:")
		for _, q := range driver.QueryNames {
			fmt.Fprintln(stderr, "  "+q.Help)
		}
		fmt.Fprintln(stderr, "  panics --member name: every panic site of a member's modules, located, never capped")
		fmt.Fprintln(stderr, "  sig --member name: every function, method and other item of a member's modules as sig Name answers it, never capped")
	}
	var pos []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return usageErr(stderr, fs, err)
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(pos) == 0 {
		fs.Usage()
		return 2
	}
	cfg, ok := cf.config(*root, stderr)
	if !ok {
		return 2
	}
	cfg.GenView = false // a query writes nothing
	if *member != "" {
		if len(pos) != 1 || pos[0] != "panics" && pos[0] != "sig" {
			fmt.Fprintln(stderr, "mk query: --member goes with panics or sig alone, without a name: mk query panics|sig --member <name> [--json]")
			return 2
		}
		return queryMember(cfg, pos[0], *member, cf.json, stdout, stderr)
	}
	s, err := driver.NewSession(cfg)
	var ue *driver.UsageError
	if err != nil && *root == "" && errors.As(err, &ue) && ue.NoSources {
		// No package here: answer std's names over a one-file root of
		// its own.
		var done func()
		s, done, err = stdSession(cfg)
		defer done()
	}
	if err != nil {
		fmt.Fprintf(stderr, "mk query: %v\n", err)
		return 2
	}
	ans, res, err := s.Query(pos[0], pos[1:])
	if err != nil {
		fmt.Fprintf(stderr, "mk query: %v\n", err)
		return 2
	}
	if ans == nil && (pos[0] == "find" || pos[0] == "members") && res.ParseFailed() {
		// The package does not parse: std's names still answer, from a
		// std-only session, with a note that the package's are missing.
		if ss, done, serr := stdSession(cfg); serr == nil {
			defer done()
			if a, _, qerr := ss.Query(pos[0], pos[1:]); qerr == nil && a != nil {
				_ = res.WriteReport(stderr, false)
				a.StdOnly = true
				ans = a
			}
		}
	}
	if ans == nil {
		_ = res.WriteReport(stderr, false)
		fmt.Fprintf(stderr, "mk query: the package does not check far enough to answer %s\n", strings.Join(pos, " "))
		return 1
	}
	if cf.json {
		_, err = stdout.Write(ans.JSON())
	} else {
		_, err = io.WriteString(stdout, ans.Text())
	}
	if err != nil {
		return 2
	}
	if !ans.Found {
		return 1
	}
	return 0
}

// stdSession is a session over a one-file root of its own, in a temporary
// directory that done removes, so std's names answer without a package.
func stdSession(cfg driver.Config) (*driver.Session, func(), error) {
	dir, err := os.MkdirTemp("", "mk-query-")
	if err != nil {
		return nil, func() {}, err
	}
	done := func() { os.RemoveAll(dir) }
	cfg.Root = filepath.Join(dir, "main.mk")
	if err = os.WriteFile(cfg.Root, []byte("export Main(sys: &System) {\n}\n"), 0o644); err != nil {
		return nil, done, err
	}
	s, err := driver.NewSession(cfg)
	return s, done, err
}

// memberListing is a member-wide listing: driver.PanicSiteList or
// driver.SigList.
type memberListing interface {
	JSON() []byte
	Text() string
}

// queryMember is `mk query panics --member name` and `mk query sig
// --member name`: every panic site, or every item's sig answer, of the
// member's modules, complete, as text or as the listing's version 1 JSON
// document. Exit 0 with the listing, 1 when the member does not check
// cleanly (the listing, when there is one, still printed; the diagnostics
// on stderr), 2 on a usage error.
func queryMember(cfg driver.Config, q, member string, asJSON bool, stdout, stderr io.Writer) int {
	dir, name, err := driver.MemberDir(cfg, member)
	if err != nil {
		fmt.Fprintf(stderr, "mk query: %v\n", err)
		return 2
	}
	cfg.Root = dir
	s, err := driver.NewSession(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "mk query: %v\n", err)
		return 2
	}
	var l memberListing // nil, not a typed nil, when there is no listing
	var res driver.Result
	what := "its panic sites"
	if q == "sig" {
		what = "its signatures"
		var sl *driver.SigList
		if sl, res = s.Signatures(name); sl != nil {
			l = sl
		}
	} else {
		var pl *driver.PanicSiteList
		if pl, res = s.PanicSites(name); pl != nil {
			l = pl
		}
	}
	if l != nil {
		if asJSON {
			_, err = stdout.Write(l.JSON())
		} else {
			_, err = io.WriteString(stdout, l.Text())
		}
		if err != nil {
			return 2
		}
	}
	if len(res.Report.Diags) > 0 || l == nil {
		_ = res.WriteReport(stderr, false)
		if l == nil {
			fmt.Fprintf(stderr, "mk query: member %s does not check far enough to list %s\n", name, what)
		}
		return 1
	}
	return 0
}
