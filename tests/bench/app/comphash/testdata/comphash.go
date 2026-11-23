// comphash, Go, std: the task of ../README.md. Pass 1 reads the input file in
// 64 KiB chunks, hashing each chunk with crypto/sha256 and compressing it with
// compress/zlib at level 6 through a 64 KiB bufio.Writer over the output file.
// Pass 2 opens the output file through a 64 KiB bufio.Reader, decompresses it
// with compress/zlib (checking header, stream end and Adler-32 trailer) in
// 64 KiB chunks while hashing with a second crypto/sha256, verifies length and
// SHA-256 match Pass 1, and prints "<bytes> <hex-sha256>".
// Usage: comphash <in> <out>.
package main

import (
	"bufio"
	"compress/zlib"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
)

const chunkSize = 64 << 10

func compressAndHash(inPath, outPath string, buf []byte) (int64, [32]byte, error) {
	var zero [32]byte
	src, err := os.Open(inPath)
	if err != nil {
		return 0, zero, err
	}
	defer src.Close()
	dst, err := os.Create(outPath)
	if err != nil {
		return 0, zero, err
	}
	w := bufio.NewWriterSize(dst, chunkSize)
	zw, err := zlib.NewWriterLevel(w, 6)
	if err != nil {
		dst.Close()
		return 0, zero, err
	}
	h := sha256.New()
	var total int64
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			h.Write(chunk)
			if _, werr := zw.Write(chunk); werr != nil {
				dst.Close()
				return 0, zero, werr
			}
			total += int64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			dst.Close()
			return 0, zero, rerr
		}
	}
	if err := zw.Close(); err != nil {
		dst.Close()
		return 0, zero, err
	}
	if err := w.Flush(); err != nil {
		dst.Close()
		return 0, zero, err
	}
	if err := dst.Close(); err != nil {
		return 0, zero, err
	}
	var sum [32]byte
	h.Sum(sum[:0])
	return total, sum, nil
}

func decompressAndHash(inPath string, buf []byte) (int64, [32]byte, error) {
	var zero [32]byte
	src, err := os.Open(inPath)
	if err != nil {
		return 0, zero, err
	}
	defer src.Close()
	zr, err := zlib.NewReader(bufio.NewReaderSize(src, chunkSize))
	if err != nil {
		return 0, zero, err
	}
	h := sha256.New()
	var total int64
	for {
		n, rerr := zr.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			total += int64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			zr.Close()
			return 0, zero, rerr
		}
	}
	if err := zr.Close(); err != nil {
		return 0, zero, err
	}
	var sum [32]byte
	h.Sum(sum[:0])
	return total, sum, nil
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: comphash <in> <out>")
		os.Exit(2)
	}
	buf := make([]byte, chunkSize)
	inTotal, inSum, err := compressAndHash(os.Args[1], os.Args[2], buf)
	if err != nil {
		die(err)
	}
	outTotal, outSum, err := decompressAndHash(os.Args[2], buf)
	if err != nil {
		die(err)
	}
	if outTotal != inTotal {
		die(errors.New("decompressed length differs from input length"))
	}
	if outSum != inSum {
		die(errors.New("decompressed SHA-256 differs from input SHA-256"))
	}
	fmt.Printf("%d %s\n", inTotal, hex.EncodeToString(inSum[:]))
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "comphash:", err)
	os.Exit(1)
}
