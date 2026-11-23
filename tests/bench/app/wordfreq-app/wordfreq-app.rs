// wordfreq-app, Rust, std: the task of README.md. Streams the input file
// through a 64 KiB read buffer, tokenizes ASCII words ([A-Za-z], folded to
// lowercase) carrying partial words across chunk boundaries, counts frequencies
// in std::collections::HashMap<String, i64> (SipHash-1-3, allocating String
// keys only on first insert via get_mut(&str)), sorts unique entries by
// (count desc, word asc) with sort_unstable_by, and writes total, unique and
// top-K lines to a 64 KiB BufWriter. Pure safe Rust (no unsafe, no hints).
// Usage: wordfreq-app <file> <top_k> [<max_words>].
use std::collections::HashMap;
use std::env;
use std::fs::File;
use std::io::{self, BufWriter, Read, Write};
use std::process;

fn record_word(counts: &mut HashMap<String, i64>, w: &str) {
    if let Some(c) = counts.get_mut(w) {
        *c += 1;
    } else {
        counts.insert(w.to_owned(), 1);
    }
}

fn main() {
    let args: Vec<String> = env::args().collect();
    if args.len() < 3 || args.len() > 4 {
        eprintln!("usage: wordfreq-app <file> <top_k> [<max_words>]");
        process::exit(2);
    }
    let top_k: usize = args[2].parse().expect("top_k");
    let limit: i64 = if args.len() == 4 {
        args[3].parse().expect("max_words")
    } else {
        -1
    };

    let mut f = File::open(&args[1]).unwrap_or_else(|e| {
        eprintln!("wordfreq-app: {}: {}", args[1], e);
        process::exit(1);
    });

    let mut counts: HashMap<String, i64> = HashMap::new();
    let mut buf = vec![0u8; 64 * 1024];
    let mut wbuf: Vec<u8> = Vec::with_capacity(256);
    let mut total: i64 = 0;
    let mut done = false;

    while !done {
        let n = f.read(&mut buf).unwrap_or_else(|e| {
            eprintln!("wordfreq-app: read: {}", e);
            process::exit(1);
        });
        if n == 0 {
            break;
        }
        let chunk = &buf[..n];
        let mut i = 0;
        while i < n {
            let c = chunk[i];
            if c.is_ascii_alphabetic() {
                if wbuf.is_empty() {
                    let start = i;
                    let mut has_upper = c.is_ascii_uppercase();
                    i += 1;
                    while i < n {
                        let b = chunk[i];
                        if b.is_ascii_lowercase() {
                            i += 1;
                        } else if b.is_ascii_uppercase() {
                            has_upper = true;
                            i += 1;
                        } else {
                            break;
                        }
                    }
                    if i == n {
                        for &b in &chunk[start..i] {
                            wbuf.push(b.to_ascii_lowercase());
                        }
                    } else if !has_upper {
                        let s = std::str::from_utf8(&chunk[start..i]).unwrap();
                        record_word(&mut counts, s);
                        total += 1;
                        if limit >= 0 && total >= limit {
                            done = true;
                            break;
                        }
                    } else {
                        wbuf.clear();
                        for &b in &chunk[start..i] {
                            wbuf.push(b.to_ascii_lowercase());
                        }
                        let s = std::str::from_utf8(&wbuf).unwrap();
                        record_word(&mut counts, s);
                        wbuf.clear();
                        total += 1;
                        if limit >= 0 && total >= limit {
                            done = true;
                            break;
                        }
                    }
                } else {
                    wbuf.push(c.to_ascii_lowercase());
                    i += 1;
                }
            } else {
                if !wbuf.is_empty() {
                    let s = std::str::from_utf8(&wbuf).unwrap();
                    record_word(&mut counts, s);
                    wbuf.clear();
                    total += 1;
                    if limit >= 0 && total >= limit {
                        done = true;
                        break;
                    }
                }
                i += 1;
            }
        }
    }
    if !done && !wbuf.is_empty() {
        let s = std::str::from_utf8(&wbuf).unwrap();
        record_word(&mut counts, s);
        total += 1;
    }

    let mut entries: Vec<(&str, i64)> = counts.iter().map(|(k, &v)| (k.as_str(), v)).collect();
    entries.sort_unstable_by(|a, b| b.1.cmp(&a.1).then_with(|| a.0.cmp(b.0)));

    let uniq = entries.len();
    let k = top_k.min(uniq);
    let stdout = io::stdout();
    let mut w = BufWriter::with_capacity(64 * 1024, stdout.lock());
    writeln!(w, "total {}", total).unwrap();
    writeln!(w, "unique {}", uniq).unwrap();
    for &(word, count) in &entries[..k] {
        writeln!(w, "{} {}", word, count).unwrap();
    }
    w.flush().unwrap();
}
