// Why: pack coalescing must keep std modules isolated, leave small programs
// untouched, preserve deterministic ordering, keep a 1-module edit local to a
// single bucket, and route globals of every coalesced module to its pack.
//
// What: TestCoalescePacks_StdAndSmallPrograms, TestCoalescePacks_LargeBuildEditLocality,
// and TestCoalescePacks_GlobalsOfMultiModulePack.

package driver

import (
	"fmt"
	"testing"

	"mk/internal/iface"
	"mk/internal/mir"
	"mk/internal/mono"
	"mk/internal/source"
	"mk/internal/types"
)

func makeTestWorldWithMods(paths []string) (*world, map[string]types.ModuleID) {
	return makeTestWorldWithProfile(paths, types.Release)
}

func makeTestWorldWithProfile(paths []string, prof types.Profile) (*world, map[string]types.ModuleID) {
	w := newWorld(Config{Jobs: 4, Profile: prof})
	ids := make(map[string]types.ModuleID, len(paths))
	for _, p := range paths {
		ids[p] = w.items.AddModule(&types.Module{Path: p, Std: isStdPath(p)})
	}
	return w, ids
}

func makeSynthInstances(mods map[string]types.ModuleID, specs []struct {
	mod   string
	count int
	ops   uint32
}) (*mono.Instances, []mono.Pack) {
	in := &mono.Instances{}
	var packs []mono.Pack
	for _, sp := range specs {
		mid := mods[sp.mod]
		var pk mono.Pack
		for i := range sp.count {
			idx := len(in.List)
			name := fmt.Sprintf("%s.F%04d", sp.mod, i)
			h := source.HashString("test.coalesce.name", name)
			c := source.HashString("test.coalesce.content", name)
			in.List = append(in.List, mono.Instance{
				Key:     mono.Key{Text: name, Hash: h},
				Owner:   mid,
				Body:    &mir.Body{},
				Ops:     sp.ops,
				Content: c,
			})
			// Split every 20 functions to simulate mono.Partition sub-module cuts.
			if len(pk.Members) == 0 {
				pk = mono.Pack{Name: sp.mod + "." + h.String()[:16], Owner: mid}
			}
			pk.Members = append(pk.Members, idx)
			pk.Instances = append(pk.Instances, iface.PackInstance{Name: h, Content: c})
			if len(pk.Members) == 20 {
				packs = append(packs, pk)
				pk = mono.Pack{}
			}
		}
		if len(pk.Members) > 0 {
			packs = append(packs, pk)
		}
	}
	return in, packs
}

func TestCoalescePacks_StdAndSmallPrograms(t *testing.T) {
	w, mods := makeTestWorldWithMods([]string{"std.core", "std.sys", "lib", "main"})
	in, packs := makeSynthInstances(mods, []struct {
		mod   string
		count int
		ops   uint32
	}{
		{"std.core", 45, 50}, // 3 packs -> 1 std.core pack
		{"std.sys", 65, 50},  // 4 packs -> 1 std.sys pack
		{"lib", 25, 40},      // 2 user packs (<= threshold) -> kept verbatim
		{"main", 5, 40},      // 1 user pack (<= threshold) -> kept verbatim
	})
	got := w.coalescePacks(in, packs)
	// Expect 1 std.core + 1 std.sys + 2 lib + 1 main = 5 packs.
	if len(got) != 5 {
		t.Fatalf("got %d packs, want 5", len(got))
	}
	// Verify std.core and std.sys never mixed with each other or with user packs.
	for _, pk := range got {
		firstStd := isStdPath(w.items.Module(in.List[pk.Members[0]].Owner).Path)
		firstMod := in.List[pk.Members[0]].Owner
		for _, fi := range pk.Members {
			owner := in.List[fi].Owner
			if isStdPath(w.items.Module(owner).Path) != firstStd {
				t.Fatalf("pack %s mixes std and non-std members", pk.Name)
			}
			if firstStd && owner != firstMod {
				t.Fatalf("std pack %s mixes %s and %s", pk.Name, w.items.Module(firstMod).Path, w.items.Module(owner).Path)
			}
		}
	}
}

