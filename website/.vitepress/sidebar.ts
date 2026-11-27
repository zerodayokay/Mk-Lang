import type { DefaultTheme } from 'vitepress'
import type { Manifest } from './sync'

type Sidebar = DefaultTheme.SidebarMulti

/** The standard library, grouped by what a module is for. */
const STD_GROUPS: [string, string[]][] = [
  ['Core', ['core', 'mem', 'coll', 'math', 'big', 'hash']],
  ['Text and data', ['text', 'regex', 'encoding', 'json', 'csv', 'compress', 'url', 'time']],
  ['Files, network and processes', ['fs', 'net', 'http', 'websocket', 'tls', 'x509', 'crypto', 'proc', 'term', 'cli', 'log', 'sqlite']],
  ['Concurrency and runtime', ['sync', 'atomic', 'runtime', 'simd']],
  ['Compile time', ['build', 'derive', 'dyn']],
  ['Testing', ['testing', 'sim']],
  ['Unsafe and low level', ['raw', 'libc', 'linux', 'mmio', 'vm', 'dl', 'free']],
]

export function sidebar(manifest: Manifest): Sidebar {
  const listed = new Set(STD_GROUPS.flatMap(([, mods]) => mods))
  const unlisted = Object.keys(manifest.std).filter((m) => !listed.has(m))
  if (unlisted.length) throw new Error(`sidebar: std modules without a group: ${unlisted.join(', ')}`)

  const std: DefaultTheme.SidebarItem[] = [
    { text: 'Overview', link: '/reference/std/' },
    ...STD_GROUPS.map(([text, mods]) => ({
      text,
      collapsed: false,
      items: mods.map((m) => {
        const page = manifest.std[m]
        if (!page) throw new Error(`sidebar: no page for std.${m}`)
        return { text: page.title, link: page.link }
      }),
    })),
    { text: 'Frameworks', items: [{ text: 'web kit', link: '/reference/kits/web' }] },
  ]

  return {
    '/guide/': [
      {
        text: 'Introduction',
        items: [
          { text: 'What is Mk?', link: '/guide/' },
          { text: 'Installation', link: '/guide/installation' },
          { text: 'Quick start', link: '/guide/quick-start' },
          { text: 'Your first program', link: '/guide/first-program' },
        ],
      },
      {
        text: 'Fundamentals',
        items: [
          { text: 'Syntax and structure', link: '/guide/syntax' },
          { text: 'Variables and scope', link: '/guide/variables' },
          { text: 'Types and conversions', link: '/guide/types' },
          { text: 'Operators', link: '/guide/operators' },
          { text: 'Strings and formatting', link: '/guide/strings' },
          { text: 'Arrays, lists and maps', link: '/guide/collections' },
        ],
      },
      {
        text: 'Language features',
        items: [
          { text: 'Control flow', link: '/guide/control-flow' },
          { text: 'Functions', link: '/guide/functions' },
          { text: 'Structs, enums and records', link: '/guide/structs-and-enums' },
          { text: 'Methods and interfaces', link: '/guide/methods-and-interfaces' },
          { text: 'Generics', link: '/guide/generics' },
          { text: 'Optionals and errors', link: '/guide/errors' },
          { text: 'Memory and ownership', link: '/guide/memory' },
          { text: 'Concurrency', link: '/guide/concurrency' },
          { text: 'Modules and packages', link: '/guide/modules' },
          { text: 'Testing and contracts', link: '/guide/testing' },
          { text: 'Compile-time code', link: '/guide/compile-time' },
          { text: 'Unsafe code and C', link: '/guide/unsafe-and-c' },
        ],
      },
      {
        text: 'Writing larger programs',
        items: [
          { text: 'Code style', link: '/guide/style' },
          { text: 'Organising a codebase', link: '/guide/codebase' },
          { text: 'Why Mk looks like this', link: '/guide/rationale' },
        ],
      },
    ],

    '/reference/std/': std,
    '/reference/kits/': std,

    '/reference/': [
      { text: 'Overview', link: '/reference/' },
      {
        text: 'Language reference',
        items: manifest.language.map((p, i) => ({ text: i === 0 ? p.title : `${i + 1}. ${p.title}`, link: p.link })),
      },
      {
        text: 'Rules and diagnostics',
        items: [
          { text: 'Memory model', link: '/reference/memory' },
          { text: 'Error codes', link: '/reference/errors' },
          { text: 'Grammar', link: '/reference/grammar' },
        ],
      },
      { text: 'Libraries', items: [{ text: 'Standard library', link: '/reference/std/' }] },
    ],

    '/examples/': [
      {
        text: 'Examples',
        items: [
          { text: 'Overview', link: '/examples/' },
          { text: 'Cookbook', link: '/examples/cookbook' },
          { text: 'Complete programs', link: '/examples/programs' },
        ],
      },
    ],

    '/tools/': [
      {
        text: 'Tools',
        items: [
          { text: 'The mk command', link: '/tools/' },
          { text: 'Packages', link: '/tools/packages' },
          { text: 'Editors', link: '/tools/editors' },
        ],
      },
    ],

    '/development/': [
      {
        text: 'Development',
        items: [
          { text: 'Building and testing', link: '/development/' },
          { text: 'Contributing', link: '/development/contributing' },
          { text: 'The compiler', link: '/development/compiler' },
          { text: 'Changelog', link: '/development/changelog' },
        ],
      },
      {
        text: 'Design notes',
        items: [{ text: 'macOS and AArch64 targets', link: '/development/design/macos-aarch64' }],
      },
    ],
  }
}
