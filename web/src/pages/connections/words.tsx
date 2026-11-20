import { BadgeCheck } from 'lucide-react'
import type * as api from '../../lib/api.ts'
import { formatDay } from '../settings/signin/shared.ts'

/*
  What the consent screen and the connections pages say of a connection, in one place. A vendor's domain, its
  client's name and every value of a formula are the vendor's or the mail's words: they are put in the page as text,
  isolated in `bdi`, never as markup.
*/

/** The most devices a connection may have at once (`oauth.FamiliesMax`, the owner's decision 13). */
export const devicesMax = 16

const partNames: Record<api.ConnectionPart, string> = {
  sender: 'who sent it and when',
  subject: 'the subject',
  body: 'the text',
  attachments: 'the attachments',
}

/** Parts in words, as they are listed: "the subject and the text". */
export function partNamesOf(parts: readonly api.ConnectionPart[]): string {
  const names = parts.map((p) => partNames[p])
  return names.length <= 1
    ? (names[0] ?? '')
    : `${names.slice(0, -1).join(', ')} and ${names[names.length - 1]}`
}

/** The parts a connection reads, in words: "who sent it and when, the subject and the text". */
export function partsWords(parts: readonly api.ConnectionPart[]): string {
  // Every connection reads the sender, asked or not (ADR 0030, "Scopes are the parts").
  const all: api.ConnectionPart[] = ['sender', 'subject', 'body', 'attachments']
  const names = all
    .filter((part) => part === 'sender' || parts.includes(part))
    .map((p) => partNames[p])
  const words =
    names.length === 1
      ? names[0]
      : `${names.slice(0, -1).join(', ')} and ${names[names.length - 1]}`
  return words.charAt(0).toUpperCase() + words.slice(1)
}

/** From when a connection reads mail. */
export function sinceWords(since: api.MailSince): string {
  if (since.new) return since.at ? `New mail only, from ${formatDay(since.at)}` : 'New mail only'
  if (since.at) return `Mail since ${formatDay(since.at)}`
  return 'All the mail Permaily keeps'
}

/** What a connection's tokens are for, as a fact: the API for apps, MCP for agents. */
export function resourceWords(resource: api.ConnectionResource): string {
  return resource === 'mcp' ? 'MCP, for agents' : 'The API, for apps'
}

const endReasons: Record<api.EndReason, string> = {
  person: 'You revoked it',
  vendor: 'The app revoked it',
  reuse: 'Permaily ended it: one of its keys was used twice',
  replaced: 'A new connection replaced it',
  account: 'Your account is being deleted',
  expired: 'It reached its end',
}

/** Why a connection or a device ended. A reason the app does not know says only that it ended. */
export function endWords(reason: api.EndReason | undefined): string {
  return (reason && endReasons[reason]) ?? 'It ended'
}

/** The name a mailbox goes by on these screens: its label, its address, or its identifier once it is gone. */
export function mailboxWords(id: string, known: ReadonlyMap<string, api.Mailbox>): string {
  const mailbox = known.get(id)
  return mailbox ? mailbox.label || mailbox.address || id : id
}

/** Who asks: the domain first, the owner the image's list names for it, then what the client calls itself. */
export function VendorLine({
  vendor,
  clientName,
}: {
  vendor: api.ConnectionVendor
  clientName: string
}) {
  return (
    <span className="flex min-w-0 flex-col gap-1">
      <span className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
        <bdi className="font-mono text-mono break-all">{vendor.domain}</bdi>
        {vendor.verified ? (
          <span className="inline-flex items-center gap-1 text-small">
            <BadgeCheck aria-hidden size={14} />
            <span>
              Verified: <bdi>{vendor.verified}</bdi>
            </span>
          </span>
        ) : (
          <span className="text-small text-ink-2">Not verified by Permaily</span>
        )}
      </span>
      {clientName && (
        <span className="text-small text-ink-2">
          Calls itself <bdi className="break-words text-ink">{clientName}</bdi>
        </span>
      )}
    </span>
  )
}

/** One part of a sentence the server wrote: fixed words, or a value set apart, marked when a model decides it. */
function Part({ part }: { part: api.SentencePart }) {
  if (!part.value && !part.model) return <>{part.text}</>
  return (
    <>
      <bdi className={part.value ? 'font-medium break-words' : undefined}>{part.text}</bdi>
      {part.model && (
        <span className="ml-1 font-mono text-micro text-ink-2">(a model decides)</span>
      )}
    </>
  )
}

