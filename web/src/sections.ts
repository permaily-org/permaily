import { Inbox, ListFilter, type LucideIcon, Mail, Plug, ReceiptText, Settings } from 'lucide-react'

export type Section = {
  path: string
  label: string
  icon: LucideIcon
}

// Sidebar order. Proof is a public page, not part of the app.
export const sections: Section[] = [
  { path: '/inbox', label: 'Inbox', icon: Inbox },
  { path: '/connections', label: 'Connections', icon: Plug },
  { path: '/rules', label: 'Rules', icon: ListFilter },
  { path: '/activity', label: 'Activity', icon: ReceiptText },
  { path: '/accounts', label: 'Accounts', icon: Mail },
  { path: '/settings', label: 'Settings', icon: Settings },
]
