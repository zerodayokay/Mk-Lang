// wordfreq-app, Go, std: the task of README.md. Streams the input file through
// a 64 KiB read buffer, tokenizes ASCII words ([A-Za-z], folded to lowercase)
// carrying partial words across chunk boundaries, counts frequencies in Go's
// built-in map[string]int64 (runtime-seeded hash; lookup by string(byteSlice)
// avoids allocation on hits), sorts unique entries with slices.SortFunc by
// (count desc, word asc), and prints total, unique and top-K lines to a 64 KiB
// bufio.Writer. Pure safe Go (no unsafe, no //go: directives).
// Usage: wordfreq-app <file> <top_k> [<max_words>].
package main

import (
	"bufio"
	"cmp"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
)

type entry struct {
	word  string
	count int64
}

func main() {
	if len(os.Args) < 3 || len(os.Args) > 4 {
		fmt.Fprintln(os.Stderr, "usage: wordfreq-app <file> <top_k> [<max_words>]")
		os.Exit(2)
	}
	topK, err := strconv.Atoi(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, "wordfreq-app:", err)
		os.Exit(2)
	}
	limit := int64(-1)
	if len(os.Args) == 4 {
		v, err := strconv.ParseInt(os.Args[3], 10, 64)
		if err != nil {
			fmt.Fprintln(os.Stderr, "wordfreq-app:", err)
			os.Exit(2)
		}
		limit = v
	}

	f, err := os.Open(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "wordfreq-app:", err)
		os.Exit(1)
	}
	defer f.Close()

	counts := make(map[string]int64, 1024)
	buf := make([]byte, 64*1024)
	wbuf := make([]byte, 0, 256)
	var total int64
	done := false

	for !done {
		n, rerr := f.Read(buf)
		if n > 0 {
			i := 0
			for i < n {
				c := buf[i]
				if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
					if len(wbuf) == 0 {
						start := i
						hasUpper := c >= 'A' && c <= 'Z'
						i++
						for i < n {
							b := buf[i]
							if b >= 'a' && b <= 'z' {
								i++
							} else if b >= 'A' && b <= 'Z' {
								hasUpper = true
								i++
							} else {
								break
							}
						}
						if i == n {
							for j := start; j < i; j++ {
								ch := buf[j]
								if ch >= 'A' && ch <= 'Z' {
									ch += 32
								}
								wbuf = append(wbuf, ch)
							}
						} else if !hasUpper {
							counts[string(buf[start:i])]++
							total++
							if limit >= 0 && total >= limit {
								done = true
								break
							}
						} else {
							wbuf = wbuf[:0]
							for j := start; j < i; j++ {
								ch := buf[j]
								if ch >= 'A' && ch <= 'Z' {
									ch += 32
								}
								wbuf = append(wbuf, ch)
							}
							counts[string(wbuf)]++
							wbuf = wbuf[:0]
							total++
							if limit >= 0 && total >= limit {
								done = true
								break
							}
						}
					} else {
						if c >= 'A' && c <= 'Z' {
							c += 32
						}
						wbuf = append(wbuf, c)
						i++
					}
				} else {
					if len(wbuf) > 0 {
						counts[string(wbuf)]++
						wbuf = wbuf[:0]
						total++
						if limit >= 0 && total >= limit {
							done = true
							break
						}
					}
					i++
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			fmt.Fprintln(os.Stderr, "wordfreq-app:", rerr)
			os.Exit(1)
		}
	}
	if !done && len(wbuf) > 0 {
		counts[string(wbuf)]++
		total++
	}

	entries := make([]entry, 0, len(counts))
	for w, c := range counts {
		entries = append(entries, entry{w, c})
	}
	slices.SortFunc(entries, func(a, b entry) int {
		if c := cmp.Compare(b.count, a.count); c != 0 {
			return c
		}
		return cmp.Compare(a.word, b.word)
	})

	uniq := len(entries)
	k := min(max(topK, 0), uniq)
	w := bufio.NewWriterSize(os.Stdout, 64*1024)
	fmt.Fprintf(w, "total %d\nunique %d\n", total, uniq)
	for i := range k {
		fmt.Fprintf(w, "%s %d\n", entries[i].word, entries[i].count)
	}
	if err := w.Flush(); err != nil {
		fmt.Fprintln(os.Stderr, "wordfreq-app:", err)
		os.Exit(1)
	}
}
