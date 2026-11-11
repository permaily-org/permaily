/**
 * Where a person was going when they were sent to sign in, as the router's state carries it. Only a path
 * inside the app counts, and never the sign-in screens themselves.
 */
export function destination(state: unknown): string {
  const from = (state as { from?: unknown } | null)?.from
  if (typeof from !== 'string' || !from.startsWith('/') || from.startsWith('//')) return '/'
  return /^\/sign-(in|up)(?![^/?#])/.test(from) ? '/' : from
}
