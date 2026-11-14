import { cx } from '../lib/styles.ts'
import { logo } from './logo.ts'

/**
 * The logo: the mark beside the name, as one picture. Its mark is 20px high, which sets the letters at the size
 * the name had as text, so a header keeps its height. It takes the colour of the text around it, and on a dark
 * surface its tip takes the highlight (`logo-tip` in styles/index.css). It is a picture and not text: it does
 * not grow with the text size, and assistive technology reads it as the product's name. The outlines are in
 * `logo.ts`, which is generated (docs/design.md, "The logo").
 */
export function Logo({ className }: { className?: string }) {
  return (
    <svg
      role="img"
      aria-label="Permaily"
      viewBox={`0 0 ${logo.width} ${logo.height}`}
      width={logo.width}
      height={logo.height}
      className={cx('block shrink-0 fill-current', className)}
    >
      <path d={logo.body} />
      <path d={logo.tip} className="logo-tip" />
    </svg>
  )
}
