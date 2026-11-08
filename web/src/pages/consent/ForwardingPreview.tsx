import { ButtonAnchor, Count, Headline, MailRow, SectionHeading, TermRow } from '../../components/ui/index.ts'
import { gmailQuery, gmailSearchUrl, gmailShortQuery, matchDetails, queryName, type QueryId } from './data.ts'
import { PreviewEyebrow } from './InboxPreview.tsx'

const stays: Array<[number, number, number]> = [
  [44, 92, 410],
  [38, 108, 300],
  [44, 76, 520],
  [44, 100, 240],
]

/** A forwarding mailbox has no inbox to preview, so the search runs in Gmail instead. */
export function ForwardingPreview({ name, queries }: { name: string; queries: QueryId[] }) {
  return (
    <>
      <PreviewEyebrow>Preview for a forwarding mailbox</PreviewEyebrow>
      <Headline as="h2" size="display-l" className="mt-2">
        <Count tone="bar">0</Count>
        <span>emails today</span>
      </Headline>
      <p className="mt-2.5 text-ink-2">
        Forwarding only sends Permaily new mail, so there's no inbox here to preview. {name} gets nothing until new mail matches.
      </p>

      <div className="mt-5 bg-sunken px-4 py-4">
        <h3 className="font-medium">Check it against your real mail</h3>
        {queries.length > 0 ? (
          <>
            <p className="mt-1.5 text-small text-ink-2">This opens the same search in Gmail, in your own account. You see the matches, and Permaily sees nothing.</p>
            <div className="mt-3 flex min-w-0 items-center gap-3 max-sm:flex-wrap max-sm:gap-y-2">
              <ButtonAnchor variant="secondary" size="sm" className="bg-raised" href={gmailSearchUrl(gmailQuery(queries))}>
                See matches in Gmail
                <span className="sr-only"> (opens in a new tab)</span>
              </ButtonAnchor>
              <code className="min-w-0 truncate font-mono text-mono text-ink-2 max-sm:whitespace-normal">{gmailShortQuery(queries)}</code>
            </div>
          </>
        ) : (
          <p className="mt-1.5 text-small text-ink-2">Tick a kind of mail and its Gmail search appears here.</p>
        )}
      </div>

      {queries.length > 0 && (
        <section className="mt-5">
          <SectionHeading title="New mail that would match" />
          {queries.map((id) => (
            <div key={id}>
              {queries.length > 1 && <h3 className="border-b border-rule pt-4 pb-2 text-small font-medium">{queryName(id)}</h3>}
              {matchDetails(id).map((row) => (
                <TermRow key={row.label} label={row.label}>
                  <span className={row.mono ? 'font-mono text-mono' : undefined}>{row.value}</span>
                </TermRow>
              ))}
            </div>
          ))}
        </section>
      )}

      <section className="mt-5">
        <SectionHeading title="Everything else stays in Gmail" />
        {stays.map((widths, i) => (
          <MailRow key={i} kind="withheld" widths={widths} />
        ))}
      </section>
    </>
  )
}
