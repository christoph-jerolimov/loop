import { defineCollection } from 'astro:content';
import { glob } from 'astro/loaders';

// Every markdown file in the repository's root docs/ folder becomes a page.
export const collections = {
  docs: defineCollection({
    loader: glob({ pattern: '**/*.md', base: '../docs' }),
  }),
};
