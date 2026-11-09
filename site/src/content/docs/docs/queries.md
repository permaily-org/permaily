---
title: Queries
description: What your app asks for, how the person approves it, and why it caps what you can fetch.
---

A query is what your app asks for, such as flights and hotels. The person approves it on the [consent screen](/docs/consent-screen/), and it caps what your app can fetch.

## Declare it when you ask for access

Your query goes in the access request, written as a search.

```ts
const { consentUrl, access } = await permaily.access.request({
  query:
    "from:(iberia.com OR booking.com OR renfe.es OR airbnb.com) " +
    "subject:(booking OR reservation OR itinerary)",
  parts: ["headers", "body_text"],
  actions: ["read"],
  expiresIn: "90d",
});
```

The consent screen shows it in plain words and as the search that runs, with a live preview on the person's own mail.

## The approved query is a cap

You can fetch anything inside it and nothing outside it. Asking for more is a new request, and the person sees exactly what it would add before approving. See [Asking for more later](/docs/consent-screen/#asking-for-more-later).

## What runs before it

The person's [rules](/docs/rules/) run first, on the whole mailbox, and your query runs on what's left. Nothing tells you that an email was held back or a detail removed.

## Where it runs

Where the provider supports it, a query compiles to a provider query, so mail outside it is never fetched. The result is then checked again inside the enclave.

## What the person sees

Your connection's page lists the queries it ran and how many emails each one returned, with no reason given for an empty result.

## Agents write their own searches

An agent connected through MCP has no fixed query. The person ticks the kinds of mail it may search, and every search it writes stays inside that scope. See [Remote MCP](/docs/remote-mcp/).

## Typed fields

A query can also ask for fields, so you get structured data instead of text to parse. See [Fields, types and tokens](/docs/fields-types-tokens/).
