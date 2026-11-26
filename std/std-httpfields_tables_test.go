// Why: the HPACK static table and Huffman code are transcriptions of RFC
// 7541's appendices; one wrong row is a silent interop bug that Appendix C's
// examples, which use a fraction of the codes, would not catch.
//
// What: hfStatic (Appendix A) and hfHuffman (Appendix B), each typed once
// here; TestHttpFields_HuffmanTable, which regenerates
// http/fields/huffman_table.mk from hfHuffman and compares it byte for
// byte (-update rewrites it); TestHttpFields_StaticTable, which checks
// http/fields/hpack_static.mk against hfStatic; and hfHuffDecodeRef and
// hfHuffEncodeRef, a bit-at-a-time model the fuzz targets compare with.
//
// How: hfCheckCode proves the rows form a complete prefix code (each
// inserted into a binary tree without meeting a leaf, the Kraft sum exactly
// one, lengths from 5 to 30, EOS all ones). The decode table is the 4-bit
// state machine over the code tree's 256 inner nodes: for each node and
// nibble, the node reached, the symbol completed on the way, and whether
// the node reached may end a string (its path since the last symbol all
// ones and at most 7 bits, RFC 7541 5.2) or EOS was met.

package std

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

// hfTablePath and hfStaticPath are the generated table and the static
// table, relative to std/.
const (
	hfTablePath  = "http/fields/huffman_table.mk"
	hfStaticPath = "http/fields/hpack_static.mk"
)

// hfStatic is RFC 7541 Appendix A, Table 1: entry i is index i + 1.
var hfStatic = [61]struct{ name, value string }{
	{":authority", ""},                   // 1
	{":method", "GET"},                   // 2
	{":method", "POST"},                  // 3
	{":path", "/"},                       // 4
	{":path", "/index.html"},             // 5
	{":scheme", "http"},                  // 6
	{":scheme", "https"},                 // 7
	{":status", "200"},                   // 8
	{":status", "204"},                   // 9
	{":status", "206"},                   // 10
	{":status", "304"},                   // 11
	{":status", "400"},                   // 12
	{":status", "404"},                   // 13
	{":status", "500"},                   // 14
	{"accept-charset", ""},               // 15
	{"accept-encoding", "gzip, deflate"}, // 16
	{"accept-language", ""},              // 17
	{"accept-ranges", ""},                // 18
	{"accept", ""},                       // 19
	{"access-control-allow-origin", ""},  // 20
	{"age", ""},                          // 21
	{"allow", ""},                        // 22
	{"authorization", ""},                // 23
	{"cache-control", ""},                // 24
	{"content-disposition", ""},          // 25
	{"content-encoding", ""},             // 26
	{"content-language", ""},             // 27
	{"content-length", ""},               // 28
	{"content-location", ""},             // 29
	{"content-range", ""},                // 30
	{"content-type", ""},                 // 31
	{"cookie", ""},                       // 32
	{"date", ""},                         // 33
	{"etag", ""},                         // 34
	{"expect", ""},                       // 35
	{"expires", ""},                      // 36
	{"from", ""},                         // 37
	{"host", ""},                         // 38
	{"if-match", ""},                     // 39
	{"if-modified-since", ""},            // 40
	{"if-none-match", ""},                // 41
	{"if-range", ""},                     // 42
	{"if-unmodified-since", ""},          // 43
	{"last-modified", ""},                // 44
	{"link", ""},                         // 45
	{"location", ""},                     // 46
	{"max-forwards", ""},                 // 47
	{"proxy-authenticate", ""},           // 48
	{"proxy-authorization", ""},          // 49
	{"range", ""},                        // 50
	{"referer", ""},                      // 51
	{"refresh", ""},                      // 52
	{"retry-after", ""},                  // 53
	{"server", ""},                       // 54
	{"set-cookie", ""},                   // 55
	{"strict-transport-security", ""},    // 56
	{"transfer-encoding", ""},            // 57
	{"user-agent", ""},                   // 58
	{"vary", ""},                         // 59
	{"via", ""},                          // 60
	{"www-authenticate", ""},             // 61
}

