import { type ReactNode, useEffect, useRef } from 'react'

/**
 * A modal dialog using the native <dialog>, so focus and Escape work without extra code. Focus goes to the
 * first control inside, or to the element marked `data-dialog-focus`: a dialog that asks something worth
 * reading marks its heading, so Enter does not answer it unread.
 */
export function Dialog({
  open,
  onClose,
  label,
  labelledBy,
  dismissable = true,
  children,
}: {
  open: boolean
  onClose: () => void
  /**
   * False while the dialog holds something the person must answer before it goes, such as codes that are
   * shown once: Escape and a click outside then leave it open.
   */
  dismissable?: boolean
  /** Name the dialog with text, or with the id of its own heading. */
  label?: string
  labelledBy?: string
  children: ReactNode
}) {
  const ref = useRef<HTMLDialogElement>(null)
  // What had focus when the dialog opened: the browser gives focus back to it when the dialog closes.
  const opener = useRef<Element | null>(null)
  useEffect(() => {
    const dialog = ref.current
    if (!dialog) return
    if (open && !dialog.open) {
      opener.current = document.activeElement
      show(dialog)
    }
    if (!open && dialog.open) dialog.close()
  }, [open])
  return (
    <dialog
      ref={ref}
      aria-label={labelledBy ? undefined : label}
      aria-labelledby={labelledBy}
      onCancel={(event) => {
        // Escape closes through the caller, at once: were the dialog to close by itself and tell the caller
        // a moment later, a key pressed in between would open it again in name only.
        event.preventDefault()
        if (dismissable) onClose()
      }}
      onClose={() => {
        // The news of an earlier closing, come after the dialog was opened again: it closes nothing.
        if (ref.current?.open) return
        if (!open || dismissable) {
          onClose()
          return
        }
        // A browser closes a dialog on a second Escape whatever it is told: it opens again. Focus is put
        // back where it was first, so that is still where it returns when the dialog closes for good,
        // whatever was clicked in the moment the dialog was away.
        if (opener.current instanceof HTMLElement && opener.current.isConnected)
          opener.current.focus()
        if (ref.current) show(ref.current)
      }}
      onClick={(event) => {
        if (dismissable && event.target === ref.current) onClose()
      }}
      className="m-auto w-[640px] max-w-[calc(100vw-32px)] bg-paper p-5 text-ink backdrop:bg-scrim sm:p-8"
    >
      {children}
    </dialog>
  )
}

function show(dialog: HTMLDialogElement) {
  dialog.showModal()
  dialog.querySelector<HTMLElement>('[data-dialog-focus]')?.focus()
}
