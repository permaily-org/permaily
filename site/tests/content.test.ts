// Checks the built site (dist/) as a reader and a crawler would see it.
// Run after `bun run build`.

import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';
import { type HTMLElement, parse } from 'node-html-parser';
import { describe, expect, it } from 'vitest';
import { TERMS_VERSION } from '../src/config/legal';

const dist = join(import.meta.dirname, '..', 'dist');

function pages(dir = dist): string[] {
  return readdirSync(dir).flatMap((entry) => {
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) return pages(full);
    return entry.endsWith('.html') ? [full] : [];
  });
}

// Redirects for old addresses (astro.config.mjs) are not pages of the site; tests/links.test.ts checks them.
const isRedirect = (file: string) => readFileSync(file, 'utf8').includes('http-equiv="refresh"');
const all = existsSync(dist) ? pages().filter((file) => !isRedirect(file)) : [];
const load = (file: string): HTMLElement => parse(readFileSync(file, 'utf8'));
const name = (file: string) => `/${relative(dist, file)}`;

describe('built site', () => {
  it('has been built', () => {
    expect(all.length, 'run `bun run build` first').toBeGreaterThan(10);
  });

  for (const file of all) {
    describe(name(file), () => {
      const doc = load(file);
      const noindex =
        doc.querySelector('meta[name="robots"]')?.getAttribute('content') === 'noindex';

      it('declares language, title and description', () => {
        expect(doc.querySelector('html')?.getAttribute('lang')).toBe('en');
        expect(doc.querySelector('title')?.text.trim()).toBeTruthy();
        expect(doc.querySelector('meta[name="description"]')?.getAttribute('content')).toBeTruthy();
      });

      it('has exactly one h1', () => {
        expect(doc.querySelectorAll('h1')).toHaveLength(1);
      });

      it('has a canonical URL unless it is not indexed', () => {
        if (noindex) return;
        expect(doc.querySelector('link[rel="canonical"]')?.getAttribute('href')).toMatch(
          /^https:\/\/permaily\.com\//,
        );
      });

      it('ships a content security policy', () => {
        const csp = doc.querySelector('meta[http-equiv="content-security-policy"]');
        expect(csp?.getAttribute('content')).toContain("default-src 'self'");
      });

      it('only links to pages that exist', () => {
        for (const a of doc.querySelectorAll('a[href]')) {
          const href = a.getAttribute('href') ?? '';
          if (!href.startsWith('/') || href.startsWith('//')) continue;
          const path = decodeURI(href.split(/[?#]/)[0]);
          const target = join(dist, path.endsWith('/') ? `${path}index.html` : path);
          const exists =
            existsSync(target) ||
            existsSync(`${target}.html`) ||
            existsSync(join(target, 'index.html'));
          expect(exists, `${href} from ${name(file)}`).toBe(true);
        }
      });

      it('has no placeholder links', () => {
        for (const a of doc.querySelectorAll('a[href^="#"]')) {
          const href = a.getAttribute('href') ?? '';
          if (href === '#' || href === '#main' || doc.getElementById(href.slice(1))) continue;
          expect.fail(`${href} in ${name(file)} leads nowhere`);
        }
      });
    });
  }
});

describe('home page', () => {
  const home = all.length ? load(join(dist, 'index.html')) : parse('');

  it('carries valid structured data', () => {
    const json = home.querySelector('script[type="application/ld+json"]')?.text ?? '{}';
    const data = JSON.parse(json);
    const types = (data['@graph'] ?? []).map((n: { '@type': string }) => n['@type']);
    expect(types).toEqual(expect.arrayContaining(['Organization', 'SoftwareApplication']));
  });

  it('never presents the demo measurement as the real build', () => {
    // The footer shows the latest release's PCR0, or says there is none.
    const footer = home.querySelector('.foot-end')?.text ?? '';
    expect(footer).not.toContain('7f3a');
    expect(footer).toMatch(/no public release yet|PCR0 [0-9a-f]{4}…[0-9a-f]{4}/);
  });
});

describe('legal pages', () => {
  const page = (path: string) => (all.length ? load(join(dist, path, 'index.html')) : parse(''));

  it('publish the current terms version', () => {
    // docs/legal.md: TERMS_VERSION changes with the terms, and the page shows it.
    const shown = page('terms').querySelector('[data-terms-version]')?.text.trim();
    expect(shown).toBe(TERMS_VERSION);
    expect(TERMS_VERSION).toMatch(/^\d{4}-\d{2}-\d{2}$/);
  });

  it('match the version the service records', () => {
    // docs/legal.md: the service records the version an account agreed to (domain.TermsVersion).
    const dir = join(import.meta.dirname, '../../internal/domain');
    const source = readdirSync(dir)
      .filter((f) => f.endsWith('.go') && !f.endsWith('_test.go'))
      .map((f) => readFileSync(join(dir, f), 'utf8'))
      .join('\n');
    const match = source.match(/\bTermsVersion\s*(?:string\s*)?=\s*"([^"]+)"/);
    expect(match?.[1]).toBe(TERMS_VERSION);
  });

  it('are linked from every page', () => {
    for (const file of all) {
      if (!file.startsWith(join(dist, 'docs')) && !file.endsWith('404.html')) {
        const hrefs = load(file)
          .querySelectorAll('footer a[href]')
          .map((a) => a.getAttribute('href'));
        expect(hrefs, name(file)).toEqual(expect.arrayContaining(['/privacy/', '/terms/']));
      }
    }
  });

  it('exist for each legal link', () => {
    for (const path of ['privacy', 'terms', 'data-processing', 'subprocessors', 'data-deletion']) {
      expect(page(path).querySelector('h1')?.text.trim(), path).toBeTruthy();
    }
  });
});
