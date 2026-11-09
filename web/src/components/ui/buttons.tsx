import type { AnchorHTMLAttributes, ComponentProps } from 'react'
import { Link, type LinkProps } from 'react-router'
import {
  type ActionTone,
  actionClass,
  type ButtonSize,
  type ButtonVariant,
  buttonClass,
} from '../../lib/styles.ts'

type ButtonStyle = { variant?: ButtonVariant; size?: ButtonSize }
type ActionStyle = { tone?: ActionTone; size?: 'md' | 'sm' }

export function Button({
  variant,
  size,
  className,
  ...props
}: ComponentProps<'button'> & ButtonStyle) {
  return <button type="button" {...props} className={buttonClass(variant, size, className)} />
}

export function ButtonLink({ variant, size, className, ...props }: LinkProps & ButtonStyle) {
  return <Link {...props} className={buttonClass(variant, size, className)} />
}

/** A button-styled link to another site. Opens in a new tab without sending a referrer. */
export function ButtonAnchor({
  variant,
  size,
  className,
  ...props
}: AnchorHTMLAttributes<HTMLAnchorElement> & ButtonStyle) {
  return (
    <a
      target="_blank"
      rel="noopener noreferrer"
      {...props}
      className={buttonClass(variant, size, className)}
    />
  )
}

export function TextAction({
  tone,
  size,
  className,
  ...props
}: ComponentProps<'button'> & ActionStyle) {
  return <button type="button" {...props} className={actionClass(tone, size, className)} />
}

export function TextLink({ tone, size, className, ...props }: LinkProps & ActionStyle) {
  return <Link {...props} className={actionClass(tone, size, className)} />
}
