// Renders the repository's own Markdown documentation (doc/, CONTRIBUTING.md,
// CHANGELOG.md, kits/web/README.md) into website pages, so the reference
// material has one source of truth: the files the compiler's test suite checks.
//
// For each source it:
//   - pins every heading to the anchor GitHub gives it ({#slug}), so links
//     written for GitHub keep working on the site;
//   - splits doc/reference.md into one page per numbered section;
//   - rewrites links between documents to site URLs, and links to other
//     repository files to GitHub;
//   - adds frontmatter: title, description and the source path for the
//     "Edit this page" link.
//
// The generated pages are git-ignored. Edit the sources, not the output.

import { existsSync, mkdirSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { dirname, join, posix, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

export const REPO_URL = 'https://github.com/zerodayokay/Mk-Lang'
export const REPO_BRANCH = 'main'

const here = dirname(fileURLToPath(import.meta.url))
export const WEBSITE_DIR = resolve(here, '..')
export const REPO_ROOT = resolve(WEBSITE_DIR, '..')
export const SRC_DIR = join(WEBSITE_DIR, 'src')

export interface PageInfo {
  /** Site URL without extension, e.g. /reference/std/fs */
  link: string
  /** Page title as it appears in the sidebar */
  title: string
  /** Repository-relative source path */
  source: string
}

export interface Manifest {
  /** doc/reference.md, one page per numbered section, in order */
  language: PageInfo[]
  /** doc/std/<name>.md keyed by <name> */
  std: Record<string, PageInfo>
}

// ---------------------------------------------------------------------------
// Where each source document goes

interface Source {
  source: string
  /** Output path under src/, without .md */
  page: string
  /** Override the document's own H1 for the page title */
  title?: string
}

const STD_MODULES = [
  'atomic', 'big', 'build', 'cli', 'coll', 'compress', 'core', 'crypto', 'csv', 'derive', 'dl', 'dyn',
  'encoding', 'free', 'fs', 'hash', 'http', 'json', 'libc', 'linux', 'log', 'math', 'mem', 'mmio', 'net',
  'proc', 'raw', 'regex', 'runtime', 'sim', 'simd', 'sqlite', 'sync', 'term', 'testing', 'text', 'time',
  'tls', 'url', 'vm', 'websocket', 'x509',
] as const

const SOURCES: Source[] = [
  { source: 'doc/memory.md', page: 'reference/memory', title: 'Memory model' },
  { source: 'doc/errors.md', page: 'reference/errors', title: 'Error codes' },
  { source: 'doc/grammar.md', page: 'reference/grammar', title: 'Grammar' },
  { source: 'doc/std/README.md', page: 'reference/std/index', title: 'Standard library' },
  ...STD_MODULES.map((m) => ({ source: `doc/std/${m}.md`, page: `reference/std/${m}` })),
  { source: 'kits/web/README.md', page: 'reference/kits/web', title: 'web kit' },
  { source: 'doc/tools.md', page: 'tools/index', title: 'The mk command' },
  { source: 'doc/packages.md', page: 'tools/packages', title: 'Packages' },
  { source: 'doc/editors.md', page: 'tools/editors', title: 'Editors' },
  { source: 'doc/cookbook.md', page: 'examples/cookbook', title: 'Cookbook' },
  { source: 'doc/examples.md', page: 'examples/programs', title: 'Complete programs' },
  { source: 'doc/style.md', page: 'guide/style', title: 'Code style' },
  { source: 'doc/codebase.md', page: 'guide/codebase', title: 'Organising a codebase' },
  { source: 'doc/rationale.md', page: 'guide/rationale', title: 'Why Mk looks like this' },
  { source: 'doc/compiler.md', page: 'development/compiler', title: 'The compiler' },
  { source: 'CONTRIBUTING.md', page: 'development/contributing', title: 'Contributing' },
  { source: 'CHANGELOG.md', page: 'development/changelog', title: 'Changelog' },
  { source: 'doc/design/macos-aarch64.md', page: 'development/design/macos-aarch64' },
]

const REFERENCE_SOURCE = 'doc/reference.md'
const REFERENCE_DIR = 'reference/language'

/** Directories that hold only generated pages, emptied before each sync so no stale page survives. */
const GENERATED_DIRS = ['reference/language', 'reference/std', 'reference/kits', 'tools', 'development/design']

/**
 * Documents the site does not render, mapped to the hand-written pages that
 * cover them. The tour (doc/overview.md) is split across the guide; the
 * README's sections live in the introduction.
 */
const REDIRECTS: Record<string, { page: string; anchors: Record<string, string> }> = {
  'doc/overview.md': {
    page: '/guide/',
    anchors: {
      'hello-world': '/guide/quick-start#hello-world',
      'comments': '/guide/syntax#comments',
      'variables-and-constants': '/guide/variables',
      'basic-types': '/guide/types',
      'strings-and-characters': '/guide/strings',
      'formatting': '/guide/strings#formatting',
      'arrays-slices-and-lists': '/guide/collections',
      'maps': '/guide/collections#maps',
      'control-flow': '/guide/control-flow',
      'if': '/guide/control-flow#if',
      'for': '/guide/control-flow#for',
      'match': '/guide/control-flow#match',
      'defer': '/guide/control-flow#defer',
      'functions': '/guide/functions',
      'structs-enums-and-records': '/guide/structs-and-enums',
      'methods-and-interfaces': '/guide/methods-and-interfaces',
      'generics': '/guide/generics',
      'optionals-and-errors': '/guide/errors',
      'panics': '/guide/errors#panics',
      'memory': '/guide/memory',
      'arenas': '/guide/memory#arenas',
      'the-heap': '/guide/memory#the-heap',
      'plain-unique-and-linear-values': '/guide/memory#plain-unique-and-linear-values',
      'borrowing': '/guide/memory#borrowing',
      'where-results-come-from': '/guide/memory#where-results-come-from',
      'views': '/guide/memory#views',
      'concurrency': '/guide/concurrency',
      'modules-packages-and-imports': '/guide/modules',
      'testing': '/guide/testing',
      'compile-time-code': '/guide/compile-time',
      'generators': '/guide/compile-time#generators',
      'unsafe-code-and-c': '/guide/unsafe-and-c',
      'habits-from-other-languages': '/reference/language/habits-from-other-languages',
    },
  },
  'README.md': {
    page: '/guide/',
    anchors: {
      'hello-world': '/guide/quick-start#hello-world',
      'getting-started': '/guide/installation',
      'install': '/guide/installation#install',
      'building-from-source': '/guide/installation#building-from-source',
      'editor-support': '/tools/editors',
      'platforms': '/guide/installation#platforms',
      'contributing': '/development/contributing',
    },
  },
  'doc/README.md': { page: '/reference/', anchors: {} },
}

// ---------------------------------------------------------------------------
// Slugs

/** The anchor GitHub gives a heading: github-slugger's algorithm. */
export function githubSlug(text: string): string {
  return text
    .toLowerCase()
    .replace(/[^\p{L}\p{M}\p{N}\p{Pc} -]/gu, '')
    .replace(/ /g, '-')
}

/** The text a heading renders to, which is what GitHub slugs. */
function headingText(raw: string): string {
  return raw
    .replace(/\[([^\]]*)\]\([^)]*\)/g, '$1') // links keep their text
    .replace(/`([^`]*)`/g, '$1') // code spans keep their content
    .replace(/(\*\*|__)(.*?)\1/g, '$2')
    .replace(/<[^>]+>/g, '')
    .trim()
}

class Slugger {
  private seen = new Map<string, number>()
  slug(text: string): string {
    const base = githubSlug(text)
    let slug = base
    let n = this.seen.get(base) ?? 0
    while (this.seen.has(slug)) slug = `${base}-${++n}`
    this.seen.set(base, n)
    this.seen.set(slug, 0)
    return slug
  }
}

// ---------------------------------------------------------------------------
// Markdown structure

interface Line {
  text: string
  /** Inside a fenced code block (fence lines included) */
  code: boolean
}

function classify(markdown: string): Line[] {
  const out: Line[] = []
  let fence: string | null = null
  for (const text of markdown.split('\n')) {
    if (fence === null) {
      const open = /^ {0,3}(`{3,}|~{3,})/.exec(text)
      if (open) {
        fence = open[1]
        out.push({ text, code: true })
        continue
      }
      out.push({ text, code: false })
    } else {
      out.push({ text, code: true })
      const close = /^ {0,3}(`{3,}|~{3,})\s*$/.exec(text)
      if (close && close[1][0] === fence[0] && close[1].length >= fence.length) fence = null
    }
  }
  return out
}

const HEADING = /^(#{1,6})\s+(.*?)\s*$/

interface Heading {
  index: number
  level: number
  text: string
  slug: string
}

function headings(lines: Line[]): Heading[] {
  const slugger = new Slugger()
  const out: Heading[] = []
  lines.forEach((line, index) => {
    if (line.code) return
    const m = HEADING.exec(line.text)
    if (!m) return
    const text = headingText(m[2])
    out.push({ index, level: m[1].length, text, slug: slugger.slug(text) })
  })
  return out
}

// ---------------------------------------------------------------------------
// Links

/** Where a document's anchors land on the site. */
interface Target {
  page: string
  /** For a split document: the page each anchor is on */
  anchorPages?: Map<string, string>
  /** For a redirected document: anchor -> full site link */
  redirects?: Record<string, string>
}

function blobUrl(repoPath: string): string {
  const full = join(REPO_ROOT, repoPath)
  const kind = existsSync(full) && statSync(full).isDirectory() ? 'tree' : 'blob'
  return `${REPO_URL}/${kind}/${REPO_BRANCH}/${repoPath.replace(/\/$/, '')}`
}

function resolveLink(href: string, fromSource: string, targets: Map<string, Target>, missing: string[]): string {
  if (/^[a-z][a-z0-9+.-]*:/i.test(href) || href.startsWith('//')) return href

  const hashAt = href.indexOf('#')
  const pathPart = hashAt === -1 ? href : href.slice(0, hashAt)
  const anchor = hashAt === -1 ? '' : decodeURIComponent(href.slice(hashAt + 1))

  const repoPath = pathPart === ''
    ? fromSource
    : posix.normalize(posix.join(posix.dirname(fromSource), pathPart))

  const target = targets.get(repoPath)
  if (!target) {
    if (repoPath.startsWith('..')) {
      missing.push(`${fromSource}: link leaves the repository: ${href}`)
      return href
    }
    return blobUrl(repoPath) + (anchor ? `#${anchor}` : '')
  }
  if (target.redirects) {
    if (!anchor) return target.page
    const to = target.redirects[anchor]
    if (!to) {
      missing.push(`${fromSource}: no site page for ${repoPath}#${anchor}`)
      return target.page
    }
    return to
  }
  if (!anchor) return target.page
  const page = target.anchorPages?.get(anchor) ?? target.page
  return `${page}#${anchor}`
}

