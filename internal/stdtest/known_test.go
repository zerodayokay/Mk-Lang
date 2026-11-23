// Why: the compiler names some std items itself (types.KnownItems), so each
// must resolve to the std item the language rules mean.
// What: TestStdKnownItemsResolve over the knownWant table.
// How: resolves std.core and its imports from the live std and compares every
// KnownItems field with knownWant, which must also cover every field.

package stdtest

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"mk/internal/ast"
	"mk/internal/resolve"
	"mk/internal/source"
	"mk/internal/syntax"
	"mk/internal/types"
	"mk/std"
)

// knownWant is every KnownItems field and the key of the std item it must
// name, written from the library documentation (doc/), not from resolve's
// own table.
var knownWant = []struct {
	field string
	get   func(*types.KnownItems) types.ItemID
	key   string
}{
	{"Arena", func(k *types.KnownItems) types.ItemID { return k.Arena }, "std.core.Arena"},
	{"Alloc", func(k *types.KnownItems) types.ItemID { return k.Alloc }, "std.core.Alloc"},
	{"Heap", func(k *types.KnownItems) types.ItemID { return k.Heap }, "std.core.Heap"},
	{"Cell", func(k *types.KnownItems) types.ItemID { return k.Cell }, "std.core.Cell"},
	{"Scope", func(k *types.KnownItems) types.ItemID { return k.Scope }, "std.core.Scope"},
	{"Io", func(k *types.KnownItems) types.ItemID { return k.Io }, "std.core.Io"},
	{"System", func(k *types.KnownItems) types.ItemID { return k.System }, "std.core.System"},
	{"Slice", func(k *types.KnownItems) types.ItemID { return k.Slice }, "std.core.Slice"},
	{"Format", func(k *types.KnownItems) types.ItemID { return k.Format }, "std.core.Format"},
	{"Eq", func(k *types.KnownItems) types.ItemID { return k.Eq }, "std.core.Eq"},
	{"Ord", func(k *types.KnownItems) types.ItemID { return k.Ord }, "std.core.Ord"},
	{"Hash", func(k *types.KnownItems) types.ItemID { return k.Hash }, "std.core.Hash"},
	{"Writer", func(k *types.KnownItems) types.ItemID { return k.Writer }, "std.core.Writer"},
	{"Mutex", func(k *types.KnownItems) types.ItemID { return k.Mutex }, "std.core.Mutex"},
	{"Chan", func(k *types.KnownItems) types.ItemID { return k.Chan }, "std.core.Chan"},
	{"SpinCore", func(k *types.KnownItems) types.ItemID { return k.SpinCore }, "std.core.SpinCore"},
	{"Atomic", func(k *types.KnownItems) types.ItemID { return k.Atomic }, "std.raw.Atomic"},
	{"Box", func(k *types.KnownItems) types.ItemID { return k.Box }, "std.core.Box"},
	{"OutOfMemory", func(k *types.KnownItems) types.ItemID { return k.OutOfMemory }, "std.core.OutOfMemory"},
	{"Canceled", func(k *types.KnownItems) types.ItemID { return k.Canceled }, "std.core.Canceled"},
	{"Closed", func(k *types.KnownItems) types.ItemID { return k.Closed }, "std.core.Closed"},
	{"Gen", func(k *types.KnownItems) types.ItemID { return k.Gen }, "std.core.Gen"},
	{"TypeInfo", func(k *types.KnownItems) types.ItemID { return k.TypeInfo }, "std.core.Type"},
	{"TargetInfo", func(k *types.KnownItems) types.ItemID { return k.TargetInfo }, "std.target.TargetInfo"},
	{"Arch", func(k *types.KnownItems) types.ItemID { return k.Arch }, "std.target.Arch"},
	{"Os", func(k *types.KnownItems) types.ItemID { return k.Os }, "std.target.Os"},
	{"Limits", func(k *types.KnownItems) types.ItemID { return k.Limits }, "std.core.Limits"},
	{"StackSize", func(k *types.KnownItems) types.ItemID { return k.StackSize }, "std.core.StackSize"},
	{"Field", func(k *types.KnownItems) types.ItemID { return k.Field }, "std.core.Field"},
	{"Variant", func(k *types.KnownItems) types.ItemID { return k.Variant }, "std.core.Variant"},
	{"Method", func(k *types.KnownItems) types.ItemID { return k.Method }, "std.core.Method"},
	{"Param", func(k *types.KnownItems) types.ItemID { return k.Param }, "std.core.Param"},
	{"Code", func(k *types.KnownItems) types.ItemID { return k.Code }, "std.core.Code"},
	{"Block", func(k *types.KnownItems) types.ItemID { return k.Block }, "std.core.Block"},
	{"TypeKind", func(k *types.KnownItems) types.ItemID { return k.TypeKind }, "std.core.TypeKind"},
	{"Receiver", func(k *types.KnownItems) types.ItemID { return k.Receiver }, "std.core.Receiver"},
	{"Flow", func(k *types.KnownItems) types.ItemID { return k.Flow }, "std.core.Flow"},
	{"FlowKind", func(k *types.KnownItems) types.ItemID { return k.FlowKind }, "std.core.FlowKind"},
	{"Target", func(k *types.KnownItems) types.ItemID { return k.Target }, "std.target.Target"},
	{"Print", func(k *types.KnownItems) types.ItemID { return k.Print }, "std.core.Print"},
	{"ArenaFmt", func(k *types.KnownItems) types.ItemID { return k.ArenaFmt }, "std.core.Arena:Fmt"},
	{"FmtRun", func(k *types.KnownItems) types.ItemID { return k.FmtRun }, "std.core.FmtRun"},
	{"FmtArena", func(k *types.KnownItems) types.ItemID { return k.FmtArena }, "std.core.FmtArena"},
	{"FmtValue", func(k *types.KnownItems) types.ItemID { return k.FmtValue }, "std.core.FmtValue"},
	{"FmtPiece", func(k *types.KnownItems) types.ItemID { return k.FmtPiece }, "std.core.FmtPiece"},
	{"FmtArg", func(k *types.KnownItems) types.ItemID { return k.FmtArg }, "std.core.FmtArg"},
	{"StrFromC", func(k *types.KnownItems) types.ItemID { return k.StrFromC }, "std.core.str:FromC"},
	// The compiler-known items of std.simd.
	{"Vec", func(k *types.KnownItems) types.ItemID { return k.Vec }, "std.simd.Vec"},
	{"Mask", func(k *types.KnownItems) types.ItemID { return k.Mask }, "std.simd.Mask"},
	{"Lane", func(k *types.KnownItems) types.ItemID { return k.Lane }, "std.simd.Lane"},
	{"CpuFeatures", func(k *types.KnownItems) types.ItemID { return k.CpuFeatures }, "std.simd.CpuFeatures"},
}

