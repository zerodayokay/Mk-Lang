// Why: compress.Inflater's resume paths are reached only where its input splits, which an OS pipe picks by chance.
// What: Inflater read through a Reader whose input pieces the test chooses (1 byte up, seeded), and Read after a failed lazy allocation.
// How: the piece Reader is a shim (czPieceShim) added to std/compress in a std copy, since only std can make a
// Reader; every answer must equal one-shot Inflate or Gunzip, offsets included.
//
//	go test ./std -run '^$' -fuzz FuzzSTDCOMPRESS_InflaterStream -fuzztime 5m -parallel 2

package std

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// czPieceShim is the std/compress file of the test std copy: PieceReader,
// a Reader with no inline buffer over data, whose every read returns the
// next 1 to maxp bytes (splitmix64 increments seeded by seed, as the test
// programs' output sizes), never more than the caller's buffer. Its state
// lives in the arena, so the Reader is from a and data.
const czPieceShim = `// Test shim (std-compress_pieces_test.go), not part of std.

import std.core
import std.raw

struct PieceSrc {
    p: *u8
    n: int
    pos: int
    s: u64
    maxp: int
}

const PieceTable = core.ReaderTable(read: PieceRead, line: core.ReadLineBytewise)

export PieceReader(a: &Arena, io: Io, data: []u8, seed: u64, maxp: int): OutOfMemory!Reader from a, data {
    st := try a.Slice[PieceSrc](1)
    st[0] = PieceSrc(p: raw.Data(data), n: data.Len(), pos: 0, s: seed, maxp: maxp)
    e := try a.Slice[u8](0)
    return core.NewReader(io, raw.Of(&PieceTable), raw.Addr(raw.DataMut(st)), e)
}

PieceRead(r: mut Reader, buf: mut []u8): ReadError!int {
    unsafe "ctx is the PieceSrc PieceReader put in its arena, alive while r is; p holds n bytes" {
        st := raw.FromAddr[PieceSrc](core.ReaderCtx(&*r))
        var v := raw.Read(st)
        left := v.n - v.pos
        if left == 0 || buf.Len() == 0 {
            return 0
        }
        v.s = v.s +% 0x9E3779B97F4A7C15
        var k := 1 + Trunc[int]((v.s >> 33) % Cast[u64](v.maxp))
        if k > left {
            k = left
        }
        if k > buf.Len() {
            k = buf.Len()
        }
        (mut buf[0..k]).CopyFrom(raw.Slice(raw.Offset(v.p, v.pos), k))
        v.pos += k
        raw.Write(st, v)
        return k
    }
}
`

// czPieceProg reads oracle records (op 1 raw, 2 gzip; the limit field is
// the seed) from stdin and streams each through an Inflater over a
// PieceReader of input pieces 1 to args[1] bytes, into output buffers of 1
// to args[2] bytes, printing czOracle's lines (and "readerror" or
// "overrun", which czParse refuses).
const czPieceProg = `import std.compress
import std.hash

Word(b: []u8, i: int, k: int): int {
    var x := 0
    for j in 0..k {
        x = x | (int(b[i + j]) << (8 * j))
    }
    return x
}

Stream(out: mut Writer, a: &Arena, io: Io, op: u8, data: []u8, seed: u64, maxp: int, maxo: int): WriteError|OutOfMemory!void {
    var r := try compress.PieceReader(a, io, data, seed, maxp)
    var z := compress.Inflater.New(a)
    if op == 2 {
        z = compress.Inflater.NewGzip(a)
    }
    buf := try a.Slice[u8](maxo)
    var s := seed ^ 0x2545F4914F6CDD1D
    var h := hash.Crc32State.New()
    var total := 0
    for {
        s = s +% 0x9E3779B97F4A7C15
        k := 1 + Trunc[int]((s >> 33) % Cast[u64](maxo))
        got := z.Read(mut r, mut buf[0..k]) else e {
            match e {
                compress.Corrupt(offset: o) {
                    try Print(out, "corrupt {o}\n", (o: o))
                }
                compress.Truncated {
                    try Print(out, "truncated\n")
                }
                compress.TooLarge {
                    try Print(out, "toolarge\n")
                }
                OutOfMemory {
                    try Print(out, "oom\n")
                }
                ReadError {
                    try Print(out, "readerror\n")
                }
                _ {
                    Panic("compress.Error gained a member this match does not handle")
                }
            }
            return
        }
        if got == 0 {
            break
        }
        if got > k {
            try Print(out, "overrun\n")
            return
        }
        h.Bytes(buf[0..got])
        total += got
    }
    c := h.Sum()
    try Print(out, "ok {total} {c:x}\n", (total: total, c: c))
}

export Main(sys: &System): WriteError|ReadError|OutOfMemory|ParseError!void {
    var out := sys.Stdout()
    var inp := sys.Stdin()
    maxp := Cast[int](try sys.args[1].ParseInt())
    maxo := Cast[int](try sys.args[2].ParseInt())
    var hdr: [13]u8 = [0; 13]
    var a := try Arena.New(sys.heap)
    defer a.Free()
    for {
        got := try inp.ReadAll(mut hdr[..])
        if got < 13 {
            return
        }
        n := Word(hdr[..], 9, 4)
        data := try a.Slice[u8](n)
        _ = try inp.ReadAll(data)
        try Stream(mut out, &a, sys.io, hdr[0], data[..], Cast[u64](Word(hdr[..], 1, 7)), maxp, maxo)
        try out.Flush()
        a.Reset()
    }
}
`

