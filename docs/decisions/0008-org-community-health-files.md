# 0008 Community health files live in the organization's .github repository

Status: Accepted.
Date: 2026-11-09

## Context

GitHub shows a security policy, code of conduct, support page, issue forms and pull request template for each repository. A repository's own files win over the organization defaults, and GitHub treats `docs/security.md` as the security policy. This repository's `docs/security.md` was the full threat model, so the Security tab showed a long document with the reporting instructions at the end.

## Decision

The organization profile and the default community health files live in [permaily-org/.github](https://github.com/permaily-org/.github): `security.md`, `support.md`, `code_of_conduct.md` (Contributor Covenant 2.1), the issue forms and the pull request template. File names are lowercase, except `.github/ISSUE_TEMPLATE/`, which GitHub ignores in lowercase. The threat model moved from `docs/security.md` to `docs/threat-model.md` so the organization's security policy applies here. `docs/contributing.md` stays in this repository because the CLA workflow links to it.

## Consequences

- Every repository in the organization shares one security policy, support page and code of conduct; changing them needs a commit to `permaily-org/.github`.
- Adding `security.md`, `support.md`, `code_of_conduct.md` or `.github/ISSUE_TEMPLATE/` here, at the root, in `docs/` or in `.github/`, would replace the organization default for this repository.
- Private vulnerability reporting is enabled on this repository; the security policy depends on it.