// hfHuffman is RFC 7541 Appendix B: entry s is symbol s's code, right
// aligned, and its length in bits; 256 is EOS. The comment holds the row's
// bits as the RFC prints them.
var hfHuffman = [257]struct {
	code uint32
	bits int
}{
	{0x1ff8, 13},     // 0 1111111111000
	{0x7fffd8, 23},   // 1 11111111111111111011000
	{0xfffffe2, 28},  // 2 1111111111111111111111100010
	{0xfffffe3, 28},  // 3 1111111111111111111111100011
	{0xfffffe4, 28},  // 4 1111111111111111111111100100
	{0xfffffe5, 28},  // 5 1111111111111111111111100101
	{0xfffffe6, 28},  // 6 1111111111111111111111100110
	{0xfffffe7, 28},  // 7 1111111111111111111111100111
	{0xfffffe8, 28},  // 8 1111111111111111111111101000
	{0xffffea, 24},   // 9 111111111111111111101010
	{0x3ffffffc, 30}, // 10 111111111111111111111111111100
	{0xfffffe9, 28},  // 11 1111111111111111111111101001
	{0xfffffea, 28},  // 12 1111111111111111111111101010
	{0x3ffffffd, 30}, // 13 111111111111111111111111111101
	{0xfffffeb, 28},  // 14 1111111111111111111111101011
	{0xfffffec, 28},  // 15 1111111111111111111111101100
	{0xfffffed, 28},  // 16 1111111111111111111111101101
	{0xfffffee, 28},  // 17 1111111111111111111111101110
	{0xfffffef, 28},  // 18 1111111111111111111111101111
	{0xffffff0, 28},  // 19 1111111111111111111111110000
	{0xffffff1, 28},  // 20 1111111111111111111111110001
	{0xffffff2, 28},  // 21 1111111111111111111111110010
	{0x3ffffffe, 30}, // 22 111111111111111111111111111110
	{0xffffff3, 28},  // 23 1111111111111111111111110011
	{0xffffff4, 28},  // 24 1111111111111111111111110100
	{0xffffff5, 28},  // 25 1111111111111111111111110101
	{0xffffff6, 28},  // 26 1111111111111111111111110110
	{0xffffff7, 28},  // 27 1111111111111111111111110111
	{0xffffff8, 28},  // 28 1111111111111111111111111000
	{0xffffff9, 28},  // 29 1111111111111111111111111001
	{0xffffffa, 28},  // 30 1111111111111111111111111010
	{0xffffffb, 28},  // 31 1111111111111111111111111011
	{0x14, 6},        // 32 010100
	{0x3f8, 10},      // 33 1111111000
	{0x3f9, 10},      // 34 1111111001
	{0xffa, 12},      // 35 111111111010
	{0x1ff9, 13},     // 36 1111111111001
	{0x15, 6},        // 37 010101
	{0xf8, 8},        // 38 11111000
	{0x7fa, 11},      // 39 11111111010
	{0x3fa, 10},      // 40 1111111010
	{0x3fb, 10},      // 41 1111111011
	{0xf9, 8},        // 42 11111001
	{0x7fb, 11},      // 43 11111111011
	{0xfa, 8},        // 44 11111010
	{0x16, 6},        // 45 010110
	{0x17, 6},        // 46 010111
	{0x18, 6},        // 47 011000
	{0x0, 5},         // 48 00000
	{0x1, 5},         // 49 00001
	{0x2, 5},         // 50 00010
	{0x19, 6},        // 51 011001
	{0x1a, 6},        // 52 011010
	{0x1b, 6},        // 53 011011
	{0x1c, 6},        // 54 011100
	{0x1d, 6},        // 55 011101
	{0x1e, 6},        // 56 011110
	{0x1f, 6},        // 57 011111
	{0x5c, 7},        // 58 1011100
	{0xfb, 8},        // 59 11111011
	{0x7ffc, 15},     // 60 111111111111100
	{0x20, 6},        // 61 100000
	{0xffb, 12},      // 62 111111111011
	{0x3fc, 10},      // 63 1111111100
	{0x1ffa, 13},     // 64 1111111111010
	{0x21, 6},        // 65 100001
	{0x5d, 7},        // 66 1011101
	{0x5e, 7},        // 67 1011110
	{0x5f, 7},        // 68 1011111
	{0x60, 7},        // 69 1100000
	{0x61, 7},        // 70 1100001
	{0x62, 7},        // 71 1100010
	{0x63, 7},        // 72 1100011
	{0x64, 7},        // 73 1100100
	{0x65, 7},        // 74 1100101
	{0x66, 7},        // 75 1100110
	{0x67, 7},        // 76 1100111
	{0x68, 7},        // 77 1101000
	{0x69, 7},        // 78 1101001
	{0x6a, 7},        // 79 1101010
	{0x6b, 7},        // 80 1101011
	{0x6c, 7},        // 81 1101100
	{0x6d, 7},        // 82 1101101
	{0x6e, 7},        // 83 1101110
	{0x6f, 7},        // 84 1101111
	{0x70, 7},        // 85 1110000
	{0x71, 7},        // 86 1110001
	{0x72, 7},        // 87 1110010
	{0xfc, 8},        // 88 11111100
	{0x73, 7},        // 89 1110011
	{0xfd, 8},        // 90 11111101
	{0x1ffb, 13},     // 91 1111111111011
	{0x7fff0, 19},    // 92 1111111111111110000
	{0x1ffc, 13},     // 93 1111111111100
	{0x3ffc, 14},     // 94 11111111111100
	{0x22, 6},        // 95 100010
	{0x7ffd, 15},     // 96 111111111111101
	{0x3, 5},         // 97 00011
	{0x23, 6},        // 98 100011
	{0x4, 5},         // 99 00100
	{0x24, 6},        // 100 100100
	{0x5, 5},         // 101 00101
	{0x25, 6},        // 102 100101
	{0x26, 6},        // 103 100110
	{0x27, 6},        // 104 100111
	{0x6, 5},         // 105 00110
	{0x74, 7},        // 106 1110100
	{0x75, 7},        // 107 1110101
	{0x28, 6},        // 108 101000
	{0x29, 6},        // 109 101001
	{0x2a, 6},        // 110 101010
	{0x7, 5},         // 111 00111
	{0x2b, 6},        // 112 101011
	{0x76, 7},        // 113 1110110
	{0x2c, 6},        // 114 101100
	{0x8, 5},         // 115 01000
	{0x9, 5},         // 116 01001
	{0x2d, 6},        // 117 101101
	{0x77, 7},        // 118 1110111
	{0x78, 7},        // 119 1111000
	{0x79, 7},        // 120 1111001
	{0x7a, 7},        // 121 1111010
	{0x7b, 7},        // 122 1111011
	{0x7ffe, 15},     // 123 111111111111110
	{0x7fc, 11},      // 124 11111111100
	{0x3ffd, 14},     // 125 11111111111101
	{0x1ffd, 13},     // 126 1111111111101
	{0xffffffc, 28},  // 127 1111111111111111111111111100
	{0xfffe6, 20},    // 128 11111111111111100110
	{0x3fffd2, 22},   // 129 1111111111111111010010
	{0xfffe7, 20},    // 130 11111111111111100111
	{0xfffe8, 20},    // 131 11111111111111101000
	{0x3fffd3, 22},   // 132 1111111111111111010011
	{0x3fffd4, 22},   // 133 1111111111111111010100
	{0x3fffd5, 22},   // 134 1111111111111111010101
	{0x7fffd9, 23},   // 135 11111111111111111011001
	{0x3fffd6, 22},   // 136 1111111111111111010110
	{0x7fffda, 23},   // 137 11111111111111111011010
	{0x7fffdb, 23},   // 138 11111111111111111011011
	{0x7fffdc, 23},   // 139 11111111111111111011100
	{0x7fffdd, 23},   // 140 11111111111111111011101
	{0x7fffde, 23},   // 141 11111111111111111011110
	{0xffffeb, 24},   // 142 111111111111111111101011
	{0x7fffdf, 23},   // 143 11111111111111111011111
	{0xffffec, 24},   // 144 111111111111111111101100
	{0xffffed, 24},   // 145 111111111111111111101101
	{0x3fffd7, 22},   // 146 1111111111111111010111
	{0x7fffe0, 23},   // 147 11111111111111111100000
	{0xffffee, 24},   // 148 111111111111111111101110
	{0x7fffe1, 23},   // 149 11111111111111111100001
	{0x7fffe2, 23},   // 150 11111111111111111100010
	{0x7fffe3, 23},   // 151 11111111111111111100011
	{0x7fffe4, 23},   // 152 11111111111111111100100
	{0x1fffdc, 21},   // 153 111111111111111011100
	{0x3fffd8, 22},   // 154 1111111111111111011000
	{0x7fffe5, 23},   // 155 11111111111111111100101
	{0x3fffd9, 22},   // 156 1111111111111111011001
	{0x7fffe6, 23},   // 157 11111111111111111100110
	{0x7fffe7, 23},   // 158 11111111111111111100111
	{0xffffef, 24},   // 159 111111111111111111101111
	{0x3fffda, 22},   // 160 1111111111111111011010
	{0x1fffdd, 21},   // 161 111111111111111011101
	{0xfffe9, 20},    // 162 11111111111111101001
	{0x3fffdb, 22},   // 163 1111111111111111011011
	{0x3fffdc, 22},   // 164 1111111111111111011100
	{0x7fffe8, 23},   // 165 11111111111111111101000
	{0x7fffe9, 23},   // 166 11111111111111111101001
	{0x1fffde, 21},   // 167 111111111111111011110
	{0x7fffea, 23},   // 168 11111111111111111101010
	{0x3fffdd, 22},   // 169 1111111111111111011101
	{0x3fffde, 22},   // 170 1111111111111111011110
	{0xfffff0, 24},   // 171 111111111111111111110000
	{0x1fffdf, 21},   // 172 111111111111111011111
	{0x3fffdf, 22},   // 173 1111111111111111011111
	{0x7fffeb, 23},   // 174 11111111111111111101011
	{0x7fffec, 23},   // 175 11111111111111111101100
	{0x1fffe0, 21},   // 176 111111111111111100000
	{0x1fffe1, 21},   // 177 111111111111111100001
	{0x3fffe0, 22},   // 178 1111111111111111100000
	{0x1fffe2, 21},   // 179 111111111111111100010
	{0x7fffed, 23},   // 180 11111111111111111101101
	{0x3fffe1, 22},   // 181 1111111111111111100001
	{0x7fffee, 23},   // 182 11111111111111111101110
	{0x7fffef, 23},   // 183 11111111111111111101111
	{0xfffea, 20},    // 184 11111111111111101010
	{0x3fffe2, 22},   // 185 1111111111111111100010
	{0x3fffe3, 22},   // 186 1111111111111111100011
	{0x3fffe4, 22},   // 187 1111111111111111100100
	{0x7ffff0, 23},   // 188 11111111111111111110000
	{0x3fffe5, 22},   // 189 1111111111111111100101
	{0x3fffe6, 22},   // 190 1111111111111111100110
	{0x7ffff1, 23},   // 191 11111111111111111110001
	{0x3ffffe0, 26},  // 192 11111111111111111111100000
	{0x3ffffe1, 26},  // 193 11111111111111111111100001
	{0xfffeb, 20},    // 194 11111111111111101011
	{0x7fff1, 19},    // 195 1111111111111110001
	{0x3fffe7, 22},   // 196 1111111111111111100111
	{0x7ffff2, 23},   // 197 11111111111111111110010
	{0x3fffe8, 22},   // 198 1111111111111111101000
	{0x1ffffec, 25},  // 199 1111111111111111111101100
	{0x3ffffe2, 26},  // 200 11111111111111111111100010
	{0x3ffffe3, 26},  // 201 11111111111111111111100011
	{0x3ffffe4, 26},  // 202 11111111111111111111100100
	{0x7ffffde, 27},  // 203 111111111111111111111011110
	{0x7ffffdf, 27},  // 204 111111111111111111111011111
	{0x3ffffe5, 26},  // 205 11111111111111111111100101
	{0xfffff1, 24},   // 206 111111111111111111110001
	{0x1ffffed, 25},  // 207 1111111111111111111101101
	{0x7fff2, 19},    // 208 1111111111111110010
	{0x1fffe3, 21},   // 209 111111111111111100011
	{0x3ffffe6, 26},  // 210 11111111111111111111100110
	{0x7ffffe0, 27},  // 211 111111111111111111111100000
	{0x7ffffe1, 27},  // 212 111111111111111111111100001
	{0x3ffffe7, 26},  // 213 11111111111111111111100111
	{0x7ffffe2, 27},  // 214 111111111111111111111100010
	{0xfffff2, 24},   // 215 111111111111111111110010
	{0x1fffe4, 21},   // 216 111111111111111100100
	{0x1fffe5, 21},   // 217 111111111111111100101
	{0x3ffffe8, 26},  // 218 11111111111111111111101000
	{0x3ffffe9, 26},  // 219 11111111111111111111101001
	{0xffffffd, 28},  // 220 1111111111111111111111111101
	{0x7ffffe3, 27},  // 221 111111111111111111111100011
	{0x7ffffe4, 27},  // 222 111111111111111111111100100
	{0x7ffffe5, 27},  // 223 111111111111111111111100101
	{0xfffec, 20},    // 224 11111111111111101100
	{0xfffff3, 24},   // 225 111111111111111111110011
	{0xfffed, 20},    // 226 11111111111111101101
	{0x1fffe6, 21},   // 227 111111111111111100110
	{0x3fffe9, 22},   // 228 1111111111111111101001
	{0x1fffe7, 21},   // 229 111111111111111100111
	{0x1fffe8, 21},   // 230 111111111111111101000
	{0x7ffff3, 23},   // 231 11111111111111111110011
	{0x3fffea, 22},   // 232 1111111111111111101010
	{0x3fffeb, 22},   // 233 1111111111111111101011
	{0x1ffffee, 25},  // 234 1111111111111111111101110
	{0x1ffffef, 25},  // 235 1111111111111111111101111
	{0xfffff4, 24},   // 236 111111111111111111110100
	{0xfffff5, 24},   // 237 111111111111111111110101
	{0x3ffffea, 26},  // 238 11111111111111111111101010
	{0x7ffff4, 23},   // 239 11111111111111111110100
	{0x3ffffeb, 26},  // 240 11111111111111111111101011
	{0x7ffffe6, 27},  // 241 111111111111111111111100110
	{0x3ffffec, 26},  // 242 11111111111111111111101100
	{0x3ffffed, 26},  // 243 11111111111111111111101101
	{0x7ffffe7, 27},  // 244 111111111111111111111100111
	{0x7ffffe8, 27},  // 245 111111111111111111111101000
	{0x7ffffe9, 27},  // 246 111111111111111111111101001
	{0x7ffffea, 27},  // 247 111111111111111111111101010
	{0x7ffffeb, 27},  // 248 111111111111111111111101011
	{0xffffffe, 28},  // 249 1111111111111111111111111110
	{0x7ffffec, 27},  // 250 111111111111111111111101100
	{0x7ffffed, 27},  // 251 111111111111111111111101101
	{0x7ffffee, 27},  // 252 111111111111111111111101110
	{0x7ffffef, 27},  // 253 111111111111111111111101111
	{0x7fffff0, 27},  // 254 111111111111111111111110000
	{0x3ffffee, 26},  // 255 11111111111111111111101110
	{0x3fffffff, 30}, // 256 111111111111111111111111111111
}

