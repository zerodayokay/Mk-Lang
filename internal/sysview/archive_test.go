// Why: the archive is committed, embedded and read by every build that
// imports a system view; its bytes must be a function of its content and
// every reader path must refuse bad bytes rather than crash.
//
// What: TestSysview_RoundTrip, _Deterministic, _NewerFormat, _Corrupt,
// _Lookup, _Extract, _ExtractAll, _ExtractOrder and _Drop, over small views
// built here.
//
// How: two fake views (base and gfx, gfx requiring base) written with
// several layouts; extraction is compared with hand-computed results.

package sysview

import (
	"bytes"
	"encoding/binary"
	"slices"
	"strings"
	"testing"
)

// fakeViews is base (a struct with an accessor, a function, an enum) and
// gfx, whose function takes base's struct.
func fakeViews() []View {
	base := View{Meta: Meta{Name: "base", Target: "x86_64-windows", Headers: []string{"base.h"},
		Inputs: map[string]string{"sdk": "1.0"}, Counts: map[string]int{"items": 5}}}
	base.Items = []Item{
		{Name: "Handle", Kind: "type", Defs: []string{"Handle"}, Deps: []Ref{{0, 1}},
			Text: "// C: typedef struct HANDLE__ *HANDLE\nexport type Handle = *HandleStruct"},
		{Name: "HandleStruct", Kind: "struct", Defs: []string{"HandleStruct"}, Deps: []Ref{{0, 2}},
			Text: "// C: struct HANDLE__, 4 bytes, align 4\nexport struct HandleStruct {\n    export unused: i32\n}"},
		{Name: "HandleStruct", Kind: "method", Defs: []string{"HandleStruct"}, Deps: []Ref{{0, 1}}, Raw: true,
			Text: "// C: member unused\nexport HandleStruct.Unused(&self): i32 {\n    return raw.Read(raw.Of(self))\n}"},
		{Name: "CloseHandle", Kind: "func", Defs: []string{"CloseHandle"}, Deps: []Ref{{0, 0}},
			Text: "// C: int CloseHandle(HANDLE)\nexport extern CloseHandle(h: Handle): i32", Link: Link{Lib: "kernel32.dll"}},
		{Name: "Color", Kind: "type", Defs: []string{"Color", "ColorRed", "ColorBlue"},
			Text: "// C: enum COLOR\nexport type Color = i32\n// C: COLOR_RED\nexport const ColorRed: Color = 0\n// C: COLOR_BLUE\nexport const ColorBlue: Color = 1"},
	}
	gfx := View{Meta: Meta{Name: "gfx", Target: "x86_64-windows", Requires: []string{"base"}, Headers: []string{"gfx.h"},
		Inputs: map[string]string{"sdk": "1.0"}}}
	gfx.Items = []Item{
		{Name: "Present", Kind: "func", Defs: []string{"Present"}, Deps: []Ref{{1, 0}},
			Text: "// C: int Present(HANDLE)\nexport extern Present(h: base.Handle): i32", Link: Link{Lib: "gfx.dll", Lazy: true}},
		{Name: "Unused", Kind: "const", Defs: []string{"Unused"}, Text: "// C: UNUSED\nexport const Unused: i32 = 7"},
	}
	return []View{base, gfx}
}

func writeAll(t *testing.T, views []View, o Options) Set {
	t.Helper()
	s := Set{}
	for _, v := range views {
		b, err := Write(v.Meta, v.Items, o)
		if err != nil {
			t.Fatal(err)
		}
		a, err := Open(b)
		if err != nil {
			t.Fatal(err)
		}
		s[v.Meta.Name] = a
	}
	return s
}

var layouts = []Options{{}, {ChunkSize: 1}, {ChunkSize: 64, FlateIndex: true}, DefaultOptions}

func TestSysview_RoundTrip(t *testing.T) {
	for _, o := range layouts {
		views := fakeViews()
		s := writeAll(t, views, o)
		for _, v := range views {
			a := s[v.Meta.Name]
			if a.Len() != len(v.Items) {
				t.Fatalf("%+v: %s has %d items, want %d", o, v.Meta.Name, a.Len(), len(v.Items))
			}
			m := a.Meta()
			if m.Name != v.Meta.Name || !slices.Equal(m.Requires, v.Meta.Requires) || m.Inputs["sdk"] != "1.0" {
				t.Errorf("%+v: meta %+v", o, m)
			}
			id, _ := Identity(v.Meta, v.Items)
			if a.ID() != id {
				t.Errorf("%+v: %s: identity differs from Identity", o, v.Meta.Name)
			}
			for i, want := range v.Items {
				got, err := a.Item(i)
				if err != nil {
					t.Fatal(err)
				}
				want.C = CText(want.Text)
				if got.Name != want.Name || got.Kind != want.Kind || got.C != want.C || got.Text != want.Text ||
					got.Raw != want.Raw || got.Link != want.Link || !slices.Equal(got.Defs, want.Defs) || !slices.Equal(got.Deps, want.Deps) {
					t.Errorf("%+v: %s item %d:\n got %+v\nwant %+v", o, v.Meta.Name, i, got, want)
				}
				if lk, err := a.ItemLink(i); err != nil || lk != want.Link {
					t.Errorf("%+v: %s item %d ItemLink = %+v, %v; want %+v", o, v.Meta.Name, i, lk, err, want.Link)
				}
			}
		}
	}
}

