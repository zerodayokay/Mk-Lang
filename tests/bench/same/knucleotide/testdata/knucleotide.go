// knucleotide, Go: the reference algorithm of README.md (2-bit packed DNA
// k-mers in a fixed open-addressed linear-probing hash table, Fibonacci hash,
// 70% load factor). Wrapping uint64 hashing and checksum (overflow class
// "wraps"). Bounds-checked (class bounds). No unsafe, no compiler directives.
package main

import (
	"fmt"
	"os"
	"strconv"
)

// tools/bench flips this line in a copy for the counting build.
const benchCount = false

const (
	im     = 139968
	ia     = 3877
	ic     = 29573
	fibMul = uint64(0x9e3779b97f4a7c15)
)

type entry struct {
	key uint64
	val uint64
}

type table struct {
	entries []entry
	cap     int
	shift   uint
	length  int
}

func newTable() table {
	return table{
		entries: make([]entry, 64),
		cap:     64,
		shift:   6,
		length:  0,
	}
}

func (tab *table) grow(rehashes *uint64) {
	if benchCount {
		*rehashes++
	}
	newCap := tab.cap * 2
	newShift := tab.shift + 1
	next := make([]entry, newCap)
	mask := newCap - 1
	sh := 64 - newShift
	for i := 0; i < tab.cap; i++ {
		e := tab.entries[i]
		if e.val != 0 {
			slot := int((e.key*fibMul)>>sh) & mask
			for {
				if next[slot].val == 0 {
					next[slot] = e
					break
				}
				slot = (slot + 1) & mask
			}
		}
	}
	tab.entries = next
	tab.cap = newCap
	tab.shift = newShift
}

func (tab *table) inc(key uint64, probes, rehashes *uint64) {
	if (tab.length+1)*10 > tab.cap*7 {
		tab.grow(rehashes)
	}
	mask := tab.cap - 1
	slot := int((key*fibMul)>>(64-tab.shift)) & mask
	for {
		if benchCount {
			*probes++
		}
		v := tab.entries[slot].val
		if v == 0 {
			tab.entries[slot] = entry{key: key, val: 1}
			tab.length++
			return
		}
		if tab.entries[slot].key == key {
			tab.entries[slot].val = v + 1
			return
		}
		slot = (slot + 1) & mask
	}
}

func (tab *table) get(key uint64, probes *uint64) uint64 {
	mask := tab.cap - 1
	slot := int((key*fibMul)>>(64-tab.shift)) & mask
	for {
		if benchCount {
			*probes++
		}
		v := tab.entries[slot].val
		if v == 0 {
			return 0
		}
		if tab.entries[slot].key == key {
			return v
		}
		slot = (slot + 1) & mask
	}
}

func packSeq(s string) uint64 {
	var key uint64
	for i := 0; i < len(s); i++ {
		var code uint64 = 3
		if s[i] == 'A' {
			code = 0
		} else if s[i] == 'C' {
			code = 1
		} else if s[i] == 'G' {
			code = 2
		}
		key = (key << 2) | code
	}
	return key
}

func countK(seq []byte, k int, probes, rehashes *uint64) uint64 {
	tab := newTable()
	n := len(seq)
	if n >= k {
		mask := (uint64(1) << uint(2*k)) - 1
		var key uint64
		for i := 0; i < k-1; i++ {
			key = (key << 2) | uint64(seq[i])
		}
		for i := k - 1; i < n; i++ {
			key = ((key << 2) | uint64(seq[i])) & mask
			tab.inc(key, probes, rehashes)
		}
	}
	h := uint64(tab.length)
	var maxVal uint64
	for i := 0; i < tab.cap; i++ {
		v := tab.entries[i].val
		if v > maxVal {
			maxVal = v
		}
	}
	h = h*31 + maxVal
	if k == 1 {
		for c := uint64(0); c < 4; c++ {
			h = h*31 + tab.get(c, probes)
		}
	} else if k == 2 {
		for c := uint64(0); c < 16; c++ {
			h = h*31 + tab.get(c, probes)
		}
	} else if k == 3 {
		h = h*31 + tab.get(packSeq("GGT"), probes)
	} else if k == 4 {
		h = h*31 + tab.get(packSeq("GGTA"), probes)
	} else if k == 6 {
		h = h*31 + tab.get(packSeq("GGTATT"), probes)
	} else if k == 12 {
		h = h*31 + tab.get(packSeq("GGTATTTTAATT"), probes)
	} else if k == 18 {
		h = h*31 + tab.get(packSeq("GGTATTTTAATTTATAGT"), probes)
	}
	return h
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: knucleotide <n> <rounds>")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[1])
	if err != nil {
		os.Exit(2)
	}
	rounds, err := strconv.Atoi(os.Args[2])
	if err != nil {
		os.Exit(2)
	}
	seq := make([]byte, n)
	p0 := 0.3029549426680
	p1 := p0 + 0.1979883004921
	p2 := p1 + 0.1975473066391
	seed := 42
	for i := 0; i < n; i++ {
		seed = (seed*ia + ic) % im
		r := float64(seed) / float64(im)
		if r < p0 {
			seq[i] = 0
		} else if r < p1 {
			seq[i] = 1
		} else if r < p2 {
			seq[i] = 2
		} else {
			seq[i] = 3
		}
	}
	ks := [7]int{1, 2, 3, 4, 6, 12, 18}
	var probes uint64
	var rehashes uint64
	var total uint64
	for r := 0; r < rounds; r++ {
		for _, k := range ks {
			total = total*31 + countK(seq, k, &probes, &rehashes)
		}
	}
	fmt.Println(total)
	if benchCount {
		fmt.Printf("counts probes=%d rehashes=%d\n", probes, rehashes)
	}
}
