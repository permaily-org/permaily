import { TextLink } from '../../components/ui/index.ts'

/** Wordmark and a way back, for the full-page mailbox flows. */
export function FlowHeader() {
  return (
    <header className="flex items-center justify-between gap-6">
      <span className="text-wordmark font-semibold">Permaily</span>
      <TextLink to="/accounts">Back to accounts</TextLink>
    </header>
  )
}
