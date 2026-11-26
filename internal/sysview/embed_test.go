// Why: the archives mk embeds are generated data; each must open, name a
// view of the table for its directory's target, and agree with the
// manifest committed beside it.
//
// What: TestSysview_EmbeddedData.
//
// How: every data/<target>/<view>.mksv is opened from the embedded tree;
// its identity must be the one views.txt lists for the view.

package sysview

import (
	"encoding/hex"
	"io/fs"
	"path"
	"slices"
	"strings"
	"testing"
)

func TestSysview_EmbeddedData(t *testing.T) {
	n := 0
	err := fs.WalkDir(data, "data", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".mksv") {
			return err
		}
		n++
		target := path.Base(path.Dir(p))
		name := strings.TrimSuffix(path.Base(p), ".mksv")
		b, ok := Embedded(target, name)
		if !ok {
			t.Fatalf("%s: Embedded does not find it", p)
		}
		a, err := Open(b)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		m := a.Meta()
		row, ok := Named(name)
		if m.Name != name || m.Target != target || !ok || !slices.Contains(row.Targets, target) {
			t.Errorf("%s: meta %s for %s; table row %v", p, m.Name, m.Target, ok)
		}
		id := a.ID()
		man := EmbeddedManifest(target)
		block := man[strings.Index(man, "\nview "+name+"\n")+1:]
		if end := strings.Index(block[1:], "\nview "); end >= 0 {
			block = block[:end+1]
		}
		if !strings.Contains(block, "  identity  "+hex.EncodeToString(id[:])+"\n") {
			t.Errorf("%s: views.txt does not list its identity", p)
		}
		for i := 0; i < a.Len(); i += 997 {
			if _, err := a.Item(i); err != nil {
				t.Fatalf("%s item %d: %v", p, i, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Skip("this mk carries no system view archive (internal/sysview/data holds none): go run ./tools/sysview makes them")
	}
}