// hfNode is an inner node of the code tree: each child is an inner node
// (at least 0) or a leaf, -1 - symbol.
type hfNode struct{ child [2]int }

// hfTree builds the code tree of hfHuffman, root 0, and checks that the
// rows form a complete prefix code.
func hfTree() ([]hfNode, error) {
	nodes := []hfNode{{child: [2]int{hfEmpty, hfEmpty}}}
	var kraft uint64
	for s, r := range hfHuffman {
		if r.bits < 5 || r.bits > 30 || r.code>>r.bits != 0 {
			return nil, fmt.Errorf("symbol %d: code %#x of %d bits", s, r.code, r.bits)
		}
		kraft += 1 << (30 - r.bits)
		n := 0
		for i := r.bits - 1; i >= 0; i-- {
			b := int(r.code>>i) & 1
			c := nodes[n].child[b]
			if c < 0 && c != hfEmpty {
				return nil, fmt.Errorf("symbol %d: its code runs through symbol %d's", s, -1-c)
			}
			if i == 0 {
				if c != hfEmpty {
					return nil, fmt.Errorf("symbol %d: its code is a prefix of another", s)
				}
				nodes[n].child[b] = -1 - s
				break
			}
			if c == hfEmpty {
				nodes = append(nodes, hfNode{child: [2]int{hfEmpty, hfEmpty}})
				c = len(nodes) - 1
				nodes[n].child[b] = c
			}
			n = c
		}
	}
	if kraft != 1<<30 {
		return nil, fmt.Errorf("Kraft sum %d / 2^30: the code is not complete", kraft)
	}
	if eos := hfHuffman[256]; eos.bits != 30 || eos.code != 1<<30-1 {
		return nil, errors.New("EOS is not 30 ones")
	}
	if len(nodes) != 256 {
		return nil, fmt.Errorf("%d inner nodes, want 256", len(nodes))
	}
	return nodes, nil
}

