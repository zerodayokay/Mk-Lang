// Why: when a name is not found (E0201), the error suggests the import that
// would bring it in, and that answer must not depend on which module
// finished resolving first.
//
// What: catalog, the driver's resolve.Catalog: the exported top-level names
// of every module this build can load, with add, ExportedBy and
// exportedName.
//
// How: it is built from the parsed files after the import step, before
// stage 3 starts, so its answers are fixed before any module resolves.

package driver

import (
	"slices"
	"sync"

	"mk/internal/ast"
	"mk/internal/source"
)

// catalog answers resolve.Catalog. Std-internal modules are never offered,
// and corpus units are never added: a unit's suggestions name only std, so
// concurrent units cannot see each other.
type catalog struct {
	in       *source.Interner
	mu       sync.RWMutex
	exports  map[string][]string // name to module paths, sorted
	searched []string            // package module paths, sorted
}

// add records the exported top-level names of a build's modules.
func (c *catalog) add(b *build) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.exports == nil {
		c.exports = map[string][]string{}
	}
	for _, m := range b.mods {
		if m.std && isStdInternal(m.name) || m.sysview {
			continue
		}
		if !m.std {
			c.searched = append(c.searched, m.path)
		}
		for _, f := range m.files {
			if f.sum == nil {
				continue
			}
			for _, n := range f.sum.Exports {
				if !slices.Contains(c.exports[n], m.path) {
					c.exports[n] = append(c.exports[n], m.path)
					slices.Sort(c.exports[n])
				}
			}
		}
	}
	slices.Sort(c.searched)
}

// ExportedBy implements resolve.Catalog.
func (c *catalog) ExportedBy(name string) (paths []string, searched []string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return slices.Clone(c.exports[name]), slices.Clone(c.searched)
}

// exportedName is the name of an exported top-level item, "" otherwise.
func exportedName(it ast.Item, in *source.Interner) string {
	var id *ast.Ident
	switch x := it.(type) {
	case *ast.StructDecl:
		if x.Export {
			id = x.Name
		}
	case *ast.EnumDecl:
		if x.Export {
			id = x.Name
		}
	case *ast.ErrorDecl:
		if x.Export {
			id = x.Name
		}
	case *ast.InterfaceDecl:
		if x.Export {
			id = x.Name
		}
	case *ast.AliasDecl:
		if x.Export {
			id = x.Name
		}
	case *ast.ConstDecl:
		if x.Export {
			id = x.Name
		}
	case *ast.GlobalDecl:
		if x.Export {
			id = x.Name
		}
	case *ast.FuncDecl:
		if x.Export && x.Sig != nil && x.Sig.Name != nil && x.Sig.Name.Owner == nil {
			id = x.Sig.Name.Name
		}
	case *ast.ExternDecl:
		if x.Export && x.Sig != nil && x.Sig.Name != nil && x.Sig.Name.Owner == nil {
			id = x.Sig.Name.Name
		}
	}
	if id == nil {
		return ""
	}
	return in.Str(id.Name)
}
