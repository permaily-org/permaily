import { useSearchParams } from 'react-router'

export type PreviewState = 'ready' | 'loading' | 'error'

/** The preview's frame from `?state=`, and a way back to the ready preview. */
export function usePreviewState(): { state: PreviewState; retry: () => void } {
  const [params, setParams] = useSearchParams()
  const value = params.get('state')
  const state: PreviewState = value === 'loading' || value === 'error' ? value : 'ready'
  const retry = () =>
    setParams((current) => {
      const next = new URLSearchParams(current)
      next.delete('state')
      return next
    })
  return { state, retry }
}
