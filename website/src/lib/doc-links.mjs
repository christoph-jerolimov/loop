// Sätteri mdast plugin: rewrites relative links between the markdown files
// in ../docs so they resolve to the rendered pages:
//   prompts.md                         -> /docs/prompts/
//   examples/session-with-comments.md  -> /docs/examples/session-with-comments/
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { defineMdastPlugin } from 'satteri';

const docsRoot = path.resolve(fileURLToPath(new URL('../../../docs/', import.meta.url)));

/** @param {string} base The site base path (`/` or `/loop`). */
export const docLinks = (base = '/') => defineMdastPlugin({
  name: 'loop-doc-links',
  link(node, ctx) {
    if (!ctx.fileURL) return;
    const filePath = path.resolve(fileURLToPath(ctx.fileURL));
    if (!filePath.startsWith(docsRoot)) return;
    const url = node.url;
    if (typeof url !== 'string' || /^(?:[a-z]+:|\/|#)/i.test(url)) return;
    const [target, hash] = url.split('#');
    if (!target.endsWith('.md')) return;
    const dir = path.dirname(path.relative(docsRoot, filePath));
    const slug = path.posix.normalize(path.posix.join(dir === '.' ? '' : dir, target.slice(0, -3)));
    ctx.setProperty(node, 'url', `${base.replace(/\/$/, '')}/docs/${slug}/${hash ? '#' + hash : ''}`);
  },
});