var czPieceExes struct {
	sync.Mutex
	exe string
}

// czPieceExe builds czPieceProg (a dev build, every check on) over a std
// copy with czPieceShim, once per test binary.
func czPieceExe(t *testing.T) string {
	t.Helper()
	czPieceExes.Lock()
	defer czPieceExes.Unlock()
	if czPieceExes.exe != "" {
		return czPieceExes.exe
	}
	dir, err := packageTempDir("mkcompresspieces")
	if err != nil {
		t.Fatal(err)
	}
	stdDir := czStdCopy(t, dir)
	if err := os.WriteFile(filepath.Join(stdDir, "compress", "testpieces.mk"), []byte(czPieceShim), 0o644); err != nil {
		t.Fatal(err)
	}
	// The shim's unsafe block needs std.compress in the copy's
	// unsafe_modules; the live std.compress has no unsafe.
	pkg := filepath.Join(stdDir, "pkg.mk")
	b, err := os.ReadFile(pkg)
	if err != nil {
		t.Fatal(err)
	}
	const list = "unsafe_modules: ["
	if !bytes.Contains(b, []byte(list)) {
		t.Fatalf("std/pkg.mk has no %q", list)
	}
	b = bytes.Replace(b, []byte(list), []byte(list+`"compress", `), 1)
	if err := os.WriteFile(pkg, b, 0o644); err != nil {
		t.Fatal(err)
	}
	czPieceExes.exe = czBuildStd(t, dir, stdDir, czPieceProg)
	return czPieceExes.exe
}

// czRunArgs is czRun with arguments for the program.
func czRunArgs(t *testing.T, exe string, recs []czRec, args ...string) []czRes {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Stdin = bytes.NewReader(czEncode(recs))
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("stream program killed after 5 minutes")
	}
	br := bufio.NewReader(&out)
	res := make([]czRes, 0, len(recs))
	for range recs {
		r, perr := czParse(br)
		if perr != nil {
			t.Fatalf("after %d answers (next record %s): %v\nexit: %v\nstderr:\n%s", len(res), recs[len(res)].name, perr, err, errb.String())
		}
		res = append(res, r)
	}
	if err != nil {
		t.Fatalf("stream program: %v\nstderr:\n%s", err, errb.String())
	}
	return res
}

// czStreamWant is what an Inflater must answer for each record, from Mk's
// own one-shot answers (ask runs the one-shot oracle): Gunzip's answer for
// gzip; for a raw stream Inflate's, except that Inflate refuses bytes after
// the final block as Corrupt at the first of them and the Inflater stops
// before them, so a Corrupt(o) whose prefix data[:o] inflates is that
// prefix's answer. A real bad element at o never inflates from data[:o]:
// the byte holding its first bit is gone, so that prefix is Truncated.
func czStreamWant(recs []czRec, ask func([]czRec) []czRes) []czRes {
	one := make([]czRec, len(recs))
	for i, r := range recs {
		one[i] = czRec{op: r.op, limit: czNoLimit, data: r.data, name: r.name}
	}
	want := ask(one)
	var pre []czRec
	var at []int
	for i, r := range recs {
		if r.op == czInflate && want[i].verdict == "corrupt" && want[i].offset >= 0 && want[i].offset <= len(r.data) {
			pre = append(pre, czRec{op: czInflate, limit: czNoLimit, data: r.data[:want[i].offset], name: r.name + " prefix"})
			at = append(at, i)
		}
	}
	if len(pre) > 0 {
		got := ask(pre)
		for j, i := range at {
			if got[j].verdict == "ok" {
				want[i] = got[j]
			}
		}
	}
	return want
}