func TestSysview_VersionedLink(t *testing.T) {
	posix := View{Meta: Meta{Name: "posix", Target: "x86_64-linux"}, Items: []Item{
		{Name: "PthreadJoin", Kind: "func", Defs: []string{"PthreadJoin"},
			Link: Link{Lib: "libc.so.6", Version: "GLIBC_2.34"}},
	}}
	a := writeAll(t, []View{posix}, DefaultOptions)["posix"]
	link, err := a.ItemLink(0)
	if err != nil || link != posix.Items[0].Link {
		t.Fatalf("ItemLink = %+v, %v", link, err)
	}
	posix.Items[0].Link.Version = "GLIBC_2.2.5"
	b := writeAll(t, []View{posix}, DefaultOptions)["posix"]
	if a.ID() == b.ID() {
		t.Fatal("archive identity did not include the symbol version")
	}
}

// TestSysview_Deterministic: the same view gives the same bytes, and the
// identity does not depend on the layout.
func TestSysview_Deterministic(t *testing.T) {
	v := fakeViews()[0]
	a, _ := Write(v.Meta, v.Items, DefaultOptions)
	b, _ := Write(fakeViews()[0].Meta, fakeViews()[0].Items, DefaultOptions)
	if !bytes.Equal(a, b) {
		t.Fatal("two writes of one view differ")
	}
	var ids [][32]byte
	for _, o := range layouts {
		d, _ := Write(v.Meta, v.Items, o)
		x, err := Open(d)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, x.ID())
	}
	for _, id := range ids[1:] {
		if id != ids[0] {
			t.Fatal("the identity depends on the layout")
		}
	}
	v.Items[3].Text += " "
	c, _ := Write(v.Meta, v.Items, DefaultOptions)
	x, _ := Open(c)
	if x.ID() == ids[0] {
		t.Fatal("a changed text kept the identity")
	}
}

func TestSysview_NewerFormat(t *testing.T) {
	v := fakeViews()[0]
	b, _ := Write(v.Meta, v.Items, DefaultOptions)
	newer := append([]byte(magic), binary.AppendUvarint(nil, FormatVersion+1)...)
	newer = append(newer, b[len(magic)+1:]...)
	_, err := Open(newer)
	if err == nil || !strings.Contains(err.Error(), "newer than this mk reads") {
		t.Fatalf("newer format: %v", err)
	}
	if _, err := Open([]byte("PK\x03\x04")); err == nil || !strings.Contains(err.Error(), "not a system view archive") {
		t.Fatalf("not an archive: %v", err)
	}
}

// TestSysview_Corrupt: every truncation and every one-byte change of an
// archive opens with an error or reads with an error, never a panic.
func TestSysview_Corrupt(t *testing.T) {
	v := fakeViews()[0]
	b, _ := Write(v.Meta, v.Items, Options{ChunkSize: 40})
	try := func(d []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic on %d bytes: %v", len(d), r)
			}
		}()
		a, err := Open(d)
		if err != nil {
			return
		}
		for i := 0; i < a.Len(); i++ {
			_, _ = a.Item(i)
		}
		_ = a.Lookup("CloseHandle")
		_, _ = Extract(Set{"base": a}, nil, true)
	}
	for n := 0; n < len(b); n++ {
		try(b[:n])
	}
	for i := range b {
		d := slices.Clone(b)
		d[i] ^= 0x5a
		try(d)
	}
}

func TestSysview_Lookup(t *testing.T) {
	s := writeAll(t, fakeViews(), DefaultOptions)
	a := s["base"]
	for name, want := range map[string][]int{
		"HandleStruct": {1, 2}, "handle_struct": {1, 2}, "ColorRed": {4}, "Color": {4}, "CloseHandle": {3}, "Nothing": nil,
	} {
		if got := a.Lookup(name); !slices.Equal(got, want) {
			t.Errorf("Lookup(%s) = %v, want %v", name, got, want)
		}
	}
}

