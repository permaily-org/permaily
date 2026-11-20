// Computes ADR 0039's v2a vectors (v2a.json) with the Standard Webhooks
// JavaScript library at the pinned revision; nothing of Permaily's code is
// used. To run it again: clone github.com/standard-webhooks/standard-webhooks
// at 3d65b1824b6697f05a5a4584e7062bd23512fc8e into ../sw beside a directory
// holding this file, `bun install --ignore-scripts --frozen-lockfile` in
// ../sw/libraries/javascript, then `bun run v2a.gen.ts > v2a.json`.
import { Webhook, WebhookAsymmetric, WebhookAsymmetricSigner } from "../sw/libraries/javascript/src/index.ts";
import * as ed25519 from "../sw/libraries/javascript/node_modules/@stablelib/ed25519";

const b64 = (b: Uint8Array) => Buffer.from(b).toString("base64");
const seedA = new Uint8Array(32);
const seedB = new Uint8Array(32); seedB[31] = 1;
const signerA = new WebhookAsymmetricSigner("whsk_" + b64(seedA));
const signerB = new WebhookAsymmetricSigner("whsk_" + b64(seedB));
const endpoint = "sub_0123456789abcdefghijklmnop";
const id = "ntc_0123456789abcdefghijklmnop";
const ts = 1795161787;
const at = new Date(ts * 1000);
const body = '{"v":1,"type":"mail.matched","id":"ntc_0123456789abcdefghijklmnop","subscription":"sub_0123456789abcdefghijklmnop","client_id":"https://trip-app.com/oauth/client.json","aud":"https://trip-app.com/h","created_at":"2026-11-20T08:03:07Z","since":"2026-11-20T08:03:00Z"}';

const verifier = new WebhookAsymmetric(endpoint, [signerA.publicKey()]);
const accepts = (sig: string, b = body, t = ts, i = id, v = verifier) => {
  try {
    // The library checks the timestamp against its own clock; the vectors
    // are checked at their timestamp, so its tolerance is not what is tested.
    const realNow = Date.now;
    Date.now = () => t * 1000;
    try { v.verify(b, { "webhook-id": i, "webhook-timestamp": String(t), "webhook-signature": sig }); } finally { Date.now = realNow; }
    return true;
  } catch { return false; }
};

const one = signerA.sign(endpoint, id, at, body);
const both = signerB.sign(endpoint, id, at, body) + " " + one;
const otherEndpoint = signerA.sign("sub_0123456789abcdefghijklmnoq", id, at, body);
const otherBody = signerA.sign(endpoint, id, at, body + " ");
const otherTime = signerA.sign(endpoint, id, new Date((ts + 1) * 1000), body);
const otherKey = signerB.sign(endpoint, id, at, body);
// The deprecated v1a: Ed25519 over the content itself, with no endpoint.
const kpA = ed25519.generateKeyPairFromSeed(seedA);
const v1a = "v1a," + b64(ed25519.sign(kpA.secretKey, new TextEncoder().encode(`${id}.${ts}.${body}`)));
const v1aAsV2a = "v2a," + v1a.slice(4);
const hmac = new Webhook("whsec_" + b64(new Uint8Array(32).fill(7))).sign(id, at, body);

const vectors = {
  source: "github.com/standard-webhooks/standard-webhooks libraries/javascript (standardwebhooks 1.3.0) at 3d65b1824b6697f05a5a4584e7062bd23512fc8e, run by gen.ts (ADR 0039)",
  public_keys: { wk1: signerA.publicKey(), wk2: signerB.publicKey() },
  endpoint, id, timestamp: String(ts), body,
  cases: [
    { name: "signed by the library", signature: one, accepts: accepts(one) },
    { name: "both keys during a rotation", signature: both, accepts: accepts(both) },
    { name: "both keys, verified by the older only", signature: both, accepts: accepts(both, body, ts, id, new WebhookAsymmetric(endpoint, [signerB.publicKey()])), keys: ["wk2"] },
    { name: "for another subscription", signature: otherEndpoint, accepts: accepts(otherEndpoint) },
    { name: "of another body", signature: otherBody, accepts: accepts(otherBody) },
    { name: "at another timestamp", signature: otherTime, accepts: accepts(otherTime) },
    { name: "by another key", signature: otherKey, accepts: accepts(otherKey) },
    { name: "the deprecated v1a", signature: v1a, accepts: accepts(v1a) },
    { name: "v1a's content labelled v2a", signature: v1aAsV2a, accepts: accepts(v1aAsV2a) },
    { name: "an HMAC v1 alone", signature: hmac, accepts: accepts(hmac) },
    { name: "none", signature: "", accepts: accepts("") },
  ],
  // The specification's own example, at the same revision.
  spec: {
    secret_key: "whsk_4uKPjji/dhB8w6GW3YYDnIMYvXcdHrvH6lidTr4Z+EY=",
    public_key: "whpk_uWKK13PfwqyOgrqT0OrWKcS2uVD7e89+oUKnkoPjx7w=",
    endpoint: "ep_2fVZdC8Kx9TR4ZoCqHWt3NnLgRb",
    id: "msg_2KWPBgLlAfxdpx2AI54pPJ85f4W",
    timestamp: "1674087231",
    body: '{"type":"contact.created","timestamp":"2022-11-03T20:26:10.344522Z","data":{"id":"1f81eb52-5198-4599-803e-771906343485"}}',
    signature: "v2a,kG9XaxZjAEpKB8KeQQb5G1Xp73YH5Rf5gxoWe7cg1kYMiyspdtZXe9w+dAwKeOurAwxfqmlOG/QR4m9s37DzAg==",
  },
};
const spec = vectors.spec;
const specSig = new WebhookAsymmetricSigner(spec.secret_key).sign(spec.endpoint, spec.id, new Date(Number(spec.timestamp) * 1000), spec.body);
if (specSig !== spec.signature) throw new Error("the library does not reproduce the specification's example");
console.log(JSON.stringify(vectors, null, 2));
