import { Bar, Headline } from '../../components/ui/index.ts'

const promises = [
  {
    title: 'It gets a Permaily token, never a Gmail token',
    detail: "The token only works for what you approve, and it can't reach anything else in your account.",
  },
  { title: "It can ask for more, but can't take more", detail: 'If it needs something new, you get a new approval screen.' },
  { title: 'Every email it opens gets a receipt', detail: 'You can see exactly what left, and when.' },
  { title: 'You can revoke it here at any time', detail: "Access ends at once. Mail it has already received can't be recalled." },
]

/** What connecting gives the agent: nothing until the person chooses. */
export function ConnectAgentPanel() {
  return (
    <aside aria-labelledby="connect-gives" className="border-t border-rule bg-raised px-4 pt-8 pb-14 sm:px-9 lg:w-[460px] lg:shrink-0 lg:border-t-0 lg:border-l">
      <p className="font-mono text-mono text-ink-2">What connecting gives it</p>
      <Headline as="h2" className="mt-5">
        <Bar width={56} height={38} />
        <span id="connect-gives">Nothing, yet.</span>
      </Headline>
      <p className="mt-5 text-ink-2">Connecting alone gives your agent no mail. You choose what it can read on the next screen.</p>
      <ul className="mt-5 border-t border-rule-strong">
        {promises.map(({ title, detail }) => (
          <li key={title} className="flex flex-col gap-0.5 border-b border-rule py-3">
            <h3 className="font-medium">{title}</h3>
            <p className="text-small text-ink-2">{detail}</p>
          </li>
        ))}
      </ul>
    </aside>
  )
}