/** Rewrites inline links and link definitions in prose, leaving code alone. */
function rewriteLinks(text: string, rewrite: (href: string) => string): string {
  // Code spans are left untouched: split them out first.
  const parts = text.split(/(`+[^`]*`+)/)
  return parts
    .map((part, i) => {
      if (i % 2 === 1) return part
      return part
        .replace(/\]\(([^()\s]+)\)/g, (_m, href: string) => `](${rewrite(href)})`)
        .replace(/^(\s*\[[^\]]+\]:\s*)(\S+)/, (_m, lead: string, href: string) => lead + rewrite(href))
    })
    .join('')
}

// ---------------------------------------------------------------------------
// Rendering

/**
 * Gives each row of the error catalogue (`| E0502 | name | ...`) an anchor,
 * so a page can link to one code: /reference/errors#e0502.
 */
function anchorErrorCode(text: string): string {
  return text.replace(/^\| (E\d{4}) \|/, (_m, code: string) => `| <span id="${code.toLowerCase()}"></span>${code} |`)
}

function plainText(markdown: string): string {
  return headingText(markdown)
    .replace(/[*_]{1,2}([^*_]+)[*_]{1,2}/g, '$1')
    .replace(/\s+/g, ' ')
    .trim()
}

function firstParagraph(lines: Line[], from: number): string {
  const para: string[] = []
  for (let i = from; i < lines.length; i++) {
    const { text, code } = lines[i]
    if (code || /^\s*(#|\||[-*+] |\d+\. |>|<)/.test(text)) {
      if (para.length) break
      continue
    }
    if (text.trim() === '') {
      if (para.length) break
      continue
    }
    para.push(text.trim())
  }
  const plain = plainText(para.join(' '))
  if (plain.length <= 180) return plain
  const cut = plain.slice(0, 177)
  return cut.slice(0, cut.lastIndexOf(' ')) + '…'
}

function frontmatter(fields: Record<string, string | number | boolean | number[]>): string {
  const body = Object.entries(fields)
    .map(([k, v]) => `${k}: ${JSON.stringify(v)}`)
    .join('\n')
  return `---\n${body}\n---\n\n`
}

const GENERATED_NOTE = (source: string) =>
  `<!-- Generated from ${source} by website/.vitepress/sync.ts. Edit the source, not this file. -->\n\n`

/**
 * Renders a run of lines: headings get their pinned anchors and are shifted
 * by `shift` levels, links are rewritten.
 */
function renderBody(
  lines: Line[],
  from: number,
  to: number,
  slugs: Map<number, string>,
  shift: number,
  rewrite: (href: string) => string,
  transform: (text: string) => string = (text) => text,
): string {
  const out: string[] = []
  for (let i = from; i < to; i++) {
    const line = lines[i]
    if (line.code) {
      out.push(line.text)
      continue
    }
    const slug = slugs.get(i)
    if (slug !== undefined) {
      const m = HEADING.exec(line.text)!
      const level = Math.max(1, m[1].length - shift)
      out.push(`${'#'.repeat(level)} ${rewriteLinks(m[2], rewrite)} {#${slug}}`)
      continue
    }
    out.push(rewriteLinks(transform(line.text), rewrite))
  }
  return out.join('\n').replace(/\n{3,}/g, '\n\n').trim() + '\n'
}

