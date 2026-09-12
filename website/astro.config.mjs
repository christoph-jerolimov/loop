// @ts-check
import { defineConfig } from 'astro/config';
import { satteri } from '@astrojs/markdown-satteri';
import { docLinks } from './src/lib/doc-links.mjs';

// The Pages workflow passes the site origin and base path from
// actions/configure-pages, so the same config serves github.io/<repo>/
// and a custom domain. Locally both default to the site root.
const site = process.env.SITE || 'https://christoph-jerolimov.github.io';
const base = process.env.BASE_PATH || '/';

export default defineConfig({
  site,
  base,
  markdown: {
    syntaxHighlight: 'shiki',
    shikiConfig: { theme: 'github-dark-default', wrap: false },
    processor: satteri({ mdastPlugins: [docLinks(base)] }),
  },
});