// stdModule is one parsed and resolved std module.
type stdModule struct {
	path  string
	id    types.ModuleID
	files []*ast.File
	info  *resolve.Info
}

// stdWorld is std modules parsed and resolved as the driver does for every
// build (std.core is the prelude). resolveStd loads std.raw and std.core;
// neither imports a target-specific module, so what the tests read holds
// for both targets.
type stdWorld struct {
	in    *source.Interner
	sm    *source.SourceMap
	items *types.Items
	rs    *resolve.Session
	mods  []stdModule
}

// modDir is a std module's import path and its directory under std/.
type modDir struct{ path, dir string }

// baseMods are std.core and what it imports, in import order: std.raw,
// std.target (TargetInfo, Arch and Os) and std.simd, which std.core
// calls on its byte paths.
var baseMods = []modDir{{"std.raw", "raw"}, {"std.target", "target"}, {"std.simd", "simd"}, {"std.core", "core"}}

// resolveStd parses and resolves std.core and its imports from the live std.
func resolveStd(t *testing.T) *stdWorld {
	t.Helper()
	return resolveStdMods(t, baseMods)
}

// resolveStdMods parses and resolves mods from the live std, in the order
// given, which must be import order (baseMods first). A module
// may import only modules listed before it.
func resolveStdMods(t *testing.T, mods []modDir) *stdWorld {
	t.Helper()
	root := stdRoot(t)
	w := &stdWorld{in: source.NewInterner(), sm: source.NewSourceMap(), items: types.NewItems()}
	sm := w.sm
	w.rs = resolve.NewSession(&resolve.Config{Interner: w.in, Sources: sm, Items: w.items, PreludePath: std.PreludeModule, PreludeNames: std.PreludeNames[:]})
	ids := map[string]types.ModuleID{}
	for _, m := range mods {
		ents, err := os.ReadDir(filepath.Join(root, m.dir))
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, e := range ents {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".mk") {
				names = append(names, e.Name())
			}
		}
		slices.Sort(names)
		mod := &types.Module{Path: m.path, Std: true, UnsafePermitted: true}
		var files []*ast.File
		for _, n := range names {
			text, err := os.ReadFile(filepath.Join(root, m.dir, n))
			if err != nil {
				t.Fatal(err)
			}
			src := sm.Add("std/"+m.dir+"/"+n, text)
			f, ds := syntax.ParseFile(src, w.in, syntax.Options{AllowBodiless: true})
			for _, d := range ds {
				t.Errorf("%s: parse %v: %s", src.Path, d.Code, d.Message)
			}
			mod.Files = append(mod.Files, src.ID)
			files = append(files, f)
		}
		input := &resolve.ModuleInput{Files: files, Imports: map[string]types.ModuleID{}}
		for _, f := range files {
			for _, il := range resolve.ImportsOf(f, w.in) {
				if dep, ok := ids[il.Path]; ok {
					input.Imports[il.Path] = dep
					if !slices.Contains(mod.Imports, dep) {
						mod.Imports = append(mod.Imports, dep)
					}
				}
			}
		}
		if core, ok := ids[std.PreludeModule]; ok {
			// Every module above std.core sees the prelude, as the driver
			// gives it (resolve.ModuleInput.Prelude).
			input.Prelude = core
			if !slices.Contains(mod.Imports, core) {
				mod.Imports = append(mod.Imports, core)
			}
		}
		id := w.items.AddModule(mod)
		ids[m.path] = id
		input.Module = id
		rm := w.rs.Begin(input)
		info := rm.Finish(1)
		ds, gs := rm.Settle()
		for _, d := range append(append(info.Diags, ds...), gs...) {
			t.Errorf("%s: resolve %v: %s", m.path, d.Code, d.Message)
		}
		w.mods = append(w.mods, stdModule{path: m.path, id: id, files: files, info: info})
	}
	return w
}

