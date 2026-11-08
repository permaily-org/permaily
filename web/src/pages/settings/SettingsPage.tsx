import { useState } from 'react'
import { useNavigate } from 'react-router'
import { Button, Dialog, SectionHeading, TextAction, ViewSwitch } from '../../components/ui/index.ts'
import { connections, user } from '../../data/sample.ts'
import { solidDanger } from '../accounts/styles.ts'
import { ExportReady } from './ExportReady.tsx'
import { EditableRow, NotificationRow, SettingRow } from './rows.tsx'
import { applyTheme, readTheme, saveTheme, type Theme } from '../../lib/theme.ts'

const notificationOptions = [
  { id: 'requests', title: 'New access requests', detail: 'An app or agent wants to connect', on: true },
  { id: 'widening', title: 'Access that would widen', detail: 'A connection or pack update asks for more', on: true },
  { id: 'unusual', title: 'Unusual rule activity', detail: 'A rule matches far more mail than usual', on: true },
  { id: 'unused', title: 'Unused access', detail: "A connection hasn't used its access in 60 days", on: true },
  { id: 'weekly', title: 'Weekly summary', detail: 'What left your mailbox this week', on: false },
]

const themeOptions: Array<{ value: Theme; label: string }> = [
  { value: 'light', label: 'Light' },
  { value: 'dark', label: 'Dark' },
  { value: 'system', label: 'Match my system' },
]

export function SettingsPage() {
  const [deleting, setDeleting] = useState(false)

  return (
    <div className="px-14 pt-10 pb-16">
      <h1 className="text-display font-medium">Settings</h1>

      <div className="mt-8 grid grid-cols-2 items-start gap-14">
        <div className="flex flex-col gap-8">
          <Profile />
          <SignIn />
          <Appearance />
        </div>
        <div className="flex flex-col gap-8">
          <Notifications />
          <YourData />
        </div>
      </div>

      <section aria-labelledby="delete-account" className="mt-8 flex items-center gap-6 border border-danger px-5 py-4">
        <div className="min-w-0 flex-1">
          <h2 id="delete-account" className="font-medium">
            Delete your account
          </h2>
          <p className="mt-0.5 text-small text-ink-2">
            Removes your rules, receipts and tokens straight away, and ends all access. Permaily can't recall mail your connections have already received.
          </p>
        </div>
        <Button variant="danger" onClick={() => setDeleting(true)}>
          Delete my account
        </Button>
      </section>

      <DeleteAccountDialog open={deleting} onClose={() => setDeleting(false)} />
    </div>
  )
}

function Profile() {
  const [name, setName] = useState(user.name)
  const [email, setEmail] = useState(user.email)
  const [language, setLanguage] = useState('English')

  return (
    <section aria-labelledby="profile">
      <SectionHeading id="profile" title="Profile" />
      <EditableRow label="Name" value={name} onSave={setName} actionLabel="Edit" />
      <EditableRow label="Contact email" value={email} onSave={setEmail} type="email" mono />
      <EditableRow label="Language" value={language} onSave={setLanguage} options={['English', 'Español', 'Català', 'Français', 'Deutsch']} />
    </section>
  )
}

const otherSession = { id: 'iphone', device: 'iPhone, Safari', seen: '2 days ago' }

