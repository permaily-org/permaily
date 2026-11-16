import { useCallback, useEffect, useState } from 'react'
import { Headline, SectionHeading, TextLink } from '../../components/ui/index.ts'
import * as api from '../../lib/api.ts'
import { useSession } from '../../lib/session.tsx'
import { pagePadding } from '../../lib/styles.ts'
import { LoadState } from '../accounts/shared.tsx'
import { messageFor } from '../auth/messages.ts'
import { formatDay } from '../settings/signin/shared.ts'
import { endWords, resourceWords } from './words.tsx'

/** The account's connections from the server, and a way to load them again or take one the server answered. */
export function useConnections() {
  const { failed } = useSession()
  const [list, setList] = useState<api.Connection[] | null>(null)
  const [failure, setFailure] = useState<string | null>(null)

  const load = useCallback(async () => {
    setFailure(null)
    try {
      setList((await api.connections()).connections)
    } catch (error) {
      const code = failed(error)
      if (code) setFailure(messageFor(code))
    }
  }, [failed])

  useEffect(() => {
    void load()
  }, [load])

  const replace = useCallback((connection: api.Connection) => {
    setList((now) => now?.map((c) => (c.id === connection.id ? connection : c)) ?? now)
  }, [])

  return { list, failure, load, replace }
}

/**
 * The address of a vendor's overview, by its domain, and of one of its connections. The overview's ends in
 * `/overview`, never in the domain: the app's server answers an address whose last part looks like a file's name
 * (`claude.ai`) as a missing file, so opening it in another tab or reloading it would fail (internal/webui).
 */
export function vendorPath(domain: string, connection?: string): string {
  const base = `/connections/${encodeURIComponent(domain)}`
  return `${base}/${connection ? encodeURIComponent(connection) : 'overview'}`
}

/** The connections grouped by vendor domain, in the order each vendor first appears. */
export function byVendor(list: api.Connection[]): Array<[api.ConnectionVendor, api.Connection[]]> {
  const groups = new Map<string, [api.ConnectionVendor, api.Connection[]]>()
  for (const c of list) {
    const group = groups.get(c.vendor.domain)
    if (group) group[1].push(c)
    else groups.set(c.vendor.domain, [c.vendor, [c]])
  }
  return [...groups.values()]
}

function headline(active: number): string {
  if (active === 0) return 'No connections.'
  return active === 1 ? '1 connection.' : `${active} connections.`
}

/** Every vendor with its connections underneath: the active ones, and the ones that ended in the last 30 days. */
export function ConnectionsPage() {
  const { list, failure, load } = useConnections()
  const active = list?.filter((c) => c.state === 'active').length ?? 0

  return (
    <div className={pagePadding}>
      <Headline>{list ? headline(active) : 'Connections'}</Headline>
      <div className="mt-6 flex max-w-[880px] flex-col gap-9">
        {list === null ? (
          <LoadState failure={failure} what="Your connections" onRetry={() => void load()} />
        ) : list.length === 0 ? (
          <p className="text-ink-2">
            An app or an agent asks to connect from its own settings. You decide here.
          </p>
        ) : (
          byVendor(list).map(([vendor, connections]) => (
            <section key={vendor.domain} aria-labelledby={`vendor-${vendor.domain}`}>
              <SectionHeading
                id={`vendor-${vendor.domain}`}
                title={
                  <TextLink to={vendorPath(vendor.domain)}>
                    <bdi>{vendor.domain}</bdi>
                  </TextLink>
                }
                action={
                  vendor.verified ? (
                    <span className="text-small text-ink-2">
                      Verified: <bdi>{vendor.verified}</bdi>
                    </span>
                  ) : undefined
                }
              />
              <ul>
                {connections.map((c) => (
                  <ConnectionRow key={c.id} connection={c} />
                ))}
              </ul>
            </section>
          ))
        )}
      </div>
    </div>
  )
}

/** One connection in a list: its client, what it is for, and how it stands. */
export function ConnectionRow({ connection: c }: { connection: api.Connection }) {
  const devices = c.devices.filter((d) => d.state === 'active').length
  return (
    <li className="flex flex-wrap items-baseline gap-x-4 gap-y-1 border-b border-rule py-[11px]">
      <TextLink to={vendorPath(c.vendor.domain, c.id)} className="min-w-0 font-medium">
        <bdi className="break-words">{c.client_name || c.vendor.domain}</bdi>
      </TextLink>
      <span className="text-small text-ink-2">{resourceWords(c.resource)}</span>
      <span className="min-w-0 flex-1 font-mono text-mono text-ink-2">
        {c.state === 'active'
          ? `${devices} ${devices === 1 ? 'device' : 'devices'}, ends ${formatDay(c.ends_at)}`
          : `ended ${formatDay(c.ended_at ?? c.ends_at)}`}
      </span>
      {c.state === 'active' && c.delivery !== 'delivering' && (
        <span className="text-small text-attention">Gets no mail now</span>
      )}
      {c.state === 'ended' && (
        <span className="text-small text-ink-2">{endWords(c.end_reason)}</span>
      )}
    </li>
  )
}
