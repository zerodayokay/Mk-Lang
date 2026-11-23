// httpparse, Go, std: the task of README.md, a strict RFC 9112 HTTP/1.1 request
// parser over a splitmix64-generated batch of valid and malformed requests
// (GET/POST/PUT/DELETE, paths and query strings, headers, fixed Content-Length
// and chunked bodies, plus 10 malformed classes). Uses bytes.IndexByte for
// delimiter scans, reused header array and chunked body buffer, and wrapping
// uint64 FNV-1a digest. Pure safe Go (no unsafe, no //go: directives).
// Usage: httpparse <num_requests> <rounds> <seed>.
package main

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
)

const (
	fnvBasis   uint64 = 0xcbf29ce484222325
	fnvPrime   uint64 = 0x100000001b3
	maxHeaders        = 64
)

type rng struct{ s uint64 }

func (g *rng) next() uint64 {
	g.s += 0x9e3779b97f4a7c15
	z := g.s
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

func appendHex(dst []byte, v int) []byte {
	const hex = "0123456789abcdef"
	if v == 0 {
		return append(dst, '0')
	}
	var tmp [16]byte
	n := 0
	for v > 0 {
		tmp[n] = hex[v&15]
		v >>= 4
		n++
	}
	for i := n - 1; i >= 0; i-- {
		dst = append(dst, tmp[i])
	}
	return dst
}

func buildBatch(n int, seed uint64) ([]byte, []int) {
	g := rng{seed}
	data := make([]byte, 0, n*384)
	starts := make([]int, 0, n+1)
	for range n {
		starts = append(starts, len(data))
		r := g.next()
		if r%20 == 0 {
			switch (r >> 8) % 10 {
			case 0:
				data = append(data, "GET /api/v1/bad HTTP/2.0\r\nHost: api.example.com\r\n\r\n"...)
			case 1:
				data = append(data, "GET /api/v1/bad HTTP/1.1\nHost: api.example.com\r\n\r\n"...)
			case 2:
				data = append(data, "GET /api/v1/bad HTTP/1.1\r\nHost : api.example.com\r\n\r\n"...)
			case 3:
				data = append(data, "GET /api/v1/bad HTTP/1.1\r\nUser-Agent: bench/1.0\r\n\r\n"...)
			case 4:
				data = append(data, "POST /api/v1/bad HTTP/1.1\r\nHost: api.example.com\r\nContent-Length: 4\r\nTransfer-Encoding: chunked\r\n\r\n4\r\nabcd\r\n0\r\n\r\n"...)
			case 5:
				data = append(data, "POST /api/v1/bad HTTP/1.1\r\nHost: api.example.com\r\nContent-Length: 4\r\nContent-Length: 5\r\n\r\nabcd"...)
			case 6:
				data = append(data, "POST /api/v1/bad HTTP/1.1\r\nHost: api.example.com\r\nContent-Length: 12abc\r\n\r\nabcd"...)
			case 7:
				data = append(data, "POST /api/v1/bad HTTP/1.1\r\nHost: api.example.com\r\nContent-Length: 20\r\n\r\nshort"...)
			case 8:
				data = append(data, "POST /api/v1/bad HTTP/1.1\r\nHost: api.example.com\r\nTransfer-Encoding: chunked\r\n\r\nZZ\r\nabcd\r\n0\r\n\r\n"...)
			default:
				data = append(data, "POST /api/v1/bad HTTP/1.1\r\nHost: api.example.com\r\nTransfer-Encoding: chunked\r\n\r\n4\r\nabcd\r\n"...)
			}
			continue
		}
		m := (r >> 5) % 4
		switch m {
		case 0:
			data = append(data, "GET "...)
		case 1:
			data = append(data, "POST "...)
		case 2:
			data = append(data, "PUT "...)
		default:
			data = append(data, "DELETE "...)
		}
		data = append(data, "/api/v1/users/"...)
		data = strconv.AppendUint(data, (r>>8)%10000, 10)
		data = append(data, "/items/"...)
		data = strconv.AppendUint(data, (r>>22)%100000, 10)
		if (r>>40)&1 == 1 {
			data = append(data, "?page="...)
			data = strconv.AppendUint(data, (r>>41)%100, 10)
			data = append(data, "&limit="...)
			data = strconv.AppendUint(data, 10+((r>>48)%90), 10)
			data = append(data, "&sort=asc"...)
		}
		data = append(data, " HTTP/1.1\r\nHost: api"...)
		data = strconv.AppendUint(data, r&7, 10)
		data = append(data, ".example.com\r\nUser-Agent: Mozilla/5.0 (Windows NT 10.0; Win64; x64) BenchAgent/1.0\r\nAccept: application/json, text/plain;q=0.9, */*;q=0.8\r\nAccept-Encoding: gzip, deflate, br\r\n"...)
		if (r>>39)&1 == 1 {
			data = append(data, "Connection: close\r\n"...)
		} else {
			data = append(data, "Connection: keep-alive\r\n"...)
		}
		r2 := g.next()
		data = append(data, "X-Request-Id: req-"...)
		data = strconv.AppendUint(data, r2%100000000, 10)
		data = append(data, "\r\n"...)
		if m == 1 || m == 2 {
			data = append(data, "Content-Type: application/json\r\n"...)
			if (r2>>30)&1 == 1 {
				data = append(data, "Transfer-Encoding: chunked\r\n\r\n"...)
				nchunks := 2 + int((r2>>32)%4)
				for ci := range nchunks {
					shift := uint(ci * 7)
					clen := 16 + int(((r2>>shift)^(uint64(ci)*13))%64)
					data = appendHex(data, clen)
					data = append(data, "\r\n"...)
					for j := range clen {
						data = append(data, byte('a'+((ci*7+j)%26)))
					}
					data = append(data, "\r\n"...)
				}
				data = append(data, "0\r\n\r\n"...)
			} else {
				blen := 32 + int((r2>>32)%224)
				data = append(data, "Content-Length: "...)
				data = strconv.AppendInt(data, int64(blen), 10)
				data = append(data, "\r\n\r\n"...)
				off := int(r2 % 26)
				for j := range blen {
					data = append(data, byte('a'+((j+off)%26)))
				}
			}
		} else {
			data = append(data, "\r\n"...)
		}
	}
	starts = append(starts, len(data))
	return data, starts
}

type sliceSpan struct{ off, len int }
type headerSpan struct{ name, value sliceSpan }
type parsedReq struct {
	method, path, query sliceSpan
	nhdrs               int
	bodyOff, bodyLen    int
	bodyFromBuf         bool
	chunked, keepAlive  bool
}

func isTchar(b byte) bool {
	if (b >= '0' && b <= '9') || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') {
		return true
	}
	switch b {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	}
	return false
}

