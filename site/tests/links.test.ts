// Signing up and signing in happen in the web app. Checks the built site (dist/)
// sends every such link there and holds no sign-in or sign-up form of its own.
// Run after `bun run build`.

import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';
import { parse } from 'node-html-parser';
import { describe, expect, it } from 'vitest';
import { LINKS } from '../src/config/site';

const dist = join(import.meta.dirname, '..', 'dist');

function files(dir = dist): string[] {
  return readdirSync(dir).flatMap((entry) => {
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) return files(full);
    return entry.endsWith('.html') ? [full] : [];
  });
}

const html = existsSync(dist)
  ? files().map((file) => ({ file, text: readFileSync(file, 'utf8') }))
  : [];
const isRedirect = (text: string) => text.includes('http-equiv="refresh"');
const pages = html.filter(({ text }) => !isRedirect(text));
const name = (file: string) => `/${relative(dist, file)}`;

const SIGN_UP = /^(sign ?up|get started|create (an|your) account|join)\b/i;
const LOG_IN = /^(log ?in|sign ?in)\b/i;
// Addresses of the old pages on this site, and anything that looks like one.
const OLD_ADDRESS = /^\/(signup|sign-up|login|log-in|sign-in)\b/;

describe('sign-up and log-in links', () => {
  it('points at the web app', () => {
    expect(LINKS.signup).toBe('https://app.permaily.com/sign-up');
    expect(LINKS.login).toBe('https://app.permaily.com/sign-in');
  });

  it('has pages to check', () => {
    expect(pages.length, 'run `bun run build` first').toBeGreaterThan(10);
  });

  for (const { file, text } of pages) {
    it(`go to the app from ${name(file)}`, () => {
      for (const a of parse(text).querySelectorAll('a[href]')) {
        const href = a.getAttribute('href') ?? '';
        const label = a.text.replace(/\s+/g, ' ').trim();
        const where = `"${label}" (${href}) in ${name(file)}`;
        expect(OLD_ADDRESS.test(href), where).toBe(false);
        if (SIGN_UP.test(label)) expect(href, where).toBe(LINKS.signup);
        if (LOG_IN.test(label)) expect(href, where).toBe(LINKS.login);
      }
    });

    it(`has no sign-in or sign-up form in ${name(file)}`, () => {
      for (const form of parse(text).querySelectorAll('form')) {
        const words = form.text.replace(/\s+/g, ' ');
        expect(words, name(file)).not.toMatch(
          /log ?in|sign ?in|sign ?up|continue with|create (an|your) account/i,
        );
        expect(form.querySelectorAll('input[type="password"]'), name(file)).toHaveLength(0);
      }
    });
  }

  it('are offered in the header, the phone menu and the docs menu', () => {
    const home = parse(pages.find(({ file }) => file === join(dist, 'index.html'))?.text ?? '');
    expect(home.querySelector('.site-act a.btn')?.getAttribute('href')).toBe(LINKS.signup);
    expect(home.querySelectorAll('#menu a').map((a) => a.getAttribute('href'))).toContain(
      LINKS.login,
    );
    const docs = parse(
      pages.find(({ file }) => file === join(dist, 'docs', 'index.html'))?.text ?? '',
    );
    expect(docs.querySelectorAll('.docs-nav-site a').map((a) => a.getAttribute('href'))).toContain(
      LINKS.login,
    );
  });
});

describe('old addresses', () => {
  const sitemap = existsSync(join(dist, 'sitemap-0.xml'))
    ? readFileSync(join(dist, 'sitemap-0.xml'), 'utf8')
    : '';

  for (const [path, target] of [
    ['signup', LINKS.signup],
    ['login', LINKS.login],
  ] as const) {
    it(`/${path}/ sends the reader to the app`, () => {
      const file = join(dist, path, 'index.html');
      expect(existsSync(file), `dist/${path}/index.html`).toBe(true);
      const doc = parse(readFileSync(file, 'utf8'));
      expect(doc.querySelector('meta[http-equiv="refresh"]')?.getAttribute('content')).toBe(
        `0;url=${target}`,
      );
      expect(doc.querySelector('meta[name="robots"]')?.getAttribute('content')).toBe('noindex');
      expect(doc.querySelector('form')).toBeNull();
      expect(sitemap).not.toContain(`/${path}/`);
    });
  }
});
