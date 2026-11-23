// fasta, Go: the reference algorithm of README.md (the Benchmarks Game's fasta
// generator, hashed by line into an int checksum). Bounds-checked, wrapping
// overflow (class bounds). No unsafe, no compiler directives.
package main

import (
	"fmt"
	"os"
	"strconv"
)

// tools/bench flips this line in a copy for the counting build.
const benchCount = false

const (
	im      = 139968
	ia      = 3877
	ic      = 29573
	lineLen = 60
)

type freq struct {
	ch byte
	p  float64
}

func makeCumulative(tab []freq) {
	cp := 0.0
	for i := range tab {
		cp = cp + tab[i].p
		tab[i].p = cp
	}
	tab[len(tab)-1].p = 1.0
}

func nextRand(seed *int) float64 {
	*seed = (*seed*ia + ic) % im
	return float64(*seed) / float64(im)
}

func lineSum(buf []byte, m int) int {
	s := 0
	for k := 0; k < m; k++ {
		s += int(buf[k]) * (k + 1)
	}
	return s
}

func repeatFasta(alu []byte, count int) int {
	var buf [61]byte
	aluLen := len(alu)
	pos := 0
	rem := count
	total := 0
	for rem > 0 {
		m := lineLen
		if rem < m {
			m = rem
		}
		for k := 0; k < m; k++ {
			buf[k] = alu[pos]
			pos++
			if pos == aluLen {
				pos = 0
			}
		}
		buf[m] = 10
		total += lineSum(buf[:], m+1)
		rem -= m
	}
	return total
}

func randomFasta(tab []freq, count int, seed *int, probes *uint64) int {
	var buf [61]byte
	rem := count
	total := 0
	for rem > 0 {
		m := lineLen
		if rem < m {
			m = rem
		}
		for k := 0; k < m; k++ {
			r := nextRand(seed)
			j := 0
			for {
				if benchCount {
					*probes++
				}
				if tab[j].p >= r {
					break
				}
				j++
			}
			buf[k] = tab[j].ch
		}
		buf[m] = 10
		total += lineSum(buf[:], m+1)
		rem -= m
	}
	return total
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: fasta <n>")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[1])
	if err != nil {
		os.Exit(2)
	}
	alu := []byte("GGCCGGGCGCGGTGGCTCACGCCTGTAATCCCAGCACTTTGGGAGGCCGAGGCGGGCGGATCACCTGAGGTCAGGAGTTCGAGACCAGCCTGGCCAACATGGTGAAACCCCGTCTCTACTAAAAATACAAAAATTAGCCGGGCGTGGTGGCGCGCGCCTGTAATCCCAGCTACTCGGGAGGCTGAGGCAGGAGAATCGCTTGAACCCGGGAGGCGGAGGTTGCAGTGAGCCGAGATCGCGCCACTGCACTCCAGCCTGGGCGACAGAGCGAGACTCCGTCTCAAAAA")
	iub := []freq{
		{'a', 0.27}, {'c', 0.12}, {'g', 0.12}, {'t', 0.27},
		{'B', 0.02}, {'D', 0.02}, {'H', 0.02}, {'K', 0.02},
		{'M', 0.02}, {'N', 0.02}, {'R', 0.02}, {'S', 0.02},
		{'V', 0.02}, {'W', 0.02}, {'Y', 0.02},
	}
	homo := []freq{
		{'a', 0.3029549426680},
		{'c', 0.1979883004921},
		{'g', 0.1975473066391},
		{'t', 0.3015094502008},
	}
	makeCumulative(iub)
	makeCumulative(homo)
	seed := 42
	var probes uint64
	total := 0
	total += repeatFasta(alu, 2*n)
	total += randomFasta(iub, 3*n, &seed, &probes)
	total += randomFasta(homo, 5*n, &seed, &probes)
	fmt.Println(total)
	if benchCount {
		fmt.Printf("counts probes=%d\n", probes)
	}
}
