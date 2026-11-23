// fasta, Rust: the reference algorithm of README.md (the Benchmarks Game's
// fasta generator, hashed by line into an i64 checksum). Bounds-checked;
// rust wraps on overflow, rust-ovf traps. No unsafe, no hints.
use std::env;
use std::process;

const IM: i64 = 139968;
const IA: i64 = 3877;
const IC: i64 = 29573;
const LINE_LEN: usize = 60;

#[derive(Clone, Copy)]
struct Freq {
    ch: u8,
    p: f64,
}

fn make_cumulative(tab: &mut [Freq]) {
    let mut cp: f64 = 0.0;
    for e in tab.iter_mut() {
        cp = cp + e.p;
        e.p = cp;
    }
    let last = tab.len() - 1;
    tab[last].p = 1.0;
}

fn next_rand(seed: &mut i64) -> f64 {
    *seed = (*seed * IA + IC) % IM;
    (*seed as f64) / (IM as f64)
}

fn line_sum(buf: &[u8], m: usize) -> i64 {
    let mut s: i64 = 0;
    for k in 0..m {
        s += (buf[k] as i64) * ((k + 1) as i64);
    }
    s
}

fn repeat_fasta(alu: &[u8], count: i64) -> i64 {
    let mut buf = [0u8; 61];
    let alu_len = alu.len();
    let mut pos: usize = 0;
    let mut rem: i64 = count;
    let mut total: i64 = 0;
    while rem > 0 {
        let m = if rem < LINE_LEN as i64 { rem as usize } else { LINE_LEN };
        for k in 0..m {
            buf[k] = alu[pos];
            pos += 1;
            if pos == alu_len {
                pos = 0;
            }
        }
        buf[m] = 10;
        total += line_sum(&buf, m + 1);
        rem -= m as i64;
    }
    total
}

fn random_fasta(tab: &[Freq], count: i64, seed: &mut i64, probes: &mut u64) -> i64 {
    let mut buf = [0u8; 61];
    let mut rem: i64 = count;
    let mut total: i64 = 0;
    while rem > 0 {
        let m = if rem < LINE_LEN as i64 { rem as usize } else { LINE_LEN };
        for k in 0..m {
            let r = next_rand(seed);
            let mut j: usize = 0;
            loop {
                if cfg!(bench_count) {
                    *probes += 1;
                }
                if tab[j].p >= r {
                    break;
                }
                j += 1;
            }
            buf[k] = tab[j].ch;
        }
        buf[m] = 10;
        total += line_sum(&buf, m + 1);
        rem -= m as i64;
    }
    total
}

fn main() {
    let args: Vec<String> = env::args().collect();
    if args.len() != 2 {
        eprintln!("usage: fasta <n>");
        process::exit(2);
    }
    let n: i64 = args[1].parse().unwrap();
    let alu = b"GGCCGGGCGCGGTGGCTCACGCCTGTAATCCCAGCACTTTGGGAGGCCGAGGCGGGCGGATCACCTGAGGTCAGGAGTTCGAGACCAGCCTGGCCAACATGGTGAAACCCCGTCTCTACTAAAAATACAAAAATTAGCCGGGCGTGGTGGCGCGCGCCTGTAATCCCAGCTACTCGGGAGGCTGAGGCAGGAGAATCGCTTGAACCCGGGAGGCGGAGGTTGCAGTGAGCCGAGATCGCGCCACTGCACTCCAGCCTGGGCGACAGAGCGAGACTCCGTCTCAAAAA";
    let mut iub = [
        Freq { ch: b'a', p: 0.27 }, Freq { ch: b'c', p: 0.12 }, Freq { ch: b'g', p: 0.12 }, Freq { ch: b't', p: 0.27 },
        Freq { ch: b'B', p: 0.02 }, Freq { ch: b'D', p: 0.02 }, Freq { ch: b'H', p: 0.02 }, Freq { ch: b'K', p: 0.02 },
        Freq { ch: b'M', p: 0.02 }, Freq { ch: b'N', p: 0.02 }, Freq { ch: b'R', p: 0.02 }, Freq { ch: b'S', p: 0.02 },
        Freq { ch: b'V', p: 0.02 }, Freq { ch: b'W', p: 0.02 }, Freq { ch: b'Y', p: 0.02 },
    ];
    let mut homo = [
        Freq { ch: b'a', p: 0.3029549426680 },
        Freq { ch: b'c', p: 0.1979883004921 },
        Freq { ch: b'g', p: 0.1975473066391 },
        Freq { ch: b't', p: 0.3015094502008 },
    ];
    make_cumulative(&mut iub);
    make_cumulative(&mut homo);
    let mut seed: i64 = 42;
    let mut probes: u64 = 0;
    let mut total: i64 = 0;
    total += repeat_fasta(alu, 2 * n);
    total += random_fasta(&iub, 3 * n, &mut seed, &mut probes);
    total += random_fasta(&homo, 5 * n, &mut seed, &mut probes);
    println!("{}", total);
    if cfg!(bench_count) {
        println!("counts probes={}", probes);
    }
}
