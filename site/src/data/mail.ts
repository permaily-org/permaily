// Sample mail for the drawings on Home: a sender, a subject, and whether an app in the example asked for it.
// Plain ASCII, so every character is one cell wide in the mono face and the layers of a drawing line up.

export interface Line {
  from: string;
  subject: string;
  /** Mail an app asked for: it passes, and stays readable. */
  asked?: boolean;
  /** A part of an email that passes which the person's rule takes out of it. A drawing covers it, always. */
  detail?: string;
}

// Six to a line, and each six holds one or two emails an app asked for, so a drawing that gives a row six
// in order (`block`) has something to pass on every row. What apps ask for is the mail services send by
// themselves: codes, receipts, orders, bookings, what customers write back. What stays is a person's own.
export const LINES: Line[] = [
  { from: 'Iberia', subject: 'Your flight to Lisbon is confirmed', asked: true },
  { from: 'Holded', subject: 'Invoice F-2291, February' },
  { from: 'Mum', subject: 'Sunday lunch? Bring the photos' },
  { from: 'GitHub', subject: 'Your verification code is 482913', asked: true },
  { from: 'Vodafone', subject: 'Your March bill is ready' },
  { from: 'Dr. Elena Vidal', subject: 'Your test results are ready' },

  { from: 'Revolut', subject: 'Your February statement' },
  {
    from: 'Stripe',
    subject: 'Payout of 1,240.00 EUR is on its way',
    asked: true,
    detail: '1,240.00 EUR',
  },
  { from: 'Sam', subject: 'Re: weekend plans' },
  { from: 'City library', subject: 'Two books are due this week' },
  { from: 'Payroll', subject: 'Your payslip for February' },
  { from: 'Notion', subject: 'Reset your password' },

  { from: 'Amazon', subject: 'Your order has shipped', asked: true },
  { from: 'Landlord', subject: 'Boiler service on Friday morning' },
  { from: 'Tax office', subject: 'Your return has been received' },
  {
    from: 'Laura Gomez',
    subject: 'Feedback: checkout is much faster now',
    asked: true,
    detail: 'Laura Gomez',
  },
  { from: 'Newsletter', subject: 'Five things worth reading' },
  { from: 'School office', subject: 'School trip, sign the form' },

  { from: 'Pharmacy', subject: 'Your prescription is ready' },
  { from: 'Uber', subject: 'Trip receipt, Visa ending 4421', asked: true, detail: '4421' },
  { from: 'Gym', subject: 'Membership renews next month' },
  { from: 'Insurer', subject: 'Policy documents for 2027' },
  { from: 'Booking.com', subject: 'Hotel reservation, 14 to 17 March', asked: true },
  { from: 'Elena Ruiz', subject: 'Re: contract draft, two small changes' },

  { from: 'Bank', subject: 'New sign-in to your account' },
  { from: 'Dentist', subject: 'Your appointment on Thursday' },
  { from: 'Slack', subject: 'Your sign-in code is 739-204', asked: true },
  { from: 'Dad', subject: 'Photos from the weekend' },
  { from: 'HR', subject: 'Your holiday request was approved' },
  { from: 'Electricity', subject: 'Your bill for March is ready' },

  { from: 'Typeform', subject: 'New response to Customer survey', asked: true },
  { from: 'Marta', subject: 'Dinner on Saturday?' },
  { from: 'Clinic', subject: 'Your lab results are available' },
  { from: 'Savings', subject: 'Your interest statement' },
  { from: 'Airbnb', subject: 'Check-in on Friday, door code 4172', asked: true, detail: '4172' },
  { from: 'Neighbour', subject: 'Your parcel is with me' },

  { from: 'Car insurance', subject: 'Your renewal quote is inside' },
  { from: 'Team', subject: 'Offsite agenda and notes' },
  {
    from: 'DHL',
    subject: 'Parcel for Calle Mayor 12 arrives today',
    asked: true,
    detail: 'Calle Mayor 12',
  },
  { from: 'Google', subject: 'Security alert: new device signed in' },
  { from: 'Mortgage', subject: 'Annual statement for 2026' },
  { from: 'Ana', subject: 'Re: the keys' },

  { from: 'Apple', subject: 'Your receipt from the App Store', asked: true },
  { from: 'Kindergarten', subject: 'Holiday schedule for spring' },
  { from: 'Pension fund', subject: 'Your yearly summary' },
  { from: 'Recruiter', subject: 'Following up on our call' },
  { from: 'Reviews', subject: 'A customer left a 5-star review', asked: true },
  { from: 'Vet', subject: 'Vaccination reminder for Luna' },

  { from: 'Water', subject: 'Meter reading needed this week' },
  { from: 'Calendly', subject: 'New booking: intro call on Tuesday', asked: true },
  { from: 'Streaming', subject: 'Your plan changes next month' },
  { from: 'Lawyer', subject: 'Documents ready for your signature' },
  { from: 'Aunt Rosa', subject: 'The recipe you asked for' },
  { from: 'Therapist', subject: 'Session moved to 5 pm' },

  {
    from: 'PayPal',
    subject: 'Receipt for your payment of 18.00 EUR',
    asked: true,
    detail: '18.00 EUR',
  },
  { from: 'Alumni', subject: 'Reunion this spring' },
  { from: 'Accountant', subject: 'Q1 figures attached' },
  { from: 'Mobile', subject: 'Your new SIM is on its way' },
  { from: 'Renfe', subject: 'Madrid to Valencia, 6 April', asked: true },
  { from: 'Charity', subject: 'Thank you for your donation' },

  { from: 'Brother', subject: 'Call me when you can' },
  { from: 'Shop', subject: 'Order 1042 is confirmed', asked: true },
  { from: 'Notary', subject: 'Appointment confirmed for Monday' },
  { from: 'Club', subject: 'Match moved to Sunday' },
  { from: 'Cloud storage', subject: 'Reset your password' },
  { from: 'Old friend', subject: 'Long time no see' },

  { from: 'Support', subject: 'Customer reply: order arrived damaged', asked: true },
  { from: 'Garage', subject: 'Your car is ready to collect' },
  { from: 'Parking', subject: 'Your parking receipt, 12 March', asked: true },
  { from: 'Hospital', subject: 'Appointment letter attached' },
  { from: 'Flatmate', subject: 'Rent and bills for April' },
  { from: 'Credit card', subject: 'Your statement is ready' },
];

