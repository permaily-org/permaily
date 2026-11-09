import { useSyncExternalStore } from 'react'
import { createStore } from './store.ts'

/** Connections revoked during this visit, shared by the Connections list, Overview and each connection's page. */
const revoked = createStore<ReadonlySet<string>>(new Set())

export function revoke(ids: string[]): void {
  revoked.set(new Set([...revoked.get(), ...ids]))
}

export function restore(ids: string[]): void {
  revoked.set(new Set([...revoked.get()].filter((id) => !ids.includes(id))))
}

/** The ids of connections revoked so far. */
export function useRevoked(): ReadonlySet<string> {
  return useSyncExternalStore(revoked.subscribe, revoked.get)
}
