import { useCallback, useEffect, useState } from 'react'
import { type Provider, providersOn } from './api.ts'

/**
 * What each provider is called on screen, and the one place that says so. Text only: no mark of theirs is
 * drawn or loaded. `meta` is Facebook to a person: the trip goes to Facebook's sign-in, and Meta's own rules
 * name the action "Continue with Facebook". The identifier stays `meta` in the API and in the code.
 */
export const providerNames: Record<Provider, string> = {
  google: 'Google',
  github: 'GitHub',
  meta: 'Facebook',
  microsoft: 'Microsoft',
}

/**
 * Sends the browser to a provider. This is the one place the app leaves for another site, and it goes only
 * where the server answered, never to anything read from the address bar: an `https:` address, or this
 * machine's own (`http://127.0.0.1`), where the providers of an enclave in dev mode are played. Anything
 * else, such as a `javascript:` address, goes nowhere. Answers whether the browser is on its way.
 */
export function leaveFor(address: string): boolean {
  let url: URL
  try {
    url = new URL(address)
  } catch {
    return false
  }
  const thisMachine =
    url.protocol === 'http:' && ['127.0.0.1', 'localhost', '[::1]'].includes(url.hostname)
  if (url.protocol !== 'https:' && !thisMachine) return false
  window.location.assign(url.href)
  return true
}

/**
 * Runs `back` when the person comes back to this page with the browser's Back button after `leaveFor`: the
 * browser may show the page as it was left, with its actions still waiting for a navigation that is over.
 */
export function useCameBack(back: () => void) {
  useEffect(() => {
    const shown = (event: PageTransitionEvent) => {
      if (event.persisted) back()
    }
    window.addEventListener('pageshow', shown)
    return () => window.removeEventListener('pageshow', shown)
  }, [back])
}

// What the server last said, for as long as the page lives: the sign-in and sign-up screens show the same
// providers at once when the person goes from one to the other.
let lastOn: Provider[] = []

/**
 * The providers that are on, to offer on the sign-in screens, and a way to ask again. Until the server
 * answers, and when it does not, none is offered: nothing of the screen waits for them.
 */
export function useProvidersOn(): [Provider[], () => void] {
  const [on, setOn] = useState(lastOn)
  const ask = useCallback(() => {
    providersOn().then(
      (list) => {
        lastOn = list
        setOn(list)
      },
      () => {},
    )
  }, [])
  useEffect(ask, [ask])
  return [on, ask]
}
