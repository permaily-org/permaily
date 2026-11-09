// Every outside link lives here, so each one can be pointed at the real thing in one place.

// Starlight builds the docs under this path, from src/content/docs/docs/.
const DOCS = '/docs';
const SOURCE = 'https://github.com/permaily-org/permaily';

export const SITE = {
  name: 'Permaily',
  tagline: 'Share only the part of your email each app or agent needs.',
  build: '7f3a…c91e',
  hash: '7f3a9d04be12c7a85e61f0d2b9437ac6e8150fd3a2c46b79d1e08f35ab6c72d49e03b18f6ca4d7250be9f1c38a7dc91e',
};

export const LINKS = {
  signup: '/signup/',
  login: '/login/',
  docs: `${DOCS}/`,
  queries: `${DOCS}/queries/`,
  selfHosting: `${DOCS}/self-hosting/`,
  trustModel: `${DOCS}/trust-model/`,
  source: SOURCE,
  releaseLog: `${SOURCE}/releases`,
  vulnerability: 'mailto:security@permaily.com',
  // These pages aren't written yet. They need legal review first.
  terms: '#terms',
  dataProcessing: '#data-processing',
  subprocessors: '#subprocessors',
};

export const NAV = [
  { label: 'Developers', href: '/developers/' },
  { label: 'Companies', href: '/companies/' },
  { label: 'Security', href: '/security/' },
  { label: 'Pricing', href: '/pricing/' },
];