// czWithSeeds gives every record a seed in its limit field (the piece
// program's seed), different per record.
func czWithSeeds(recs []czRec, seed uint64) []czRec {
	out := make([]czRec, len(recs))
	r := czRng{seed}
	for i, c := range recs {
		c.limit = int64(r.next() >> 9) // 55 bits: the program reads 7 bytes
		out[i] = c
	}
	return out
}

// czStreamCheck streams recs at every piece pattern and compares each
// answer with want exactly.
func czStreamCheck(t *testing.T, recs []czRec, want []czRes, patterns [][2]int) {
	t.Helper()
	exe := czPieceExe(t)
	for pi, pc := range patterns {
		got := czRunArgs(t, exe, czWithSeeds(recs, uint64(pi)+77), fmt.Sprint(pc[0]), fmt.Sprint(pc[1]))
		bad := 0
		for i, r := range recs {
			if got[i].String() != want[i].String() {
				t.Errorf("input pieces 1 to %d, output 1 to %d, %s (op %d, %d bytes: % x): streamed %v, one-shot %v",
					pc[0], pc[1], r.name, r.op, len(r.data), r.data[:min(len(r.data), 32)], got[i], want[i])
				if bad++; bad == 10 {
					t.Fatal("stopping after 10 differences")
				}
			}
		}
	}
}

// czBitWriter writes RFC 1951 bit fields, least significant bit first.
type czBitWriter struct {
	b    []byte
	acc  uint64
	nacc uint
}

func (w *czBitWriter) bits(v uint64, n uint) {
	w.acc |= v << w.nacc
	w.nacc += n
	for w.nacc >= 8 {
		w.b = append(w.b, byte(w.acc))
		w.acc >>= 8
		w.nacc -= 8
	}
}

// code writes a Huffman code of n bits, most significant bit first.
func (w *czBitWriter) code(c uint64, n uint) {
	var r uint64
	for i := uint(0); i < n; i++ {
		r = r<<1 | (c>>i)&1
	}
	w.bits(r, n)
}

func (w *czBitWriter) bytes() []byte {
	if w.nacc > 0 {
		return append(w.b, byte(w.acc))
	}
	return w.b
}

// czFixedLit writes literal c of the fixed code.
func (w *czBitWriter) fixedLit(c int) {
	if c < 144 {
		w.code(uint64(0x30+c), 8)
	} else {
		w.code(uint64(0x190+c-144), 9)
	}
}

// czLateErrors are raw streams whose bad element starts several bytes in,
// after literals, so with 1-byte pieces its first bits were loaded into the
// bit buffer before the refill that brings its last ones, and the element
// starts before the input buffer's first byte (a negative offset in the
// buffer after MoveDown): a fixed-code distance symbol 30 and 31, a
// litlen symbol 286 and 287, a distance past the start, a stored block
// whose NLEN is wrong after a fixed block, and a BTYPE 3 second block.
func czLateErrors() []czRec {
	var recs []czRec
	add := func(name string, f func(w *czBitWriter)) {
		var w czBitWriter
		f(&w)
		recs = append(recs, czRec{op: czInflate, limit: czNoLimit, data: w.bytes(), name: name})
	}
	lits := func(w *czBitWriter, n int) {
		for i := 0; i < n; i++ {
			w.fixedLit('a' + i%7)
		}
	}
	for _, n := range []int{0, 1, 2, 3, 5, 8, 13, 40} {
		for _, d := range []uint64{30, 31} {
			add(fmt.Sprintf("distance %d after %d literals", d, n), func(w *czBitWriter) {
				w.bits(1, 1)
				w.bits(1, 2)
				lits(w, n)
				w.code(1, 7) // length 3
				w.code(d, 5)
			})
		}
		for _, s := range []uint64{286, 287} {
			add(fmt.Sprintf("symbol %d after %d literals", s, n), func(w *czBitWriter) {
				w.bits(1, 1)
				w.bits(1, 2)
				lits(w, n)
				w.code(0xC0+s-280, 8)
			})
		}
		add(fmt.Sprintf("distance past the start after %d literals", n), func(w *czBitWriter) {
			w.bits(1, 1)
			w.bits(1, 2)
			lits(w, n)
			w.code(1, 7)
			w.code(29, 5) // distance 24577 to 32768, past every n here
			w.bits(0, 13)
		})
		add(fmt.Sprintf("bad NLEN after a fixed block of %d literals", n), func(w *czBitWriter) {
			w.bits(0, 1)
			w.bits(1, 2)
			lits(w, n)
			w.code(0, 7) // end of block
			w.bits(1, 1)
			w.bits(0, 2)
			w.bits(0, (8-w.nacc)%8)
			w.bits(3, 16)
			w.bits(3, 16) // NLEN must be ^3
			w.bits(0x616263, 24)
		})
		add(fmt.Sprintf("BTYPE 3 after a fixed block of %d literals", n), func(w *czBitWriter) {
			w.bits(0, 1)
			w.bits(1, 2)
			lits(w, n)
			w.code(0, 7)
			w.bits(1, 1)
			w.bits(3, 2)
			w.bits(0, 16)
		})
	}
	return recs
}

