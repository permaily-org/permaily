---
title: Untrusted content
description: Why every message is marked as untrusted, what Permaily does about prompt injection, and what it can't do.
---

Email is attacker-controlled text. Anyone can send a message written to steer the language model that reads it. Permaily can reduce this risk, and it can't remove it.

## What Permaily does

- Wraps message content in clear markers as untrusted data.
- Includes the sender's authentication results.
- Defaults to read-only.
- Requires a person's approval for each send and delete.

## What you should do

- Ask for `read` unless a feature needs more.
- Keep the markers when you pass a message to a model, and tell the model that marked text is data, not instructions.
- Check the sender's authentication results before you rely on who a message is from.
- Keep a person's approval in front of anything that sends, deletes or shares.

## What's left

A model can still be misled by text it's allowed to read. Narrowing what it sees limits what an attacker can reach, and it doesn't make what's left safe to obey.
