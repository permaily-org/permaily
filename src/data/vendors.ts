// Sample numbers that add up to 18,164. The logos are single-colour shapes from Simple Icons (see logos.json).
// Each logo is its owner's trademark, so check each brand's rules before publishing.

import logos from './logos.json';

export type LogoKey = keyof typeof logos;

export interface Vendor {
  logo: LogoKey;
  name: string;
  intent: string;
  emails: number;
}

export const VENDORS: Vendor[] = [
  { logo: 'chatgpt', name: 'ChatGPT', intent: 'Draft client replies', emails: 1040 },
  { logo: 'claude', name: 'Claude', intent: 'Summarize contracts', emails: 862 },
  { logo: 'salesforce', name: 'Salesforce', intent: 'Log sales emails', emails: 9120 },
  { logo: 'hubspot', name: 'HubSpot', intent: 'Track deal replies', emails: 3820 },
  { logo: 'zendesk', name: 'Zendesk', intent: 'Create support tickets', emails: 2204 },
  { logo: 'expensify', name: 'Expensify', intent: 'Collect receipts', emails: 1118 },
];

export { logos };
