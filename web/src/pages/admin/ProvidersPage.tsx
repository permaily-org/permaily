import { type FormEvent, useCallback, useEffect, useRef, useState } from 'react'
import { Button, Dialog, SectionHeading, TextAction } from '../../components/ui/index.ts'
import * as api from '../../lib/api.ts'
import { providerNames } from '../../lib/providers.ts'
import { Field } from '../auth/Field.tsx'
import { SettingRow } from '../settings/rows.tsx'
import { AskDialog, adminMessage, ListState, useAdmin, useAdminChange } from './shared.tsx'

/**
 * Permaily's application at each provider: which are set, and for the super admin setting and removing one.
 * A secret is typed into a password field, sent once and forgotten: the field is emptied as soon as it is
 * sent, whatever the answer, and the server never answers it back.
 */
export function ProvidersPage() {
  const { failed, superAdmin } = useAdmin()
  const change = useAdminChange()
  const [apps, setApps] = useState<api.ProviderApp[] | null>(null)
  const [failure, setFailure] = useState<string | null>(null)
  const [editing, setEditing] = useState<api.Provider | null>(null)
  const [removing, setRemoving] = useState<api.Provider | null>(null)
  const [pending, setPending] = useState(false)
  const [removeError, setRemoveError] = useState<string | null>(null)
  const [said, setSaid] = useState('')

  const load = useCallback(async () => {
    setFailure(null)
    try {
      setApps(await api.providerApps())
    } catch (error) {
      const code = failed(error)
      if (code) setFailure(adminMessage(code, 'provider'))
    }
  }, [failed])

  useEffect(() => {
    void load()
  }, [load])

  async function remove() {
    if (!removing || pending) return
    setPending(true)
    setRemoveError(null)
    const provider = removing
    const result = await change(() => api.removeProviderApp(provider), 'provider')
    setPending(false)
    if ('done' in result) {
      setRemoving(null)
      setSaid(`${providerNames[provider]} removed.`)
      void load()
      return
    }
    setRemoveError(result.said)
  }

  const set = new Map((apps ?? []).map((app) => [app.provider, app.client_id]))
  return (
    <>
      <h1 className="text-display-s font-medium md:text-display">Providers</h1>
      <section aria-labelledby="provider-apps" className="mt-8 max-w-3xl">
        <SectionHeading id="provider-apps" title="Sign-in providers" />
        {!superAdmin && (
          <p className="mt-3 text-small text-ink-2">Only the super admin can set up providers.</p>
        )}
        <p role="status" className="py-2 text-small text-ink-2 empty:hidden">
          {said}
        </p>
        {apps === null ? (
          <ListState failure={failure} what="The providers" onRetry={() => void load()} />
        ) : (
          api.providers.map((provider) => {
            const name = providerNames[provider]
            const clientId = set.get(provider)
            return (
              <SettingRow
                key={provider}
                label={name}
                action={
                  superAdmin ? (
                    <span className="flex shrink-0 gap-4">
                      <TextAction
                        onClick={() => {
                          setSaid('')
                          setEditing(provider)
                        }}
                      >
                        {clientId === undefined ? 'Set up' : 'Change'}
                        <span className="sr-only"> {name}</span>
                      </TextAction>
                      {clientId !== undefined && (
                        <TextAction
                          tone="danger"
                          onClick={() => {
                            setSaid('')
                            setRemoveError(null)
                            setRemoving(provider)
                          }}
                        >
                          Remove<span className="sr-only"> {name}</span>
                        </TextAction>
                      )}
                    </span>
                  ) : undefined
                }
              >
                {clientId === undefined ? (
                  <span className="text-ink-2">Not set up</span>
                ) : (
                  <>
                    <span className="block">Set up</span>
                    <span className="block wrap-anywhere font-mono text-small text-ink-2">
                      Client ID {clientId}
                    </span>
                  </>
                )}
              </SettingRow>
            )
          })
        )}
      </section>

      <Dialog
        open={editing !== null}
        dismissable={!pending}
        onClose={() => setEditing(null)}
        labelledBy="provider-app"
      >
        {editing && (
          <AppForm
            provider={editing}
            clientId={set.get(editing) ?? ''}
            onBusy={setPending}
            onDone={() => {
              setSaid(`${providerNames[editing]} set up.`)
              setEditing(null)
              void load()
            }}
            onCancel={() => setEditing(null)}
          />
        )}
      </Dialog>

      <AskDialog
        id="remove-provider-app"
        open={removing !== null}
        title={removing ? `Remove ${providerNames[removing]}?` : ''}
        error={removeError}
        pending={pending}
        action={removing ? `Remove ${providerNames[removing]}` : ''}
        danger
        keep="Keep it"
        onAct={() => void remove()}
        onClose={() => setRemoving(null)}
      >
        {removing
          ? `Nobody can sign in with ${providerNames[removing]}, or link it, until it is set up again.`
          : ''}
      </AskDialog>
    </>
  )
}

