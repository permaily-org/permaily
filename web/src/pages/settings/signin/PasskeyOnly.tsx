import { useState } from 'react'
import { useConfirmed } from '../../../components/Confirm.tsx'
import { Button, Dialog, InfoTip, TextAction } from '../../../components/ui/index.ts'
import * as api from '../../../lib/api.ts'
import { isCancelled } from '../../../lib/cancelled.ts'
import { useSession } from '../../../lib/session.tsx'
import { messageFor } from '../../auth/messages.ts'
import { SettingRow } from '../rows.tsx'
import { RecoveryCodes } from './RecoveryCodes.tsx'
import type { SignInSection } from './shared.ts'

/** What the dialog of this part is asking, or showing. */
type Step =
  | { kind: 'turn-on' }
  | { kind: 'turn-off' }
  | { kind: 'new-codes' }
  /** The codes the server just gave, in memory only while they are on screen. */
  | { kind: 'codes'; codes: string[]; replaced: boolean }

function Address({ children }: { children: string }) {
  return <span className="wrap-anywhere font-mono text-small text-ink">{children}</span>
}

/**
 * "Passkey only" and its recovery codes. Turning it on means mail no longer signs in to the account: passkeys
 * do, and the recovery codes are the only other way in. It asks first, both ways, and so does asking for
 * new codes, which makes the old ones stop working.
 */
