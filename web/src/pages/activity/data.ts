import { connectionById, connectionLabel, receipts, type Receipt } from '../../data/sample.ts'

export const thisWeek = receipts.filter((r) => r.day !== 'Last week')

/** Seconds for each receipt's timestamp, so the panel can show the full time. */
const seconds: Record<string, string> = {
  r_8f21c4: '18',
  r_8f21c3: '11',
  r_7d10a9: '52',
  r_7c0b12: '07',
  r_7b9e04: '33',
  r_6f33a1: '26',
  r_6f339e: '41',
  r_6e0c77: '03',
  r_5a91d0: '58',
  r_5a8f2b: '14',
  r_4c22e8: '39',
  r_7d02b7: '46',
  r_7a0e51: '04',
  r_7a0e4f: '02',
  r_6e5b10: '21',
  r_6e2a03: '03',
}

export function fullTime(receipt: Receipt): string {
  return `${receipt.day}, ${receipt.time}:${seconds[receipt.id] ?? '00'} CET`
}

export function domainOf(address: string): string {
  return address.slice(address.indexOf('@') + 1)
}

function fnv(input: string, seed: number): string {
  let hash = seed >>> 0
  for (const char of input) {
    hash ^= char.charCodeAt(0)
    hash = Math.imul(hash, 16777619) >>> 0
  }
  return hash.toString(16).padStart(8, '0')
}

/** A short stand-in for the receipt's hash, stable for each id. */
export function shortHash(id: string): string {
  const full = fnv(id, 2166136261) + fnv(id, 374761393)
  return `${full.slice(0, 6)}…${full.slice(-4)}`
}

/** The receipt written just before this one, which its hash chains to. */
export function previousReceipt(receipt: Receipt): Receipt | undefined {
  return receipts[receipts.indexOf(receipt) + 1]
}

export function groupByDay(list: Receipt[]): Array<{ day: Receipt['day']; rows: Receipt[] }> {
  const groups: Array<{ day: Receipt['day']; rows: Receipt[] }> = []
  for (const receipt of list) {
    const last = groups.at(-1)
    if (last?.day === receipt.day) last.rows.push(receipt)
    else groups.push({ day: receipt.day, rows: [receipt] })
  }
  return groups
}

export function matchesSearch(receipt: Receipt, query: string): boolean {
  const q = query.trim().toLowerCase()
  if (!q) return true
  return [receipt.sender, receipt.from, receipt.subject].some((field) => field.toLowerCase().includes(q))
}

/** "Claude · Chat", or the id if the connection is gone. */
function connectionText(id: string): string {
  const connection = connectionById(id)
  return connection ? connectionLabel(connection) : id
}

function csvField(value: string | number): string {
  const text = String(value)
  return /[",\n]/.test(text) ? `"${text.replace(/"/g, '""')}"` : text
}

export function receiptsCsv(list: Receipt[]): string {
  const header = ['receipt', 'day', 'time', 'connection', 'sender', 'from', 'subject', 'parts', 'body_chars', 'attachment', 'query', 'removed']
  const rows = list.map((r) => [
    r.id,
    r.day,
    r.time,
    connectionText(r.connectionId),
    r.sender,
    r.from,
    r.subject,
    r.parts,
    r.bodyChars,
    r.attachment,
    r.query,
    r.removed?.what ?? '',
  ])
  return [header, ...rows].map((row) => row.map(csvField).join(',')).join('\n')
}

export function downloadText(filename: string, text: string, type: string): void {
  const url = URL.createObjectURL(new Blob([text], { type }))
  const link = document.createElement('a')
  link.href = url
  link.download = filename
  link.click()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}
