// The latest published release of the enclave image, read once at build time,
// so the site shows the real PCR0 instead of an example value.
//
// The measurement comes from pcrs.json in the latest GitHub release. Set
// PERMAILY_RELEASE_JSON to a JSON object {"tag","pcr0"} to override it (tests,
// offline builds). When no release exists yet, `release` is null and the site
// says so instead of showing a made-up hash.

const SOURCE = 'https://github.com/permaily-org/permaily';

export type Release = { tag: string; pcr0: string; url: string };

async function load(): Promise<Release | null> {
  const override = process.env.PERMAILY_RELEASE_JSON;
  if (override) {
    const { tag, pcr0 } = JSON.parse(override) as { tag: string; pcr0: string };
    return { tag, pcr0, url: `${SOURCE}/releases/tag/${tag}` };
  }
  try {
    const res = await fetch(`${SOURCE}/releases/latest/download/pcrs.json`, {
      signal: AbortSignal.timeout(10_000),
    });
    if (!res.ok) return null;
    const pcrs = (await res.json()) as { PCR0?: string };
    const tag = res.url.match(/\/download\/([^/]+)\/pcrs\.json$/)?.[1];
    if (!tag || !pcrs.PCR0 || !/^[0-9a-f]{96}$/.test(pcrs.PCR0)) return null;
    return { tag, pcr0: pcrs.PCR0, url: `${SOURCE}/releases/tag/${tag}` };
  } catch {
    return null;
  }
}

export const release = await load();
