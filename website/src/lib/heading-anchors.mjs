// Sätteri hast plugin: gives every h2/h3 an id and appends a "#" anchor
// link, so sections of the rendered docs can be linked to directly.
//
// Astro assigns heading ids in a plugin that runs after user plugins and
// keeps an id that is already set, so this plugin computes the id with the
// same slugger and text extraction Astro uses; the outline built from
// render().headings therefore matches the anchors exactly.
import Slugger from 'github-slugger';
import { satteriCollectHastText } from '@astrojs/markdown-satteri';
import { defineHastPlugin } from 'satteri';

export const headingAnchors = defineHastPlugin({
  name: 'loop-heading-anchors',
  before(_root, ctx) {
    ctx.data.loopSlugger = new Slugger();
  },
  element: {
    filter: ['h2', 'h3'],
    visit(node, ctx) {
      let id = node.properties?.id;
      if (typeof id !== 'string' || id === '') {
        const slugger = ctx.data.loopSlugger ?? new Slugger();
        id = slugger.slug(satteriCollectHastText(node));
        ctx.setProperty(node, 'id', id);
      }
      // The "#" glyph comes from CSS so the heading text Astro collects for
      // the outline stays clean; aria-label names the link for screen readers.
      ctx.appendChild(node, {
        type: 'element',
        tagName: 'a',
        properties: { className: ['anchor'], href: `#${id}`, ariaLabel: 'Link to this section' },
        children: [],
      });
    },
  },
});
