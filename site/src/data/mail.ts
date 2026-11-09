// Sample mail. The numbers on the site add up: 47 + 212 + 388 + 1,204 + 10,129 + 324 = 12,304.

export type AppId = 'trip' | 'ledgerly';

export interface App {
  id: AppId;
  name: string;
  /** How many emails it can read with Permaily. */
  count: number;
  reads: string;
}

// The two apps the hero compares. Their counts are the access grid's columns:
// flights and hotels for Trip App, and invoices plus receipts for Ledgerly.
export const APPS: App[] = [
  { id: 'trip', name: 'Trip App', count: 47, reads: 'Flights and hotels' },
  { id: 'ledgerly', name: 'Ledgerly', count: 600, reads: 'Invoices and receipts' },
];

export interface Mail {
  date: string;
  from: string;
  subject: string;
  snippet: string;
  /** The apps that can read it with Permaily. */
  apps?: AppId[];
  /** A detail a rule removes from an email the app can read. It has to appear in the snippet. */
  cut?: { app: AppId; text: string };
  /** Held back from every connection by a rule, such as Sign-in and security. */
  never?: boolean;
}

export const MAIL: Mail[] = [
  {
    date: '14 Mar',
    from: 'Holded',
    subject: 'Invoice F-2291, February',
    snippet: 'Total due €1,240.00, payable by 31 March',
    apps: ['ledgerly'],
  },
  {
    date: '12 Mar',
    from: 'Iberia',
    subject: 'Your flight to Lisbon is confirmed',
    snippet: 'Booking KX72QM, Madrid to Lisbon, 14 March at 09:40',
    apps: ['trip'],
  },
  {
    date: '11 Mar',
    from: 'Vodafone',
    subject: 'Your March bill is ready',
    snippet: 'Card ending 4417 will be charged €38.90',
    apps: ['ledgerly'],
    cut: { app: 'ledgerly', text: '4417' },
  },
  {
    date: '10 Mar',
    from: 'Dr. Elena Vidal',
    subject: 'Your test results are ready',
    snippet: 'Log in to the patient portal to see them',
  },
  {
    date: '09 Mar',
    from: 'Booking.com',
    subject: 'Hotel reservation, 14 to 17 March',
    snippet: 'Hotel in Lisbon, 3 nights, free cancellation until 12 March',
    apps: ['trip'],
  },
  {
    date: '07 Mar',
    from: 'Revolut',
    subject: 'Your February statement',
    snippet: 'Closing balance €4,812.36, spent €1,966.20 this month',
  },
  {
    date: '02 Mar',
    from: 'Booking.com',
    subject: 'Reset your password',
    snippet: 'Use this link in the next 30 minutes',
    never: true,
  },
  {
    date: '27 Feb',
    from: 'Renfe',
    subject: 'Madrid to Valencia, 6 April',
    snippet: 'Train AVE 3061, coach 4, seat 12A',
    apps: ['trip'],
  },
  {
    date: '24 Feb',
    from: 'Google',
    subject: '482913 is your verification code',
    snippet: 'Don’t share this code with anyone',
    never: true,
  },
  {
    date: '19 Feb',
    from: 'Airbnb',
    subject: 'Reservation confirmed in Porto',
    snippet: 'Door code 7351, check-in Friday 24 April',
    apps: ['trip'],
    cut: { app: 'trip', text: '7351' },
  },
];

/** Whether an app can read an email with Permaily. */
export const reads = (mail: Mail, app: AppId) => mail.apps?.includes(app) ?? false;

// The seven emails used in the How it works animation, which shows Trip App.
export const LANES: Mail[] = [MAIL[1]!, MAIL[5]!, MAIL[3]!, MAIL[4]!, MAIL[6]!, MAIL[0]!, MAIL[9]!];