// hfEmpty marks a child not yet placed while the tree is built.
const hfEmpty = -1 << 20

// The flags of a decode-table entry, as the generated file names them.
const (
	hfEmit   = 1 << 16
	hfAccept = 1 << 17
	hfFail   = 1 << 18
)

// hfDecodeTable is the 256 × 16 decode table: entry 16 s + x is the step
// from inner node s on the four bits x, most significant first.
func hfDecodeTable(nodes []hfNode) []uint32 {
	// depth and ones: each inner node's distance from the root and whether
	// its path is all ones.
	depth := make([]int, len(nodes))
	ones := make([]bool, len(nodes))
	ones[0] = true
	for n := range nodes {
		for b, c := range nodes[n].child {
			if c >= 0 {
				depth[c] = depth[n] + 1
				ones[c] = ones[n] && b == 1
			}
		}
	}
	out := make([]uint32, 16*len(nodes))
	for s := range nodes {
		for x := 0; x < 16; x++ {
			cur, e := s, uint32(0)
			for i := 3; i >= 0; i-- {
				c := nodes[cur].child[(x>>i)&1]
				if c >= 0 {
					cur = c
					continue
				}
				sym := -1 - c
				if sym == 256 {
					e |= hfFail
					break
				}
				e |= hfEmit | uint32(sym)<<8
				cur = 0
			}
			if e&hfFail == 0 {
				e |= uint32(cur)
				if ones[cur] && depth[cur] <= 7 {
					e |= hfAccept
				}
			}
			out[16*s+x] = e
		}
	}
	return out
}

