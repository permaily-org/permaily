---
title: Consent screen
description: What a person sees before anything is shared, and how an app or agent gets them there.
---

This is where a person decides. An app or agent sends them here, and they see what would be shared, on their own mail, before anything leaves.

## What the person sees

- **Who is asking,** with facts about them: how long they've been registered, how many users they have, and whether they're verified.
- **What they want,** in plain language, with the pack name.
- **A live preview on their own mail.** For example, "47 of 12,304 emails would be shared", followed by real subject lines, with everything else visibly withheld.
- **What they'll never get.** [Never share](/docs/concepts/#never-share) sits above every request.
- **How long access lasts,** and how to revoke it.

## Send someone there

Permaily hosts this screen, so there's nothing for you to build. Asking for access returns a `consentUrl`. Send the person there, and they come back to your redirect address. The [Quickstart](/docs/) shows the request.

## Asking for more later

An access pins a pack version. A new version that widens what matches needs fresh consent, and the person sees only the difference. Narrowing can apply automatically.

## Agents choose differently

An app asks for something specific, such as a pack. An agent connected through MCP has no such request to make, so consent works the other way round. The agent starts with nothing, and the person ticks the rules it can use while the preview updates. See [Remote MCP](/docs/remote-mcp/).

## Revoking

Revoking is one action, and it takes effect immediately. The screen states what stops working. It also states plainly that Permaily can't recall mail the connection already received.
