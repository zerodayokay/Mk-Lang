// Why: spawning a separate clang process per fine-grained mono.Pack dominates
// clean and release builds on Windows (CreateProcess + driver startup + one
// ThinLTO backend process per bitcode object). Coalescing small packs into a
// bounded number of deterministic compile packs slashes process spawns and
// ThinLTO import fan-out without breaking incremental edit locality, module
// isolation for small programs, or warm/cold byte-identity.
//
// What: world.coalescePacks, which merges packs within each std module and
// deterministically buckets small user modules (stratified into shared vs
// leaf tiers by the intra-package import graph) when a build has many user
// packs.
//
// How: the grouping is a pure function of the sorted mono.Partition output,
// module paths, and types.Module.Imports (never cache state, GOMAXPROCS, or
// file paths):
//   - std.* packs stay strictly separate from user packs and from other std
//     modules, merging packs of the same std module up to maxStdPackOps so
//     unchanged std modules stay cached across builds (and std.sys stays
//     separate from std.core/std.rt/std.task).
//   - When a build has at most userCoalesceThreshold user packs (all small
//     programs and unit tests), user packs are kept verbatim.
//   - Otherwise, large user modules (total ops > maxBucketModOps, such as a
//     hub Main) keep their own packs isolated so edits in small modules never
//     touch them. Small user modules are stratified into two tiers using
//     types.Module.Imports:
//       * shared modules (imported by at least one other small user module)
//       * leaf modules (imported by no other small user module)
//     Separating leaf modules from shared modules guarantees that editing a
//     leaf module never dirties a shared library bitcode pack and therefore
//     never cascades ThinLTO backend cache misses to unrelated modules.
//     Within the leaf tier, when modules have diverse non-std dependencies,
//     siblings sharing a primary dependency hash to the same bucket to shrink
//     cross-bucket ThinLTO imports; otherwise each module hashes by its path.

package driver

import (
	"bytes"
	"cmp"
	"hash/fnv"
	"slices"

	"mk/internal/iface"
	"mk/internal/mono"
	"mk/internal/types"
)

const (
	// maxStdPackOps caps the merged size of a single std module's pack so a
	// huge std module still splits across workers.
	maxStdPackOps uint64 = 80_000

	// userCoalesceThreshold is the user pack count above which small user
	// modules are coalesced into deterministic buckets. Programs at or below
	// this count keep mono.Partition's exact packs.
	userCoalesceThreshold = 16

	// userPackBuckets is the target bucket count across small user modules in
	// large multi-module builds: cuts clang process spawns ~5x while keeping
	// balanced 8-core scheduling and compact 1-module edit buckets.
	userPackBuckets = 16

	// maxBucketModOps is the maximum total MIR ops of a single user module
	// eligible for cross-module bucketing. Larger modules (e.g. a hub main
	// module or a giant single-module package) stay isolated in their own packs.
	maxBucketModOps uint64 = 4_000

	// maxCoalescedBucketOps caps any hash-bucketed pack (~3 small modules) so
	// birthday collisions never create an oversized straggler bucket that slows
	// clean load balance or 1-module edit recompilation.
	maxCoalescedBucketOps uint64 = 12_000
)

