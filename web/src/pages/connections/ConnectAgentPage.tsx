import { Check } from 'lucide-react'
import { useEffect, useRef, useState, type ReactNode, type Ref } from 'react'
import { Headline, StepMarker, TextAction, TextLink, Tile, type StepState } from '../../components/ui/index.ts'
import { cx, focusRing } from '../../lib/styles.ts'
import { ConnectAgentPanel } from './ConnectAgentPanel.tsx'

const address = 'https://mcp.permaily.com'

type AgentId = 'claude' | 'chatgpt' | 'other'
type Stage = 'choose' | 'copy' | 'wait'

const agents: Array<{ id: AgentId; name: string; initials: string; detail: string; subject: string; setup: string }> = [
  {
    id: 'claude',
    name: 'Claude',
    initials: 'CL',
    detail: 'Added as a custom connector',
    subject: 'Claude',
    setup: "In Claude's settings, add a custom connector and paste this address.",
  },
  {
    id: 'chatgpt',
    name: 'ChatGPT',
    initials: 'CG',
    detail: 'Added as a connector',
    subject: 'ChatGPT',
    setup: "In ChatGPT's settings, add a connector and paste this address.",
  },
  {
    id: 'other',
    name: 'Another agent',
    initials: 'MCP',
    detail: 'Any app that connects over MCP',
    subject: 'your agent',
    setup: "In your agent's settings, add a remote MCP server and paste this address.",
  },
]

async function copyAddress(): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(address)
    return true
  } catch {
    return false
  }
}

export function ConnectAgentPage() {
  const [agentId, setAgentId] = useState<AgentId | null>(null)
  const [stage, setStage] = useState<Stage>('choose')
  const [copied, setCopied] = useState(false)
  const headings = useRef<Partial<Record<Stage, HTMLHeadingElement | null>>>({})
  const pendingFocus = useRef(false)
  const agent = agents.find((a) => a.id === agentId)

  useEffect(() => {
    if (!pendingFocus.current) return
    pendingFocus.current = false
    headings.current[stage]?.focus()
  }, [stage])

  const go = (next: Stage) => {
    pendingFocus.current = true
    setStage(next)
  }

  const copyAndContinue = async () => {
    setCopied(await copyAddress())
    go('wait')
  }

  const headingRef = (key: Stage) => (el: HTMLHeadingElement | null) => {
    headings.current[key] = el
  }

  return (
    <main className="flex min-h-svh flex-col lg:flex-row">
      <div className="min-w-0 flex-1 px-4 pt-8 pb-14 sm:px-14">
        <header className="flex items-center justify-between gap-4">
          <span className="text-wordmark font-semibold">Permaily</span>
          <TextLink to="/connections">Back to connections</TextLink>
        </header>
        <Headline className="mt-8">Connect an agent.</Headline>

        <ol className="mt-8 border-t border-rule">
          {stage === 'choose' || !agent ? (
            <Step number={1} state="current" title="Choose your agent" headingRef={headingRef('choose')}>
              <div className="mt-4 grid gap-3 sm:grid-cols-3">
                {agents.map((a) => (
                  <AgentChoice
                    key={a.id}
                    agent={a}
                    chosen={a.id === agentId}
                    onChoose={() => {
                      setAgentId(a.id)
                      go('copy')
                    }}
                  />
                ))}
              </div>
            </Step>
          ) : (
            <FoldedStep number={1} label={`Agent chosen: ${agent.name}`} action={<TextAction onClick={() => go('choose')}>Change</TextAction>} />
          )}

          {stage === 'wait' && agent ? (
            <FoldedStep number={2} label={copied ? 'Address copied' : 'Address ready, copy it below'} />
          ) : (
            <Step
              number={2}
              state={stage === 'copy' ? 'current' : 'next'}
              title={agent && stage === 'copy' ? `Add this address to ${agent.subject}` : 'Add the address to your agent'}
              aside={stage === 'copy' ? <span className="font-mono text-mono">copy it now</span> : <Aside>unlocks after step 1</Aside>}
              headingRef={headingRef('copy')}
            >
              {stage === 'copy' && agent && (
                <>
                  <AddressBox onCopy={copyAndContinue} label="Copy" className="mt-2" />
                  <p className="mt-2.5 text-ink-2">{agent.setup}</p>
                </>
              )}
            </Step>
          )}

          <Step
            number={3}
            state={stage === 'wait' ? 'current' : 'next'}
            title="Choose what it can see"
            aside={stage === 'wait' ? undefined : <Aside>{agent ? `waiting for ${agent.subject}` : 'unlocks after step 2'}</Aside>}
            headingRef={headingRef('wait')}
          >
            {stage === 'wait' && agent && <Waiting subject={agent.subject} />}
          </Step>
        </ol>
      </div>

      <ConnectAgentPanel />
    </main>
  )
}

