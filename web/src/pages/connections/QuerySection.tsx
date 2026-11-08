import { useState } from 'react'
import { Bar, DayHeading, Mark, SectionHeading, TermRow, TextAction, TextLink, ViewSwitch } from '../../components/ui/index.ts'
import { exampleReceipt, fieldRows, queryDefinition, queryFile, typeText, type ExampleValue, type Field, type QueryDefinition } from '../../data/queries.ts'
import { useRules } from '../../data/ruleScopes.ts'
import { formatCount, receipts, ruleCovers, type Connection, type Receipt, type Rule } from '../../data/sample.ts'
import { cx } from '../../lib/styles.ts'
import { dayLabels } from './DetailData.ts'

type View = 'readable' | 'file'

/** Columns that need room, by the width of the output table itself: what each field is from 448px, its type from 672px. */
const labelColumn = '@max-md:hidden'
const typeColumn = '@max-2xl:hidden'

/**
 * What the connection asked for: the query that picks emails, and the output it gets back for each
 * one. People can read it in plain words, or see the exact file it was approved as.
 */
export function QuerySection({ connection }: { connection: Connection }) {
  const [view, setView] = useState<View>('readable')
  const definition = queryDefinition(connection.id)
  if (!definition) return null

  return (
    <section aria-labelledby="query-heading">
      <SectionHeading
        id="query-heading"
        title="Query and output"
        action={
          <ViewSwitch
            size="sm"
            label="Show the query and output as"
            value={view}
            onChange={setView}
            options={[
              { value: 'readable', label: 'Readable' },
              { value: 'file', label: 'File' },
            ]}
          />
        }
      />
      {view === 'readable' ? <Readable connection={connection} definition={definition} /> : <QueryFileView connection={connection} />}
    </section>
  )
}

/** Every email comes with these, before the fields the query asks for. */
function basicInfo(definition: QueryDefinition): Field[] {
  return [
    { name: 'from', label: 'Who sent it', type: 'string' },
    { name: 'date', label: 'When it arrived', type: 'datetime' },
    { name: 'subject', label: 'Subject', type: 'string' },
    ...(definition.parts.includes('body_text') ? [{ name: 'body_text', label: 'Body text', type: 'string' } satisfies Field] : []),
  ]
}

function basicValues(receipt: Receipt): Record<string, ExampleValue> {
  return {
    from: receipt.from,
    date: `${dayLabels[receipt.day]}, ${receipt.time}`,
    subject: receipt.subject,
    body_text: `${formatCount(receipt.bodyChars)} characters`,
  }
}

