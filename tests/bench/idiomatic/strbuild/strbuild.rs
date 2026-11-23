// strbuild, Rust, idiomatic std: the task of README.md with std::string::String
// growing from empty per document, appending literals, slices, repeated chunks
// and formatted integers via std::fmt::Write, then hashing the built bytes
// with FNV-1a 64 plus their length.
use std::fmt::Write;

const WORDS: [&str; 16] = [
    "alpha", "bravo", "charlie", "delta",
    "echo", "foxtrot", "golf", "hotel",
    "india", "juliet", "kilo", "lima",
    "mike", "november", "oscar", "papa",
];

const SRC: &str = "the quick brown fox jumps over the lazy dog 0123456789 ABCDEFGHIJKLMNOPQRSTUVWXYZ ";

fn fnv(b: &[u8]) -> u64 {
    let mut h: u64 = 0xcbf29ce484222325;
    for &c in b {
        h = (h ^ c as u64).wrapping_mul(0x100000001b3);
    }
    h
}

fn build_doc(d: usize, lines: usize) -> u64 {
    let mut s = String::new();
    for i in 0..lines {
        let x = d * 131 + i * 17;
        s.push_str("rec[");
        let _ = write!(&mut s, "{}", i);
        s.push_str("] ");
        s.push_str(WORDS[x & 15]);
        s.push_str(":");
        let off = (x * 7) % 40;
        let slen = 8 + (x % 25);
        s.push_str(&SRC[off..off + slen]);
        let reps = (i & 3) + 1;
        for _ in 0..reps {
            s.push_str("-=");
        }
        s.push_str(" #");
        let _ = write!(&mut s, "{}", x);
        s.push_str("\n");
    }
    let b = s.as_bytes();
    fnv(b).wrapping_add(b.len() as u64)
}

fn main() {
    let a: Vec<String> = std::env::args().collect();
    if a.len() != 3 {
        eprintln!("usage: strbuild <docs> <lines>");
        std::process::exit(2);
    }
    let docs: usize = a[1].parse().unwrap();
    let lines: usize = a[2].parse().unwrap();
    let mut total: u64 = 0;
    for d in 0..docs {
        total = total.wrapping_add(build_doc(d, lines));
    }
    println!("{}", total);
}