// ---------------------------------------------------------------------------
// The sync

interface Prepared {
  source: string
  lines: Line[]
  heads: Heading[]
}

function prepare(source: string): Prepared {
  const text = readFileSync(join(REPO_ROOT, source), 'utf8').replace(/\r\n?/g, '\n')
  const lines = classify(text)
  return { source, lines, heads: headings(lines) }
}

function titleOf(p: Prepared): string {
  const h1 = p.heads.find((h) => h.level === 1)
  return h1 ? h1.text : posix.basename(p.source, '.md')
}

const linkOf = (page: string) => '/' + page.replace(/(^|\/)index$/, '$1')

/** Writes a page, leaving an unchanged file alone so the dev server does not reload. */
function write(page: string, content: string): void {
  const file = join(SRC_DIR, page + '.md')
  if (existsSync(file) && readFileSync(file, 'utf8') === content) return
  mkdirSync(dirname(file), { recursive: true })
  writeFileSync(file, content)
}

export interface SyncResult {
  manifest: Manifest
  /** Links that could not be mapped to a page; the build should fail on these */
  problems: string[]
}

export function syncDocs(): SyncResult {
  const problems: string[] = []
  const targets = new Map<string, Target>()

  for (const [source, r] of Object.entries(REDIRECTS)) {
    targets.set(source, { page: r.page, redirects: r.anchors })
  }

  // The reference, split at its numbered sections.
  const ref = prepare(REFERENCE_SOURCE)
  const sections = ref.heads.filter((h) => h.level === 2 && /^\d+\. /.test(h.text))
  const sectionPages = sections.map((h, i) => {
    const name = h.text.replace(/^\d+\.\s*/, '')
    const page = i === 0 ? `${REFERENCE_DIR}/index` : `${REFERENCE_DIR}/${githubSlug(name)}`
    return { head: h, name, page, end: sections[i + 1]?.index ?? ref.lines.length }
  })
  const anchorPages = new Map<string, string>()
  for (const h of ref.heads) {
    const section = [...sectionPages].reverse().find((s) => s.head.index <= h.index) ?? sectionPages[0]
    anchorPages.set(h.slug, linkOf(section.page))
  }
  targets.set(REFERENCE_SOURCE, { page: linkOf(sectionPages[0].page), anchorPages })

  const prepared = SOURCES.map((s) => ({ ...s, doc: prepare(s.source) }))
  for (const s of prepared) targets.set(s.source, { page: linkOf(s.page) })

  // Clean the previous output.
  for (const dir of GENERATED_DIRS) rmSync(join(SRC_DIR, dir), { recursive: true, force: true })

  const manifest: Manifest = { language: [], std: {} }

  // Reference pages.
  const refSlugs = new Map(ref.heads.map((h) => [h.index, h.slug] as const))
  const refRewrite = (href: string) => resolveLink(href, REFERENCE_SOURCE, targets, problems)
  sectionPages.forEach((s, i) => {
    const startBody = i === 0 ? ref.heads[0].index + 1 : s.head.index + 1
    const title = i === 0 ? 'Language reference' : s.name
    const description =
      firstParagraph(ref.lines, startBody) || `${s.name}: section ${i + 1} of the Mk language reference.`
    let body = `# ${title}${i === 0 ? '' : ` {#${s.head.slug}}`}\n\n`
    // The first page keeps the document's preamble, then its "1. Start here" section.
    body += renderBody(ref.lines, startBody, s.end, refSlugs, i === 0 ? 0 : 1, refRewrite)
    write(
      s.page,
      frontmatter({ title, description, source: REFERENCE_SOURCE, outline: [2, 3] }) +
        GENERATED_NOTE(REFERENCE_SOURCE) +
        body,
    )
    manifest.language.push({ link: linkOf(s.page), title: i === 0 ? 'Start here' : s.name, source: REFERENCE_SOURCE })
  })

  // Every other document, one page each.
  for (const s of prepared) {
    const { doc } = s
    const h1 = doc.heads.find((h) => h.level === 1)
    const title = s.title ?? titleOf(doc)
    const slugs = new Map(doc.heads.map((h) => [h.index, h.slug] as const))
    const rewrite = (href: string) => resolveLink(href, s.source, targets, problems)
    const bodyStart = h1 ? h1.index + 1 : 0
    const description = firstParagraph(doc.lines, bodyStart)
    const heading = h1 ? `# ${rewriteLinks(HEADING.exec(doc.lines[h1.index].text)![2], rewrite)} {#${h1.slug}}\n\n` : ''
    const transform = s.source === 'doc/errors.md' ? anchorErrorCode : undefined
    const body = heading + renderBody(doc.lines, bodyStart, doc.lines.length, slugs, 0, rewrite, transform)
    write(
      s.page,
      frontmatter({ title, description, source: s.source, outline: [2, 3] }) + GENERATED_NOTE(s.source) + body,
    )
    const m = /^doc\/std\/(\w+)\.md$/.exec(s.source)
    if (m && m[1] !== 'README') manifest.std[m[1]] = { link: linkOf(s.page), title: titleOf(doc).replace(/:.*$/, ''), source: s.source }
  }

  return { manifest, problems: [...new Set(problems)] }
}

/** Paths whose change should trigger a re-sync in the dev server. */
export function watchedSources(): string[] {
  return [join(REPO_ROOT, 'doc'), join(REPO_ROOT, 'CONTRIBUTING.md'), join(REPO_ROOT, 'CHANGELOG.md'), join(REPO_ROOT, 'kits/web/README.md')]
}

/** The newest released version, from the changelog's first version heading. */
export function currentVersion(): string {
  const changelog = readFileSync(join(REPO_ROOT, 'CHANGELOG.md'), 'utf8')
  const m = /^## (\d+\.\d+\.\d+)/m.exec(changelog)
  if (!m) throw new Error('CHANGELOG.md has no version heading')
  return m[1]
}