// TestSTDCOMPRESS_InflaterInputPieces: every seeded corruption of
// TestSTDCOMPRESS_CorruptVerdicts (raw and gzip), the hand edge vectors and
// the late errors, streamed with input pieces of 1 byte, 1 to 7 bytes and
// 1 to 3000 bytes (each with its own output sizes), give exactly Mk's
// one-shot answer, offsets included.
func TestSTDCOMPRESS_InflaterInputPieces(t *testing.T) {
	recs := czCorruptSet(t, 1, 6000)
	for _, v := range czVectors(t) {
		if v.rec.limit == czNoLimit {
			recs = append(recs, v.rec)
		}
	}
	recs = append(recs, czLateErrors()...)
	oracle := czOracleExe(t, "")
	want := czStreamWant(recs, func(r []czRec) []czRes { return czRun(t, oracle, r) })
	// The late errors are Corrupt, not Truncated: the vectors test what
	// they are named for.
	for i, r := range recs {
		if strings.Contains(r.name, "after") && want[i].verdict != "corrupt" {
			t.Errorf("%s: one-shot %v, want corrupt", r.name, want[i])
		}
	}
	czStreamCheck(t, recs, want, [][2]int{{1, 7}, {7, 300}, {3000, 70000}})
}

// TestSTDCOMPRESS_InflaterBigStreams: the long streams of
// TestSTDCOMPRESS_InflaterStreams and TestSTDCOMPRESS_GzipInflaterStreams'
// level set, raw and gzip, through 1 to 7 byte pieces (a refill every few
// bytes over a megabyte of output, so every element kind is split), equal
// to the one-shot answers.
func TestSTDCOMPRESS_InflaterBigStreams(t *testing.T) {
	text := czInput(2, 300000, 11)
	var recs []czRec
	for _, lv := range czLevels {
		recs = append(recs, czRec{op: czInflate, data: czDeflateGo(t, text, lv), name: fmt.Sprintf("raw text level %d", lv)})
		recs = append(recs, czRec{op: czGunzip, data: czGzipGo(t, text, lv), name: fmt.Sprintf("gzip text level %d", lv)})
	}
	recs = append(recs,
		czRec{op: czInflate, data: czDeflateGo(t, czInput(0, 200000, 12), 6), name: "random"},
		czRec{op: czInflate, data: czDeflateGo(t, czInput(3, 300000, 13), 6), name: "runs"},
		czRec{op: czInflate, data: czDeflateGo(t, czInput(4, 300000, 14), 9), name: "pattern"},
		czRec{op: czInflate, data: append(czDeflateGo(t, text, 6), "trailing bytes"...), name: "trailing"})
	for i := range recs {
		recs[i].limit = czNoLimit
	}
	oracle := czOracleExe(t, "")
	want := czStreamWant(recs, func(r []czRec) []czRes { return czRun(t, oracle, r) })
	for i, r := range recs {
		if want[i].verdict != "ok" {
			t.Fatalf("%s: one-shot %v", r.name, want[i])
		}
	}
	czStreamCheck(t, recs, want, [][2]int{{1, 70000}, {7, 1}, {7, 5000}})
}

// czSC3Prog calls Read three times over stdin and prints each answer.
const czSC3Prog = `import std.compress

Once(out: mut Writer, z: mut compress.Inflater, r: mut Reader, buf: mut []u8): WriteError!void {
    got := z.Read(r, buf) else e {
        match e {
            compress.Corrupt(offset: o) {
                try Print(out, "corrupt {o}\n", (o: o))
            }
            compress.Truncated {
                try Print(out, "truncated\n")
            }
            compress.TooLarge {
                try Print(out, "toolarge\n")
            }
            OutOfMemory {
                try Print(out, "oom\n")
            }
            ReadError {
                try Print(out, "readerror\n")
            }
            _ {
                Panic("compress.Error gained a member this match does not handle")
            }
        }
        return
    }
    try Print(out, "ok {got}\n", (got: got))
}

export Main(sys: &System): WriteError!void {
    var out := sys.Stdout()
    var inp := sys.Stdin()
    var buf: [64]u8 = [0; 64]
    var z := compress.Inflater.New(sys.arena)
    for _ in 0..3 {
        try Once(mut out, mut z, mut inp, mut buf[..])
    }
}
`

