import { type ReactNode, useCallback, useEffect, useRef, useState } from 'react'
import { ProviderButton } from '../../components/brands/ProviderButton.tsx'
import { ApiError, errorCode, type Provider, startProvider } from '../../lib/api.ts'
import {
  arrivedInvitation,
  dropProviderArrival,
  forgetKeptInvitation,
  keepInvitation,
} from '../../lib/arrival.ts'
import { leaveFor, providerNames, useCameBack } from '../../lib/providers.ts'
import { messageFor } from './messages.ts'

/**
 * Signing in with Google, GitHub, Facebook (`meta`) or Microsoft: one action for each provider that is on,
 * always in the same order. The server answers where to send the browser, and takes the provider's answer only in this
 * browser. Nothing is asked first: the provider says who the person is, and the server what comes of it.
 * They share one group with the other ways in that need no address, which the screen hands over as `first`.
 */
export function ProviderSignIn({
  first,
  on,
  askAgain,
  invitation,
  onInvitationRefused,
  terms,
  onTermsRefused,
  returnTo,
}: {
  /** What comes before the providers in their group: signing in with a passkey. */
  first?: ReactNode
  /** The providers that are on, in the order they are offered in. */
  on: Provider[]
  /** Asks the server again which are on, after it refused one. */
  askAgain: () => void
  /**
   * On the sign-up screen: the invitation to start with. Undefined when none was typed, which still starts,
   * since the account may exist already; null when what was typed cannot be one, and the screen said so.
   */
  invitation?: () => string | null | undefined
  /** The server refused the invitation before the browser left. */
  onInvitationRefused?: (message: string) => void
  /** On the sign-up screen: the version of the terms the person agreed to, sent with an invitation. */
  terms?: string
  /** The server refused the agreement before the browser left. */
  onTermsRefused?: (message: string) => void
  /** The path of the app the person was going to. */
  returnTo?: string
}) {
  const [leaving, setLeaving] = useState<Provider | null>(null)
  const [error, setError] = useState<string | null>(null)
  const errorRef = useRef<HTMLParagraphElement>(null)
  // The provider that was refused may be gone from the screen, and focus with its action.
  const lost = useRef(false)

  // Back from the provider with the Back button: the page is as it was left, with the invitation still in
  // its memory, so the copy kept for the trip is of no use.
  useCameBack(
    useCallback(() => {
      forgetKeptInvitation()
      setLeaving(null)
    }, []),
  )

  // biome-ignore lint/correctness/useExhaustiveDependencies: runs once the providers are read again
  useEffect(() => {
    if (!lost.current) return
    lost.current = false
    if (document.activeElement === document.body) errorRef.current?.focus()
  }, [on])

  async function go(provider: Provider) {
    if (leaving) return
    const code = invitation?.()
    if (code === null) return
    // What an earlier try came back with is of no use once another starts.
    dropProviderArrival('sign-in')
    setError(null)
    setLeaving(provider)
    try {
      const { url } = await startProvider(provider, {
        invitation: code,
        terms,
        returnTo: returnTo && returnTo !== '/' ? returnTo : undefined,
      })
      // The page is about to go, and its memory with it. An invitation crosses the trip in the tab's own
      // storage, so the sign-up screen has it filled in should the provider's account need one: the one
      // typed here, or on the sign-in screen the one that arrived by link.
      keepInvitation(invitation ? code : arrivedInvitation())
      // The actions stay as they are while the browser leaves.
      if (!leaveFor(url)) throw new ApiError('other')
    } catch (failure) {
      forgetKeptInvitation()
      setLeaving(null)
      const failed = errorCode(failure)
      if (failed === 'invalid_invitation' && onInvitationRefused) {
        onInvitationRefused(messageFor(failed))
      } else if (failed === 'terms_needed' && onTermsRefused) {
        onTermsRefused(messageFor(failed))
      } else if (failed === 'provider_off') {
        // Turned off since the screen was drawn: it is no longer offered.
        setError(
          `Signing in with ${providerNames[provider]} isn't available right now. Sign in another way.`,
        )
        lost.current = true
        askAgain()
      } else {
        setError(messageFor(failed))
      }
    }
  }

  // With no provider on, nothing about providers is shown, unless one was just refused.
  if (!first && on.length === 0 && !error) return null
  return (
    <div className="mt-5 flex flex-col gap-3 border-t border-rule pt-5">
      {first}
      {on.map((provider) => (
        <ProviderButton
          key={provider}
          provider={provider}
          disabled={leaving !== null}
          onClick={() => go(provider)}
        />
      ))}
      <p
        ref={errorRef}
        tabIndex={-1}
        role="alert"
        className="text-small text-danger outline-none empty:hidden"
      >
        {error}
      </p>
      <p role="status" className="text-small text-ink-2 empty:hidden">
        {leaving && `Taking you to ${providerNames[leaving]}`}
      </p>
    </div>
  )
}