// hfArray writes a const array of values, per line, in mk fmt's layout.
func hfArray(b *strings.Builder, name, typ string, vals []string, per int) {
	fmt.Fprintf(b, "const %s: [%d]%s = [\n", name, len(vals), typ)
	for i := 0; i < len(vals); i += per {
		j := min(i+per, len(vals))
		fmt.Fprintf(b, "    %s,\n", strings.Join(vals[i:j], ", "))
	}
	b.WriteString("]\n")
}

// genHuffTable returns the text of http/fields/huffman_table.mk.
func genHuffTable(t *testing.T) string {
	t.Helper()
	nodes, err := hfTree()
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("// Generated by std/std-httpfields_tables_test.go from RFC 7541 Appendix B. Do not edit.\n")
	b.WriteString("// Regenerate with go test ./std -run TestHttpFields_HuffmanTable -update.\n")
	b.WriteString("//\n")
	b.WriteString("// Why: Huffman coding (RFC 7541 5.2) must cost no work per bit, so both\n")
	b.WriteString("// directions read tables.\n")
	b.WriteString("//\n")
	b.WriteString("// What: HuffCode and HuffBits, the code of each symbol (256 is EOS), and\n")
	b.WriteString("// HuffDec, the decoder's state machine, with its flags HuffEmit, HuffAccept\n")
	b.WriteString("// and HuffFail.\n")
	b.WriteString("//\n")
	b.WriteString("// How: a state is an inner node of the code tree, 0 the root. HuffDec[16 s +\n")
	b.WriteString("// x] is the step from state s on the four bits x, most significant first:\n")
	b.WriteString("// bits 0 to 7 the state reached, bits 8 to 15 the symbol completed on the\n")
	b.WriteString("// way when HuffEmit is set (the shortest code is 5 bits, so at most one),\n")
	b.WriteString("// HuffAccept when the state reached may end a string (its path since the\n")
	b.WriteString("// last symbol is at most 7 ones), HuffFail when the bits complete EOS.\n\n")
	codes := make([]string, 257)
	bits := make([]string, 257)
	for s, r := range hfHuffman {
		codes[s] = fmt.Sprintf("0x%x", r.code)
		bits[s] = fmt.Sprint(r.bits)
	}
	b.WriteString("// The code of each symbol, right aligned.\n")
	hfArray(&b, "HuffCode", "u32", codes, 12)
	b.WriteString("\n// The length of each symbol's code, in bits.\n")
	hfArray(&b, "HuffBits", "u8", bits, 24)
	b.WriteString("\n// The flags of a HuffDec entry.\n")
	fmt.Fprintf(&b, "const HuffEmit: u32 = 0x%x\nconst HuffAccept: u32 = 0x%x\nconst HuffFail: u32 = 0x%x\n", hfEmit, hfAccept, hfFail)
	dec := hfDecodeTable(nodes)
	steps := make([]string, len(dec))
	for i, e := range dec {
		steps[i] = fmt.Sprintf("0x%05x", e)
	}
	b.WriteString("\n// The decoder's steps, 16 per state.\n")
	hfArray(&b, "HuffDec", "u32", steps, 16)
	return b.String()
}