/** What sits between a sender and its subject, and between one email and the next. */
export const GAP = '  ';
export const SEP = '   ·   ';

const at = (index: number) => LINES[((index % LINES.length) + LINES.length) % LINES.length]!;

/** The emails of one row: a different run of the sample for each row, with no email twice. */
export const rowOf = (row: number, count: number, step = 3): Line[] =>
  Array.from({ length: count }, (_, k) => at(row * 7 + k * step));

// Mail an app asked for that goes on whole, for the part of a drawing that shows what is shared and has no
// way to cover a detail.
const SHARED = LINES.filter((line) => line.asked && !line.detail);

/** The emails of one row of shared mail: a different run for each row, with no email twice. */
export const sharedOf = (row: number, count: number): Line[] =>
  Array.from(
    { length: count },
    (_, k) => SHARED[(((row * 5 + k * 3) % SHARED.length) + SHARED.length) % SHARED.length]!,
  );

/**
 * The emails of one row, taken in order: row 0 holds the first `count`, row 1 the next, and so on. No email
 * is in two rows as long as the rows hold no more than the sample does.
 */
export const block = (row: number, count: number): Line[] =>
  Array.from({ length: count }, (_, k) => at(row * count + k));

/** A row as one string, the way the drawings set it. */
export const rowText = (lines: Line[]) =>
  lines.map((line) => `${line.from}${GAP}${line.subject}${SEP}`).join('');
