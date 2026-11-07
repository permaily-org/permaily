import { LayoutGrid, ListFilter, Mail, Plug, ReceiptText, Settings, type LucideIcon } from 'lucide-react'

export type Section = {
  path: string
  label: string
  icon: LucideIcon
  purpose: string
}

// Sidebar order. Proof is a public page, not part of the app.
export const sections: Section[] = [
  { path: '/', label: 'Overview', icon: LayoutGrid, purpose: 'Who can read what, and anything waiting for a decision.' },
  { path: '/connections', label: 'Connections', icon: Plug, purpose: 'Each app, agent and vendor, what it can read and what it has read.' },
  { path: '/rules', label: 'Rules', icon: ListFilter, purpose: 'Your rules, community packs and Never share.' },
  { path: '/activity', label: 'Activity', icon: ReceiptText, purpose: 'A receipt for every email that leaves.' },
  { path: '/accounts', label: 'Accounts', icon: Mail, purpose: 'Connected mailboxes and how Permaily reaches them.' },
  { path: '/settings', label: 'Settings', icon: Settings, purpose: 'Profile, sign-in, notifications and your data.' },
]
