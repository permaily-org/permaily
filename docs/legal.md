# Legal

## Documents

| Document | Where | Status |
|---|---|---|
| Source license | [../license.md](../license.md) | Draft, pending lawyer review (ADR 0006) |
| Contributor License Agreement | [contributing.md](contributing.md), enforced by `.github/workflows/cla.yml` | Draft, pending lawyer review |
| Trademark policy | [trademark.md](trademark.md) | Draft |
| Commercial licensing | [commercial.md](commercial.md) | Draft |
| Third-party notices | [../third-party-notices.md](../third-party-notices.md) | Generated, checked in CI |
| Privacy policy | `site/src/pages/privacy.astro` | Draft |
| Terms of service | not written (`#terms` placeholder on the site) | Missing |
| Data processing agreement | not written (`#data-processing`) | Missing |
| Subprocessors | not written (`#subprocessors`) | Missing |

## Rules

- Terms, DPA and subprocessors need a lawyer before the site accepts sign-ups.
- When terms exist, a `TERMS_VERSION` constant is checked by a test against the published page, and accounts record the version they accepted. A material change (data use, pricing, liability) is announced before it takes effect.
- The subprocessor list comes from [data.md](data.md); a new subprocessor is added there first.
- The GPL-2.0 kernel and `nsm.ko` shipped inside the enclave image are distributed unmodified with a pointer to AWS's source (`third-party-notices.md`).

## TODO

- [ ] Lawyer review of the license, CLA and trademark policy.
- [ ] Terms, DPA and subprocessor pages, replacing the placeholders tracked by `site/tests/content.test.ts`.