func lowerASCII(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + 32
	}
	return b
}

func eqIgnoreASCII(a []byte, lit string) bool {
	if len(a) != len(lit) {
		return false
	}
	for i := range a {
		if lowerASCII(a[i]) != lit[i] {
			return false
		}
	}
	return true
}

func hexVal(b byte) int {
	if b >= '0' && b <= '9' {
		return int(b - '0')
	}
	if b >= 'a' && b <= 'f' {
		return int(b - 'a' + 10)
	}
	if b >= 'A' && b <= 'F' {
		return int(b - 'A' + 10)
	}
	return -1
}

func parseRequest(buf []byte, hdrs *[maxHeaders]headerSpan, bodyBuf *[]byte, out *parsedReq) bool {
	n := len(buf)
	nl := bytes.IndexByte(buf, '\n')
	if nl < 1 || buf[nl-1] != '\r' {
		return false
	}
	lineEnd := nl - 1
	sp1 := bytes.IndexByte(buf[:lineEnd], ' ')
	if sp1 <= 0 {
		return false
	}
	for i := range sp1 {
		if !isTchar(buf[i]) {
			return false
		}
	}
	rest1 := sp1 + 1
	sp2Rel := bytes.IndexByte(buf[rest1:lineEnd], ' ')
	if sp2Rel <= 0 {
		return false
	}
	sp2 := rest1 + sp2Rel
	if buf[rest1] != '/' {
		return false
	}
	qpos := -1
	for i := rest1; i < sp2; i++ {
		c := buf[i]
		if c < 0x21 || c > 0x7e {
			return false
		}
		if c == '?' && qpos < 0 {
			qpos = i
		}
	}
	verStart := sp2 + 1
	if lineEnd-verStart != 8 || string(buf[verStart:lineEnd]) != "HTTP/1.1" {
		return false
	}

	out.method = sliceSpan{0, sp1}
	if qpos >= 0 {
		out.path = sliceSpan{rest1, qpos - rest1}
		out.query = sliceSpan{qpos + 1, sp2 - (qpos + 1)}
	} else {
		out.path = sliceSpan{rest1, sp2 - rest1}
		out.query = sliceSpan{0, 0}
	}

	pos := nl + 1
	nhdrs := 0
	hasHost := false
	hasCL := false
	clVal := 0
	chunked := false
	keepAlive := true
	headDone := false

	for pos < n {
		lnRel := bytes.IndexByte(buf[pos:], '\n')
		if lnRel < 1 {
			return false
		}
		lnEnd := pos + lnRel - 1
		if buf[lnEnd] != '\r' {
			return false
		}
		if lnEnd == pos {
			pos += lnRel + 1
			headDone = true
			break
		}
		if buf[pos] == ' ' || buf[pos] == '\t' {
			return false
		}
		colonRel := bytes.IndexByte(buf[pos:lnEnd], ':')
		if colonRel <= 0 {
			return false
		}
		nameStart := pos
		nameLen := colonRel
		for i := range nameLen {
			if !isTchar(buf[nameStart+i]) {
				return false
			}
		}
		vs := nameStart + nameLen + 1
		ve := lnEnd
		for vs < ve && (buf[vs] == ' ' || buf[vs] == '\t') {
			vs++
		}
		for ve > vs && (buf[ve-1] == ' ' || buf[ve-1] == '\t') {
			ve--
		}
		for i := vs; i < ve; i++ {
			c := buf[i]
			if (c < 0x20 && c != '\t') || c == 0x7f {
				return false
			}
		}
		if nhdrs >= maxHeaders {
			return false
		}
		hdrs[nhdrs] = headerSpan{sliceSpan{nameStart, nameLen}, sliceSpan{vs, ve - vs}}
		nhdrs++

		name := buf[nameStart : nameStart+nameLen]
		val := buf[vs:ve]
		if nameLen == 4 && eqIgnoreASCII(name, "host") {
			if hasHost || len(val) == 0 {
				return false
			}
			hasHost = true
		} else if nameLen == 14 && eqIgnoreASCII(name, "content-length") {
			if len(val) == 0 {
				return false
			}
			v := 0
			for _, c := range val {
				if c < '0' || c > '9' {
					return false
				}
				v = v*10 + int(c-'0')
				if v > 1_073_741_824 {
					return false
				}
			}
			if hasCL && v != clVal {
				return false
			}
			hasCL = true
			clVal = v
		} else if nameLen == 17 && eqIgnoreASCII(name, "transfer-encoding") {
			if chunked || !eqIgnoreASCII(val, "chunked") {
				return false
			}
			chunked = true
		} else if nameLen == 10 && eqIgnoreASCII(name, "connection") {
			if eqIgnoreASCII(val, "close") {
				keepAlive = false
			} else if eqIgnoreASCII(val, "keep-alive") {
				keepAlive = true
			}
		}
		pos += lnRel + 1
	}

	if !headDone || !hasHost || (hasCL && chunked) {
		return false
	}
	out.nhdrs = nhdrs
	out.chunked = chunked
	out.keepAlive = keepAlive

	if chunked {
		*bodyBuf = (*bodyBuf)[:0]
		for {
			if pos >= n {
				return false
			}
			szRel := bytes.IndexByte(buf[pos:], '\n')
			if szRel < 1 {
				return false
			}
			szEnd := pos + szRel - 1
			if buf[szEnd] != '\r' {
				return false
			}
			sz := 0
			digits := 0
			for k := pos; k < szEnd; k++ {
				c := buf[k]
				if c == ';' {
					break
				}
				hv := hexVal(c)
				if hv < 0 {
					return false
				}
				sz = sz*16 + hv
				if sz > 16_777_216 {
					return false
				}
				digits++
			}
			if digits == 0 {
				return false
			}
			pos += szRel + 1
			if sz == 0 {
				if pos+2 != n || buf[pos] != '\r' || buf[pos+1] != '\n' {
					return false
				}
				break
			}
			if pos+sz+2 > n {
				return false
			}
			*bodyBuf = append(*bodyBuf, buf[pos:pos+sz]...)
			pos += sz
			if buf[pos] != '\r' || buf[pos+1] != '\n' {
				return false
			}
			pos += 2
		}
		out.bodyOff = 0
		out.bodyLen = len(*bodyBuf)
		out.bodyFromBuf = true
	} else if hasCL {
		if n-pos != clVal {
			return false
		}
		out.bodyOff = pos
		out.bodyLen = clVal
		out.bodyFromBuf = false
	} else {
		if pos != n {
			return false
		}
		out.bodyOff = 0
		out.bodyLen = 0
		out.bodyFromBuf = false
	}
	return true
}

