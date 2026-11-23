// regex-redux, Go, idiomatic std: the task of ../README.md with regexp
// (RE2-style linear-time engine). Generates the deterministic FASTA DNA
// sequence, compiles the cleanup regex, 9 DNA match-count patterns, and 5
// IUB replacement patterns once, and runs rounds passes folding all counts
// and sequence lengths into a wrapping uint64 checksum. Built with
// GOAMD64=v2 -pgo=off.
package main

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
)

const (
	im = 139968
	ia = 3877
	ic = 29573
)

var countPats = [9]string{
	`agggtaaa|tttaccct`,
	`[cgt]gggtaaa|tttaccc[acg]`,
	`a[act]ggtaaa|tttacc[agt]t`,
	`ag[act]gtaaa|tttac[agt]ct`,
	`agg[act]taaa|ttta[agt]cct`,
	`aggg[acg]aaa|ttt[cgt]ccct`,
	`agggt[cgt]aa|tt[acg]accct`,
	`agggta[cgt]a|t[acg]taccct`,
	`agggtaa[cgt]|[acg]ttaccct`,
}

var replPats = [5]string{
	`tHa[Nt]`,
	`aND|caN|Ha[DS]|WaS`,
	`a[NSt]|BY`,
	`<[^>]*>`,
	`\|[^|][^|]*\|`,
}

var replWith = [5]string{
	"<4>",
	"<3>",
	"<2>",
	"|",
	"-",
}

func nextRand(seed *int) int {
	*seed = (*seed*ia + ic) % im
	return *seed
}

func homoBase(r int) byte {
	switch {
	case r < 42404:
		return 'a'
	case r < 70116:
		return 'c'
	case r < 97766:
		return 'g'
	default:
		return 't'
	}
}

func iubBase(r int) byte {
	switch {
	case r < 37791:
		return 'a'
	case r < 54587:
		return 'c'
	case r < 71383:
		return 'g'
	case r < 109174:
		return 't'
	case r < 111973:
		return 'B'
	case r < 114772:
		return 'D'
	case r < 117571:
		return 'H'
	case r < 120370:
		return 'K'
	case r < 123169:
		return 'M'
	case r < 125968:
		return 'N'
	case r < 128767:
		return 'R'
	case r < 131566:
		return 'S'
	case r < 134365:
		return 'V'
	case r < 137164:
		return 'W'
	default:
		return 'Y'
	}
}

func writeSection(out []byte, header string, count int, iub bool, seed *int) []byte {
	out = append(out, header...)
	col := 0
	for i := 0; i < count; i++ {
		r := nextRand(seed)
		if iub {
			out = append(out, iubBase(r))
		} else {
			out = append(out, homoBase(r))
		}
		col++
		if col == 60 {
			out = append(out, '\n')
			col = 0
		}
	}
	if col > 0 {
		out = append(out, '\n')
	}
	return out
}

func genFasta(n int) string {
	out := make([]byte, 0, n+n/60+128)
	seed := 42
	n1 := n / 2
	n2 := n - n1
	out = writeSection(out, ">ONE Homo sapiens frequency\n", n1, false, &seed)
	out = writeSection(out, ">TWO IUB ambiguity codes\n", n2, true, &seed)
	return string(out)
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: regex-redux <n> <rounds>")
		os.Exit(2)
	}
	n, _ := strconv.Atoi(os.Args[1])
	rounds, _ := strconv.Atoi(os.Args[2])
	raw := genFasta(n)
	clean := regexp.MustCompile(`>[^\n]*\n|\n`)
	var counts [9]*regexp.Regexp
	for i, p := range countPats {
		counts[i] = regexp.MustCompile(p)
	}
	var repls [5]*regexp.Regexp
	for i, p := range replPats {
		repls[i] = regexp.MustCompile(p)
	}
	var total uint64
	for range rounds {
		seq := clean.ReplaceAllString(raw, "")
		h := uint64(len(raw))*131 + uint64(len(seq))
		for _, re := range counts {
			h = h*131 + uint64(len(re.FindAllStringIndex(seq, -1)))
		}
		cur := seq
		for i, re := range repls {
			cur = re.ReplaceAllString(cur, replWith[i])
		}
		h = h*131 + uint64(len(cur))
		total += h
	}
	fmt.Println(total)
}
