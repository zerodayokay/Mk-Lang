// Why: borrow's own-method view scan (viewKeepsScan in internal/borrow
// typewalk.go) judges a body by the methods of the view types it uses, so a
// cached body of another module must be invalidated when such a method is
// added or removed, without missing on every unrelated member edit.
//
// What: ModuleKeepsKey and ModuleKeepsPath (the read's key), keepsNS (a
// module's keeping view methods as one hashed part) and declaresKeep.
//
// How: keepsNS lists view methods that declare a store into self, a keep
// or a hand-out, and non-generic free constructors that return a local view from a
// parameter, private ones included, by key and declaration hash.

package iface

import (
	"encoding/hex"
	"strings"

	"mk/internal/types"
)

// ModuleKeepsKey is the Key of a ReadExports read of module path's view
// methods that borrow's own-method scan may count (keepsNS): path followed
// by "#keeps". No module path contains '#', and no other key ends with it,
// so it is taken for no module or type namespace read, nor for a
// ModuleFuncsKey.
func ModuleKeepsKey(path string) types.Key { return types.KeyFromString(path + keepsSuffix) }

// ModuleKeepsPath is the module path of a ModuleKeepsKey, and false for any
// other key.
func ModuleKeepsPath(k types.Key) (string, bool) { return strings.CutSuffix(k.String(), keepsSuffix) }

const keepsSuffix = "#keeps"

// keepsNS is module m's keeping view methods, memoized per world: every
// Type.Name method of a view struct of m whose signature declaresKeep and
// each non-generic free constructor with a borrowed result of such a view, by its
// key and the hash of its declaration, private ones included. A method added,
// removed or changed there changes the hash; a method that declares none of
// them does not, so it costs no reader a miss (the type namespace, which lists
// every member, would make every warm build of std miss). It is the same for
// every reader: a foreign body's scan counts the private methods too, as the
// view's own module does.
func (w *SessionWorld) keepsNS(m types.ModuleID) *namespace {
	w.mu.Lock()
	n, ok := w.keeps[m]
	w.mu.Unlock()
	if ok {
		return n
	}
	items := w.Env.Items
	var ns []nameEntry
	for _, id := range w.H.moduleItems(m) {
		it := items.Get(id)
		if it.Kind != types.KindFunc {
			continue
		}
		fd, ok := it.Decl.(*types.FuncDecl)
		if !ok || fd == nil || !declaresKeep(&fd.Sig) {
			continue
		}
		ownerID := it.Owner
		if ownerID == types.NoItem {
			ownerID = it.BorrowedConstructorResult(w.Env.Types)
			if ownerID == types.NoItem {
				continue
			}
		}
		owner := items.Get(ownerID)
		sd, isStruct := owner.Decl.(*types.StructDecl)
		if owner.Module != m || !isStruct || sd == nil || !sd.View {
			continue
		}
		h := w.H.Decl(it)
		ns = append(ns, nameEntry{name: it.Key.String() + "\x00" + hex.EncodeToString(h[:]), kind: uint8(it.Kind), exported: it.Exported})
	}
	n = newNamespace(ns)
	w.mu.Lock()
	if w.keeps == nil {
		w.keeps = make(map[types.ModuleID]*namespace)
	}
	w.keeps[m] = n
	w.mu.Unlock()
	return n
}

// declaresKeep reports whether a view method's signature declares what
// borrow's keepScan.method may count, judged on the signature's flows alone
// (a superset: keepScan.method also judges the types): with a receiver, a
// parameter stored into self, a store into self, or a source from self (a
// hand-out); without one, any source (a namespace function whose result is
// the view made from a parameter: a keep).
func declaresKeep(sig *types.Signature) bool {
	if sig.Receiver == types.NoReceiver {
		return len(sig.Flows.Sources) > 0
	}
	for i, p := range sig.Params {
		if i > 0 && p.Into == 1 {
			return true
		}
	}
	for _, st := range sig.Flows.Stores {
		if st.Into == 1 {
			return true
		}
	}
	for _, src := range sig.Flows.Sources {
		if src.Param == 1 {
			return true
		}
	}
	return false
}
