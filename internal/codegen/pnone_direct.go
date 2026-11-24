// Why: mutual and self-recursive `become` tail-call loops (interpreters,
// parsers, state machines without a const fn dispatch table) otherwise stay on
// the platform C ABI and spill arguments past the register count on every hop.
//
// What: directTailCycles (Tarjan strongly connected components over direct
// TermTailCall edges within the eligible candidate set).
//
// How: walks every node in `funcs` in deterministic sorted order and returns
// every function index belonging to a non-trivial strongly connected component
// (size >= 2, or size == 1 with a self-loop `fi -> fi`), so `computePnoneSet`
// promotes those direct tail cycles and their downstream tail callees to
// `preserve_nonecc`.

package codegen

import "sort"

// directTailCycles returns sorted Funcs indices in `funcs` that belong to a
// non-trivial cycle (length >= 2 SCC or self-edge) along `directTails`.
func directTailCycles(funcs map[int]bool, binfo []pnoneBodyInfo) []int {
	if len(funcs) == 0 {
		return nil
	}
	nodes := make([]int, 0, len(funcs))
	for fi := range funcs {
		nodes = append(nodes, fi)
	}
	sort.Ints(nodes)

	n := len(binfo)
	index := make([]int, n)
	low := make([]int, n)
	onStack := make([]bool, n)
	var stack []int
	var out []int
	nextIdx := 1

	var visit func(v int)
	visit = func(v int) {
		index[v] = nextIdx
		low[v] = nextIdx
		nextIdx++
		stack = append(stack, v)
		onStack[v] = true

		self := false
		for _, w := range binfo[v].directTails {
			if w == v {
				self = true
			}
			if !funcs[w] {
				continue
			}
			if index[w] == 0 {
				visit(w)
				if low[w] < low[v] {
					low[v] = low[w]
				}
			} else if onStack[w] && index[w] < low[v] {
				low[v] = index[w]
			}
		}

		if low[v] != index[v] {
			return
		}
		var comp []int
		for {
			w := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			onStack[w] = false
			comp = append(comp, w)
			if w == v {
				break
			}
		}
		if len(comp) >= 2 || (len(comp) == 1 && self) {
			out = append(out, comp...)
		}
	}

	for _, fi := range nodes {
		if index[fi] == 0 {
			visit(fi)
		}
	}
	sort.Ints(out)
	return out
}