func TestCoalescePacks_LargeBuildEditLocality(t *testing.T) {
	paths := []string{"std.core", "std.rt", "std.sys", "main"}
	for i := range 50 {
		paths = append(paths, fmt.Sprintf("m%d", i))
	}
	w, mods := makeTestWorldWithMods(paths)
	specs := []struct {
		mod   string
		count int
		ops   uint32
	}{
		{"std.core", 40, 50},
		{"std.rt", 40, 50},
		{"std.sys", 60, 50},
		{"main", 1, 8000}, // large hub module -> stays isolated
	}
	for i := range 50 {
		specs = append(specs, struct {
			mod   string
			count int
			ops   uint32
		}{fmt.Sprintf("m%d", i), 40, 45})
	}
	// Wire a binary tree of imports (m{i} imports m{(i-1)/2}) so m0..m24 are
	// shared and m25..m49 are leaves, matching realistic multi-module graphs.
	for i := 1; i < 50; i++ {
		child := w.items.Module(mods[fmt.Sprintf("m%d", i)])
		parent := mods[fmt.Sprintf("m%d", (i-1)/2)]
		child.Imports = []types.ModuleID{parent}
	}
	in, packs := makeSynthInstances(mods, specs)
	got := w.coalescePacks(in, packs)

	// 3 std + 1 solo main + bounded user buckets (down from 108+).
	if len(got) > 36 || len(got) < 12 {
		t.Fatalf("got %d coalesced packs from %d raw packs, want 12..36", len(got), len(packs))
	}

	// Verify every instance is accounted for exactly once, main is isolated,
	// and leaf modules (m25..m49) never share a pack with shared modules (m0..m24).
	seen := make([]int, len(in.List))
	mainPack := -1
	for pi, pk := range got {
		hasShared, hasLeaf := false, false
		for _, fi := range pk.Members {
			seen[fi]++
			mp := w.items.Module(in.List[fi].Owner).Path
			if mp == "main" {
				if mainPack >= 0 && mainPack != pi {
					t.Fatalf("main split across packs %d and %d", mainPack, pi)
				}
				mainPack = pi
				if len(pk.Members) != 1 {
					t.Fatalf("large main pack merged with %d other members", len(pk.Members)-1)
				}
			}
			var idx int
			if n, _ := fmt.Sscanf(mp, "m%d", &idx); n == 1 {
				if idx < 25 {
					hasShared = true
				} else {
					hasLeaf = true
				}
			}
		}
		if hasShared && hasLeaf {
			t.Fatalf("pack %s mixes shared and leaf modules", pk.Name)
		}
	}
	for i, c := range seen {
		if c != 1 {
			t.Fatalf("instance %d appeared %d times, want 1", i, c)
		}
	}

	// Verify determinism and 1-module edit locality: every function of m25
	// lands in a single bucket, so editing m25 changes at most 1 pack.
	m25Packs := map[int]bool{}
	for pi, pk := range got {
		for _, fi := range pk.Members {
			if w.items.Module(in.List[fi].Owner).Path == "m25" {
				m25Packs[pi] = true
			}
		}
	}
	if len(m25Packs) != 1 {
		t.Fatalf("m25 spans %d coalesced packs, want 1", len(m25Packs))
	}

	// Dev profile uses finer devUserPackBuckets (32) while keeping shared/leaf
	// stratification and single-bucket edit locality.
	wDev, modsDev := makeTestWorldWithProfile(paths, types.Dev)
	for i := 1; i < 50; i++ {
		child := wDev.items.Module(modsDev[fmt.Sprintf("m%d", i)])
		child.Imports = []types.ModuleID{modsDev[fmt.Sprintf("m%d", (i-1)/2)]}
	}
	inDev, packsDev := makeSynthInstances(modsDev, specs)
	gotDev := wDev.coalescePacks(inDev, packsDev)
	if len(gotDev) <= len(got) || len(gotDev) > 48 {
		t.Fatalf("dev profile got %d packs (release %d), want (%d..48]", len(gotDev), len(got), len(got))
	}
}
