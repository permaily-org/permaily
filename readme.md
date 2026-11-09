# Permaily

[![CI](https://github.com/permaily-org/permaily/actions/workflows/ci.yml/badge.svg?branch=develop)](https://github.com/permaily-org/permaily/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/permaily-org/permaily?include_prereleases&label=release)](https://github.com/permaily-org/permaily/releases)
[![Reproducible build](https://img.shields.io/badge/build-reproducible-brightgreen)](docs/verification.md)
[![License](https://img.shields.io/badge/license-personal%20use-blue)](license.md)

**Give apps and AI agents the emails they need. Not your whole inbox.**

Travel apps, assistants and AI agents keep asking for full access to your Gmail to read a handful of messages. Permaily sits between your mailbox and everyone who wants to read it: each app, agent or vendor gets only the mail you approve, nothing else.

## Why Permaily

- **Share a slice, not the inbox.** Approve exactly what each connection can read, such as flight bookings for your travel app or invoices for your accountant, and preview what it would get before you say yes.
- **Never share what matters most.** Your own rules hold back sensitive mail, like sign-in codes and password resets, or strip details such as your home address, before any app sees a thing.
- **A receipt for every email.** Every message that leaves Permaily is recorded: who got it, which parts and why.
- **Revoke in one click.** Cut off one connection or a whole vendor at any time.
- **Built for AI agents.** Connect Claude, ChatGPT and other agents through MCP, and they only ever see the mail you allowed.

## Don't trust us. Verify.

Most services ask you to trust their privacy policy. Permaily runs inside an isolated, hardware-protected environment (AWS Nitro Enclaves) that even we can't look into, and it is built reproducibly from the public code in this repository.

Anyone can check, with cryptography and not promises, that the service answering at `permaily.com` runs exactly this code, and that the connection to it ends inside that protected environment:

```sh
scripts/verify.sh <release>
```

[How verification works](docs/verification.md)

## Status

Permaily is in early development, built in the open.

| Capability | Status |
|---|---|
| Reproducible service image anyone can rebuild | Available |
| Cryptographic verification of the running service (`scripts/verify.sh`) | Available |
| Connect your mailbox by forwarding | Planned |
| Per-app and per-agent access rules, receipts and revocation | Planned |
| Remote MCP server for Claude, ChatGPT and other agents | Planned |
| API for apps | Planned |

## License

The code is public so anyone can audit and verify it. It is free for personal use; companies and organizations need a commercial license. See [license.md](license.md) and [docs/commercial.md](docs/commercial.md).
