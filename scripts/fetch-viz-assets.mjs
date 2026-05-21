import { mkdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const args = new Map();
for (const arg of process.argv.slice(2)) {
  const match = arg.match(/^--([^=]+)=(.*)$/);
  if (match) args.set(match[1], match[2]);
}

const designSystemRepo = args.get('design-system-repo') || process.env.DS_REPO || 'dzarlax/design-system';
let designSystemRef = args.get('design-system-ref') || process.env.DS_REF || 'latest';
const visNetworkVersion = args.get('vis-network-version') || process.env.VIS_NETWORK_VERSION || '9.1.9';
const visTimelineVersion = args.get('vis-timeline-version') || process.env.VIS_TIMELINE_VERSION || '7.7.3';

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.dirname(scriptDir);
const vendorDir = path.join(repoRoot, 'internal', 'viz', 'static', 'assets', 'vendor');
await mkdir(vendorDir, { recursive: true });

async function fetchText(url) {
  const res = await fetch(url, { headers: { 'User-Agent': 'personal-memory-build' } });
  if (!res.ok) throw new Error(`${url} failed (${res.status})`);
  return res.text();
}

async function download(url, filename) {
  const res = await fetch(url, { headers: { 'User-Agent': 'personal-memory-build' } });
  if (!res.ok) throw new Error(`${url} failed (${res.status})`);
  await writeFile(path.join(vendorDir, filename), new Uint8Array(await res.arrayBuffer()));
}

if (designSystemRef === 'latest') {
  const release = JSON.parse(await fetchText(`https://api.github.com/repos/${designSystemRepo}/releases/latest`));
  designSystemRef = release.tag_name;
}
if (!designSystemRef) throw new Error(`Could not resolve ${designSystemRepo} release`);

const dsBase = `https://cdn.jsdelivr.net/gh/${designSystemRepo}@${designSystemRef}/dist`;
console.log(`Fetching design system from ${dsBase} ...`);
await download(`${dsBase}/dzarlax.css`, 'dzarlax.css');
await download(`${dsBase}/dzarlax.js`, 'dzarlax.js');

console.log(`Fetching vis-network ${visNetworkVersion} and vis-timeline ${visTimelineVersion} ...`);
const unpkg = 'https://unpkg.com';
await download(`${unpkg}/vis-network@${visNetworkVersion}/standalone/umd/vis-network.min.js`, 'vis-network.min.js');
await download(`${unpkg}/vis-timeline@${visTimelineVersion}/standalone/umd/vis-timeline-graph2d.min.js`, 'vis-timeline-graph2d.min.js');
await download(`${unpkg}/vis-timeline@${visTimelineVersion}/styles/vis-timeline-graph2d.min.css`, 'vis-timeline-graph2d.min.css');

console.log(`OK - bundle at ${vendorDir}`);
