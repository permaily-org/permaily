// Astro's CSP support hashes the inline scripts it renders itself, but some
// inline scripts rendered by Starlight components are not registered. After the
// build, this adds the SHA-256 of every inline <script> that the page's CSP
// <meta> does not cover yet, so the policy stays strict without hand-kept hashes.

import { createHash } from 'node:crypto';
import { readdir, readFile, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const META = /(<meta http-equiv="content-security-policy" content=")([^"]*)(")/;
const INLINE_SCRIPT = /<script(?![^>]*\bsrc=)([^>]*)>([\s\S]*?)<\/script>/g;

async function htmlFiles(dir) {
  const entries = await readdir(dir, { withFileTypes: true });
  const nested = await Promise.all(
    entries.map((e) => {
      const full = join(dir, e.name);
      if (e.isDirectory()) return htmlFiles(full);
      return e.name.endsWith('.html') ? [full] : [];
    }),
  );
  return nested.flat();
}

export function addInlineScriptHashes(html) {
  const meta = html.match(META);
  if (!meta) return html;
  let policy = meta[2];
  for (const [, attrs, body] of html.matchAll(INLINE_SCRIPT)) {
    // Data blocks (JSON-LD) are not executed, so CSP does not apply to them.
    if (/type="application\/(ld\+)?json"/.test(attrs) || !body.trim()) continue;
    const hash = `'sha256-${createHash('sha256').update(body).digest('base64')}'`;
    if (policy.includes(hash)) continue;
    policy = policy.replace(
      /script-src ([^;]*);/,
      (_, sources) => `script-src ${sources} ${hash};`,
    );
  }
  return html.replace(META, `$1${policy}$3`);
}

export default function cspInlineHashes() {
  return {
    name: 'csp-inline-hashes',
    hooks: {
      'astro:build:done': async ({ dir, logger }) => {
        const files = await htmlFiles(fileURLToPath(dir));
        let changed = 0;
        for (const file of files) {
          const before = await readFile(file, 'utf8');
          const after = addInlineScriptHashes(before);
          if (after !== before) {
            await writeFile(file, after);
            changed++;
          }
        }
        logger.info(`added missing inline script hashes to ${changed} of ${files.length} pages`);
      },
    },
  };
}
