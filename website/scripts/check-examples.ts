// Checks the Mk examples on the hand-written pages against a real compiler.
//
//   1. `mk corpus` judges every `mk` block by its verdict line, as the
//      repository's test suite does for doc/: an `ok` block must compile, a
//      `reject` block must fail with exactly the listed codes.
//   2. Every `ok` program with a `Main` that is followed by its output is
//      built and run, and what it prints is compared with the page.
//
// The pages generated from doc/ are already checked by the repository's own
// tests (`mk corpus`), so only the site's own pages are judged here.
//
// Usage: npm run check:examples   (needs mk on PATH, or MK=/path/to/mk)
//
// Output blocks may be a plain `text` block (the program's output when run
// with no arguments) or a shell session:
//
//   $ mk run wordfreq.mk -- poem.txt
//   ...output...
//   $ echo $?
//   1
//
// A `text <name>` block before a program is a file the program reads; it is
// written next to the program before it runs.

import { spawnSync } from 'node:child_process'
import { cpSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, relative, resolve } from 'node:path'
import { REPO_ROOT, SRC_DIR } from '../.vitepress/sync.ts'

// A path is resolved now, since the checks run in other directories.
const MK = process.env.MK?.includes('/') ? resolve(process.env.MK) : (process.env.MK ?? 'mk')

interface Fence {
  lang: string
  meta: string
  content: string
  /** 1-based line of the opening fence */
  line: number
}

function fences(markdown: string): Fence[] {
  const out: Fence[] = []
  const lines = markdown.split('\n')
  for (let i = 0; i < lines.length; i++) {
    const open = /^(`{3,})(\S*)\s*(.*)$/.exec(lines[i])
    if (!open) continue
    const body: string[] = []
    let j = i + 1
    for (; j < lines.length && !(lines[j].startsWith(open[1]) && lines[j].trim() === open[1]); j++) body.push(lines[j])
    out.push({ lang: open[2], meta: open[3].trim(), content: body.join('\n'), line: i + 1 })
    i = j
  }
  return out
}

function pages(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const path = join(dir, e.name)
    if (e.isDirectory()) return e.name === 'public' ? [] : pages(path)
    if (!e.name.endsWith('.md')) return []
    return readFileSync(path, 'utf8').includes('<!-- Generated from') ? [] : [path]
  })
}

function run(cmd: string, args: string[], cwd: string) {
  const r = spawnSync(cmd, args, { cwd, encoding: 'utf8' })
  if (r.error) throw new Error(`cannot run ${cmd}: ${r.error.message}. Set MK to the mk executable.`)
  return r
}

const tidy = (s: string) =>
  s
    .replace(/\r\n/g, '\n')
    .split('\n')
    .map((l) => l.trimEnd())
    .join('\n')
    .replace(/^\n+|\n+$/g, '')

const failures: string[] = []
const sources = pages(SRC_DIR)
const work = mkdtempSync(join(tmpdir(), 'mk-site-examples-'))

try {
  // 1. The corpus judge. It needs a repository root: doc/reference.md holds
  //    the test prelude. Pages are flattened into doc/site/ so one pattern
  //    selects them all.
  const root = join(work, 'root')
  mkdirSync(join(root, 'doc', 'site'), { recursive: true })
  cpSync(join(REPO_ROOT, 'doc', 'reference.md'), join(root, 'doc', 'reference.md'))
  const flat = new Map<string, string>()
  for (const page of sources) {
    const name = relative(SRC_DIR, page).replace(/[\\/]/g, '__')
    flat.set(`doc/site/${name}`, relative(SRC_DIR, page))
    writeFileSync(join(root, 'doc', 'site', name), readFileSync(page, 'utf8'))
  }
  const corpus = run(MK, ['corpus', '--root', root, '-only', 'doc/site/*', '-v'], root)
  const verdicts = [...corpus.stdout.matchAll(/^(doc\/site\/\S+):(\d+) (\S+(?: E\d{4})*) (PASS|FAIL.*)$/gm)]
  let judged = 0
  for (const [, doc, line, verdict, result] of verdicts) {
    judged++
    if (!result.startsWith('PASS')) failures.push(`${flat.get(doc)}:${line}: ${verdict} ${result}`)
  }
  if (corpus.status !== 0 && failures.length === 0) {
    failures.push(`mk corpus failed:\n${corpus.stdout}\n${corpus.stderr}`)
  }
  console.log(`corpus: ${judged} blocks judged`)

  // 2. Run the programs whose output the page shows.
  let ran = 0
  let skipped = 0
  for (const page of sources) {
    const rel = relative(SRC_DIR, page)
    const dir = join(work, 'run', rel.replace(/[\\/]/g, '__'))
    mkdirSync(dir, { recursive: true })
    const fs = fences(readFileSync(page, 'utf8'))
    fs.forEach((f, i) => {
      if (f.lang === 'text' && f.meta) writeFileSync(join(dir, f.meta), f.content + '\n')
      const next = fs[i + 1]
      if (f.lang !== 'mk' || !f.content.startsWith('// verdict: ok') || !/^export Main\(/m.test(f.content)) return
      if (!next || next.lang !== 'text' || next.meta) return
      // Only an output block written directly after the program counts.
      const where = `${rel}:${f.line}`
      const sessions = next.content.startsWith('$ ') ? next.content.split(/^(?=\$ )/m) : [`$ mk run main.mk\n${next.content}`]
      let file = 'main.mk'
      let status: number | null = null
      for (const session of sessions) {
        const [command, ...expected] = session.split('\n')
        const runCmd = /^\$ mk run (\S+\.mk)(?: -- (.*))?$/.exec(command)
        if (runCmd) {
          file = runCmd[1]
          writeFileSync(join(dir, file), f.content)
          const args = runCmd[2] ? runCmd[2].split(/\s+/) : []
          // stdout and stderr share one pipe, so they interleave as on a terminal.
          const script = 'mk="$1"; file="$2"; shift 2; if [ $# -gt 0 ]; then "$mk" run "$file" -- "$@"; else "$mk" run "$file"; fi 2>&1'
          const r = spawnSync('/bin/sh', ['-c', script, 'sh', MK, file, ...args], { cwd: dir, encoding: 'utf8' })
          status = r.status
          const got = tidy(r.stdout)
          const want = tidy(expected.join('\n'))
          if (got !== want) failures.push(`${where}: output differs\n--- page\n${want}\n--- mk run ${file}\n${got}`)
          ran++
        } else if (command === '$ echo $?') {
          if (String(status) !== tidy(expected.join('\n'))) {
            failures.push(`${where}: exit status ${status}, page says ${tidy(expected.join('\n'))}`)
          }
        } else {
          skipped++
          return
        }
      }
    })
  }
  console.log(`run: ${ran} programs run, ${skipped} sessions skipped`)
} finally {
  rmSync(work, { recursive: true, force: true })
}

if (failures.length) {
  console.error(`\n${failures.length} problem(s):\n`)
  for (const f of failures) console.error(f + '\n')
  process.exit(1)
}
console.log('all examples match the compiler')