function AppForm({
  provider,
  clientId,
  onBusy,
  onDone,
  onCancel,
}: {
  provider: api.Provider
  clientId: string
  onBusy: (busy: boolean) => void
  onDone: () => void
  onCancel: () => void
}) {
  const change = useAdminChange()
  const name = providerNames[provider]
  const [id, setId] = useState(clientId)
  const [secret, setSecret] = useState('')
  const [errors, setErrors] = useState<{ id?: string; secret?: string; form?: string }>({})
  const [pending, setPending] = useState(false)
  const idRef = useRef<HTMLInputElement>(null)
  const secretRef = useRef<HTMLInputElement>(null)

  async function submit(event: FormEvent) {
    event.preventDefault()
    if (pending) return
    const typedId = id.trim()
    if (!typedId) {
      setErrors({ id: 'Enter the client ID.' })
      idRef.current?.focus()
      return
    }
    if (!secret) {
      setErrors({ secret: 'Enter the client secret.' })
      secretRef.current?.focus()
      return
    }
    // The secret is in this function's memory until the server has it, and in the field no longer.
    const sent = secret
    setSecret('')
    setErrors({})
    setPending(true)
    onBusy(true)
    const result = await change(() => api.setProviderApp(provider, typedId, sent), 'provider')
    setPending(false)
    onBusy(false)
    if ('done' in result) {
      onDone()
      return
    }
    if (result.code === 'invalid_provider_app') {
      setErrors({ secret: result.said ?? undefined })
    } else if (result.said) {
      setErrors({ form: `Nothing was changed. ${result.said}` })
    }
    secretRef.current?.focus()
  }

  return (
    <>
      <h2 id="provider-app" className="text-heading font-medium">
        {clientId ? `Change ${name}` : `Set up ${name}`}
      </h2>
      <p id="provider-app-how" className="mt-3 text-body text-ink-2">
        Enter the client ID and secret of Permaily's application at {name}. The secret is sent once,
        and never shown again.
      </p>
      <form noValidate onSubmit={submit} className="mt-6 flex flex-col gap-5">
        <Field
          id="client-id"
          label="Client ID"
          // The dialog puts focus here when it opens.
          data-dialog-focus
          type="text"
          autoComplete="off"
          autoCapitalize="none"
          autoCorrect="off"
          spellCheck={false}
          inputRef={idRef}
          value={id}
          onChange={(event) => setId(event.target.value)}
          error={errors.id}
          describedBy="provider-app-how"
          className="font-mono"
        />
        <Field
          id="client-secret"
          label="Client secret"
          type="password"
          autoComplete="off"
          inputRef={secretRef}
          value={secret}
          onChange={(event) => setSecret(event.target.value)}
          error={errors.secret}
          className="font-mono"
        />
        <p role="alert" className="text-small text-danger empty:hidden">
          {errors.form}
        </p>
        <p role="status" className="sr-only">
          {pending ? 'Saving' : ''}
        </p>
        <div className="flex flex-wrap gap-3">
          <Button type="submit" aria-disabled={pending}>
            Save
          </Button>
          <Button variant="secondary" disabled={pending} onClick={onCancel}>
            Cancel
          </Button>
        </div>
      </form>
    </>
  )
}
