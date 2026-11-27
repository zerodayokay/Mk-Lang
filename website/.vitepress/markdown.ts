// Markdown rendering for Mk code.
//
// Every `mk` block in the documentation opens with a verdict line that the
// compiler's corpus test checks ("// verdict: ok", "parse", or "reject"
// followed by error codes). On the site the line is removed; a rejected
// example is labelled with the codes it fails with, each linked to the
// error catalogue.

import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import type { LanguageRegistration } from 'shiki'
import type { MarkdownRenderer } from 'vitepress'
import { REPO_ROOT } from './sync'

/** The TextMate grammar the VS Code extension ships, so the site highlights Mk the same way. */
export function mkGrammar(): LanguageRegistration {
  const path = join(REPO_ROOT, 'editors/vscode/syntaxes/mk.tmLanguage.json')
  const grammar = JSON.parse(readFileSync(path, 'utf8')) as LanguageRegistration
  return { ...grammar, name: 'mk', aliases: ['Mk'] }
}

const VERDICT = /^\/\/ verdict: (ok|parse|reject)((?: E\d{4})*)\s*\n/

export interface Verdict {
  kind: 'ok' | 'parse' | 'reject'
  codes: string[]
}

/** Splits a block's verdict line from its code. */
export function readVerdict(content: string): { verdict: Verdict | null; code: string } {
  const m = VERDICT.exec(content)
  if (!m) return { verdict: null, code: content }
  const codes = m[2].trim() ? m[2].trim().split(/\s+/) : []
  return { verdict: { kind: m[1] as Verdict['kind'], codes }, code: content.slice(m[0].length) }
}

/** HTML the pages may use inline; any other tag is written out as text. */
const KNOWN_TAG = /^<\/?(a|abbr|b|br|code|del|em|i|img|kbd|mark|s|samp|small|span|strong|sub|sup|u|var)(\s[^>]*)?\/?>$|^<!--/i

const fenceLang = (info: string) => info.trim().split(/\s+/)[0]

/**
 * A `text` block straight after an `mk` block is that program's output, as
 * the documentation writes it. A shell session (`$ mk run ...`) is not.
 */
function isOutput(tokens: { type: string; info: string; content: string }[], idx: number): boolean {
  const prev = tokens[idx - 1]
  return prev?.type === 'fence' && fenceLang(prev.info) === 'mk' && !tokens[idx].content.startsWith('$ ')
}

/** Installs the Mk block handling. `base` is the site's base path, for the links it writes. */
export function mkMarkdown(md: MarkdownRenderer, base: string): void {
  const fence = md.renderer.rules.fence!
  md.renderer.rules.fence = (tokens, idx, options, env, self) => {
    const token = tokens[idx]
    const lang = fenceLang(token.info)
    // The grammar is written in an EBNF-like notation Shiki has no grammar for.
    if (lang === 'ebnf') token.info = 'txt'
    if (lang === 'text' && isOutput(tokens, idx)) {
      return `<div class="mk-output">${fence(tokens, idx, options, env, self)}</div>`
    }
    if (lang !== 'mk') return fence(tokens, idx, options, env, self)

    const { verdict, code } = readVerdict(token.content)
    token.content = code
    const html = fence(tokens, idx, options, env, self)
    if (verdict?.kind !== 'reject') return html

    const codes = verdict.codes
      .map((c) => `<a class="mk-code" href="${base}reference/errors#${c.toLowerCase()}">${c}</a>`)
      .join(' ')
    return (
      `<div class="mk-rejected">` +
      `<div class="mk-rejected-label"><span class="mk-rejected-title">Does not compile</span>${codes}</div>` +
      html +
      `</div>`
    )
  }

  // The documentation writes placeholders such as `<M>` in prose. GitHub drops
  // unknown tags; here they would break the Vue template, so show them as text.
  const htmlInline = md.renderer.rules.html_inline!
  md.renderer.rules.html_inline = (tokens, idx, options, env, self) => {
    const { content } = tokens[idx]
    if (KNOWN_TAG.test(content) || !content.startsWith('<')) return htmlInline(tokens, idx, options, env, self)
    return md.utils.escapeHtml(content)
  }

  // The pages are compiled as Vue templates, and Mk's format strings write
  // literal braces as `{{`. Escape them so Vue never reads an interpolation.
  for (const rule of ['text', 'code_inline'] as const) {
    const render = md.renderer.rules[rule]!
    md.renderer.rules[rule] = (tokens, idx, options, env, self) =>
      render(tokens, idx, options, env, self).replace(/\{\{|\}\}/g, (b) => (b === '{{' ? '&#123;&#123;' : '&#125;&#125;'))
  }
}
