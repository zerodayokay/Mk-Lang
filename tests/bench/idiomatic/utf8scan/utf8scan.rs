// utf8scan, Rust, idiomatic std: the task of README.md with std::str::from_utf8
// validation and str::chars decoding over a generated mixed-script UTF-8 buffer
// (ASCII, Latin-1, Greek, Cyrillic, CJK, and 4-byte emoji), folding codepoint
// count and scalar sum each round and toggling one ASCII letter between rounds.
const UNIT: &str = "The quick brown fox jumps over 42 lazy dogs! Caf\u{00e9} na\u{00ef}ve r\u{00e9}sum\u{00e9} \u{00fc}ber\n\u{0391}\u{03bb}\u{03b3}\u{03cc}\u{03c1}\u{03b9}\u{03b8}\u{03bc}\u{03bf}\u{03c2} \u{03ba}\u{03b1}\u{03b9} \u{041f}\u{0440}\u{0438}\u{0432}\u{0435}\u{0442} \u{043c}\u{0438}\u{0440} 2026\n\u{65e5}\u{672c}\u{8a9e}\u{30c6}\u{30ad}\u{30b9}\u{30c8} \u{4e2d}\u{6587}\u{6d4b}\u{8bd5} \u{d55c}\u{ad6d}\u{c5b4} \u{6c34}\u{306f}\u{51b7}\u{305f}\u{3044}\nStatus: OK \u{1f600}\u{1f680}\u{1f30d}\u{1f525}\u{2728}\u{1f4bb}\u{1f389} end-of-record.\n";

fn main() {
    let a: Vec<String> = std::env::args().collect();
    if a.len() != 3 {
        eprintln!("usage: utf8scan <n> <rounds>");
        std::process::exit(2);
    }
    let n: usize = a[1].parse().unwrap();
    let rounds: usize = a[2].parse().unwrap();
    let ub = UNIT.as_bytes();
    let ulen = ub.len();
    let mut buf = Vec::with_capacity(n * ulen);
    for _ in 0..n {
        buf.extend_from_slice(ub);
    }
    let mut total: u64 = 0;
    for r in 0..rounds {
        let s = std::str::from_utf8(&buf).unwrap();
        let mut count: u64 = 0;
        let mut cpsum: u64 = 0;
        for c in s.chars() {
            count = count.wrapping_add(1);
            cpsum = cpsum.wrapping_add(c as u64);
        }
        total = total.wrapping_add(cpsum).wrapping_add(count);
        let idx = (r % n) * ulen;
        buf[idx] ^= 0x20;
    }
    println!("{}", total);
}
