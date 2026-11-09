# 0005 One repository for the service, the web app and the public site

Status: Accepted.
Date: 2026-11-09

## Context

The public site lived in `permaily-org/permaily-website` and copied the design tokens, which had already drifted from the web app (missing tokens, a different dark `--bar`).

## Decision

The site moved into `site/` with its history (git subtree). Colors and fonts live once in `packages/ui/colors.css`, imported by relative path from `web/` and `site/`. There are no package-manager workspaces: `web/` and `site/` keep separate lockfiles, so the enclave build (`nix/web.nix`) never depends on the site's dependencies and site changes never change the PCRs.

## Consequences

- One CI and one set of gates (Biome, design check, notices) for everything.
- Amplify must point at this repo with `AMPLIFY_MONOREPO_APP_ROOT=site`; the old repo is archived after the switch.
