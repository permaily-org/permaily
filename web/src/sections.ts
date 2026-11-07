import { EyeOff, LayoutGrid, ListFilter, Mail, Plug, ReceiptText, Settings, type LucideIcon } from 'lucide-react'
import { attentionCount } from './data/sample.ts'

export type Section = {
  path: string
  label: string
  icon: LucideIcon
  /** How many items in this section are waiting on the person. Shown only when above zero. */
  waiting: number
}

// Sidebar order. Proof is a public page, not part of the app.
export const sections: Section[] = [
  { path: '/', label: 'Overview', icon: LayoutGrid, waiting: 0 },
  { path: '/connections', label: 'Connections', icon: Plug, waiting: attentionCount('connections') },
  { path: '/rules', label: 'Rules', icon: ListFilter, waiting: attentionCount('rules') },
  { path: '/never-share', label: 'Never share', icon: EyeOff, waiting: 0 },
  { path: '/activity', label: 'Activity', icon: ReceiptText, waiting: 0 },
  { path: '/accounts', label: 'Accounts', icon: Mail, waiting: 0 },
  { path: '/settings', label: 'Settings', icon: Settings, waiting: 0 },
]