func TestSysview_Extract(t *testing.T) {
	for _, o := range layouts {
		s := writeAll(t, fakeViews(), o)
		mods, err := Extract(s, map[string][]string{"gfx": {"Present"}}, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(mods) != 2 || mods[0].Name != "base" || mods[1].Name != "gfx" {
			t.Fatalf("modules %+v", mods)
		}
		if !slices.Equal(mods[0].Items, []int{0, 1, 2}) || !slices.Equal(mods[1].Items, []int{0}) {
			t.Errorf("kept %v and %v", mods[0].Items, mods[1].Items)
		}
		want := "// The system view c.gfx for x86_64-windows, shipped with mk (see doc/reference.md, \"System views\").\n" +
			"// headers: gfx.h\n\nimport c.base\n\n// C: int Present(HANDLE)\nexport extern Present(h: base.Handle): i32\n"
		if mods[1].Text != want {
			t.Errorf("gfx text:\n%s\nwant:\n%s", mods[1].Text, want)
		}
		if !strings.Contains(mods[0].Text, "\nimport std.raw\n") || strings.Contains(mods[0].Text, "CloseHandle") {
			t.Errorf("base text:\n%s", mods[0].Text)
		}
		lines := strings.Split(mods[0].Text, "\n")
		for k, i := range mods[0].Items {
			first, _, _ := strings.Cut(fakeViews()[0].Items[i].Text, "\n")
			if lines[mods[0].Lines[k]-1] != first {
				t.Errorf("item %d at line %d: %q", i, mods[0].Lines[k], lines[mods[0].Lines[k]-1])
			}
		}
	}
}

// TestSysview_ExtractAll keeps everything; a view no one names is still a
// module when a kept item lies in it.
func TestSysview_ExtractAll(t *testing.T) {
	s := writeAll(t, fakeViews(), DefaultOptions)
	mods, err := Extract(s, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(mods) != 2 || len(mods[0].Items) != 5 || len(mods[1].Items) != 2 {
		t.Fatalf("modules %+v", mods)
	}
	mods, err = Extract(Set{"gfx": s["gfx"]}, map[string][]string{"gfx": {"Present"}}, false)
	if err == nil || !strings.Contains(err.Error(), "no archive for the system view base") {
		t.Fatalf("missing required view: %v", err)
	}
}

// TestSysview_ExtractOrder: the text does not depend on the order names are
// wanted in.
func TestSysview_ExtractOrder(t *testing.T) {
	s := writeAll(t, fakeViews(), DefaultOptions)
	a, _ := Extract(s, map[string][]string{"base": {"Color", "CloseHandle"}, "gfx": {"Unused"}}, false)
	b, _ := Extract(s, map[string][]string{"gfx": {"Unused"}, "base": {"CloseHandle", "Color", "Color"}}, false)
	if len(a) != len(b) {
		t.Fatal("module counts differ")
	}
	for i := range a {
		if a[i].Text != b[i].Text {
			t.Errorf("%s differs:\n%s\n---\n%s", a[i].Name, a[i].Text, b[i].Text)
		}
	}
}

func TestSysview_Drop(t *testing.T) {
	views, dropped, err := DropItems(fakeViews(), map[string]map[int]string{"base": {1: "does not check"}})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range dropped {
		names = append(names, d.View+"."+d.Name)
	}
	// HandleStruct and its accessor, Handle (needs HandleStruct),
	// CloseHandle and gfx.Present (need Handle).
	want := []string{"base.Handle", "base.HandleStruct", "base.HandleStruct", "base.CloseHandle", "gfx.Present"}
	if !slices.Equal(names, want) {
		t.Fatalf("dropped %v, want %v", names, want)
	}
	if len(views[0].Items) != 1 || views[0].Items[0].Name != "Color" {
		t.Fatalf("base kept %+v", views[0].Items)
	}
	if len(views[1].Items) != 1 || views[1].Items[0].Name != "Unused" {
		t.Fatalf("gfx kept %+v", views[1].Items)
	}
	if !strings.Contains(dropped[0].Reason, "needs base.HandleStruct") {
		t.Errorf("reason %q", dropped[0].Reason)
	}
	s := writeAll(t, views, DefaultOptions)
	if _, err := Extract(s, nil, true); err != nil {
		t.Fatal(err)
	}
}

func TestSysview_BoundsErrors(t *testing.T) {
	// Corrupt fold table referencing an out-of-range item index must fail Open.
	v := fakeViews()[0]
	rawIdx := make([]byte, 12+4*2+4*2+8*1)
	binary.LittleEndian.PutUint32(rawIdx[0:], 1) // nStr
	binary.LittleEndian.PutUint32(rawIdx[4:], 1) // nItem
	binary.LittleEndian.PutUint32(rawIdx[8:], 1) // nFold
	binary.LittleEndian.PutUint32(rawIdx[12+4:], 0)
	binary.LittleEndian.PutUint32(rawIdx[12+8+4:], 0)
	binary.LittleEndian.PutUint32(rawIdx[12+8+8:], 0)     // fold strIdx=0
	binary.LittleEndian.PutUint32(rawIdx[12+8+8+4:], 999) // fold itemIdx OOB
	if (&Archive{}).index(rawIdx) {
		t.Fatal("index accepted OOB fold item index")
	}
	if _, _, err := DropItems([]View{v}, map[string]map[int]string{"base": {-1: "neg"}}); err == nil {
		t.Fatal("DropItems accepted negative item index")
	}
	badViews := fakeViews()
	badViews[0].Items[0].Deps = []Ref{{View: -1, Item: 0}}
	if _, _, err := DropItems(badViews, nil); err == nil {
		t.Fatal("DropItems accepted negative Ref.View")
	}
	badViews[0].Items[0].Deps = []Ref{{View: 0, Item: 999}}
	if _, _, err := DropItems(badViews, nil); err == nil {
		t.Fatal("DropItems accepted out-of-range Ref.Item")
	}
}