/** A formula's sentence as the server wrote it: its lead, then a line for each group. Never words of the app's. */
export function Sentence({ sentence }: { sentence: api.QuerySentence }) {
  if (sentence.lines.length <= 1 && sentence.lead.length === 0) {
    return (
      <span>
        {(sentence.lines[0]?.parts ?? []).map((part, i) => (
          // The parts of a sentence never move: their place is their key.
          // biome-ignore lint/suspicious/noArrayIndexKey: a sentence's parts have no identity but their place
          <Part key={i} part={part} />
        ))}
      </span>
    )
  }
  return (
    <span className="flex flex-col gap-1">
      <span>
        {sentence.lead.map((part, i) => (
          // biome-ignore lint/suspicious/noArrayIndexKey: a sentence's parts have no identity but their place
          <Part key={i} part={part} />
        ))}
      </span>
      <ul className="flex list-disc flex-col gap-0.5 pl-5">
        {sentence.lines.map((line, i) => (
          // biome-ignore lint/suspicious/noArrayIndexKey: a sentence's lines have no identity but their place
          <li key={i}>
            {line.parts.map((part, j) => (
              // biome-ignore lint/suspicious/noArrayIndexKey: a sentence's parts have no identity but their place
              <Part key={j} part={part} />
            ))}
          </li>
        ))}
      </ul>
    </span>
  )
}

/*
  A connection's webhook (ADR 0039): what the consent screen and the connection's page say of it. These words are a
  first draft for the designer's review (docs/web.md, "Webhooks"). The host is the vendor's own domain; the time zone
  is the person's, and only the person is shown it.
*/

const weekdays: Record<string, string> = {
  monday: 'Monday',
  tuesday: 'Tuesday',
  wednesday: 'Wednesday',
  thursday: 'Thursday',
  friday: 'Friday',
  saturday: 'Saturday',
  sunday: 'Sunday',
}

/** When a schedule runs, as wall time: "every day at 08:00", "every Monday at 08:00". */
export function scheduleWords(schedule: api.WebhookSchedule): string {
  if (schedule.every === 'week') {
    return `every ${weekdays[schedule.on ?? ''] ?? 'week'} at ${schedule.at}`
  }
  return `every day at ${schedule.at}`
}

/** What a webhook tells its vendor, one line a kind: new mail, then its schedule in the person's zone. */
export function webhookLines(
  webhook: { host: string; new_mail?: boolean; schedule?: api.WebhookSchedule },
  zone: string,
): string[] {
  const lines: string[] = []
  if (webhook.new_mail) lines.push(`Tells ${webhook.host} when new mail it may read arrives`)
  if (webhook.schedule) {
    const when = scheduleWords(webhook.schedule)
    lines.push(`Tells ${webhook.host} ${when}, your time${zone ? ` (${zone})` : ''}`)
  }
  return lines
}

/** What every webhook is told, and what it is never told. */
export function webhookNeverWords(host: string): string {
  return `${host} is told only that there is mail to fetch, never the mail. It fetches with this connection, and every message it gets is in your activity.`
}

/** A webhook's state, as the connection's page says it. */
export function webhookStateWords(webhook: api.ConnectionWebhook): string {
  switch (webhook.state) {
    case 'on':
      return 'On'
    case 'off':
      return 'Turned off by you'
    default:
      return `Stopped: ${webhook.host} asked not to be told any more`
  }
}

/** Why calls to a webhook fail, in fixed words: never what the vendor's server sent. */
export function webhookFailureWords(webhook: api.ConnectionWebhook): string {
  switch (webhook.failure) {
    case 'no_answer':
      return `${webhook.host} didn't answer`
    case 'redirect':
      return `${webhook.host} answered with a redirect, which Permaily doesn't follow`
    case 'refused':
      return `Permaily couldn't make a secure connection to ${webhook.host}`
    default:
      return `${webhook.host} answered with an error`
  }
}

/** The browser's time zone, an IANA name, or "" when it doesn't say. */
export function browserTimeZone(): string {
  return Intl.DateTimeFormat().resolvedOptions().timeZone ?? ''
}

/**
 * The time zones the picker offers: the browser's list, which the server checks against the zones it was built with
 * (`time_zone_invalid`), with the browser's own zone in it.
 */
export function timeZones(own: string): string[] {
  let zones: string[] = []
  try {
    zones = Intl.supportedValuesOf('timeZone')
  } catch {
    zones = []
  }
  return own && !zones.includes(own) ? [own, ...zones].sort() : zones
}

/** "Mon 23 Nov, 08:00" in a time zone (the browser's own when none or one it doesn't know). */
export function formatWhen(iso: string, zone?: string): string {
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return ''
  const options: Intl.DateTimeFormatOptions = {
    weekday: 'short',
    day: 'numeric',
    month: 'short',
    hour: '2-digit',
    minute: '2-digit',
    hourCycle: 'h23',
  }
  try {
    return new Intl.DateTimeFormat('en-GB', { ...options, timeZone: zone || undefined }).format(
      date,
    )
  } catch {
    return new Intl.DateTimeFormat('en-GB', options).format(date)
  }
}