function SignIn() {
  const [passkeys, setPasskeys] = useState(['MacBook, added 6 March'])
  const [sessions, setSessions] = useState([{ id: 'this', device: 'MacBook, Firefox', seen: 'This device' }, otherSession])
  const [reviewing, setReviewing] = useState(false)
  const newPasskey = 'This browser, added today'

  return (
    <section aria-labelledby="sign-in">
      <SectionHeading id="sign-in" title="Sign-in" />
      {passkeys.length === 0 ? (
        <SettingRow label="Passkey">
          <span className="text-ink-2">None. You sign in with a link sent to your contact email.</span>
        </SettingRow>
      ) : (
        passkeys.map((passkey) => (
          <SettingRow
            key={passkey}
            label="Passkey"
            action={
              <TextAction onClick={() => setPasskeys((list) => list.filter((p) => p !== passkey))} aria-label={`Remove passkey ${passkey}`}>
                Remove
              </TextAction>
            }
          >
            {passkey}
          </SettingRow>
        ))
      )}
      <SettingRow
        label="Sessions"
        action={
          <TextAction aria-expanded={reviewing} onClick={() => setReviewing((v) => !v)}>
            {reviewing ? 'Done' : 'Review'}
          </TextAction>
        }
      >
        {sessions.length === 1 ? '1 device signed in' : `${sessions.length} devices signed in`}
      </SettingRow>
      {reviewing && (
        <ul className="border-b border-rule py-1.5 pl-[132px]">
          {sessions.map((session) => (
            <li key={session.id} className="flex items-center justify-between gap-3 py-1.5 text-small">
              <span>
                {session.device} <span className="text-ink-2">· {session.seen}</span>
              </span>
              {session.id !== 'this' && (
                <TextAction size="sm" onClick={() => setSessions((list) => list.filter((s) => s.id !== session.id))}>
                  Sign out
                </TextAction>
              )}
            </li>
          ))}
        </ul>
      )}
      {!passkeys.includes(newPasskey) && (
        <TextAction className="mt-2.5" onClick={() => setPasskeys((list) => [...list, newPasskey])}>
          Add a passkey
        </TextAction>
      )}
    </section>
  )
}

function Appearance() {
  const [theme, setTheme] = useState<Theme>(readTheme)

  return (
    <section aria-labelledby="appearance">
      <SectionHeading id="appearance" title="Appearance" />
      <div className="mt-3">
        <ViewSwitch
          label="Appearance"
          options={themeOptions}
          value={theme}
          onChange={(next) => {
            setTheme(next)
            applyTheme(next)
            saveTheme(next)
          }}
        />
      </div>
    </section>
  )
}

function Notifications() {
  const [on, setOn] = useState(() => new Set(notificationOptions.filter((n) => n.on).map((n) => n.id)))

  function toggle(id: string, checked: boolean) {
    setOn((current) => {
      const next = new Set(current)
      if (checked) next.add(id)
      else next.delete(id)
      return next
    })
  }

  return (
    <section aria-labelledby="notifications">
      <SectionHeading id="notifications" title="Notifications" />
      {notificationOptions.map((option) => (
        <NotificationRow
          key={option.id}
          title={option.title}
          detail={option.detail}
          checked={on.has(option.id)}
          onChange={(checked) => toggle(option.id, checked)}
        />
      ))}
    </section>
  )
}

function YourData() {
  const [retention, setRetention] = useState('Kept for 1 year')
  const [exported, setExported] = useState(false)

  return (
    <section aria-labelledby="your-data">
      <SectionHeading id="your-data" title="Your data" />
      <EditableRow
        label="Receipts"
        value={retention}
        onSave={setRetention}
        options={['Kept for 30 days', 'Kept for 90 days', 'Kept for 1 year', 'Kept for 2 years']}
      />
      <SettingRow label="Queued mail">0 emails. Deleted after delivery, or after 7 days</SettingRow>
      <SettingRow label="Export" action={exported ? undefined : <TextAction onClick={() => setExported(true)}>Export</TextAction>}>
        Rules, access settings and receipts
      </SettingRow>
      {exported && <ExportReady />}
    </section>
  )
}

function DeleteAccountDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const navigate = useNavigate()

  return (
    <Dialog open={open} onClose={onClose} label="Delete your account">
      <h2 className="text-heading font-medium">Delete your account?</h2>
      <p className="mt-3 text-body text-ink-2">
        All {connections.length} connections lose access now. Your rules and receipts are deleted, and this can't be undone. Permaily can't recall mail
        connections have already received.
      </p>
      <div className="mt-6 flex flex-wrap gap-3">
        <button
          type="button"
          className={solidDanger}
          onClick={() => {
            onClose()
            navigate('/')
          }}
        >
          Delete everything
        </button>
        <Button variant="secondary" onClick={onClose}>
          Keep my account
        </Button>
      </div>
    </Dialog>
  )
}