// coalescePacks deterministically merges fine-grained mono.Partition packs
// into fewer compile packs for codegen, clang, and ThinLTO.
func (w *world) coalescePacks(in *mono.Instances, packs []mono.Pack) []mono.Pack {
	if len(packs) <= 1 {
		return packs
	}

	packOps := func(pk mono.Pack) uint64 {
		var ops uint64
		for _, fi := range pk.Members {
			ops += uint64(in.List[fi].Ops)
		}
		return ops
	}
	modPath := func(id types.ModuleID) string {
		return w.items.Module(id).Path
	}

	var stdPacks []mono.Pack
	var userPacks []mono.Pack
	for _, pk := range packs {
		if isStdPath(modPath(pk.Owner)) {
			stdPacks = append(stdPacks, pk)
		} else {
			userPacks = append(userPacks, pk)
		}
	}

	out := make([]mono.Pack, 0, len(packs))

	// 1. Coalesce packs within each std module (preserving std module boundaries).
	out = append(out, coalesceSameModule(stdPacks, packOps, maxStdPackOps)...)

	// 2. User packs: leave small builds untouched; stratify & bucket small modules in large builds.
	if len(userPacks) <= userCoalesceThreshold {
		out = append(out, userPacks...)
	} else {
		// Group user packs by owning module (already sorted by module path in mono.Partition).
		type modGroup struct {
			path  string
			owner types.ModuleID
			packs []mono.Pack
			ops   uint64
		}
		var groups []modGroup
		for _, pk := range userPacks {
			p := modPath(pk.Owner)
			ops := packOps(pk)
			if len(groups) > 0 && groups[len(groups)-1].owner == pk.Owner {
				g := &groups[len(groups)-1]
				g.packs = append(g.packs, pk)
				g.ops += ops
			} else {
				groups = append(groups, modGroup{path: p, owner: pk.Owner, packs: []mono.Pack{pk}, ops: ops})
			}
		}

		// Separate large isolated modules from small bucketable modules.
		var small []modGroup
		smallSet := make(map[types.ModuleID]bool, len(groups))
		for _, g := range groups {
			if g.ops > maxBucketModOps {
				out = append(out, g.packs...)
			} else {
				small = append(small, g)
				smallSet[g.owner] = true
			}
		}

		if len(small) <= userPackBuckets {
			// Already at or below the bucket budget: emit one pack per small module.
			for _, g := range small {
				out = append(out, mergePacks(g.packs))
			}
		} else {
			// Compute in-degree among small user modules using types.Module.Imports.
			inDeg := make(map[types.ModuleID]int, len(small))
			for _, g := range small {
				mod := w.items.Module(g.owner)
				for _, dep := range mod.Imports {
					if smallSet[dep] {
						inDeg[dep]++
					}
				}
			}
			var shared, leaves []modGroup
			for _, g := range small {
				if inDeg[g.owner] > 0 {
					shared = append(shared, g)
				} else {
					leaves = append(leaves, g)
				}
			}

			// Split userPackBuckets proportionally between shared and leaf tiers.
			sharedB, leafB := splitBuckets(len(shared), len(leaves), userPackBuckets)

			emitTier := func(tier []modGroup, nb int, byParent bool) {
				if len(tier) == 0 {
					return
				}
				if nb <= 0 || len(tier) <= nb {
					for _, g := range tier {
						out = append(out, mergePacks(g.packs))
					}
					return
				}
				// For leaves, optionally key by first non-std dependency when
				// dependencies are diverse enough to spread evenly across nb buckets.
				useParent := false
				parents := make([]string, len(tier))
				if byParent {
					distinct := map[string]bool{}
					for i, g := range tier {
						mod := w.items.Module(g.owner)
						for _, dep := range mod.Imports {
							dp := modPath(dep)
							if !isStdPath(dp) {
								parents[i] = dp
								distinct[dp] = true
								break
							}
						}
					}
					useParent = len(distinct) >= nb
				}
				buckets := make([][]modGroup, nb)
				for i, g := range tier {
					key := g.path
					if useParent && parents[i] != "" {
						key = parents[i]
					}
					b := int(modBucket(key, uint64(nb)))
					buckets[b] = append(buckets[b], g)
				}
				for _, gs := range buckets {
					if len(gs) == 0 {
						continue
					}
					// Split oversized buckets along module boundaries so hash
					// collisions never form a giant straggler pack.
					var cur []mono.Pack
					var curOps uint64
					for _, g := range gs {
						if len(cur) > 0 && curOps+g.ops > maxCoalescedBucketOps {
							out = append(out, mergePacks(cur))
							cur, curOps = nil, 0
						}
						cur = append(cur, g.packs...)
						curOps += g.ops
					}
					if len(cur) > 0 {
						out = append(out, mergePacks(cur))
					}
				}
			}
			emitTier(shared, sharedB, false)
			emitTier(leaves, leafB, true)
		}
	}

	// Deterministic final order by first member's (module path, instance hash),
	// matching mono.Partition's ordering invariant.
	slices.SortFunc(out, func(a, b mono.Pack) int {
		ia, ib := in.List[a.Members[0]], in.List[b.Members[0]]
		return cmp.Or(cmp.Compare(modPath(ia.Owner), modPath(ib.Owner)), bytes.Compare(ia.Key.Hash[:], ib.Key.Hash[:]))
	})
	return out
}

// splitBuckets divides total buckets proportionally between a and b items,
// giving each non-empty side at least one bucket.
func splitBuckets(a, b, total int) (int, int) {
	if a == 0 {
		return 0, total
	}
	if b == 0 {
		return total, 0
	}
	sa := (total*a + (a+b)/2) / (a + b)
	if sa < 1 {
		sa = 1
	}
	if sa > total-1 {
		sa = total - 1
	}
	return sa, total - sa
}

// coalesceSameModule merges adjacent packs belonging to the same module up to maxOps.
func coalesceSameModule(packs []mono.Pack, packOps func(mono.Pack) uint64, maxOps uint64) []mono.Pack {
	if len(packs) <= 1 {
		return packs
	}
	var out []mono.Pack
	var cur mono.Pack
	var curOps uint64
	hasCur := false
	flush := func() {
		if hasCur {
			out = append(out, cur)
			cur = mono.Pack{}
			curOps = 0
			hasCur = false
		}
	}
	for _, pk := range packs {
		ops := packOps(pk)
		if hasCur && cur.Owner == pk.Owner && curOps+ops <= maxOps {
			cur.Members = append(cur.Members, pk.Members...)
			cur.Instances = append(cur.Instances, pk.Instances...)
			curOps += ops
			continue
		}
		flush()
		cur = mono.Pack{
			Name:      pk.Name,
			Owner:     pk.Owner,
			Members:   slices.Clone(pk.Members),
			Instances: slices.Clone(pk.Instances),
		}
		curOps = ops
		hasCur = true
	}
	flush()
	return out
}

// mergePacks combines non-empty packs (already in mono.Partition order) into
// one pack preserving member order.
func mergePacks(ps []mono.Pack) mono.Pack {
	if len(ps) == 1 {
		return ps[0]
	}
	total := 0
	for _, pk := range ps {
		total += len(pk.Members)
	}
	first := ps[0]
	out := mono.Pack{
		Name:      first.Name,
		Owner:     first.Owner,
		Members:   make([]int, 0, total),
		Instances: make([]iface.PackInstance, 0, total),
	}
	for _, pk := range ps {
		out.Members = append(out.Members, pk.Members...)
		out.Instances = append(out.Instances, pk.Instances...)
	}
	return out
}

// modBucket hashes a key into [0, n) using FNV-1a 64-bit.
func modBucket(key string, n uint64) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return h.Sum64() % n
}
