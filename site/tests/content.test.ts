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

  it('say how long the sign-in rule’s decisions are kept as the service keeps them', () => {
    // ADR 0031, "Bounds" and "How long": a decision is kept until an hour (inbox.DecisionKeptAfter) past the
    // latest its message can expire, its arrival plus the longest retention (contracts.MailboxRetentionDays),
    // whatever happens to the message; it is dropped only by a later write of its record, and a record not
    // written again stays until the mailbox is removed (services/mailboxes, decisions.go).
    const go = (path: string) => readFileSync(join(import.meta.dirname, '../..', path), 'utf8');
    const days = go('internal/contracts/mailboxes.go').match(
      /MailboxRetentionDays = \[\]int\{([\d, ]+)\}/,
    )?.[1];
    const longest = Math.max(...(days ?? '').split(',').map(Number));
    expect(longest).toBe(365);
    expect(go('internal/inbox/rule_decisions.go')).toMatch(
      /\bDecisionKeptAfter\s*=\s*time\.Hour\b/,
    );
    const rows = page('privacy').querySelectorAll('tbody tr');
    const decisions = rows.filter((tr) =>
      tr.querySelector('th')?.text.includes('holds back sign-in mail'),
    );
    expect(decisions, 'one row for the rule’s decisions').toHaveLength(1);
    const kept = decisions[0]?.querySelectorAll('td').at(-1)?.text ?? '';
    expect(kept).toContain(`${longest} days and an hour from when the message arrived`);
    expect(kept).toContain('even if the message is deleted sooner');
    expect(kept).toContain('until the mailbox is disconnected or the account is removed');
    // The model's answers kept for a message are a row of their own, which goes with the message.
    const answers = rows.filter(
      (tr) =>
        tr.querySelector('th')?.text.trim() === 'What a model said about a message and a topic',
    );
    expect(answers[0]?.querySelectorAll('td').at(-1)?.text.trim()).toBe('As long as the message');
  });

  it('say what is open as the image composes it', () => {
    // The image composes mailboxes, the model, connections and the default rule (cmd/permaily-enclave, ADR 0026,
    // 0028, 0030 and 0031), so the privacy page says them in the present tense and keeps "planned" for what is not
    // built: the person's own rules (step 7) and the receipts' screen (step 8).
    const go = (path: string) => readFileSync(join(import.meta.dirname, '../..', path), 'utf8');
    const composition = go('cmd/permaily-enclave/connections.go');
    expect(composition).toMatch(/Rules:\s+\[\]rules\.Rule\{rules\.Default\(\)\}/);
    expect(go('cmd/permaily-enclave/main.go')).toMatch(/mountConnections\(p\.connections,/);
    const privacy = page('privacy');
    const headings = privacy.querySelectorAll('h2').map((h) => h.text);
    expect(headings.filter((h) => /planned/i.test(h))).toEqual([]);
    const text = privacy.text.replace(/\s+/g, ' ');
    expect(text).not.toMatch(
      /aren’t open yet|once (mailboxes|connections) open|none of this is running/i,
    );
    expect(text).toContain(
      'Rules of your own, and a screen that shows you your receipts, aren’t built yet',
    );
    // A connection's end and its revocation, as internal/oauth sets them.
    const oauth = go('internal/oauth/oauth.go');
    expect(oauth).toMatch(/ConnectionDefaultDays = 90\b/);
    expect(oauth).toMatch(/ConnectionMaxDays\s+= 365\b/);
    expect(oauth).toMatch(/ConnectionFresh = 5 \* time\.Second/);
    expect(text).toContain('after 90 days, or the time the app asked for, a year at most');
    expect(text).toContain('anything being sent stops within five seconds');
    expect(oauth).toMatch(/CodeTTL = 60 \* time\.Second/);
    expect(oauth).toMatch(/AccessTokenTTL = time\.Hour/);
    expect(oauth).toMatch(/RefreshTokenTTL = 30 \* 24 \* time\.Hour/);
    expect(oauth).toMatch(/RequestTTL = 15 \* time\.Minute/);
    expect(text).toContain(
      'A request fifteen minutes, a code a minute, an access token an hour, a refresh token 30 days',
    );
    // What the model is sent: at most 256 KiB of the text (inbox.MessageTextMax).
    expect(go('internal/inbox/box.go')).toMatch(/MessageTextMax = 256 << 10/);
    expect(text).toContain('the subject and the text, up to 256 KiB');
    // Zero data retention: asked of OpenAI, not approved, as the image says (deploy/enclave.json).
    const model = JSON.parse(go('deploy/enclave.json')).model;
    expect(model.zero_data_retention).toBe(false);
    expect(text).toContain(
      'We asked OpenAI for zero data retention on 15 November 2026; it hasn’t approved it yet',
    );
    expect(page('subprocessors').text).toContain('it hasn’t approved it yet');
  });

  describe('say what the code keeps, sends and removes', () => {
    // The adversarial review of pull request 148 (docs/legal.md, "Decided (2026-11-16)"): each fact below is read
    // from the code it describes, so a change there fails here until the pages change with it.
    const go = (path: string) => readFileSync(join(import.meta.dirname, '../..', path), 'utf8');
    const text = (path: string) => page(path).text.replace(/\s+/g, ' ');

    // deploy/tofu/mail.tf sets the mail table's point-in-time recovery to one day (pull request 149); without
    // `recovery_period_in_days` DynamoDB keeps its default 35, which the pages would no longer say.
    const recovery = go('deploy/tofu/mail.tf').match(/recovery_period_in_days\s*=\s*(\d+)/);
    it.skipIf(!recovery)('the backups’ one day, as deploy/tofu/mail.tf sets it', () => {
      expect(Number(recovery?.[1])).toBe(1);
      for (const path of ['privacy', 'data-deletion']) {
        expect(text(path), path).toContain('Our database keeps a rolling one-day backup');
        expect(text(path), path).not.toMatch(/35 days|35-day/);
      }
    });

    it('what the model is sent, in the words of the model’s notice', () => {
      // web/src/lib/model-notice.ts is the notice the service records (ModelNoticeVersion); the pages say the same.
      const notice = go('web/src/lib/model-notice.ts');
      const said =
        'We do not add sender or recipient header fields, account identifiers or attachment files to the model request.';
      expect(notice).toContain(said);
      expect(notice).toContain(
        'even if the decision is to hold the message back from the app or agent',
      );
      for (const path of ['privacy', 'subprocessors']) {
        expect(text(path), path).toContain(said);
        expect(text(path), path).toContain('are not anonymised or redacted');
        expect(text(path), path).not.toMatch(/Never the sender/);
      }
      expect(text('terms')).toContain('even when the connection’s own query uses no model');
    });

    it('a new mailbox’s retention and its choices, as internal/contracts sets them', () => {
      const contracts = go('internal/contracts/mailboxes.go');
      expect(contracts).toMatch(/MailboxRetentionDays = \[\]int\{7, 30, 90, 180, 365\}/);
      expect(contracts).toMatch(/MailboxRetentionDefault = 90\b/);
      expect(text('privacy')).toContain(
        'New mail expires after 90 days unless you choose 7, 30, 180 or 365 days for that mailbox',
      );
    });

    it('how long connection records stay, and that their lists are not removed', () => {
      // services/connections: an ended connection's records go EndedKept after it ends; nothing removes an
      // account's lists (ListName) or their index, also when the account is removed (docs/legal.md, item 15).
      // The pull request that removes them changes this test and the pages together.
      expect(go('internal/services/connections/connections.go')).toMatch(
        /EndedKept = 30 \* 24 \* time\.Hour/,
      );
      const dir = join(import.meta.dirname, '../../internal/services/connections');
      const source = readdirSync(dir)
        .filter((f) => f.endsWith('.go') && !f.endsWith('_test.go'))
        .map((f) => readFileSync(join(dir, f), 'utf8'))
        .join('\n');
      expect(source).not.toMatch(/RemoveRecord\([^)]*ListName\(/);
      for (const path of ['privacy', 'data-deletion']) {
        expect(text(path), path).toContain(
          'records are scheduled for removal 30 days after the connection ends, including when the account itself has already been removed',
        );
        expect(text(path), path).toContain(
          'Our connection-list records and their account index currently have no automatic removal',
        );
      }
      expect(text('data-deletion')).not.toMatch(/the devices that signed in, your connections,/);
    });

    it('the vendor documents’ cache, as internal/vendors bounds it', () => {
      const vendors = go('internal/vendors/vendors.go');
      expect(vendors).toMatch(/CacheMaxAge\s+= 24 \* time\.Hour/);
      expect(vendors).toMatch(/StaleMax\s+= time\.Hour/);
      expect(text('privacy')).toContain('freshness capped at 24 hours');
      expect(text('privacy')).toContain(
        'up to one further hour of stale use after a fetch failure',
      );
    });

    it('pending sign-ins as stored, not as memory only', () => {
      // services/accounts keeps them sealed in DynamoDB (pending.go).
      expect(go('internal/services/accounts/pending.go')).toMatch(/pending#/);
      const rows = page('privacy').querySelectorAll('tbody tr');
      const pending = rows.filter((tr) =>
        tr.querySelector('th')?.text.startsWith('Something you started'),
      );
      expect(pending).toHaveLength(1);
      const cells = pending[0]?.querySelectorAll('td').map((td) => td.text) ?? [];
      expect(cells.join(' ')).not.toMatch(/memory only|never stored/i);
      expect(cells.join(' ')).toContain('Our database, sealed');
    });

    it('no release that was not published, and no country limit that does not exist', () => {
      const changelog = page('changelog');
      expect(changelog.querySelector('h1')?.text.trim()).toBe('Release history');
      expect(changelog.text).not.toMatch(/v1\.\d+\.\d+/);
      expect(changelog.querySelector('a[href*="/releases"]')).toBeTruthy();
      expect(text('about')).not.toContain('for people in the United States');
      expect(text('about')).toContain('Invitations are not limited to one country');
    });
  });

  it('exist for each legal link', () => {
    for (const path of ['privacy', 'terms', 'data-processing', 'subprocessors', 'data-deletion']) {
      expect(page(path).querySelector('h1')?.text.trim(), path).toBeTruthy();
    }
  });
});
