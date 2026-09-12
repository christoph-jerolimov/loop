// @ts-check
import { defineConfig } from 'astro/config';
import { satteri } from '@astrojs/markdown-satteri';
import { docLinks } from './src/lib/doc-links.mjs';

export default defineConfig({
  site: 'https://christoph-jerolimov.github.io',
  // Set `base: '/loop'` when deploying below a sub path; all links go through
  // the href() helper so nothing else needs to change.
  markdown: {
    syntaxHighlight: 'shiki',
    shikiConfig: { theme: 'github-dark-default', wrap: false },
    processor: satteri({ mdastPlugins: [docLinks] }),
  },
});
