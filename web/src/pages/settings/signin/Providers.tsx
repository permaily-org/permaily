import { useCallback, useEffect, useRef, useState } from 'react'
import { useConfirmed } from '../../../components/Confirm.tsx'
import { Button, Dialog, TextAction } from '../../../components/ui/index.ts'
import * as api from '../../../lib/api.ts'
import { dropProviderArrival, useProviderArrival } from '../../../lib/arrival.ts'
import { isCancelled } from '../../../lib/cancelled.ts'
import { leaveFor, providerNames, useCameBack } from '../../../lib/providers.ts'
import { useSession } from '../../../lib/session.tsx'
import { cx } from '../../../lib/styles.ts'
import { solidDanger } from '../../accounts/styles.ts'
import { linkProblem, messageFor } from '../../auth/messages.ts'
import { SettingRow } from '../rows.tsx'
import { formatDay, type SignInSection } from './shared.ts'

/** What the section says of a try to link: that it worked, said calmly, or why it did not. */
type Outcome = { quiet: boolean; text: string }

/**
 * The providers that sign in to the account, and linking another of the ones that are on. Linking leaves the
 * app for the provider and comes back to Settings, where the address says how it ended (lib/arrival.ts):
 * that is said here, and "linked" only once the server's own list agrees. Nothing a provider said of the
 * person is kept by the server, so a row shows which provider and since when, and nothing else.
 */
