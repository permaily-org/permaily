# 0020 OpenAI's models for what depends on meaning

Status: Accepted. Not built.
Date: 2026-11-11

## Context

Three things in ADR 0019 cannot be done by exact rules: deciding whether a message is about a topic (`about`), turning a rule written in plain words into a formula, and removing from a message what talks about something. Each needs a model outside the enclave, so text leaves it. The plan had assumed Anthropic's API for classification in a later phase (F3); `about` is now part of the first version of connections.

In September and October 2026 a new kind of model interface appeared: a decision model takes content and statements, and returns for each statement a number from 0 to 1, the probability that it is true, instead of text. OpenAI's Decisions API (generally available since 2026-10-22, on GPT-6 Luna, charged by input only) and TypeSafe AI's Jev are the two looked at.

## Options

1. **OpenAI's Decisions API.** Its data controls page says it supports zero data retention, for accounts OpenAI approves.
2. **Jev.** Cheaper. Zero data retention only by an enterprise agreement asked of its sales team; its own documentation says it is weak with dates, numbers and adversarial content.
3. **Claude through AWS Bedrock.** No new party sees content, since AWS is trusted already. Not a decision model: it writes text, slower and dearer per decision. Not tried.

## Decision

OpenAI, for the three:

- **`about`: the Decisions API.** Each `about` condition is a statement; all the ones a message needs go in one call. Only the field the condition names is sent (the subject, or the body text cut at a maximum): never the sender, the recipients or an attachment.
- **Two thresholds, on the side of not delivering.** For a query, a condition matches at 0.80 or more. For a rule that protects, 0.30 is enough to hold.
- **Exact conditions first.** The model is asked only when the result depends on it.
- **A decision is kept.** What the model said of a message and a topic is stored sealed, so asking again gives the same answer, and the receipt names the model.
- **If the model does not answer, nothing leaves:** no match for a query, a match for a rule that protects.
- **A topic is short text, shown as it is** in the consent sentence, so a vendor cannot hide instructions to the model in it.
- **Rules in plain words: a model that writes** (OpenAI's Responses API, with the answer bound to the formula's schema). It receives the person's sentence and nothing of their mail. What it proposes is shown as the sentence the system writes from the formula, and nothing is active until the person confirms.
- **Removing by meaning: the Decisions API again, not a model that rewrites.** The text is split into sentences, the model is asked of each whether it talks about the topic, and the enclave drops those over the threshold. What leaves is always the original text minus pieces: the model cannot add or change a word. The price is that a whole sentence goes, not two words inside it.
- **One piece, replaceable.** The enclave reaches the model through one interface (a statement in, a probability out), with the provider, the model and the address compiled into the image. Another provider is a release, not a redesign.

## Zero data retention

Required, and not there on the first day: OpenAI has to approve it for the organization, which its owner asks for. Decided: `about` works from the first day, and until zero data retention is confirmed the consent screen says that the provider may keep the text for a time. When it is confirmed, that notice goes.

Zero data retention is a contract, not something the enclave can prove. The enclave proves which model and which address it calls, because both are in the measured image. That OpenAI keeps nothing is OpenAI's word, and the trust page will say so.

## Accepted risks

- **OpenAI sees mail.** The text of the fields that `about` conditions and removals by meaning examine, for the messages where the exact conditions did not settle the matter.
- **The sender writes the text.** A message can be written to push the model's answer. The damage is bounded to that message: it leaves when it should not, or is held when it should not.
- **The model may change under the same name.** Nothing found says a version can be pinned. Decisions already made are kept; new ones could differ. Receipts name the model.
- **An API key inside the enclave,** sealed like every other secret, and replaced through the enclave.

## To verify before building on it

- A spike with labelled sample mail, never real mail: how often it is right, how long a call takes, what a message costs, and how it holds against messages written to mislead it.
- OpenAI's own pages, which could not be opened while deciding: prices, limits per call, and whether a model version can be pinned. The numbers above come from its community announcement and from articles.
