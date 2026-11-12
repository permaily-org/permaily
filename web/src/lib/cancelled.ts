/**
 * Thrown by an action that needed the person to confirm it's them, when they chose not to: nothing was done,
 * and there is nothing to say about it (`src/components/Confirm.tsx`).
 */
export class ConfirmCancelled extends Error {
  constructor() {
    super('cancelled')
    this.name = 'ConfirmCancelled'
  }
}

/** Whether an action ended because the person chose not to confirm it's them. */
export function isCancelled(error: unknown): boolean {
  return error instanceof ConfirmCancelled
}
