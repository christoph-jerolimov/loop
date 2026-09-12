import { getCollection, type CollectionEntry } from 'astro:content';

export type Doc = CollectionEntry<'docs'>;

/** Sidebar order for known pages; anything else follows alphabetically. */
const order = ['cli', 'workflow', 'configuration', 'prompts'];

/** Files under examples/ are prompt templates and are shown as source, not rendered. */
export const isExample = (doc: Doc) => doc.id.startsWith('examples/');

export function titleOf(doc: Doc): string {
  const name = doc.id.split('/').pop() ?? doc.id;
  const fromName = name.replace(/-/g, ' ').replace(/^\w/, (c) => c.toUpperCase());
  if (isExample(doc)) return fromName;
  const h1 = doc.body?.match(/^#\s+(.+?)\s*$/m);
  return h1 ? h1[1].replace(/`([^`]+)`/g, '$1') : fromName;
}

export function summaryOf(doc: Doc): string {
  const body = (doc.body ?? '').replace(/^#\s+.+$/m, '').trim();
  const para = body.split(/\n\s*\n/).find((p) => p && !p.startsWith('#') && !p.startsWith('```') && !p.startsWith('|'));
  return (para ?? '').replace(/\s+/g, ' ').replace(/`/g, '').slice(0, 160);
}

export function hrefOf(doc: Doc): string {
  return href(`/docs/${doc.id}/`);
}

export function href(path: string): string {
  const base = import.meta.env.BASE_URL.replace(/\/$/, '');
  return `${base}${path}`;
}

export async function sortedDocs(): Promise<Doc[]> {
  const docs = await getCollection('docs');
  return docs.sort((a, b) => {
    const ia = order.indexOf(a.id);
    const ib = order.indexOf(b.id);
    if (isExample(a) !== isExample(b)) return isExample(a) ? 1 : -1;
    if (ia !== -1 || ib !== -1) return (ia === -1 ? 99 : ia) - (ib === -1 ? 99 : ib);
    return a.id.localeCompare(b.id);
  });
}
