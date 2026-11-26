// Why: a system view shipped with mk is the importer's output made durable:
// the archive must hold exactly the blocks prune keeps and drops, with the
// references prune follows, or a build from the archive would see other
// items than a build from the headers.
//
// What: ViewItems, which cuts the bindings of one translation unit into
// sysview items (one per prune block) with their dependencies resolved to
// item indexes, the views each binding requires, and the externs only the
// shim defines (which a build with no C cannot have), marked to drop.
//
// How: the same cut as prune (blocks of non-empty lines after the head,
// names from defName, identifiers from scanIdents). A plain identifier
// that some block defines refers to every block defining it, exactly; a
// qualified a.N whose a is an earlier binding's import refers to the blocks
// of that binding whose names fold to N's, as prune's want does. A
// qualified raw.N marks the item as needing std.raw.

package cimport

import (
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"slices"
	"strings"

	"mk/internal/sysview"
)

// ImporterFormat names the mapping from C to Mk this importer writes (the
// cache's format): an input of every generated system view.
const ImporterFormat = formatVersion

// ToolsetHeadersHash is the hash of mk's MSVC toolset headers (names and
// texts), which the names of a Windows SDK view depend on: an input of
// every Windows system view.
func ToolsetHeadersHash() string {
	h := sha256.New()
	files := MicrosoftHeaders()
	names := slices.Sorted(maps.Keys(files))
	for _, n := range names {
		h.Write([]byte(n + "\x00" + files[n] + "\x00"))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// ViewItems returns, for each binding (texts in import order, names[i] the
// import name of texts[i]), its items and required views, and the items to
// drop by view name and index with the reason: the externs shims lists
// (static inline functions and declared macros, defined only by a shim
// compiled from the headers).
func ViewItems(names, texts []string, shims []ShimFunc) ([]sysview.View, map[string]map[int]string) {
	type cut struct {
		blocks []*block
		texts  []string
		raws   []bool
		alias  map[string]string // import alias to binding name
		exact  map[string][]int
		folded map[string][]int
	}
	cuts := make([]*cut, len(texts))
	pos := map[string]int{}
	for i, n := range names {
		pos[n] = i
	}
	for i, text := range texts {
		c := &cut{alias: map[string]string{}, exact: map[string][]int{}, folded: map[string][]int{}}
		cuts[i] = c
		lines := strings.Split(text, "\n")
		head := len(lines)
		for k, l := range lines {
			if strings.HasPrefix(l, "// C:") {
				head = k
				for head > 0 && lines[head-1] != "" {
					head--
				}
				break
			}
		}
		for _, l := range lines[:head] {
			if p, ok := strings.CutPrefix(l, "import c."); ok {
				if j, ok := pos[p]; ok && j < i {
					c.alias[p] = p
				}
			}
		}
		for k := head; k < len(lines); {
			if lines[k] == "" {
				k++
				continue
			}
			b := &block{lo: k}
			for k < len(lines) && lines[k] != "" {
				k++
			}
			b.hi = k
			for _, l := range lines[b.lo:b.hi] {
				if n := defName(l); n != "" {
					b.defs = append(b.defs, n)
				}
			}
			if len(b.defs) == 0 {
				continue // a comment block defines nothing a build can name
			}
			idx := len(c.blocks)
			c.blocks = append(c.blocks, b)
			c.texts = append(c.texts, strings.Join(lines[b.lo:b.hi], "\n"))
			seen := map[string]bool{}
			for _, d := range b.defs {
				if !seen[d] {
					seen[d] = true
					c.exact[d] = append(c.exact[d], idx)
				}
				if f := fold(d); !seen["\x00"+f] {
					seen["\x00"+f] = true
					c.folded[f] = append(c.folded[f], idx)
				}
			}
		}
		c.raws = make([]bool, len(c.blocks))
		for k, b := range c.blocks {
			seen := map[string]bool{}
			for _, l := range lines[b.lo:b.hi] {
				scanIdents(l, func(id, qual string) {
					if qual != "" {
						if id == "raw" {
							c.raws[k] = true
						} else if _, ok := c.alias[id]; ok {
							b.quals = append(b.quals, qualRef{id, qual})
						}
						return
					}
					if !seen[id] && len(c.exact[id]) > 0 {
						seen[id] = true
						b.refs = append(b.refs, id)
					}
				})
			}
		}
	}
	views := make([]sysview.View, len(texts))
	drop := map[string]map[int]string{}
	for i, c := range cuts {
		// The required views, in import order, are the earlier bindings a
		// block of this one names.
		usedReq := map[string]bool{}
		for _, b := range c.blocks {
			for _, q := range b.quals {
				usedReq[q.mod] = true
			}
		}
		var req []string
		for _, n := range names[:i] {
			if usedReq[n] {
				req = append(req, n)
			}
		}
		reqIdx := map[string]int{}
		for k, r := range req {
			reqIdx[r] = k + 1
		}
		v := sysview.View{Meta: sysview.Meta{Name: names[i], Requires: req}}
		for k, b := range c.blocks {
			it := sysview.Item{Name: b.defs[0], Kind: itemKind(strings.Split(c.texts[k], "\n")), C: sysview.CText(c.texts[k]),
				Defs: b.defs, Text: c.texts[k], Raw: c.raws[k]}
			seen := map[sysview.Ref]bool{}
			add := func(r sysview.Ref) {
				if !seen[r] && !(r.View == 0 && r.Item == k) {
					seen[r] = true
					it.Deps = append(it.Deps, r)
				}
			}
			// A name refers to every block defining it. The reference goes
			// to the first; the first of each name it defines lists the
			// others (below), so a type's many accessor blocks cost one
			// reference per user, not one per accessor.
			for _, r := range b.refs {
				add(sysview.Ref{View: 0, Item: c.exact[r][0]})
			}
			for _, d := range b.defs {
				if ds := c.exact[d]; ds[0] == k {
					for _, j := range ds[1:] {
						add(sysview.Ref{View: 0, Item: j})
					}
				}
			}
			for _, q := range b.quals {
				o := cuts[pos[q.mod]]
				for _, j := range o.folded[fold(q.name)] {
					// The first definer of each of j's names stands for j.
					for _, d := range o.blocks[j].defs {
						if fold(d) == fold(q.name) {
							add(sysview.Ref{View: reqIdx[q.mod], Item: o.exact[d][0]})
						}
					}
				}
			}
			v.Items = append(v.Items, it)
		}
		views[i] = v
	}
	for _, s := range shims {
		if s.Import < 0 || s.Import >= len(cuts) {
			continue
		}
		for _, j := range cuts[s.Import].exact[s.Name] {
			if views[s.Import].Items[j].Kind != "func" {
				continue
			}
			d := drop[names[s.Import]]
			if d == nil {
				d = map[int]string{}
				drop[names[s.Import]] = d
			}
			reason := "a declared macro, defined only by a shim compiled from the headers"
			if s.Inline {
				reason = "a static inline function, defined only by a shim compiled from the headers"
			}
			d[j] = reason
		}
	}
	return views, drop
}

// itemKind is the kind of the first export line of a block's lines.
func itemKind(lines []string) string {
	for _, l := range lines {
		rest, ok := strings.CutPrefix(l, "export ")
		if !ok {
			continue
		}
		switch {
		case strings.HasPrefix(rest, "extern var "):
			return "var"
		case strings.HasPrefix(rest, "extern "):
			return "func"
		case strings.HasPrefix(rest, "const "):
			return "const"
		case strings.HasPrefix(rest, "type "):
			return "type"
		case strings.HasPrefix(rest, "struct "), strings.HasPrefix(rest, "packed"), strings.HasPrefix(rest, "align"):
			return "struct"
		}
		return "method"
	}
	return ""
}