export function PasskeyOnly({
  list,
  address,
  section,
}: {
  list: api.PasskeyList
  /** The account's own address, the one mail would sign in. */
  address: string
  section: SignInSection
}) {
  const confirmed = useConfirmed()
  const session = useSession()
  const [step, setStep] = useState<Step | null>(null)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const on = list.passkey_only
  const left = list.recovery_codes_left
  const canTurnOn = list.passkeys.length > 0

  function open(next: Step) {
    setError(null)
    setStep(next)
  }

  async function run(action: () => Promise<Step | null>) {
    if (pending) return
    setError(null)
    setPending(true)
    try {
      const next = await action()
      setStep(next)
      // The codes are on screen before the rows behind them are brought up to date.
      void section.refresh()
    } catch (failure) {
      // The person chose not to confirm it's them: the question stays, as it was.
      if (isCancelled(failure)) return
      const code = section.failed(failure)
      if (!code) return
      setError(messageFor(code))
      // Refused for how the account is now: the rows are read again so they say how it is.
      if (code === 'passkey_needed' || code === 'passkey_only_off') void section.refresh()
    } finally {
      setPending(false)
    }
  }

  // The session's account says whether it is on, which the admin panel reads: it follows the server's answer.
  function said(on: boolean) {
    if (session.state.status === 'signedIn') {
      session.accountChanged({ ...session.state.me.account, passkey_only: on })
    }
  }

  const turnOn = () =>
    run(async () => {
      const answer = await confirmed(() => api.setPasskeyOnly(true))
      said(answer.passkey_only)
      if (answer.recovery_codes.length > 0) {
        return { kind: 'codes', codes: answer.recovery_codes, replaced: false }
      }
      // It was on already, turned on from another device: the codes shown there are the ones that work.
      section.announce('Passkey only was on already')
      return null
    })

  const turnOff = () =>
    run(async () => {
      said((await confirmed(() => api.setPasskeyOnly(false))).passkey_only)
      section.announce('Passkey only is off')
      return null
    })

  const newCodes = () =>
    run(async () => ({
      kind: 'codes',
      codes: await confirmed(() => api.newRecoveryCodes()),
      replaced: true,
    }))

  const cancel = (
    <Button variant="secondary" disabled={pending} onClick={() => setStep(null)}>
      {step?.kind === 'turn-off'
        ? 'Keep it on'
        : step?.kind === 'new-codes'
          ? 'Keep the codes I have'
          : 'Not now'}
    </Button>
  )
  // The actions are not disabled while they wait (run ignores a second click): confirming it's you may open
  // meanwhile, and the browser gives focus back only to what can take it.
  // The heading of a question takes focus, not its first action: Enter must not answer it unread.
  const question = {
    id: 'passkey-only-dialog',
    tabIndex: -1,
    'data-dialog-focus': true,
    className: 'text-heading font-medium outline-none',
  }
  const alert = (
    <p role="alert" className="mt-4 text-small text-danger empty:hidden">
      {error}
    </p>
  )

  return (
    <>
      <SettingRow
        label="Passkey only"
        action={
          on ? (
            <TextAction onClick={() => open({ kind: 'turn-off' })}>
              Turn off<span className="sr-only"> passkey only</span>
            </TextAction>
          ) : canTurnOn ? (
            <TextAction onClick={() => open({ kind: 'turn-on' })}>
              Turn on<span className="sr-only"> passkey only</span>
            </TextAction>
          ) : undefined
        }
      >
        <span className="inline-flex items-center gap-1.5">
          {on ? 'On' : 'Off'}
          <InfoTip label="What passkey only means" align="start">
            With passkey only on, a message sent to your email address no longer signs you in. Your
            passkeys do, and recovery codes are the only other way in.
          </InfoTip>
        </span>
        {!on && !canTurnOn && (
          <span className="block text-small text-ink-2">Add a passkey to turn this on.</span>
        )}
      </SettingRow>
      {on && (
        <SettingRow
          label="Recovery codes"
          action={
            <TextAction onClick={() => open({ kind: 'new-codes' })}>Make new codes</TextAction>
          }
        >
          {left === 0 ? 'None left' : `${left} left`}
        </SettingRow>
      )}

      <Dialog
        open={step !== null}
        // Codes that are shown once stay until the person says they kept them.
        dismissable={step?.kind !== 'codes' && !pending}
        onClose={() => setStep(null)}
        labelledBy="passkey-only-dialog"
      >
        {step?.kind === 'turn-on' && (
          <>
            <h2 {...question}>Sign in with passkeys only?</h2>
            <div className="mt-3 flex flex-col gap-3 text-body text-ink-2">
              <p>
                A message sent to <Address>{address}</Address> will no longer sign you in. Only your
                passkeys will.
              </p>
              <p>
                You get 10 recovery codes, shown once. Each signs you in one time if you lose every
                passkey. Without a passkey or a code there is no other way in, and Permaily can't
                let you in either.
              </p>
            </div>
            {alert}
            <div className="mt-6 flex flex-wrap gap-3">
              <Button aria-disabled={pending} onClick={turnOn}>
                Turn on passkey only
              </Button>
              {cancel}
            </div>
          </>
        )}
        {step?.kind === 'turn-off' && (
          <>
            <h2 {...question}>Turn off passkey only?</h2>
            <div className="mt-3 flex flex-col gap-3 text-body text-ink-2">
              <p>
                A message sent to <Address>{address}</Address> will sign you in again, so whoever
                reads that mailbox can get in.
              </p>
              <p>Your recovery codes stop working. Your passkeys keep working.</p>
            </div>
            {alert}
            <div className="mt-6 flex flex-wrap gap-3">
              <Button aria-disabled={pending} onClick={turnOff}>
                Turn off passkey only
              </Button>
              {cancel}
            </div>
          </>
        )}
        {step?.kind === 'new-codes' && (
          <>
            <h2 {...question}>Make new recovery codes?</h2>
            <p className="mt-3 text-body text-ink-2">
              {left === 0
                ? 'You get 10 new codes, shown once.'
                : `You get 10 new codes, shown once. The ${left === 1 ? 'code' : `${left} codes`} you have left ${left === 1 ? 'stops' : 'stop'} working as soon as they are made.`}
            </p>
            {alert}
            <div className="mt-6 flex flex-wrap gap-3">
              <Button aria-disabled={pending} onClick={newCodes}>
                Make new codes
              </Button>
              {cancel}
            </div>
          </>
        )}
        {step?.kind === 'codes' && (
          <RecoveryCodes
            codes={step.codes}
            headingId="passkey-only-dialog"
            replaced={step.replaced}
            onKept={() => {
              section.announce('Recovery codes kept')
              setStep(null)
            }}
          />
        )}
      </Dialog>
    </>
  )
}