func fnvByte(h uint64, b byte) uint64 { return (h ^ uint64(b)) * fnvPrime }

func fnvBytes(h uint64, s []byte) uint64 {
	for _, b := range s {
		h = (h ^ uint64(b)) * fnvPrime
	}
	return h
}

func fnvBytesLower(h uint64, s []byte) uint64 {
	for _, b := range s {
		h = (h ^ uint64(lowerASCII(b))) * fnvPrime
	}
	return h
}

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: httpparse <num_requests> <rounds> <seed>")
		os.Exit(2)
	}
	numReq, err1 := strconv.Atoi(os.Args[1])
	rounds, err2 := strconv.Atoi(os.Args[2])
	seed, err3 := strconv.ParseUint(os.Args[3], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil {
		fmt.Fprintln(os.Stderr, "httpparse: invalid arguments")
		os.Exit(2)
	}

	data, starts := buildBatch(numReq, seed)
	var hdrs [maxHeaders]headerSpan
	bodyBuf := make([]byte, 0, 1024)
	var parsed parsedReq

	var okCnt, errCnt, hdrCnt, bodyCnt, chunkedCnt, kaCnt int64
	h := fnvBasis

	for range rounds {
		for i := range numReq {
			reqBuf := data[starts[i]:starts[i+1]]
			if parseRequest(reqBuf, &hdrs, &bodyBuf, &parsed) {
				okCnt++
				hdrCnt += int64(parsed.nhdrs)
				bodyCnt += int64(parsed.bodyLen)
				if parsed.chunked {
					chunkedCnt++
				}
				if parsed.keepAlive {
					kaCnt++
				}
				h = fnvBytes(h, reqBuf[parsed.method.off:parsed.method.off+parsed.method.len])
				h = fnvByte(h, 1)
				h = fnvBytes(h, reqBuf[parsed.path.off:parsed.path.off+parsed.path.len])
				h = fnvByte(h, 2)
				h = fnvBytes(h, reqBuf[parsed.query.off:parsed.query.off+parsed.query.len])
				h = fnvByte(h, 3)
				for k := range parsed.nhdrs {
					hs := hdrs[k]
					h = fnvBytesLower(h, reqBuf[hs.name.off:hs.name.off+hs.name.len])
					h = fnvByte(h, 4)
					h = fnvBytes(h, reqBuf[hs.value.off:hs.value.off+hs.value.len])
					h = fnvByte(h, 5)
				}
				if parsed.bodyFromBuf {
					h = fnvBytes(h, bodyBuf)
				} else {
					h = fnvBytes(h, reqBuf[parsed.bodyOff:parsed.bodyOff+parsed.bodyLen])
				}
				h = fnvByte(h, 6)
			} else {
				errCnt++
				h = fnvByte(h, 0xff)
				h = fnvByte(h, byte(i%251))
			}
		}
	}

	fmt.Printf("ok=%d err=%d hdrs=%d body=%d chunked=%d keepalive=%d fnv=%016x\n",
		okCnt, errCnt, hdrCnt, bodyCnt, chunkedCnt, kaCnt, h)
}
