# 0006 Source-available personal use license with a CLA

Status: Accepted. Pending legal review.
Date: 2026-11-08

## Context

The code is public so anyone can verify the service, not to let others run it commercially. The owner wants: verification and contributions allowed, self-hosting only for personal use, no use by any organization (including nonprofits), and paid commercial licenses.

## Options

- PolyForm Noncommercial: allows nonprofits, schools and governments.
- PolyForm Strict: forbids modifications, which blocks contributions.
- BSL, FSL, Elastic: allow internal commercial use or convert to open source later.
- A custom license.

## Decision

A custom "Permaily Personal Use License" (`license.md`) for natural persons only, with verification explicitly allowed, a CLA (`docs/contributing.md`, enforced by `.github/workflows/cla.yml`) so contributions can be relicensed commercially, and a trademark policy.

## Consequences

- Not open source (OSI); communicated as source-available.
- Dependencies must have permissive licenses (allowlist in `scripts/third-party-notices.ts`).
- Must be reviewed by a lawyer before selling licenses or enforcing it.