// TestSTDCOMPRESS_ReadAfterOOM: when the second of Read's two
// lazy allocations fails, Read fails with OutOfMemory, and so does every
// later Read: none returns 0, which is the end of a complete stream. The
// std copy's InCap is 2^62, so the input buffer cannot be allocated while
// the window can.
func TestSTDCOMPRESS_ReadAfterOOM(t *testing.T) {
	dir := t.TempDir()
	stdDir := czStdCopy(t, dir)
	p := filepath.Join(stdDir, "compress", "inflater.mk")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	const from = "const InCap = 16384"
	if !bytes.Contains(b, []byte(from)) {
		t.Fatalf("inflater.mk has no %q", from)
	}
	b = bytes.Replace(b, []byte(from), []byte("const InCap = 4611686018427387904"), 1)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	exe := czBuildStd(t, dir, stdDir, czSC3Prog)
	got := runNative(t, exe, string(czDeflateGo(t, []byte("hello, hello, hello"), 6)))
	if want := "oom\noom\noom\n"; got.stdout != want || got.code != 0 {
		t.Errorf("got %q (exit %d, stderr %s), want %q", got.stdout, got.code, got.stderr, want)
	}
}

// czPieceSrv is a running piece program (1-byte input pieces) for the fuzz
// target, with its pipes.
var czPieceSrv struct {
	sync.Mutex
	s *czServer
}

func czAskPieces(t *testing.T, recs []czRec) []czRes {
	czPieceSrv.Lock()
	defer czPieceSrv.Unlock()
	if czPieceSrv.s == nil {
		cmd := exec.Command(czPieceExe(t), "1", "300")
		in, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		out, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		var errb bytes.Buffer
		cmd.Stderr = &errb
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		czPieceSrv.s = &czServer{cmd: cmd, in: in, out: bufio.NewReader(out), errb: &errb}
	}
	s := czPieceSrv.s
	if _, err := s.in.Write(czEncode(recs)); err != nil {
		czPieceSrv.s = nil
		s.cmd.Wait()
		t.Fatalf("stream program died: %v\nstderr:\n%s", err, s.errb.String())
	}
	res := make([]czRes, 0, len(recs))
	for range recs {
		r, err := czParse(s.out)
		if err != nil {
			czPieceSrv.s = nil
			s.in.Close()
			s.cmd.Wait()
			t.Fatalf("stream program: %v\nstderr:\n%s", err, s.errb.String())
		}
		res = append(res, r)
	}
	return res
}

// FuzzSTDCOMPRESS_InflaterStream: the fuzz target of the piece Reader. Each input, as a raw
// stream and as gzip, streamed in 1-byte input pieces through Inflater
// (New and NewGzip) must give exactly the one-shot answer (czStreamWant),
// offsets included; a panic or a hang in either program fails it.
func FuzzSTDCOMPRESS_InflaterStream(f *testing.F) {
	for kind := 0; kind < 5; kind++ {
		b := czInput(kind, 200, 9)
		f.Add(czDeflateGo(f, b, 6))
		f.Add(czDeflateGo(f, b, 0))
		f.Add(czGzipGo(f, b, 6))
	}
	for _, r := range czLateErrors() {
		f.Add(r.data)
	}
	f.Add([]byte{})
	f.Add([]byte{0x24, 0x30, 0x00, 0x00})
	f.Fuzz(func(t *testing.T, data []byte) {
		recs := []czRec{
			{op: czInflate, limit: czNoLimit, data: data, name: "raw"},
			{op: czGunzip, limit: czNoLimit, data: data, name: "gzip"},
		}
		want := czStreamWant(recs, func(r []czRec) []czRes { return czAsk(t, r) })
		seeded := czWithSeeds(recs, binary.LittleEndian.Uint64(append(append([]byte{}, data...), make([]byte, 8)...)))
		got := czAskPieces(t, seeded)
		for i, r := range recs {
			if got[i].String() != want[i].String() {
				t.Errorf("%s (% x): streamed %v, one-shot %v", r.name, data[:min(len(data), 64)], got[i], want[i])
			}
		}
	})
}
