# loop website

The public site for loop, built with [Astro](https://astro.build). The
landing page explains what loop is and how a run works; the Docs section
renders every markdown file from the repository's root `docs/` folder with
Shiki syntax highlighting, so the docs stay in one place.

```sh
npm install
npm run dev      # http://localhost:4321
npm run build    # static output in dist/
```

How the docs section works:

- `src/content.config.ts` defines a `docs` content collection over `../docs/**/*.md`.
- `src/pages/docs/[...slug].astro` renders one page per file. Files under
  `docs/examples/` are prompt templates and are shown as highlighted source
  instead of rendered markdown.
- `src/lib/doc-links.mjs` rewrites relative links between the markdown files
  (`prompts.md`, `examples/session-with-comments.md`) to the site routes.

To deploy under a sub path (for example GitHub Pages at `/loop/`), set
`base` in `astro.config.mjs`; every internal link goes through the `href()`
helper in `src/lib/docs.ts`.
