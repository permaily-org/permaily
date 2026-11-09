import { type ReactNode, useEffect, useRef } from 'react'

/** A modal dialog using the native <dialog>, so focus and Escape work without extra code. */
export function Dialog({
  open,
  onClose,
  label,
  labelledBy,
  children,
}: {
  open: boolean
  onClose: () => void
  /** Name the dialog with text, or with the id of its own heading. */
  label?: string
  labelledBy?: string
  children: ReactNode
}) {
  const ref = useRef<HTMLDialogElement>(null)
  useEffect(() => {
    const dialog = ref.current
    if (!dialog) return
    if (open && !dialog.open) dialog.showModal()
    if (!open && dialog.open) dialog.close()
  }, [open])
  return (
    <dialog
      ref={ref}
      aria-label={labelledBy ? undefined : label}
      aria-labelledby={labelledBy}
      onClose={onClose}
      onClick={(event) => {
        if (event.target === ref.current) onClose()
      }}
      className="m-auto w-[640px] max-w-[calc(100vw-32px)] bg-paper p-5 text-ink backdrop:bg-scrim sm:p-8"
    >
      {children}
    </dialog>
  )
}
