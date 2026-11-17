// The mail providers Permaily works with, as the band of logos under Home's hero shows them.
// Every entry has a mark: a single-colour shape from Simple Icons (see logos.json), beside its name.
// Each mark is its owner's trademark. Read its owner's rules before adding one, as for the vendors of the
// Companies page. A provider with no such shape is left out, not set as a name alone.

import logos from './logos.json';

export interface Integration {
  name: string;
  logo: keyof typeof logos;
}

export const INTEGRATIONS: Integration[] = [
  { name: 'Gmail', logo: 'gmail' },
  { name: 'Outlook', logo: 'outlook' },
  { name: 'Yahoo Mail', logo: 'yahoo' },
  { name: 'iCloud Mail', logo: 'icloud' },
  { name: 'Google Workspace', logo: 'google' },
  { name: 'Microsoft 365', logo: 'microsoft' },
  { name: 'Proton Mail', logo: 'protonmail' },
];
