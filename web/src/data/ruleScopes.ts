import { useMemo, useSyncExternalStore } from 'react'
import { rules, type AppliesTo, type Rule } from './sample.ts'
import { createStore } from './store.ts'

/*
  Who each rule applies to, shared by the rule editor and each connection's page, so a change made
  on either side shows on the other.
*/
const scopes = createStore<Readonly<Record<string, AppliesTo>>>(Object.fromEntries(rules.map((rule) => [rule.id, rule.appliesTo])))

export function setRuleScopes(changes: Record<string, AppliesTo>): void {
  scopes.set({ ...scopes.get(), ...changes })
}

export function useRuleScopes(): Readonly<Record<string, AppliesTo>> {
  return useSyncExternalStore(scopes.subscribe, scopes.get)
}

/** The person's rules, each with who it applies to now. */
export function useRules(): Rule[] {
  const current = useRuleScopes()
  return useMemo(() => rules.map((rule) => ({ ...rule, appliesTo: current[rule.id] ?? rule.appliesTo })), [current])
}
