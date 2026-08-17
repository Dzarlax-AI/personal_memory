import { access, readFile, readdir } from 'node:fs/promises';
import path from 'node:path';

const outputDir = path.resolve('dist');
const configuredBase = process.env.DOCS_BASE?.trim() || '/personal_memory';
const base = configuredBase === '/'
  ? ''
  : `/${configuredBase.replace(/^\/+|\/+$/g, '')}`;
const origin = new URL(process.env.DOCS_SITE || 'https://docs.invalid').origin;

async function walk(directory) {
  const entries = await readdir(directory, { withFileTypes: true });
  const files = await Promise.all(entries.map((entry) => {
    const target = path.join(directory, entry.name);
    return entry.isDirectory() ? walk(target) : [target];
  }));
  return files.flat();
}

async function exists(file) {
  try {
    await access(file);
    return true;
  } catch {
    return false;
  }
}

async function resolveTarget(pathname) {
  if (base && pathname !== base && !pathname.startsWith(`${base}/`)) {
    return null;
  }

  const localPath = decodeURIComponent(pathname.slice(base.length)).replace(/^\/+/, '');
  const exact = path.join(outputDir, localPath);
  const candidates = pathname.endsWith('/')
    ? [path.join(exact, 'index.html')]
    : [exact, `${exact}.html`, path.join(exact, 'index.html')];

  for (const candidate of candidates) {
    if (await exists(candidate)) return candidate;
  }
  return null;
}

const htmlFiles = (await walk(outputDir)).filter((file) => file.endsWith('.html'));
const failures = [];

for (const file of htmlFiles) {
  const relative = path.relative(outputDir, file).split(path.sep).join('/');
  const route = relative === 'index.html'
    ? `${base || ''}/`
    : `${base || ''}/${path.posix.dirname(relative)}/`;
  const html = await readFile(file, 'utf8');
  const tags = html.matchAll(/<(?:a|area|link|script|img|source|iframe)\b[^>]*>/gi);

  for (const tagMatch of tags) {
    const tag = tagMatch[0];
    if (/^<link\b/i.test(tag) && /\brel=["']canonical["']/i.test(tag)) continue;
    const references = tag.matchAll(/\b(?:href|src)=["']([^"'<>]+)["']/g);

    for (const match of references) {
      const raw = match[1].replaceAll('&amp;', '&');
      if (/^(?:data:|mailto:|tel:|javascript:)/i.test(raw) || raw.startsWith('//')) continue;

      const url = new URL(raw, `${origin}${route}`);
      if (url.origin !== origin) continue;
      if (!(await resolveTarget(url.pathname))) {
        failures.push(`${relative}: ${raw}`);
      }
    }
  }
}

if (failures.length > 0) {
  console.error(`Broken generated-site links (${failures.length}):`);
  for (const failure of failures) console.error(`- ${failure}`);
  process.exit(1);
}

console.log(`Checked ${htmlFiles.length} generated HTML files: all local targets exist.`);
