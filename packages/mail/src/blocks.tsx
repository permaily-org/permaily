// One component for each kind of block a message is made of, and the layout around them. The words are not
// here: Go writes them (internal/domain/mail). Built for templates (scripts/build.ts), each value is a marker
// that becomes a Go html/template action; in the preview, it is the sample value itself.

import type { CSSProperties, ReactNode } from 'react'
import {
  Body,
  Button,
  Column,
  Container,
  Head,
  Heading,
  Hr,
  Html,
  Row,
  Section,
  Text,
} from 'react-email'
import { mono, sans, tokens } from './tokens.ts'

const t = {
  paper: tokens.paper.light,
  sunken: tokens.sunken.light,
  ink: tokens.ink.light,
  ink2: tokens['ink-2'].light,
  rule: tokens.rule.light,
}

/** A fact of a notice: when, how, which browser. A machine fact (a time) is set in monospace. */
export type Fact = { label: string; value: string; machine?: boolean }

export type Block =
  | { kind: 'lead'; text: string }
  | { kind: 'paragraph'; text: string }
  | { kind: 'note'; text: string }
  | { kind: 'code'; text: string }
  | { kind: 'action'; url: string }
  | { kind: 'facts'; facts: Fact[] }

export const kinds = ['lead', 'paragraph', 'note', 'code', 'action', 'facts'] as const

const text: CSSProperties = { fontFamily: sans, margin: '0 0 16px', color: t.ink }

export function Lead({ text: words }: { text: string }) {
  return (
    <Heading
      as="h1"
      className="pm-ink"
      style={{ ...text, fontSize: '20px', lineHeight: '28px', fontWeight: 600, margin: '0 0 20px' }}
    >
      {words}
    </Heading>
  )
}

export function Paragraph({ text: words }: { text: string }) {
  return (
    <Text className="pm-ink" style={{ ...text, fontSize: '16px', lineHeight: '24px' }}>
      {words}
    </Text>
  )
}

export function Note({ text: words }: { text: string }) {
  return (
    <Text
      className="pm-ink-2"
      style={{ ...text, fontSize: '14px', lineHeight: '21px', color: t.ink2 }}
    >
      {words}
    </Text>
  )
}

/** The code of a sign-in or a confirmation: the reason the message exists, large and on one line. */
export function Code({ text: code }: { text: string }) {
  return (
    <Section
      className="pm-sunken"
      style={{ backgroundColor: t.sunken, borderRadius: '2px', margin: '0 0 24px' }}
    >
      <Text
        className="pm-ink pm-code"
        style={{
          fontFamily: mono,
          fontSize: '40px',
          lineHeight: '48px',
          fontWeight: 600,
          letterSpacing: '6px',
          whiteSpace: 'nowrap',
          userSelect: 'all',
          WebkitUserSelect: 'all',
          color: t.ink,
          margin: 0,
          padding: '20px 24px',
        }}
      >
        {code}
      </Text>
    </Section>
  )
}

/** The label of an action's button, the one word of chrome beside the wordmark: the only action is to sign in. */
export const actionLabel = 'Sign in'

/**
 * A link: a button that says what it does, and under it the address in full, in small type, as the plain text
 * shows it, to read where it leads or to copy. The button is the app's primary one, filled with ink (the colour of
 * text, which turns light in the dark theme), not with the black of the bars, which means withheld. It is no wider
 * than its label and lighter than the code above it. The fill is the cell's, which Outlook draws. Outlook ignores the
 * link's padding and, the build having dropped React Email's conditional comments, keeps only its mso-text-raise,
 * which lifts the label by the bottom padding: so the cell gives Outlook the rest (mso-padding-alt, no bottom).
 * Elsewhere the padding is the link's, so the whole button is the link. The address is text, not a second link:
 * the message has one link, the button's.
 */