function Readable({ connection, definition }: { connection: Connection; definition: QueryDefinition }) {
  const rules = useRules()
  const removing = (ruleId: string) => rules.find((r) => r.id === ruleId && r.effect === 'remove' && ruleCovers(r.appliesTo, connection))
  const receipt = exampleReceipt(definition, receipts) ?? receipts.find((r) => r.connectionId === connection.id)
  const values: Record<string, ExampleValue> = receipt ? { ...basicValues(receipt), ...definition.example?.values } : {}
  const groups = [
    { label: 'Basic info', rows: fieldRows(basicInfo(definition)) },
    { label: 'Fields it asks for', rows: fieldRows(definition.fields) },
  ]

  return (
    <>
      <TermRow label="Matches">
        <span className="flex flex-col gap-1">
          <span>{definition.matchText}</span>
          {definition.search.length > 0 && <code className="font-mono text-micro text-ink-2 [overflow-wrap:anywhere]">{definition.search.join(' ')}</code>}
        </span>
      </TermRow>

      <div className="mt-5 @container">
        {/* The header cells set the column widths. A hidden column hides in every row, so the rest stay aligned. */}
        <table className="w-full table-fixed border-collapse">
          <caption className="sr-only">Output for each email</caption>
          <thead>
            <tr className="border-b border-rule-strong text-left text-small text-ink-2">
              <th scope="col" className="w-[45%] pb-2 font-normal @md:w-[156px]">
                Output
              </th>
              <th scope="col" className={cx('pb-2 font-normal', labelColumn)}>
                What it is
              </th>
              <th scope="col" className={cx('w-[150px] pb-2 font-normal', typeColumn)}>
                Type
              </th>
              {receipt && (
                <th scope="col" className="pb-2 font-normal @md:w-[184px]">
                  From {receipt.sender}, {dayLabels[receipt.day].toLowerCase()}
                </th>
              )}
            </tr>
          </thead>
          {groups.map((group) => (
            <tbody key={group.label}>
              {/* One cell per column, so the heading lines up whichever columns show. It may run into the empty cells. */}
              <tr>
                <th scope="rowgroup" className="text-left font-normal whitespace-nowrap">
                  <DayHeading>{group.label}</DayHeading>
                </th>
                <td className={labelColumn} />
                <td className={typeColumn} />
                {receipt && <td />}
              </tr>
              {group.rows.length === 0 ? (
                <tr className="border-b border-rule">
                  <td className="py-2.5 text-body text-ink-2">None</td>
                  <td className={labelColumn} />
                  <td className={typeColumn} />
                  {receipt && <td />}
                </tr>
              ) : (
                group.rows.map(({ field, depth, path }) => (
                  <tr key={path} className="border-b border-rule align-top">
                    <td className="py-2.5 pr-3">
                      <span className="flex items-center font-mono text-mono" style={{ paddingLeft: depth * 14 }}>
                        {depth > 0 && <span aria-hidden className="mr-1.5 mb-1 inline-block size-2 shrink-0 border-b border-l border-ink-3" />}
                        <span className="[overflow-wrap:anywhere]">{field.name}</span>
                      </span>
                      {/* Without its own column, what the field is sits under its name. */}
                      <span className="block text-small text-ink-2 @md:hidden" style={{ paddingLeft: depth * 14 }}>
                        {field.label}
                      </span>
                    </td>
                    <td className={cx('py-2.5 pr-3 text-body', labelColumn)}>{field.label}</td>
                    <td className={cx('py-2.5 pr-3 text-small text-ink-2', typeColumn)}>{typeText(field)}</td>
                    {receipt && <td className="py-2.5">{!field.fields && <Value value={values[path]} removing={removing} />}</td>}
                  </tr>
                ))
              )}
            </tbody>
          ))}
        </table>
      </div>
    </>
  )
}

/** A value that leaves is marked as shared. One a rule takes out is a bar. An empty one is a dash. */
function Value({ value, removing }: { value: ExampleValue | undefined; removing: (ruleId: string) => Rule | undefined }) {
  if (value === undefined || value === null) {
    return (
      <span className="text-ink-3">
        —<span className="sr-only">empty</span>
      </span>
    )
  }
  if (typeof value === 'object') {
    const rule = removing(value.removableBy)
    if (!rule) return <Value value={value.value} removing={removing} />
    return (
      <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <Bar width={44} />
        <span className="text-small text-ink-2">
          removed by{' '}
          <TextLink to={`/rules/${rule.id}`} size="sm">
            {rule.name}
          </TextLink>
        </span>
      </span>
    )
  }
  return (
    <span className="[overflow-wrap:anywhere]">
      <Mark size="sm">{typeof value === 'boolean' ? (value ? 'Yes' : 'No') : value}</Mark>
    </span>
  )
}

type CopyState = 'idle' | 'copied' | 'failed'

/** The exact file the connection was approved as. Comments are dimmer, as in the docs. */
function QueryFileView({ connection }: { connection: Connection }) {
  const [copy, setCopy] = useState<CopyState>('idle')
  const file = queryFile(connection)
  const name = `${connection.id}.yml`

  async function copyFile() {
    try {
      await navigator.clipboard.writeText(file)
      setCopy('copied')
    } catch {
      setCopy('failed')
    }
  }

  return (
    <div className="pt-3.5">
      <div className="flex items-center justify-between gap-4">
        <span className="font-mono text-mono text-ink-2">{name}</span>
        <TextAction size="sm" onClick={copyFile}>
          {copy === 'copied' ? 'Copied' : 'Copy'}
          <span className="sr-only"> {name}</span>
        </TextAction>
      </div>
      <pre tabIndex={0} aria-label={name} className="mt-2 overflow-x-auto bg-sunken px-4 py-3.5 font-mono text-mono leading-[1.65] focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ink">
        <code>
          {file.split('\n').map((line, i) => (
            <span key={i} className={line.startsWith('#') ? 'text-ink-2' : 'text-ink'}>
              {line}
              {'\n'}
            </span>
          ))}
        </code>
      </pre>
      <p aria-live="polite" className="mt-1.5 text-small text-ink-2 empty:hidden">
        {copy === 'failed' ? "Couldn't copy. Select the file and copy it yourself." : ''}
      </p>
    </div>
  )
}
