import { resolve } from 'node:path'
import { defineConfig, type Plugin } from 'vitepress'
import { mkGrammar, mkMarkdown } from './markdown'
import { sidebar } from './sidebar'
import { REPO_URL, currentVersion, githubSlug, syncDocs, watchedSources } from './sync'

// The reference pages are generated from the repository's doc/ before the
// site is built, and again in the dev server whenever a source changes.
const { manifest, problems } = syncDocs()
if (problems.length) {
  throw new Error(`docs sync: links that map to no page:\n  ${problems.join('\n  ')}`)
}

/** Set MK_DOCS_BASE (e.g. /Mk-Lang/) to serve the site from a subpath, as GitHub Pages does. */
const base = process.env.MK_DOCS_BASE ?? '/'
const version = currentVersion()

function resyncOnDocChange(): Plugin {
  return {
    name: 'mk-docs-resync',
    configureServer(server) {
      const sources = watchedSources()
      server.watcher.add(sources)
      server.watcher.on('change', (file) => {
        if (sources.some((s) => resolve(file).startsWith(s))) {
          const { problems } = syncDocs()
          for (const p of problems) server.config.logger.warn(`docs sync: ${p}`)
        }
      })
    },
  }
}

export default defineConfig({
  lang: 'en-US',
  title: 'Mk',
  titleTemplate: ':title · Mk',
  description: 'Mk is a small compiled language for memory-safe systems programming without a garbage collector.',
  base,
  srcDir: 'src',
  cleanUrls: true,
  appearance: true,

  head: [
    ['link', { rel: 'icon', type: 'image/svg+xml', href: `${base}favicon.svg` }],
    ['meta', { name: 'theme-color', content: '#0d7a6f' }],
    ['meta', { property: 'og:type', content: 'website' }],
    ['meta', { property: 'og:site_name', content: 'Mk' }],
  ],

  transformPageData(page) {
    // Social cards repeat each page's own title and description.
    const title = page.frontmatter.title ?? page.title
    const description = page.frontmatter.description ?? page.description
    page.frontmatter.head ??= []
    const full = page.frontmatter.titleTemplate === false ? title : `${title} · Mk`
    page.frontmatter.head.push(['meta', { property: 'og:title', content: title ? full : 'Mk' }])
    if (description) page.frontmatter.head.push(['meta', { property: 'og:description', content: description }])
  },

  markdown: {
    languages: [mkGrammar()],
    theme: { light: 'github-light', dark: 'github-dark' },
    anchor: { slugify: githubSlug },
    config: (md) => mkMarkdown(md, base),
  },

  vite: {
    plugins: [resyncOnDocChange()],
    // Each page is its own chunk, loaded only when visited, and the search
    // index loads only when search opens. The longest reference pages and the
    // index of the whole reference (about 2.4 MB, 0.6 MB gzipped) are large by nature.
    build: { chunkSizeWarningLimit: 2600 },
  },

  themeConfig: {
    logo: { src: '/logo.svg', alt: '' },
    siteTitle: 'Mk',

    nav: [
      { text: 'Guide', link: '/guide/', activeMatch: '^/guide/' },
      { text: 'Reference', link: '/reference/', activeMatch: '^/reference/(?!std/|kits/)' },
      { text: 'Standard library', link: '/reference/std/', activeMatch: '^/reference/(std|kits)/' },
      { text: 'Examples', link: '/examples/', activeMatch: '^/examples/' },
      { text: 'Tools', link: '/tools/', activeMatch: '^/tools/' },
      {
        text: `v${version}`,
        items: [
          { text: 'Changelog', link: '/development/changelog' },
          { text: 'Releases', link: `${REPO_URL}/releases` },
          { text: 'Building and testing', link: '/development/' },
          { text: 'Contributing', link: '/development/contributing' },
        ],
      },
    ],

    sidebar: sidebar(manifest),

    outline: { level: [2, 3], label: 'On this page' },

    search: {
      provider: 'local',
      options: {
        detailedView: true,
      },
    },

    editLink: {
      // VitePress serializes this function into the client bundle, so it must
      // be self-contained: keep the URL in step with REPO_URL and REPO_BRANCH.
      pattern: ({ filePath, frontmatter }) => {
        const source = typeof frontmatter.source === 'string' ? frontmatter.source : `website/src/${filePath}`
        return `https://github.com/zerodayokay/Mk-Lang/edit/main/${source}`
      },
      text: 'Edit this page on GitHub',
    },

    socialLinks: [{ icon: 'github', link: REPO_URL, ariaLabel: 'Mk on GitHub' }],

    footer: {
      message: 'Released under the MIT License.',
      copyright: `Mk ${version}`,
    },

    docFooter: { prev: 'Previous', next: 'Next' },
    externalLinkIcon: true,
  },
})
