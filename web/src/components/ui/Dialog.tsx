import { useEffect, useRef, type ReactNode } from 'react'

/** A modal dialog using the native <dialog>, so focus and Escape work without extra code. */
export function Dialog({ open, onClose, label, children }: { open: boolean; onClose: () => void; label: string; children: ReactNode }) {
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
      aria-label={label}
      onClose={onClose}
      onClick={(event) => {
        if (event.target === ref.current) onClose()
      }}
      className="m-auto w-[640px] max-w-[calc(100vw-32px)] bg-paper p-8 text-ink backdrop:bg-ink/40"
    >
      {children}
    </dialog>
  )
}
