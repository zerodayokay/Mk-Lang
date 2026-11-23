// knucleotide, Rust: the reference algorithm of README.md (2-bit packed DNA
// k-mers in a fixed open-addressed linear-probing hash table, Fibonacci hash,
// 70% load factor). Wrapping u64 hashing and checksum (overflow class "wraps").
// Bounds-checked. No unsafe, no hints.
use std::env;
use std::process;

const IM: i64 = 139968;
const IA: i64 = 3877;
const IC: i64 = 29573;
const FIB_MUL: u64 = 0x9e3779b97f4a7c15;

#[derive(Clone, Copy)]
struct Entry {
    key: u64,
    val: u64,
}

struct Table {
    entries: Vec<Entry>,
    cap: usize,
    shift: u32,
    len: usize,
}

impl Table {
    fn new() -> Self {
        Self {
            entries: vec![Entry { key: 0, val: 0 }; 64],
            cap: 64,
            shift: 6,
            len: 0,
        }
    }

    fn grow(&mut self, rehashes: &mut u64) {
        if cfg!(bench_count) {
            *rehashes = rehashes.wrapping_add(1);
        }
        let new_cap = self.cap * 2;
        let new_shift = self.shift + 1;
        let mut next = vec![Entry { key: 0, val: 0 }; new_cap];
        let mask = new_cap - 1;
        let sh = 64 - new_shift;
        for i in 0..self.cap {
            let e = self.entries[i];
            if e.val != 0 {
                let mut slot = ((e.key.wrapping_mul(FIB_MUL)) >> sh) as usize & mask;
                loop {
                    if next[slot].val == 0 {
                        next[slot] = e;
                        break;
                    }
                    slot = (slot + 1) & mask;
                }
            }
        }
        self.entries = next;
        self.cap = new_cap;
        self.shift = new_shift;
    }

    fn inc(&mut self, key: u64, probes: &mut u64, rehashes: &mut u64) {
        if (self.len + 1) * 10 > self.cap * 7 {
            self.grow(rehashes);
        }
        let mask = self.cap - 1;
        let mut slot = ((key.wrapping_mul(FIB_MUL)) >> (64 - self.shift)) as usize & mask;
        loop {
            if cfg!(bench_count) {
                *probes = probes.wrapping_add(1);
            }
            let v = self.entries[slot].val;
            if v == 0 {
                self.entries[slot] = Entry { key, val: 1 };
                self.len += 1;
                return;
            }
            if self.entries[slot].key == key {
                self.entries[slot].val = v.wrapping_add(1);
                return;
            }
            slot = (slot + 1) & mask;
        }
    }

    fn get(&self, key: u64, probes: &mut u64) -> u64 {
        let mask = self.cap - 1;
        let mut slot = ((key.wrapping_mul(FIB_MUL)) >> (64 - self.shift)) as usize & mask;
        loop {
            if cfg!(bench_count) {
                *probes = probes.wrapping_add(1);
            }
            let v = self.entries[slot].val;
            if v == 0 {
                return 0;
            }
            if self.entries[slot].key == key {
                return v;
            }
            slot = (slot + 1) & mask;
        }
    }
}

fn pack_seq(s: &[u8]) -> u64 {
    let mut key: u64 = 0;
    for &c in s {
        let code: u64 = if c == b'A' {
            0
        } else if c == b'C' {
            1
        } else if c == b'G' {
            2
        } else {
            3
        };
        key = (key << 2) | code;
    }
    key
}

fn count_k(seq: &[u8], k: usize, probes: &mut u64, rehashes: &mut u64) -> u64 {
    let mut tab = Table::new();
    let n = seq.len();
    if n >= k {
        let mask = (1u64 << (2 * k)) - 1;
        let mut key: u64 = 0;
        for i in 0..k - 1 {
            key = (key << 2) | (seq[i] as u64);
        }
        for i in k - 1..n {
            key = ((key << 2) | (seq[i] as u64)) & mask;
            tab.inc(key, probes, rehashes);
        }
    }
    let mut h: u64 = tab.len as u64;
    let mut max_val: u64 = 0;
    for i in 0..tab.cap {
        let v = tab.entries[i].val;
        if v > max_val {
            max_val = v;
        }
    }
    h = h.wrapping_mul(31).wrapping_add(max_val);
    if k == 1 {
        for c in 0u64..4 {
            h = h.wrapping_mul(31).wrapping_add(tab.get(c, probes));
        }
    } else if k == 2 {
        for c in 0u64..16 {
            h = h.wrapping_mul(31).wrapping_add(tab.get(c, probes));
        }
    } else if k == 3 {
        h = h.wrapping_mul(31).wrapping_add(tab.get(pack_seq(b"GGT"), probes));
    } else if k == 4 {
        h = h.wrapping_mul(31).wrapping_add(tab.get(pack_seq(b"GGTA"), probes));
    } else if k == 6 {
        h = h.wrapping_mul(31).wrapping_add(tab.get(pack_seq(b"GGTATT"), probes));
    } else if k == 12 {
        h = h.wrapping_mul(31).wrapping_add(tab.get(pack_seq(b"GGTATTTTAATT"), probes));
    } else if k == 18 {
        h = h.wrapping_mul(31).wrapping_add(tab.get(pack_seq(b"GGTATTTTAATTTATAGT"), probes));
    }
    h
}

fn main() {
    let args: Vec<String> = env::args().collect();
    if args.len() != 3 {
        eprintln!("usage: knucleotide <n> <rounds>");
        process::exit(2);
    }
    let n: usize = args[1].parse().unwrap();
    let rounds: usize = args[2].parse().unwrap();
    let mut seq = vec![0u8; n];
    let p0: f64 = 0.3029549426680;
    let p1: f64 = p0 + 0.1979883004921;
    let p2: f64 = p1 + 0.1975473066391;
    let mut seed: i64 = 42;
    for i in 0..n {
        seed = (seed * IA + IC) % IM;
        let r = (seed as f64) / (IM as f64);
        if r < p0 {
            seq[i] = 0;
        } else if r < p1 {
            seq[i] = 1;
        } else if r < p2 {
            seq[i] = 2;
        } else {
            seq[i] = 3;
        }
    }
    let ks = [1usize, 2, 3, 4, 6, 12, 18];
    let mut probes: u64 = 0;
    let mut rehashes: u64 = 0;
    let mut total: u64 = 0;
    for _ in 0..rounds {
        for &k in &ks {
            total = total.wrapping_mul(31).wrapping_add(count_k(&seq, k, &mut probes, &mut rehashes));
        }
    }
    println!("{}", total);
    if cfg!(bench_count) {
        println!("counts probes={} rehashes={}", probes, rehashes);
    }
}
