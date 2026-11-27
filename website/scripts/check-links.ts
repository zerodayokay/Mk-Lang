// Checks every internal link in the built site, anchors included.
//
// VitePress fails the build on a link to a missing page, but not on a link
// to a missing heading. This reads every page in .vitepress/dist, collects
// the ids each one defines, and checks every internal href and #fragment.
//
// Usage: npm run build && npm run check:links

import { existsSync, readdirSync, readFileSync } from 'node:fs'
import { join, relative } from 'node:path'
import { WEBSITE_DIR } from '../.vitepress/sync.ts'

const DIST = join(WEBSITE_DIR, '.vitepress', 'dist')
const BASE = process.env.MK_DOCS_BASE ?? '/'

if (!existsSync(DIST)) {
  console.error('No build found. Run `npm run build` first.')
  process.exit(1)
}

function htmlFiles(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const path = join(dir, e.name)
    if (e.isDirectory()) return e.name === 'assets' ? [] : htmlFiles(path)
    return e.name.endsWith('.html') ? [path] : []
  })
}

/** The page a site path is served from, with clean URLs. */
function pageFor(path: string): string | null {
  const rel = decodeURIComponent(path.slice(BASE.length))
  const file = rel === '' || rel.endsWith('/') ? `${rel}index.html` : rel.endsWith('.html') ? rel : `${rel}.html`
  const full = join(DIST, file)
  return existsSync(full) ? full : null
}

const ids = new Map<string, Set<string>>()
function idsOf(file: string): Set<string> {
  let set = ids.get(file)
  if (!set) {
    set = new Set([...readFileSync(file, 'utf8').matchAll(/\sid="([^"]+)"/g)].map((m) => m[1]))
    ids.set(file, set)
  }
  return set
}

const problems: string[] = []
const files = htmlFiles(DIST)
let checked = 0

for (const file of files) {
  const html = readFileSync(file, 'utf8')
  const page = '/' + relative(DIST, file).replace(/\\/g, '/')
  for (const [, raw] of html.matchAll(/<a\s[^>]*href="([^"]*)"/g)) {
    const href = raw.replace(/&amp;/g, '&')
    if (/^(https?:|mailto:|tel:|javascript:|data:)/.test(href) || href === '') continue
    checked++
    // Relative links stay relative in the HTML: resolve them as a browser would.
    const url = new URL(href, `http://site${BASE}${page.slice(1).replace(/(^|\/)index\.html$/, '$1').replace(/\.html$/, '')}`)
    const path = href.startsWith('#') ? '' : url.pathname
    const hash = url.hash ? url.hash.slice(1) : undefined
    let target: string | null = file
    if (path !== '') {
      if (!path.startsWith(BASE)) {
        problems.push(`${page}: link outside the site base: ${href}`)
        continue
      }
      target = pageFor(path)
      if (!target) {
        problems.push(`${page}: no page for ${href}`)
        continue
      }
    }
    if (hash && !idsOf(target).has(decodeURIComponent(hash))) {
      problems.push(`${page}: no #${hash} on ${'/' + relative(DIST, target)}`)
    }
  }
}

const unique = [...new Set(problems)]
console.log(`checked ${checked} internal links on ${files.length} pages`)
if (unique.length) {
  console.error(`\n${unique.length} broken link(s):`)
  for (const p of unique) console.error(`  ${p}`)
  process.exit(1)
}
console.log('every internal link and anchor resolves')
