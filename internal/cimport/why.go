// Why: when a C declaration does not appear in a binding under its C name,
// users need a direct answer: whether it was bound under a recased Mk name,
// merged with its tag, skipped with a reason, or a function-like macro without
// a prototype.
//
// What: ExplainWhy, which answers `mk cimport --why <CName>` for one C import
// using its generated module text, skipped declaration list, function-like
// macro list and merged typedef table.
//
// How: checks skipped declarations first, then merged typedefs, then scans the
// module's `// C:` annotations and `export` lines, then un-prototyped
// function-like macros.

package cimport

import (
	"fmt"
	"slices"
	"strings"
)

// ExplainWhy explains what happened to C name cname in import impName, returning
// (explanation, true) when cname is accounted for by that import, else ("", false).
func ExplainWhy(impName, modText string, skipped []SkippedDecl, funcLike []string, merged map[string]string, cname string) (string, bool) {
	var memberSkips []string
	for _, s := range skipped {
		if s.Name == cname {
			return fmt.Sprintf("c.%s: %s (%s) was skipped: %s", impName, cname, s.Kind, s.Reason), true
		}
		if s.Kind == "member" && strings.HasSuffix(s.Name, "."+cname) {
			memberSkips = append(memberSkips, fmt.Sprintf("c.%s: %s (member) was skipped: %s", impName, s.Name, s.Reason))
		}
	}
	if exp, ok := findBoundInMod(impName, modText, cname); ok {
		return exp, true
	}
	if mk, ok := merged[cname]; ok && mk != "" {
		return fmt.Sprintf("c.%s: %s (typedef) was merged into %s", impName, cname, mk), true
	}
	if slices.Contains(funcLike, cname) {
		return fmt.Sprintf("c.%s: %s is a function-like macro with no prototype in pkg.mk macros:", impName, cname), true
	}
	if len(memberSkips) > 0 {
		return strings.Join(memberSkips, "\n"), true
	}
	return "", false
}

func findBoundInMod(impName, modText, cname string) (string, bool) {
	if modText == "" {
		return "", false
	}
	lines := strings.Split(modText, "\n")
	for i := 0; i < len(lines); i++ {
		cLine, ok := strings.CutPrefix(lines[i], "// C: ")
		if !ok {
			continue
		}
		if !cCommentMatches(cLine, cname) {
			// Also check if the export line directly names extern cname(...)
			exp := nextExportLine(lines, i+1)
			if !strings.HasPrefix(exp, "export extern "+cname+"(") && !strings.HasPrefix(exp, "export extern var "+cname+":") {
				continue
			}
		}
		exp := nextExportLine(lines, i+1)
		if exp == "" {
			continue
		}
		exp = strings.TrimSuffix(strings.TrimSpace(exp), " {")
		note := ""
		if strings.Contains(cLine, "(incomplete:") || strings.Contains(cLine, "Clang gave no layout") || strings.Contains(cLine, "alignment ") && strings.Contains(cLine, "only pointers") {
			note = " (pointer-only)"
		} else if i+1 < len(lines) && strings.Contains(lines[i+1], "static inline: defined by the shim") {
			note = " (static inline via shim)"
		} else if strings.Contains(cLine, "defined by the shim") {
			note = " (declared macro via shim)"
		}
		return fmt.Sprintf("c.%s: %s is bound as `%s`%s", impName, cname, exp, note), true
	}
	return "", false
}

func nextExportLine(lines []string, from int) string {
	for i := from; i < len(lines); i++ {
		l := lines[i]
		if l == "" {
			return ""
		}
		if strings.HasPrefix(l, "export ") {
			return l
		}
	}
	return ""
}

func cCommentMatches(cLine, cname string) bool {
	switch {
	case cLine == cname:
		return true
	case strings.HasPrefix(cLine, "#define "+cname+" "), strings.HasPrefix(cLine, "#define "+cname+"("):
		return true
	case cLine == "const "+cname || cLine == "extern "+cname:
		return true
	case cLine == "enum "+cname:
		return true
	case strings.HasPrefix(cLine, "struct "+cname+",") || strings.HasPrefix(cLine, "struct "+cname+" ") || strings.HasPrefix(cLine, "struct "+cname+":"):
		return true
	case strings.HasPrefix(cLine, "union "+cname+",") || strings.HasPrefix(cLine, "union "+cname+" ") || strings.HasPrefix(cLine, "union "+cname+":"):
		return true
	case strings.HasPrefix(cLine, "typedef "):
		return cLine == "typedef "+cname || strings.HasSuffix(cLine, " "+cname) || strings.HasSuffix(cLine, "*"+cname)
	}
	return false
}
