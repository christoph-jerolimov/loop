# loop website

The public site for loop, built with [Astro](https://astro.build). The
landing page explains what loop is and how a run works; the Docs section
renders every markdown file from the repository's root `docs/` folder with
Shiki syntax highlighting, so the docs stay in one place.

```sh
npm install
npm run dev      # http://localhost:4321
npm run check    # type-check .astro and .ts files (also runs in CI)
npm run build    # static output in dist/
```

How the docs section works:

- `src/content.config.ts` defines a `docs` content collection over `../docs/**/*.md`.
- `src/pages/docs/[...slug].astro` renders one page per file. Files under
  `docs/examples/` are prompt templates and are shown as highlighted source
  instead of rendered markdown.
- `src/lib/doc-links.mjs` rewrites relative links between the markdown files
  (`prompts.md`, `examples/session-with-comments.md`) to the site routes.

## Deployment

`.github/workflows/pages.yml` builds the site and publishes it to GitHub
Pages on every push to `main` that touches `website/`, `docs/` or the
workflow itself. It reads the site origin and base path from
`actions/configure-pages`, so the build works for
`https://<user>.github.io/loop/` and for a custom domain without changes.
The repository's Pages source must be set to "GitHub Actions" once, under
Settings → Pages.

Locally the site builds for the root path. To reproduce the Pages build:

```sh
BASE_PATH=/loop npm run build
```

Every internal link goes through the `href()` helper in `src/lib/docs.ts`
and the docs link plugin, so no page needs to know the base path.
