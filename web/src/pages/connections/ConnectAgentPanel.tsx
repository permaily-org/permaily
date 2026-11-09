import { Bar, Headline } from '../../components/ui/index.ts'

const promises = [
  'It gets a Permaily token, never a Gmail token',
  "It can ask for more, but can't take more",
  'Every email it opens gets a receipt',
  'You can revoke it here at any time',
]

/** What connecting gives the agent: nothing until the person chooses. */
export function ConnectAgentPanel() {
  return (
    <aside
      aria-labelledby="connect-gives"
      className="border-t border-rule bg-raised px-4 pt-8 pb-14 sm:px-9 lg:w-[460px] lg:shrink-0 lg:border-t-0 lg:border-l"
    >
      <p className="font-mono text-mono text-ink-2">What connecting gives it</p>
      <Headline as="h2" className="mt-5">
        <Bar width={56} height={38} />
        <span id="connect-gives">Nothing, yet.</span>
      </Headline>
      <ul className="mt-6 border-t border-rule-strong">
        {promises.map((promise) => (
          <li key={promise} className="border-b border-rule py-3 font-medium">
            {promise}
          </li>
        ))}
      </ul>
    </aside>
  )
}
