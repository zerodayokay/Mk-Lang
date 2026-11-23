// comphash, Rust, eco flate2 and sha2 (Cargo.lock pins the shared vendored
// crate tree): the task of ../../README.md. Pass 1 reads the input file in
// 64 KiB chunks, streaming SHA-256 with sha2::Sha256 and compressing with
// flate2::write::ZlibEncoder (RFC 1950, Compression::new(6), default pure-Rust
// miniz_oxide backend) through a 64 KiB BufWriter over the output file. Pass 2
// opens the output file through a 64 KiB BufReader and drives flate2::Decompress
// with the zlib header (checking FCHECK/FDICT, Adler-32 trailer and explicit
// Status::StreamEnd so truncated streams fail) into 64 KiB chunks while
// streaming a second sha2::Sha256, verifies length and SHA-256 match Pass 1,
// and prints "<bytes> <hex-sha256>".
// Usage: comphash <in> <out>.
use flate2::write::ZlibEncoder;
use flate2::{Compression, Decompress, FlushDecompress, Status};
use sha2::{Digest, Sha256};
use std::fs::File;
use std::io::{BufRead, BufReader, BufWriter, Read, Write};
use std::process::exit;

const CHUNK_SIZE: usize = 64 << 10;
const HEX: &[u8; 16] = b"0123456789abcdef";

fn die(what: &str) -> ! {
    eprintln!("comphash: {what}");
    exit(1);
}

fn compress_and_hash(in_path: &str, out_path: &str, buf: &mut [u8]) -> (u64, [u8; 32]) {
    let mut src = File::open(in_path).unwrap_or_else(|e| die(&format!("open input: {e}")));
    let dst = File::create(out_path).unwrap_or_else(|e| die(&format!("create output: {e}")));
    let mut enc = ZlibEncoder::new(BufWriter::with_capacity(CHUNK_SIZE, dst), Compression::new(6));
    let mut sha = Sha256::new();
    let mut total: u64 = 0;
    loop {
        let n = src.read(buf).unwrap_or_else(|e| die(&format!("read input: {e}")));
        if n == 0 {
            break;
        }
        sha.update(&buf[..n]);
        enc.write_all(&buf[..n]).unwrap_or_else(|e| die(&format!("compress write: {e}")));
        total += n as u64;
    }
    let w = enc.finish().unwrap_or_else(|e| die(&format!("compress finish: {e}")));
    w.into_inner().unwrap_or_else(|e| die(&format!("flush output: {}", e.into_error())));
    let mut digest = [0u8; 32];
    digest.copy_from_slice(&sha.finalize());
    (total, digest)
}

fn decompress_and_hash(in_path: &str, out: &mut [u8]) -> (u64, [u8; 32]) {
    let src = File::open(in_path).unwrap_or_else(|e| die(&format!("open compressed: {e}")));
    let mut r = BufReader::with_capacity(CHUNK_SIZE, src);
    let mut z = Decompress::new(true);
    let mut sha = Sha256::new();
    loop {
        let input = r.fill_buf().unwrap_or_else(|e| die(&format!("read compressed: {e}")));
        let eof = input.is_empty();
        let (before_in, before_out) = (z.total_in(), z.total_out());
        let status = z
            .decompress(input, out, FlushDecompress::None)
            .unwrap_or_else(|e| die(&format!("inflate: {e}")));
        let used = (z.total_in() - before_in) as usize;
        let made = (z.total_out() - before_out) as usize;
        r.consume(used);
        if made > 0 {
            sha.update(&out[..made]);
        }
        match status {
            Status::StreamEnd => break,
            _ if eof && made == 0 => die("the zlib stream is truncated"),
            _ => {}
        }
    }
    let mut digest = [0u8; 32];
    digest.copy_from_slice(&sha.finalize());
    (z.total_out(), digest)
}

fn main() {
    let args: Vec<String> = std::env::args().collect();
    if args.len() != 3 {
        eprintln!("usage: comphash <in> <out>");
        exit(2);
    }
    let mut buf = vec![0u8; CHUNK_SIZE];
    let (in_total, in_digest) = compress_and_hash(&args[1], &args[2], &mut buf);
    let (out_total, out_digest) = decompress_and_hash(&args[2], &mut buf);
    if out_total != in_total {
        die("decompressed length differs from input length");
    }
    if out_digest != in_digest {
        die("decompressed SHA-256 differs from input SHA-256");
    }
    let mut hex = String::with_capacity(64);
    for &b in &in_digest {
        hex.push(HEX[(b >> 4) as usize] as char);
        hex.push(HEX[(b & 15) as usize] as char);
    }
    println!("{in_total} {hex}");
}
