// Generates the reference pages from the repository's documentation.
// `vitepress dev` and `vitepress build` run the same sync through the site
// config; this script runs it on its own and reports links it cannot map.

import { syncDocs } from '../.vitepress/sync.ts'

const { manifest, problems } = syncDocs()
console.log(`synced ${manifest.language.length} reference sections and ${Object.keys(manifest.std).length} std modules`)
for (const p of problems) console.error(`unmapped link: ${p}`)
process.exitCode = problems.length ? 1 : 0
