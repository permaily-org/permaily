// The vendors of the Companies page, each with the job it was given.
// The logos are single-colour shapes from Simple Icons (see logos.json). Each logo is its owner's trademark,
// so check each brand's rules before publishing.

import logos from './logos.json';

export type LogoKey = keyof typeof logos;

export interface Vendor {
  logo: LogoKey;
  name: string;
  intent: string;
}

export const VENDORS: Vendor[] = [
  { logo: 'chatgpt', name: 'ChatGPT', intent: 'Draft client replies' },
  { logo: 'claude', name: 'Claude', intent: 'Summarize contracts' },
  { logo: 'salesforce', name: 'Salesforce', intent: 'Log sales emails' },
  { logo: 'hubspot', name: 'HubSpot', intent: 'Track deal replies' },
  { logo: 'zendesk', name: 'Zendesk', intent: 'Create support tickets' },
  { logo: 'expensify', name: 'Expensify', intent: 'Collect receipts' },
];

// A sample company inbox. The senders are invented.
export interface CompanyMail {
  date: string;
  from: string;
  subject: string;
  snippet: string;
  /** The vendors that can read it with Permaily, each for its own job. */
  vendors: LogoKey[];
  /** A detail the company's policy removes from an email a vendor can read. It has to appear in the snippet. */
  cut?: { app: LogoKey; text: string };
}

export const COMPANY_MAIL: CompanyMail[] = [
  {
    date: '14 Mar',
    from: 'Halden Logistics',
    subject: 'Re: renewal quote for 2027',
    snippet: 'Can you hold the price if we sign this month?',
    vendors: ['salesforce', 'hubspot', 'chatgpt'],
  },
  {
    date: '14 Mar',
    from: 'Support form',
    subject: 'Can’t export the monthly report',
    snippet: 'From Northwind Foods, marked urgent',
    vendors: ['zendesk'],
  },
  {
    date: '13 Mar',
    from: 'Okafor & Partners',
    subject: 'Services agreement, draft 3',
    snippet: 'Changes to clauses 4 and 9 are marked',
    vendors: ['claude'],
  },
  {
    date: '13 Mar',
    from: 'Lucía Ferrer',
    subject: 'Question about delivery times',
    snippet: 'Is two weeks still realistic for the June order?',
    vendors: ['chatgpt'],
  },
  {
    date: '12 Mar',
    from: 'Payroll',
    subject: 'Your March payslip',
    snippet: 'Net pay and deductions for this period',
    vendors: [],
  },
  {
    date: '12 Mar',
    from: 'City Taxi',
    subject: 'Receipt for 11 March',
    snippet: 'Airport to office, €23.40, card ending 4417',
    vendors: ['expensify'],
    cut: { app: 'expensify', text: '4417' },
  },
  {
    date: '11 Mar',
    from: 'Northwind Foods',
    subject: 'Intro call next week?',
    snippet: 'We’re comparing three suppliers',
    vendors: ['salesforce'],
  },
  {
    date: '09 Mar',
    from: 'IT desk',
    subject: 'Reset your password',
    snippet: 'Use this link in the next 30 minutes',
    vendors: [],
  },
  {
    date: '08 Mar',
    from: 'Hotel Baixa',
    subject: 'Invoice for 2 nights',
    snippet: 'Lisbon, 6 to 8 March, €212.00',
    vendors: ['expensify'],
  },
  {
    date: '07 Mar',
    from: 'Halden Logistics',
    subject: 'Signed order form',
    snippet: 'Countersigned copy attached, terms as agreed',
    vendors: ['claude', 'salesforce'],
  },
];

/** Whether a vendor can read an email with Permaily. */
export const reads = (mail: CompanyMail, vendor: LogoKey) => mail.vendors.includes(vendor);

export { logos };
