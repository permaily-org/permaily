import { connections, mailboxes, vendors, type Mailbox } from '../../data/sample.ts'

export const forwardingMailbox = mailboxes.find((m) => m.method === 'forwarding') ?? mailboxes[0]

/** The address Gmail forwards to. Unique per mailbox. */
export const forwardingAddress = 'm7x2k9q4@in.permaily.com'

/** What the person pastes into a Gmail filter. It covers what connections' approved queries ask for. Rules apply after the mail arrives. */
export const filterSearch =
  'from:(iberia.com OR booking.com OR renfe.es OR airbnb.com OR holded.com OR vodafone.es OR iberdrola.es OR endesa.es) OR subject:(invoice OR factura OR receipt)'

export const expectations = [
  {
    title: 'Gmail will show you a notice',
    detail: "It appears for a week after you turn forwarding on. That's expected, and it means Gmail is keeping you informed.",
  },
  {
    title: 'Only new mail is forwarded',
    detail: 'Older mail stays out of reach. A Google account connection can include it later.',
  },
  {
    title: 'Approving new queries means updating the filter',
    detail: 'Permaily tells you when your filter falls behind what your connections ask for.',
  },
  {
    title: 'You can stop in Gmail at any time',
    detail: "Deleting the filter stops the mail, and you don't need Permaily to do it.",
  },
]

export const methods = [
  {
    id: 'forwarding',
    title: 'Forwarding',
    summary: 'Gmail sends Permaily only the mail your connections ask for.',
    facts: [
      ['Access to your account', 'None'],
      ['What it receives', 'Only what your Gmail filter forwards'],
      ['History', 'New mail only'],
      ['Actions', 'None, so no inbox tidying'],
      ['Setup', 'About 3 minutes in Gmail settings'],
    ],
  },
  {
    id: 'google',
    title: 'Google account',
    summary: 'Permaily fetches matching mail when a connection asks.',
    facts: [
      ['Access to your account', 'Read and organise, revocable in Google'],
      ['What it receives', 'Fetched on demand, never stored'],
      ['History', 'All of it'],
      ['Actions', 'Label, archive and draft, if you allow'],
      ['Setup', 'About 30 seconds'],
    ],
  },
] as const

export function connectionsUsing(mailbox: Mailbox) {
  return connections.filter((c) => mailbox.usedBy.includes(c.id))
}

/** The vendors behind the connections that read a mailbox. */
export function vendorsUsing(mailbox: Mailbox) {
  const using = connectionsUsing(mailbox)
  return vendors.filter((v) => using.some((c) => c.vendorId === v.id))
}

export function listNames(names: string[]): string {
  if (names.length <= 1) return names.join('')
  return `${names.slice(0, -1).join(', ')} and ${names.at(-1)}`
}