// TestHttpFields_HuffmanTable regenerates http/fields/huffman_table.mk from
// hfHuffman and compares it byte for byte; -update rewrites it.
func TestHttpFields_HuffmanTable(t *testing.T) {
	want := genHuffTable(t)
	if *update {
		if err := os.WriteFile(hfTablePath, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if got := readText(t, hfTablePath); got != want {
		t.Errorf("%s is not what hfHuffman gives; rerun go test ./std -run TestHttpFields_HuffmanTable -update and read the diff", hfTablePath)
	}
}

// hfStaticEntry matches one entry of hpack_static.mk's table.
var hfStaticEntry = regexp.MustCompile(`StaticEntry\(name: "([^"]*)", value: "([^"]*)"\)`)

// TestHttpFields_StaticTable: hpack_static.mk's 61 entries are Appendix A's,
// in order.
func TestHttpFields_StaticTable(t *testing.T) {
	ms := hfStaticEntry.FindAllStringSubmatch(readText(t, hfStaticPath), -1)
	if len(ms) != len(hfStatic) {
		t.Fatalf("%s holds %d static entries, want %d", hfStaticPath, len(ms), len(hfStatic))
	}
	for i, m := range ms {
		if m[1] != hfStatic[i].name || m[2] != hfStatic[i].value {
			t.Errorf("index %d: %q: %q, want %q: %q", i+1, m[1], m[2], hfStatic[i].name, hfStatic[i].value)
		}
	}
}

// TestHttpFields_HuffmanModel: the model encodes every symbol to its row
// and decodes it back, and refuses EOS, long padding and padding that is
// not all ones, so the fuzz targets compare with a model that is right.
func TestHttpFields_HuffmanModel(t *testing.T) {
	all := make([]byte, 256)
	for i := range all {
		all[i] = byte(i)
	}
	enc := hfHuffEncodeRef(all)
	if dec, err := hfHuffDecodeRef(enc); err != nil || string(dec) != string(all) {
		t.Fatalf("round trip of every byte: %v", err)
	}
	// "www.example.com" is RFC 7541 C.4.1's f1e3 c2e5 f23a 6ba0 ab90 f4ff.
	if got := fmt.Sprintf("%x", hfHuffEncodeRef([]byte("www.example.com"))); got != "f1e3c2e5f23a6ba0ab90f4ff" {
		t.Errorf("www.example.com: %s", got)
	}
	for _, bad := range [][]byte{{0xff, 0xff, 0xff, 0xff}, {0x1f, 0xff}, {0x00}} {
		if _, err := hfHuffDecodeRef(bad); err == nil {
			t.Errorf("%x decodes", bad)
		}
	}
}

// hfHuffEncodeRef is s Huffman coded, padded with ones, bit by bit.
func hfHuffEncodeRef(s []byte) []byte {
	var out []byte
	var cur byte
	n := 0
	for _, c := range s {
		r := hfHuffman[c]
		for i := r.bits - 1; i >= 0; i-- {
			cur = cur<<1 | byte(r.code>>i)&1
			n++
			if n == 8 {
				out = append(out, cur)
				cur, n = 0, 0
			}
		}
	}
	if n > 0 {
		out = append(out, cur<<(8-n)|byte(1<<(8-n)-1))
	}
	return out
}

// hfHuffDecodeRef decodes b bit by bit against the code tree, with RFC 7541
// 5.2's checks: EOS is an error, and the padding must be at most 7 bits,
// all ones.
func hfHuffDecodeRef(b []byte) ([]byte, error) {
	nodes, err := hfTree()
	if err != nil {
		return nil, err
	}
	var out []byte
	cur, depth, ones := 0, 0, true
	for _, c := range b {
		for i := 7; i >= 0; i-- {
			bit := int(c>>i) & 1
			depth++
			ones = ones && bit == 1
			n := nodes[cur].child[bit]
			if n >= 0 {
				cur = n
				continue
			}
			if -1-n == 256 {
				return nil, errors.New("EOS")
			}
			out = append(out, byte(-1-n))
			cur, depth, ones = 0, 0, true
		}
	}
	if depth > 7 || !ones {
		return nil, errors.New("bad padding")
	}
	return out, nil
}
