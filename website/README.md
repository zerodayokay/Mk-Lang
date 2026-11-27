# Mk website

The documentation website for Mk, built with [VitePress](https://vitepress.dev). It needs Node.js 20 or newer.

```sh
npm install
npm run dev          # http://localhost:5173
npm run build        # static site in .vitepress/dist
npm run preview      # serve the build
```

## How it is put together

- `src/` holds the pages written for the site: the home page, the guide, and the landing pages of the examples, reference and development sections.
- The reference, standard library, tools and contributor pages are **generated** from `../doc`, `../CONTRIBUTING.md`, `../CHANGELOG.md` and `../kits/web/README.md` by `.vitepress/sync.ts` each time the site builds, and again in the dev server when a source changes. They are git-ignored: edit the source document, not the generated page.
- Mk code is highlighted with the TextMate grammar of `../editors/vscode`. Each `mk` block's `// verdict:` line is hidden, and a rejected example is labelled with its error codes (`.vitepress/markdown.ts`).

## Checks

```sh
npm run check                           # typecheck, production build, every internal link and anchor
MK=/path/to/mk npm run check:examples   # compile and run every example on the hand-written pages
```

`check:examples` judges each `mk` block with `mk corpus`, as the repository's tests do for `doc/`, then runs each program whose output the page shows and compares the output.

## Deploying

The build is static files. To serve the site from a subpath, set `MK_DOCS_BASE`, for example `MK_DOCS_BASE=/Mk-Lang/ npm run build` for GitHub Pages.
