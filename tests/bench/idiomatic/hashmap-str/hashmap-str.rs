// hashmap-str, Rust, idiomatic std: the task of README.md with
// std::collections::HashMap<&str, u64> and its default hasher (SipHash-1-3
// under RandomState's random per-process key: DoS resistant); keys are
// 8-24 byte ASCII slices borrowed from one contiguous String buffer, and
// the map grows from empty each round.
use std::collections::HashMap;

const ALPH: &[u8] = b"abcdefghijklmnopqrstuvwxyz012345";

fn mix(mut z: u64) -> u64 {
    z = (z ^ (z >> 30)).wrapping_mul(0xbf58476d1ce4e5b9);
    z = (z ^ (z >> 27)).wrapping_mul(0x94d049bb133111eb);
    z ^ (z >> 31)
}

fn fill_key(buf: &mut Vec<u8>, idx: usize, tag: u8, st: &mut u64, seed: u64) -> (usize, usize) {
    let lo = buf.len();
    *st = st.wrapping_add(0x9e3779b97f4a7c15);
    let z = mix(*st);
    let length = 8 + (z % 17) as usize;
    let u = (idx as u64).wrapping_mul(0x9e3779b97f4a7c15) ^ seed.wrapping_mul(0x517cc1b727220a95);
    for j in 0..7 {
        buf.push(ALPH[((u >> (5 * j)) & 31) as usize]);
    }
    buf.push(tag);
    let mut w = z >> 8;
    for j in 8..length {
        if j == 16 {
            *st = st.wrapping_add(0x9e3779b97f4a7c15);
            w = mix(*st);
        }
        buf.push(ALPH[(w & 31) as usize]);
        w >>= 5;
    }
    (lo, buf.len())
}

fn round(keys: &[&str], misses: &[&str], r: u64) -> u64 {
    let mut m: HashMap<&str, u64> = HashMap::new();
    let n = keys.len();
    for i in 0..n {
        m.insert(keys[i], (i as u64).wrapping_add(r));
    }
    let mut sum: u64 = 0;
    for i in 0..n {
        let k = keys[i];
        if let Some(v) = m.get(k) {
            sum = sum.wrapping_add(*v);
        }
        if m.contains_key(misses[i]) {
            sum = sum.wrapping_add(1);
        }
        if i % 2 == 0 {
            m.remove(k);
        }
    }
    for &k in keys {
        if m.contains_key(k) {
            sum = sum.wrapping_add(3);
        }
    }
    sum.wrapping_add(m.len() as u64)
}

fn main() {
    let a: Vec<String> = std::env::args().collect();
    if a.len() != 4 {
        eprintln!("usage: hashmap-str <n> <rounds> <seed>");
        std::process::exit(2);
    }
    let n: usize = a[1].parse().unwrap();
    let rounds: u64 = a[2].parse().unwrap();
    let seed: u64 = a[3].parse().unwrap();
    let mut buf = Vec::with_capacity(2 * n * 24);
    let mut kspans = Vec::with_capacity(n);
    let mut mspans = Vec::with_capacity(n);
    let mut st = seed;
    for i in 0..n {
        kspans.push(fill_key(&mut buf, i, b'k', &mut st, seed));
    }
    for i in 0..n {
        mspans.push(fill_key(&mut buf, i, b'm', &mut st, seed));
    }
    let text = String::from_utf8(buf).unwrap();
    let keys: Vec<&str> = kspans.iter().map(|&(lo, hi)| &text[lo..hi]).collect();
    let misses: Vec<&str> = mspans.iter().map(|&(lo, hi)| &text[lo..hi]).collect();
    let mut total: u64 = 0;
    for r in 0..rounds {
        total = total.wrapping_add(round(&keys, &misses, r));
    }
    println!("{}", total);
}
