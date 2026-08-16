import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

const configuredBase = process.env.DOCS_BASE?.trim() || '/personal_memory';
const base = configuredBase === '/'
  ? '/'
  : `/${configuredBase.replace(/^\/+|\/+$/g, '')}`;
const site = process.env.DOCS_SITE || undefined;

export default defineConfig({
  ...(site ? { site } : {}),
  base,
  integrations: [
    starlight({
      title: 'Personal Memory',
      sidebar: [
        {
          label: 'Start here',
          items: [{ slug: 'index' }],
        },
      ],
    }),
  ],
});
