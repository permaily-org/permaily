// What the legal pages say about who runs the service and under which law, in
// one place. docs/legal.md explains each value, the decisions behind it and
// what is still open. Values marked TODO LEGAL depend on something not
// confirmed yet; docs/legal.md lists each one. A visitor never sees the marker.

// The version of the terms of service, the date it was published. Change it
// whenever the terms change, together with the service's domain.TermsVersion
// (internal/domain) and the web app's (web/src/lib/terms.ts):
// site/tests/content.test.ts checks that the page shows it and that it is the
// service's. The service records on each account the version its person
// agreed to and when (ADR 0017, "Agreeing to the terms").
export const TERMS_VERSION = '2026-11-12';

// The date shown at the top of every legal page, which is also the date the
// page takes effect.
export const LEGAL_UPDATED = '12 November 2026';

// Decided by the maintainers on 2026-11-12: one natural person operates the
// service and is the contracting party. The licensors of the code
// (license.md) are a separate matter.
export const OPERATOR = {
  name: 'Tony Raúl Blanco Fernández',
  // TODO LEGAL: the state is taken from a public profile and not confirmed.
  // If it is not Florida, change STATE, GOVERNING_LAW, COURTS and the Florida
  // sections of docs/legal.md together.
  state: 'Florida',
  country: 'the United States',
  // TODO LEGAL: no postal address is published for now (docs/legal.md, "No
  // postal address"). Do not add a placeholder here.
  privacy: 'privacy@permaily.com',
  support: 'support@permaily.com',
  security: 'security@permaily.com',
};

// TODO LEGAL: Florida, see OPERATOR.state.
export const GOVERNING_LAW = 'the State of Florida';
export const COURTS = 'the state and federal courts located in Florida';

// Who may hold an account. 18 keeps the service clear of COPPA and of a
// minor's capacity to agree to the terms.
export const MINIMUM_AGE = 18;

// Decided on 2026-11-12: invitations are not limited to one country. The
// service is invitation-only because it is closed for now, not to limit where
// people live. Who may not be invited is set by the United States sanctions
// the operator is bound by; the terms say so in general words, and the rule
// the operator follows before each invitation, with the places it covers
// today, is in docs/legal.md, "Procedures". Inviting someone in the EU, the
// EEA, Switzerland or the UK is the operator's choice at that moment, against
// the checklist in docs/legal.md, "Before inviting anyone in Europe".