// TestStdKnownItemsResolve: every KnownItems field names the std
// item knownWant gives, and knownWant covers every field (a field added to
// the contract without a row here fails the test). std.simd is resolved
// before std.core, which imports it, as the driver resolves the
// prelude's closure first (its four fields).
func TestStdKnownItemsResolve(t *testing.T) {
	w := resolveStdMods(t, baseMods)
	k := w.rs.Known()
	if n := reflect.TypeFor[types.KnownItems]().NumField(); n != len(knownWant) {
		t.Errorf("types.KnownItems has %d fields, knownWant %d rows: add the new field's row", n, len(knownWant))
	}
	for _, row := range knownWant {
		if _, ok := reflect.TypeFor[types.KnownItems]().FieldByName(row.field); !ok {
			t.Errorf("knownWant row %s: no such KnownItems field", row.field)
		}
		id := row.get(&k)
		if row.field == "Target" {
			// The driver adds the const Target to std.target when std declares
			// TargetInfo (internal/driver target.go addTargetItem); std itself
			// must leave the key free (Target is a universe name, E0102).
			if id != types.NoItem {
				t.Errorf("KnownItems.Target: std declares %s itself; the driver makes it", w.items.Get(id).Key)
			}
			if k.TargetInfo == types.NoItem {
				t.Errorf("KnownItems.Target: no TargetInfo for the driver to build it from")
			}
			continue
		}
		if id == types.NoItem {
			t.Errorf("KnownItems.%s: no std item (want %s)", row.field, row.key)
			continue
		}
		if got := w.items.Get(id).Key.String(); got != row.key {
			t.Errorf("KnownItems.%s: %s, want %s", row.field, got, row.key)
		}
	}
}