function Step({
  number,
  state,
  title,
  aside,
  headingRef,
  children,
}: {
  number: number
  state: StepState
  title: string
  aside?: ReactNode
  headingRef?: Ref<HTMLHeadingElement>
  children?: ReactNode
}) {
  return (
    <li className="flex gap-5 border-b border-rule pt-4 pb-5">
      <StepMarker state={state} number={number} />
      <div className="min-w-0 flex-1">
        <div className="flex min-h-7 flex-wrap items-center justify-between gap-x-4">
          <h2 ref={headingRef} tabIndex={-1} className="text-title font-medium outline-none">
            {title}
          </h2>
          {aside}
        </div>
        {children}
      </div>
    </li>
  )
}

function FoldedStep({ number, label, action }: { number: number; label: string; action?: ReactNode }) {
  return (
    <li className="flex items-center gap-5 border-b border-rule py-3.5">
      <StepMarker state="done" number={number} />
      <p className="min-w-0 flex-1 text-lead text-ink-2">{label}</p>
      {action}
    </li>
  )
}

function Aside({ children }: { children: ReactNode }) {
  return <span className="font-mono text-mono text-ink-2">{children}</span>
}

function AgentChoice({ agent, chosen, onChoose }: { agent: (typeof agents)[number]; chosen: boolean; onChoose: () => void }) {
  return (
    <button
      type="button"
      aria-pressed={chosen}
      onClick={onChoose}
      className={cx('relative flex flex-col items-start gap-3 rounded-sm border p-4 text-left hover:bg-sunken', chosen ? 'border-ink' : 'border-rule hover:border-ink', focusRing)}
    >
      <Tile initials={agent.initials} />
      <span className="flex flex-col gap-0.5">
        <span className="font-medium">{agent.name}</span>
        <span className="text-small text-ink-2">{agent.detail}</span>
      </span>
      {chosen && <Check aria-hidden size={16} className="absolute top-4 right-4" />}
    </button>
  )
}

function AddressBox({ onCopy, label, className }: { onCopy: () => void; label: string; className?: string }) {
  return (
    <div className={cx('flex h-9 items-center justify-between gap-4 bg-sunken px-3', className)}>
      <code className="min-w-0 truncate font-mono text-small select-all">{address}</code>
      <TextAction onClick={onCopy}>
        {label}
        <span className="sr-only"> the address</span>
      </TextAction>
    </div>
  )
}

/** Step 3 while the agent connects. The page moves on by itself once it does. */
function Waiting({ subject }: { subject: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <>
      <p role="status" className="mt-1.5 font-mono text-mono text-ink-2">
        Waiting for {subject} to connect
      </p>
      <p className="mt-3 text-heading font-medium">Add the address in {subject}, then come back.</p>
      <AddressBox className="mt-4" label={copied ? 'Copied' : 'Copy'} onCopy={async () => setCopied(await copyAddress())} />
      <p className="mt-4 text-small text-ink-2">
        Already added it? <TextLink to="/consent/claude">Continue</TextLink>
      </p>
    </>
  )
}