export function Providers({
  on,
  linked,
  list,
  section,
}: {
  /** The providers that are on. */
  on: api.Provider[]
  linked: api.LinkedProvider[]
  list: api.PasskeyList
  section: SignInSection
}) {
  const { refresh: askSession } = useSession()
  const confirmed = useConfirmed()
  const arrived = useProviderArrival()
  const [outcome, setOutcome] = useState<Outcome | null>(null)
  const [leaving, setLeaving] = useState<api.Provider | null>(null)
  const [unlinking, setUnlinking] = useState<api.Provider | null>(null)
  const [pending, setPending] = useState(false)
  const [unlinkError, setUnlinkError] = useState<string | null>(null)
  const outcomeRef = useRef<HTMLParagraphElement>(null)
  // The result of a trip the person just came back from is where they look first.
  const cameBack = useRef(false)

  useCameBack(useCallback(() => setLeaving(null), []))

  // How a trip to a provider ended, said once. That one is linked is the server's list to say, not the
  // address's: the address may be an old one, or written by someone else.
  // biome-ignore lint/correctness/useExhaustiveDependencies: runs when a trip's end arrives
  useEffect(() => {
    if (arrived?.screen !== 'settings') return
    dropProviderArrival('settings')
    if (arrived.kind === 'linked') {
      if (!linked.some((one) => one.provider === arrived.provider)) return
      const name = providerNames[arrived.provider]
      cameBack.current = true
      setOutcome({
        quiet: true,
        text: list.passkey_only
          ? `${name} is linked. It won't sign you in while passkey only is on.`
          : `${name} is linked. It signs you in from now on.`,
      })
    } else if (arrived.kind === 'failed') {
      if (arrived.reason === 'account_closing') {
        // The account's own screen says so, once the session hears of it.
        void askSession()
        return
      }
      cameBack.current = true
      setOutcome({ quiet: false, text: linkProblem(arrived.reason, arrived.provider) })
    }
  }, [arrived])

  useEffect(() => {
    if (!outcome || !cameBack.current) return
    cameBack.current = false
    outcomeRef.current?.focus()
  }, [outcome])

  const since = new Map(linked.map((one) => [one.provider, one.linked_at]))
  const shown = api.providers.filter((provider) => on.includes(provider) || since.has(provider))
  // Mail signs in unless the account signs in with passkeys only; then each passkey, and each provider.
  const waysIn = list.passkeys.length + linked.length + (list.passkey_only ? 0 : 1)

  async function link(provider: api.Provider) {
    if (leaving) return
    setOutcome(null)
    setLeaving(provider)
    try {
      // Confirming it's the person, when the server asks for it, happens here, before the page is left.
      const { url } = await confirmed(() =>
        api.linkProvider(provider, `/settings#linked=${provider}`),
      )
      // The actions stay as they are while the browser leaves.
      if (!leaveFor(url)) throw new api.ApiError('other')
    } catch (failure) {
      setLeaving(null)
      if (isCancelled(failure)) return
      const code = section.failed(failure)
      if (!code) return
      const name = providerNames[provider]
      setOutcome({
        quiet: false,
        text:
          code === 'provider_off'
            ? `Signing in with ${name} isn't available right now.`
            : code === 'provider_linked'
              ? `This account has a ${name} account linked already.`
              : `Couldn't start linking ${name}. ${messageFor(code)}`,
      })
      // Refused for how things are now: the rows are read again so they say how it is.
      if (code === 'provider_off' || code === 'provider_linked') {
        section.focusSection()
        void section.refresh()
      }
    }
  }

  async function unlink(provider: api.Provider) {
    if (pending) return
    setUnlinkError(null)
    setPending(true)
    try {
      await confirmed(() => api.unlinkProvider(provider))
    } catch (failure) {
      // The person chose not to confirm it's them: the question stays, as it was.
      if (isCancelled(failure)) {
        setPending(false)
        return
      }
      const code = section.failed(failure)
      if (!code) return
      // Unlinked already, from another device, is what was wanted.
      if (code !== 'not_found') {
        setPending(false)
        setUnlinkError(messageFor(code))
        // Another way in was taken away meanwhile: the row stops offering what the server refuses.
        if (code === 'last_way_in') void section.refresh()
        return
      }
    }
    // The action goes with what it unlinked, so focus moves to the section it was in.
    setUnlinking(null)
    setOutcome(null)
    section.announce(`${providerNames[provider]} unlinked`)
    section.focusSection()
    await section.refresh()
    setPending(false)
  }

  if (shown.length === 0 && !outcome) return null
  const failed = outcome !== null && !outcome.quiet
  return (
    <>
      {shown.map((provider) => {
        const name = providerNames[provider]
        const at = since.get(provider)
        if (at === undefined) {
          return (
            <SettingRow
              key={provider}
              label={name}
              action={
                <TextAction aria-disabled={leaving !== null} onClick={() => link(provider)}>
                  Link<span className="sr-only"> {name}</span>
                </TextAction>
              }
            >
              <span className="text-ink-2">
                {leaving === provider ? `Taking you to ${name}` : 'Not linked'}
              </span>
            </SettingRow>
          )
        }
        const day = formatDay(at)
        // The only way left to sign in stays: the server refuses too.
        const kept = waysIn <= 1
        return (
          <SettingRow
            key={provider}
            label={name}
            action={
              kept ? undefined : (
                <TextAction
                  onClick={() => {
                    setUnlinkError(null)
                    setUnlinking(provider)
                  }}
                >
                  Unlink<span className="sr-only"> {name}</span>
                </TextAction>
              )
            }
          >
            {day ? `Linked since ${day}` : 'Linked'}
            {list.passkey_only && (
              <span className="block text-small text-ink-2">
                Doesn't sign you in while passkey only is on.
              </span>
            )}
            {!on.includes(provider) && (
              <span className="block text-small text-ink-2">
                Signing in with {name} is off right now.
              </span>
            )}
            {kept && (
              <span className="block text-small text-ink-2">
                Can't be unlinked: it is the only way left to sign in.
              </span>
            )}
          </SettingRow>
        )
      })}
      <p
        ref={outcomeRef}
        tabIndex={-1}
        role={failed ? 'alert' : 'status'}
        className={cx(
          'border-b border-rule py-2 text-small outline-none empty:hidden',
          failed ? 'text-danger' : 'text-ink-2',
        )}
      >
        {outcome?.text}
      </p>

      <Dialog
        open={unlinking !== null}
        dismissable={!pending}
        onClose={() => setUnlinking(null)}
        labelledBy="unlink-provider"
      >
        {unlinking && (
          <>
            <h2
              id="unlink-provider"
              tabIndex={-1}
              data-dialog-focus
              className="text-heading font-medium outline-none"
            >
              Unlink {providerNames[unlinking]}?
            </h2>
            <div className="mt-3 flex flex-col gap-3 text-body text-ink-2">
              <p>
                {providerNames[unlinking]} stops signing you in to Permaily now. You can link it
                again later.
              </p>
              <p>
                Permaily may stay listed in your {providerNames[unlinking]} account until you remove
                it there.
              </p>
            </div>
            <p role="alert" className="mt-4 text-small text-danger empty:hidden">
              {unlinkError}
            </p>
            <div className="mt-6 flex flex-wrap gap-3">
              <button
                type="button"
                className={cx(solidDanger, pending && 'opacity-60')}
                aria-disabled={pending}
                onClick={() => unlink(unlinking)}
              >
                Unlink {providerNames[unlinking]}
              </button>
              <Button variant="secondary" disabled={pending} onClick={() => setUnlinking(null)}>
                Keep it
              </Button>
            </div>
          </>
        )}
      </Dialog>
    </>
  )
}