export function Action({ url }: { url: string }) {
  return (
    <Section style={{ margin: '0 0 24px' }}>
      <table role="presentation" border={0} cellPadding={0} cellSpacing={0}>
        <tbody>
          <tr>
            <td
              className="pm-fill"
              style={{ backgroundColor: t.ink, borderRadius: '2px', msoPaddingAlt: '14px 24px 0' }}
            >
              <Button
                href={url}
                className="pm-on-fill"
                style={{
                  color: t.paper,
                  fontFamily: sans,
                  fontSize: '16px',
                  fontWeight: 500,
                  lineHeight: '20px',
                  padding: '14px 24px',
                }}
              >
                {actionLabel}
              </Button>
            </td>
          </tr>
        </tbody>
      </table>
      <Text
        className="pm-ink-2"
        style={{
          fontFamily: mono,
          fontSize: '13px',
          lineHeight: '19px',
          color: t.ink2,
          margin: '12px 0 0',
          wordBreak: 'break-all',
          overflowWrap: 'anywhere',
        }}
      >
        {url}
      </Text>
    </Section>
  )
}

/** The facts of a notice, one a row, between hairlines. */
export function Facts({ children }: { children: ReactNode }) {
  return (
    <Section className="pm-rule" style={{ borderTop: `1px solid ${t.rule}`, margin: '0 0 24px' }}>
      {children}
    </Section>
  )
}

export function FactRow({ label, value }: { label: string; value: ReactNode }) {
  return (
    <Row className="pm-rule" style={{ borderBottom: `1px solid ${t.rule}` }}>
      <Column
        className="pm-ink-2"
        style={{
          width: '88px',
          verticalAlign: 'top',
          padding: '10px 12px 10px 0',
          fontFamily: sans,
          fontSize: '14px',
          lineHeight: '22px',
          color: t.ink2,
        }}
      >
        {label}
      </Column>
      <Column
        className="pm-ink"
        style={{
          verticalAlign: 'top',
          padding: '10px 0',
          fontFamily: sans,
          fontSize: '15px',
          lineHeight: '22px',
          color: t.ink,
        }}
      >
        {value}
      </Column>
    </Row>
  )
}

export function MachineValue({ children }: { children: ReactNode }) {
  return <span style={{ fontFamily: mono, fontSize: '14px' }}>{children}</span>
}

// Dark values for clients that read prefers-color-scheme; the inline light values stand where they do not.
const dark = {
  paper: tokens.paper.dark,
  sunken: tokens.sunken.dark,
  ink: tokens.ink.dark,
  ink2: tokens['ink-2'].dark,
  rule: tokens.rule.dark,
}

export const stylesheet = [
  ':root{color-scheme:light dark;supported-color-schemes:light dark}',
  '@media (prefers-color-scheme:dark){',
  `.pm-paper,body.pm-paper>table>tbody>tr>td{background-color:${dark.paper}!important}`,
  `.pm-sunken{background-color:${dark.sunken}!important}`,
  `.pm-ink{color:${dark.ink}!important}`,
  `.pm-ink-2{color:${dark.ink2}!important}`,
  `.pm-rule{border-color:${dark.rule}!important}`,
  `.pm-fill{background-color:${dark.ink}!important}`,
  `.pm-on-fill{color:${dark.paper}!important}`,
  '}',
  '@media (max-width:480px){',
  '.pm-frame{padding:24px 16px!important}',
  '.pm-code{font-size:34px!important;letter-spacing:4px!important;padding:16px!important}',
  '}',
].join('')

/** The page around a message: the wordmark, the blocks, a closing hairline. Nothing remote, nothing else. */
export function Layout({ title, children }: { title: string; children: ReactNode }) {
  return (
    <Html lang="en" dir="ltr">
      <Head>
        <meta name="color-scheme" content="light dark" />
        <meta name="supported-color-schemes" content="light dark" />
        <title>{title}</title>
        <style>{stylesheet}</style>
      </Head>
      <Body className="pm-paper" style={{ backgroundColor: t.paper, margin: 0, padding: 0 }}>
        <Container
          className="pm-paper pm-frame"
          style={{ backgroundColor: t.paper, maxWidth: '560px', padding: '40px 24px' }}
        >
          <Text
            className="pm-ink"
            style={{
              fontFamily: sans,
              fontSize: '17px',
              lineHeight: '20px',
              fontWeight: 600,
              letterSpacing: '-0.3px',
              color: t.ink,
              margin: '0 0 20px',
            }}
          >
            Permaily
          </Text>
          <Hr
            className="pm-rule"
            style={{ borderTop: `1px solid ${t.rule}`, margin: '0 0 28px' }}
          />
          {children}
          <Hr className="pm-rule" style={{ borderTop: `1px solid ${t.rule}`, margin: '8px 0 0' }} />
        </Container>
      </Body>
    </Html>
  )
}
