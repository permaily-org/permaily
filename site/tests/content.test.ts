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

describe('the receipts page', () => {
  const go = (path: string) => readFileSync(join(import.meta.dirname, '../..', path), 'utf8');
  const text = () =>
    (all.length ? load(join(dist, 'docs/receipts/index.html')) : parse('')).text.replace(
      /\s+/g,
      ' ',
    );

  it('says receipts are chained, and how a person checks theirs, as the code does it', () => {
    // ADR 0037: the chain (pull requests 1 and 2), the person's routes and the offline check (pull request 3).
    expect(go('cmd/permaily-enclave/main.go')).toMatch(/httpapi\.ReceiptsAPI\(/);
    expect(go('internal/httpapi/receipts.go')).toContain('mux.HandleFunc("GET "+path+"/export"');
    const verify = go('cmd/permaily-verify/receipts.go');
    expect(verify).toMatch(/fs\.StringVar\(&o\.file, "receipts",/);
    expect(verify).toMatch(/fs\.Var\(o\.heads, "receipts-head",/);
    const page = text();
    expect(page).not.toMatch(
      /chaining receipts by hash|Until receipts are chained|an export are not built/,
    );
    // The command is in a code block, whose markup the parser keeps as text: read it in the page's source.
    expect(go('site/src/content/docs/docs/receipts.md')).toContain(
      'permaily-verify -receipts receipts.jsonl -receipts-head 1234:<sha256>',
    );
    expect(page).toContain('-receipts-head');
    expect(page).toContain('GET /api/v1/receipts/export');
    expect(page).toContain(
      'The Activity screen, which shows them and downloads the export, is not built yet',
    );
    // A break the file reports fails the check, as the verifier does it (the review of pull request 3, F1).
    expect(verify).toMatch(
      /r\.Intact = r\.Consistent && len\(r\.Broken\) == 0 && len\(r\.Breaks\) == 0/,
    );
    expect(verify).toMatch(/r\.OK = r\.Intact/);
    expect(page).toContain('It passes only a file that holds together and reports no break.');
    expect(page).toContain(
      'the check cannot tell whether the storage broke it or someone edited the file',
    );
    expect(page).not.toMatch(/not as a fault of the file/);
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
    // built: the person's own rules (step 7). The receipts' screen and its downloads are built (ADR 0041).
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
    expect(text).toContain('Rules of your own aren’t built yet');
    expect(text).not.toMatch(
      /screen that shows (them|your receipts) to you isn’t built|Viewing and exporting them are planned/,
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

    it('the backups’ one day, as deploy/tofu/mail.tf sets it', () => {
      // The mail table's point-in-time recovery keeps one day (pull request 149); without
      // `recovery_period_in_days` DynamoDB keeps its default 35, which the pages no longer say.
      const recovery = go('deploy/tofu/mail.tf').match(/recovery_period_in_days\s*=\s*(\d+)/);
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

    it('connections removed with the account, as services/accounts and services/connections do it', () => {
      // docs/legal.md, item 15 (pull request 152): removing an account calls connections.Lifecycle.RemoveAll first of
      // all (removeConnections, in removeLocked, and again in settle once the account's record is gone), and RemoveAll
      // removes every connection the list names, the account's index entry and every epoch of its list (ListName);
      // the hourly pass deletes the index entry of a removed list; once RemoveAll succeeded the account is listed for
      // RemoveAgain, connections.RemoveAgainAfter later, for a value a write under way staged after the removal. A
      // change there fails here until the pages change with it.
      const body = (source: string, signature: RegExp) => {
        const at = source.search(signature);
        expect(at, String(signature)).toBeGreaterThanOrEqual(0);
        return source.slice(at, source.indexOf('\n}\n', at));
      };
      const settings = go('internal/services/accounts/settings.go');
      const removeConnections = body(settings, /func \(s \*Service\) removeConnections\(/);
      expect(removeConnections).toMatch(/\.RemoveAll\(ctx, id\)/);
      expect(
        removeConnections.search(/\.RemoveAll\(ctx, id\)/),
        'the last pass is listed once RemoveAll succeeded',
      ).toBeLessThan(removeConnections.search(/connections\.RemoveAgainAfter/));
      expect(removeConnections).toMatch(/s\.point\(ctx, connectionsAgainPartition, key, id, 0\)/);
      expect(body(settings, /func \(s \*Service\) RemoveDue\(/)).toMatch(
        /s\.removeConnectionsAgain\(ctx, key, now\)/,
      );
      expect(
        body(
          go('internal/services/accounts/connections.go'),
          /func \(s \*Service\) removeConnectionsAgain\(/,
        ),
      ).toMatch(/c\.RemoveAgain\(ctx, id\)/);
      const removeLocked = body(settings, /func \(s \*Service\) removeLocked\(/);
      const first = removeLocked.search(/s\.removeConnections\(ctx, doc\.ID\)/);
      expect(first, 'the removal removes the connections').toBeGreaterThanOrEqual(0);
      expect(first, 'before anything else').toBeLessThan(removeLocked.search(/mailboxesOf\(\)/));
      expect(first, 'before the account’s record goes').toBeLessThan(
        removeLocked.search(/s\.clear\(/),
      );
      const settle = body(settings, /func \(s \*Service\) settle\(/);
      expect(
        settle.search(/s\.removeConnections\(ctx, id\)/),
        'again once the record is gone',
      ).toBeLessThan(settle.search(/s\.clear\(ctx, id, entry\)/));
      expect(settle).toMatch(/s\.removeConnections\(ctx, id\)/);
      const lifecycle = go('internal/services/connections/lifecycle.go');
      const remove = body(lifecycle, /func \(s \*Service\) RemoveAll\(/);
      expect(remove).toMatch(/list := func\(e uint64\) string \{ return ListName\(account, e\) \}/);
      expect(remove).toMatch(/s\.removeConnStrict\(ctx, m\.ID, note\)/);
      // The last pass comes after the longest a write of the connections stays under way, 30 minutes.
      expect(lifecycle).toMatch(/WriteMax = 10 \* time\.Minute/);
      expect(lifecycle).toMatch(/removeAgainMargin = 20 \* time\.Minute/);
      expect(lifecycle).toMatch(/RemoveAgainAfter = WriteMax \+ removeAgainMargin/);
      const service = go('internal/services/connections/service.go');
      for (const write of [
        /func \(s \*Service\) writeSigned\(/,
        /func \(s \*Service\) writeSealed\(/,
      ]) {
        expect(body(service, write)).toMatch(/ctx = store\.WriteWithin\(ctx, WriteMax\)/);
      }
      // The removal's own fence notes what it trims.
      expect(remove).toMatch(/s\.endAll\(ctx, account, note\)/);
      const again = body(lifecycle, /func \(s \*Service\) RemoveAgain\(/);
      expect(again).toMatch(/s\.RemoveAll\(ctx, account\)/);
      expect(again).toMatch(/s\.removeFamily\(ctx, e\.Key\)/);
      expect(again).toMatch(/Records\.RemoveRecord\(ctx, connName\(e\.Key\)\)/);
      expect(again).toMatch(/DeletePartition\(ctx, partition\)/);
      expect(remove).toMatch(/DeleteEntry\(ctx, partitionAccounts, account\)/);
      expect(remove).toMatch(/s\.removeEpochs\(ctx, list\)/);
      expect(body(lifecycle, /func \(s \*Service\) removeEpochs\(/)).toMatch(
        /Records\.RemoveRecord\(/,
      );
      // The hourly pass deletes an index entry written again, or put back, after the list was removed.
      const sweep = body(lifecycle, /func \(s \*Service\) sweepAccount\(/);
      expect(sweep).toMatch(/s\.listRemoved\(ctx, account\)/);
      expect(sweep).toMatch(/return rerr/);
      expect(sweep).toMatch(/DeleteEntry\(ctx, partitionAccounts, account\)/);
      expect(go('cmd/permaily-enclave/connections.go')).toMatch(/sweepEvery = time\.Hour/);
      const person = go('internal/services/connections/person.go');
      expect(body(person, /func \(s \*Service\) removeConnStrict\(/)).toMatch(
        /s\.removeConnOf\(ctx, id, d\)/,
      );
      const removeConn = body(person, /func \(s \*Service\) removeConnOf\(/);
      expect(removeConn).toMatch(/s\.removeFamily\(/);
      expect(removeConn).toMatch(/Records\.RemoveRecord\(ctx, connName\(id\)\)/);
      // A rotation of the list removes the epoch it closed once its successor is made; the next write or the hourly pass finishes one stopped halfway.
      const lists = go('internal/services/connections/lists.go');
      const writeOnce = body(lists, /func \(s \*Service\) writeListOnce\(/);
      expect(writeOnce).toMatch(/s\.pruneList\(ctx, l\.Account, next\.Epoch\)/);
      expect(writeOnce).toMatch(
        /if l\.Pruning && s\.pruneList\(ctx, l\.Account, l\.Epoch\) == nil/,
      );
      expect(body(lists, /func \(s \*Service\) pruneList\(/)).toMatch(
        /Records\.RemoveRecord\(ctx, ListName\(account, e\)\)/,
      );
      // A change under way at that removal can store a copy after it: a last removal of every epoch rotations closed
      // (from Settled, which Next carries, so a later rotation forgets none) runs RemoveAgainAfter after they were
      // last known removed (PrunedAt, set by the write that clears Pruning), by the hourly pass or the next write; a
      // pass that cannot fails, so the next tries again.
      expect(writeOnce).toMatch(/l\.Pruned\(s\.now\(\)\)/);
      expect(writeOnce).toMatch(
        /if l\.RemoveAgainDue\(s\.now\(\)\) && s\.removeListAgain\(ctx, l\.Account, l\.Settled, l\.Epoch\) == nil \{\s*l\.RemovedAgain\(\)/,
      );
      expect(body(lists, /func \(s \*Service\) removeListAgain\(/)).toMatch(
        /for e := from; e < current; e\+\+ \{\s*if err := s\.cfg\.Records\.RemoveRecord\(ctx, ListName\(account, e\)\)/,
      );
      const list = go('internal/services/connections/list.go');
      expect(body(list, /func \(l \*ListState\) RemoveAgainDue\(/)).toMatch(
        /!now\.Before\(l\.PrunedAt\.Add\(RemoveAgainAfter\)\)/,
      );
      expect(list).toMatch(
        /func \(l \*ListState\) Pruned\(now time\.Time\) \{ l\.Pruning, l\.PrunedAt = false, now \}/,
      );
      expect(
        body(list, /func \(l \*ListState\) Next\(/),
        'a rotation keeps the earlier epochs waiting',
      ).not.toMatch(/Settled|PrunedAt/);
      expect(sweep).toMatch(/if l\.Pruning \|\| l\.RemoveAgainDue\(s\.now\(\)\) \{/);
      expect(sweep).toMatch(/return errUnpruned/);
      // What the pages say is the outcome the tests check: after the value a late write staged lands, only the
      // current epoch is stored once RemoveAgainAfter passed and the hourly pass or the next write ran (V70), also
      // after several rotations (V71); a reader that remakes a successor removes the epoch before it (V68); every
      // earlier epoch reads as removed, whatever is put back (V66).
      const retired = go('internal/services/connections/list_retired_test.go');
      const v70 = body(
        retired,
        /func TestV70AValueStagedAfterARotationsRemovalGoesWithTheLastRemoval\(/,
      );
      expect(v70).toMatch(
        /w\.clock\.add\(RemoveAgainAfter\)[\s\S]*Sweep\(ctx\)[\s\S]*onlyCurrent\(t, w, accountA, 1\)/,
      );
      expect(v70).toMatch(/"the next write"/);
      const v71 = body(retired, /func TestV71SeveralRotationsBeforeTheLateValueLands\(/);
      expect(v71).toMatch(
        /rotateTo\(t, a, accountA, 3\)[\s\S]*Sweep\(ctx\)[\s\S]*onlyCurrent\(t, w, accountA, 3\)/,
      );
      expect(body(retired, /func lateOnEpoch0\(/)).toMatch(
        /lateTable\{RecordTable: w\.mem, target: ListName\(accountA, 0\)\}/,
      );
      const epochs = go('internal/services/connections/list_epochs_test.go');
      expect(body(epochs, /func TestV68AFreshEnclaveFinishesARotation\(/)).toMatch(
        /fresh\.readList\(ctx, accountA\)[\s\S]*if op == "create" \{[\s\S]*onlyCurrent\(t, w, accountA, 1\)[\s\S]*fresh\.updateList/,
      );
      expect(body(epochs, /func onlyCurrent\(/)).toMatch(/at\.Version\(\) < 2/);
      // While the account lives, an ended connection's records go EndedKept after it ends.
      expect(go('internal/services/connections/connections.go')).toMatch(
        /EndedKept = 30 \* 24 \* time\.Hour/,
      );
      // The tokens' lookup entries are found only by their hash and expire on their own: a refresh token in 30 days.
      expect(go('internal/oauth/oauth.go')).toMatch(/RefreshTokenTTL = 30 \* 24 \* time\.Hour/);
      for (const path of ['privacy', 'data-deletion']) {
        const said = text(path);
        expect(said, path).toContain(
          'When the account is removed, after the seven-day cancellation window, we remove your connections with it: the list of your connections, each connection’s record and its devices’ token records.',
        );
        expect(said, path).toContain(
          'The tokens’ lookup entries expire within 30 days and are refused from the removal on.',
        );
        expect(said, path).toContain(
          'the part a new one replaces is removed as soon as the new one is stored, and if that is interrupted the next change to the list or the next successful hourly cleanup pass removes it. A change to the list under way at that moment can store a copy of the replaced part after its removal; nothing reads it, and a last cleanup pass at least 30 minutes after the removal deletes it, leaving only the integrity record of its removal.',
        );
        expect(said, path).not.toMatch(
          /replaces is removed as soon as the new one is stored, leaving only/,
        );
        expect(said, path).not.toMatch(/earlier closed part of the list can still hold/);
        expect(said, path).toContain(
          'Database backups can keep removed records for a further day.',
        );
        expect(said, path).toContain(
          'it can write your account’s identifier into our index of accounts with connections again; we remove that entry on the next successful hourly cleanup pass.',
        );
        expect(said, path).toContain(
          'a last cleanup pass at least 30 minutes after the removal deletes it',
        );
        expect(said, path).toContain(
          'A copy stored the same way for a connection that had ended and was already removed during the seven-day window can stay; nothing reads it either.',
        );
        expect(said, path).not.toMatch(/within an hour/);
        expect(said, path).not.toMatch(/no automatic removal/i);
      }
      expect(text('data-deletion')).toContain(
        'the devices that signed in, your connections, your receipts',
      );
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

    it('integrity records locked ten years from their write, as the image locks them', () => {
      // The review of pull request 174, F2: S3Commits.Append locks each commit for Retention from the moment it writes
      // it, the image's Retention is deploy/enclave.json's anchor_retention_days, and removing an account neither ends
      // nor extends a lock. The catalogue's and the record of incidents' entries are sealed entries of the database,
      // not locked themselves: their commits are.
      expect(JSON.parse(go('deploy/enclave.json')).anchor_retention_days).toBe(3650);
      expect(go('cmd/permaily-enclave/platform_enclave.go')).toMatch(
        /Retention: time\.Duration\(retentionDays\) \* 24 \* time\.Hour/,
      );
      expect(go('internal/store/s3commits.go')).toMatch(
        /c\.LockedUntil = s\.now\(\)\.Add\(s\.Retention\)/,
      );
      for (const path of ['privacy', 'data-deletion']) {
        expect(text(path), path).toContain('locked for ten years from when it is written');
        expect(text(path), path).toContain(
          'Removing your account neither ends nor extends those locks',
        );
        expect(text(path), path).toContain('are not locked themselves');
        expect(text(path), path).not.toMatch(
          /ten years after your account is removed|locked for ten years as well/,
        );
      }
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
