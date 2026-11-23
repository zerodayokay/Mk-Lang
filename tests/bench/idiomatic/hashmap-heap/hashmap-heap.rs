// hashmap-heap, Rust, idiomatic std: the task of README.md with
// std::collections::HashMap<u64, u64> on the default global allocator
// (SipHash-1-3 under RandomState); the map grows from empty.
use std::collections::HashMap;

fn mix(mut z: u64) -> u64 {
    z = (z ^ (z >> 30)).wrapping_mul(0xbf58476d1ce4e5b9);
    z = (z ^ (z >> 27)).wrapping_mul(0x94d049bb133111eb);
    z ^ (z >> 31)
}

fn round(n: usize, seed: u64) -> u64 {
    let mut m: HashMap<u64, u64> = HashMap::new();
    let mut st = seed;
    for i in 0..n {
        st = st.wrapping_add(0x9e3779b97f4a7c15);
        m.insert(mix(st), i as u64);
    }
    let mut sum: u64 = 0;
    st = seed;
    for i in 0..n {
        st = st.wrapping_add(0x9e3779b97f4a7c15);
        let k = mix(st);
        if let Some(v) = m.get(&k) {
            sum = sum.wrapping_add(*v);
        }
        if m.contains_key(&mix(st ^ 0x5555555555555555)) {
            sum = sum.wrapping_add(1);
        }
        if i % 2 == 0 {
            m.remove(&k);
        }
    }
    st = seed;
    for _ in 0..n {
        st = st.wrapping_add(0x9e3779b97f4a7c15);
        if m.contains_key(&mix(st)) {
            sum = sum.wrapping_add(3);
        }
    }
    sum.wrapping_add(m.len() as u64)
}

fn main() {
    let a: Vec<String> = std::env::args().collect();
    if a.len() != 4 {
        eprintln!("usage: hashmap-heap <n> <rounds> <seed>");
        std::process::exit(2);
    }
    let n: usize = a[1].parse().unwrap();
    let rounds: u64 = a[2].parse().unwrap();
    let seed: u64 = a[3].parse().unwrap();
    let mut total: u64 = 0;
    for r in 0..rounds {
        total = total.wrapping_add(round(n, seed.wrapping_add(r)));
    }
    println!("{}", total);
}
