import { type ReactNode, useId } from 'react'
import type { Provider } from '../../lib/api.ts'
import { cx } from '../../lib/styles.ts'

/*
  Each provider's mark, as the provider publishes it: the paths, colours and proportions are copied from the
  files of their own brand pages, and only the wrapping changed (React attributes, ids of their own, a view box
  around the mark). Do not redraw or recolour them. Where each comes from, and the date it was taken, is in
  docs/web.md ("Each provider's mark"). These files are the one place outside packages/ui/colors.css where a
  colour is written (docs/design.md), and the marks are drawn only on a provider's sign-in button.

  The marks are decorative: the button's words name it. So each is hidden from assistive technology.
*/

/** Every mark takes the same 20px square, the size of Google's "G" on its 40px button. */
const size = 20

function Svg({
  provider,
  viewBox,
  className,
  children,
}: {
  provider: Provider
  viewBox: string
  className?: string
  children: ReactNode
}) {
  return (
    <svg
      aria-hidden="true"
      focusable="false"
      data-mark={provider}
      width={size}
      height={size}
      viewBox={viewBox}
      fill="none"
      className={cx('shrink-0', className)}
    >
      {children}
    </svg>
  )
}

/**
 * Google's standard colour "G", from the "Sign in with Google" button of its pre-approved assets
 * (signin-assets.zip, Android + Web, SVG, Light, no text, square): the same in the light and dark assets. The
 * view box is the G's own 20px square inside that 40px button. The sweep of colour is a CSS conic gradient in
 * `brands.css`, as in Google's file.
 */
function GoogleG() {
  // Ids are the page's, not the mark's: each G names its parts on its own.
  const id = `g-${useId().replace(/[^a-zA-Z0-9_-]/g, '')}`
  const circle =
    'M7.25922 19.7927C7.25922 12.6759 13.0209 6.90668 20.1283 6.90668C27.2357 6.90668 32.9973 12.6759 32.9973 19.7927C32.9973 26.9094 27.2357 32.6786 20.1283 32.6786C13.0209 32.6786 7.25921 26.9094 7.25922 19.7927Z'
  const blur = (name: string, x: number, y: number, width: number, height: number) => (
    <filter
      id={`${id}-${name}`}
      x={x}
      y={y}
      width={width}
      height={height}
      filterUnits="userSpaceOnUse"
      colorInterpolationFilters="sRGB"
    >
      <feFlood floodOpacity="0" result="BackgroundImageFix" />
      <feBlend mode="normal" in="SourceGraphic" in2="BackgroundImageFix" result="shape" />
      <feGaussianBlur stdDeviation="1" result="effect1_foregroundBlur" />
    </filter>
  )
  return (
    <Svg provider="google" viewBox="10 10 20 20">
      <mask
        id={`${id}-mask`}
        className="provider-google-mask"
        maskUnits="userSpaceOnUse"
        x="10"
        y="10"
        width="20"
        height="20"
      >
        <path
          d="M29.3987 18.1814H19.9849V22.0445H25.3598C25.1286 23.294 24.4294 24.3596 23.3676 25.0712C22.4746 25.6716 21.3266 26.0211 19.9849 26.0211C17.3864 26.0211 15.1823 24.2666 14.3947 21.9004C14.1952 21.2989 14.0853 20.6599 14.0853 19.9983C14.0853 19.3367 14.1952 18.6966 14.3947 18.0962C15.1823 15.7311 17.3864 13.9755 19.9849 13.9755C21.4524 13.9755 22.767 14.4816 23.8039 15.4713L26.6653 12.6057C24.936 10.9908 22.6786 10 19.9849 10C16.0832 10 12.705 12.2414 11.0618 15.5076C10.383 16.8592 10 18.3834 10 19.9994C10 21.6155 10.383 23.1396 11.0618 24.4913C12.705 27.7597 16.0832 30 19.9849 30C22.6797 30 24.9485 29.1137 26.6018 27.5861C28.4887 25.8452 29.5732 23.2702 29.5732 20.2275C29.5732 19.5182 29.5131 18.835 29.3987 18.1825V18.1814Z"
          fill="#E94FFF"
        />
      </mask>
      <g mask={`url(#${id}-mask)`}>
        <g filter={`url(#${id}-blur0)`}>
          <g clipPath={`url(#${id}-sweep)`}>
            <g transform="matrix(0.00804129 -0.00805186 0.00804128 0.00805186 19.6819 19.7927)">
              <foreignObject x="-2105.64" y="-2105.64" width="4211.29" height="4211.29">
                <div className="provider-google-sweep" />
              </foreignObject>
            </g>
          </g>
          <path d={circle} />
        </g>
        <g filter={`url(#${id}-blur1)`}>
          <ellipse
            cx="20.0496"
            cy="20.2413"
            rx="5.39634"
            ry="2.83537"
            transform="rotate(24.4473 20.0496 20.2413)"
            fill="#3186FF"
          />
        </g>
        <g filter={`url(#${id}-blur2)`}>
          <ellipse cx="33.3538" cy="18.2155" rx="7.43918" ry="3.09357" fill="#3186FF" />
        </g>
        <g filter={`url(#${id}-blur3)`}>
          <ellipse cx="25.2744" cy="16.2195" rx="7.40854" ry="2.37805" fill="#FF4641" />
        </g>
        <g filter={`url(#${id}-blur4)`}>
          <ellipse cx="29.5427" cy="12.9268" rx="7.40854" ry="2.37805" fill="#FF5B8B" />
        </g>
        <g filter={`url(#${id}-blur5)`}>
          <ellipse cx="24.4817" cy="19.878" rx="8.5061" ry="3.10976" fill="#3186FF" />
        </g>
        <g filter={`url(#${id}-blur6)`}>
          <ellipse
            cx="25.1842"
            cy="14.0197"
            rx="4.53882"
            ry="2.37805"
            transform="rotate(-28.6599 25.1842 14.0197)"
            fill="#FF4641"
          />
        </g>
      </g>
      <defs>
        {blur('blur0', 5.25922, 4.90668, 29.7381, 29.772)}
        <clipPath id={`${id}-sweep`}>
          <path d={circle} />
        </clipPath>
        {blur('blur1', 12.9977, 14.828, 14.1038, 10.8265)}
        {blur('blur2', 23.9146, 13.1219, 18.8784, 10.1871)}
        {blur('blur3', 15.8659, 11.8415, 18.8171, 8.7561)}
        {blur('blur4', 20.1341, 8.54878, 18.8171, 8.7561)}
        {blur('blur5', 13.9756, 14.7683, 21.0122, 10.2195)}
        {blur('blur6', 19.0404, 9.00419, 12.2878, 10.0309)}
      </defs>
    </Svg>
  )
}

/**
 * GitHub's Invertocat (GitHub_Logos.zip, SVG, GitHub_Invertocat_Black and _White, the same path in each):
 * black in the light theme and white in the dark one, the highest contrast on the page (`brands.css`).
 */
function Invertocat() {
  return (
    <Svg provider="github" viewBox="0 0 98 96" className="provider-github-mark">
      <path d="M41.4395 69.3848C28.8066 67.8535 19.9062 58.7617 19.9062 46.9902C19.9062 42.2051 21.6289 37.0371 24.5 33.5918C23.2559 30.4336 23.4473 23.7344 24.8828 20.959C28.7109 20.4805 33.8789 22.4902 36.9414 25.2656C40.5781 24.1172 44.4062 23.543 49.0957 23.543C53.7852 23.543 57.6133 24.1172 61.0586 25.1699C64.0254 22.4902 69.2891 20.4805 73.1172 20.959C74.457 23.543 74.6484 30.2422 73.4043 33.4961C76.4668 37.1328 78.0937 42.0137 78.0937 46.9902C78.0937 58.7617 69.1934 67.6621 56.3691 69.2891C59.623 71.3945 61.8242 75.9883 61.8242 81.252L61.8242 91.2051C61.8242 94.0762 64.2168 95.7031 67.0879 94.5547C84.4102 87.9512 98 70.6289 98 49.1914C98 22.1074 75.9883 6.69539e-07 48.9043 4.309e-07C21.8203 1.92261e-07 -1.9479e-07 22.1074 -4.3343e-07 49.1914C-6.20631e-07 70.4375 13.4941 88.0469 31.6777 94.6504C34.2617 95.6074 36.75 93.8848 36.75 91.3008L36.75 83.6445C35.4102 84.2188 33.6875 84.6016 32.1562 84.6016C25.8398 84.6016 22.1074 81.1563 19.4277 74.7441C18.375 72.1602 17.2266 70.6289 15.0254 70.3418C13.877 70.2461 13.4941 69.7676 13.4941 69.1934C13.4941 68.0449 15.4082 67.1836 17.3223 67.1836C20.0977 67.1836 22.4902 68.9063 24.9785 72.4473C26.8926 75.2227 28.9023 76.4668 31.2949 76.4668C33.6875 76.4668 35.2187 75.6055 37.4199 73.4043C39.0469 71.7773 40.291 70.3418 41.4395 69.3848Z" />
    </Svg>
  )
}

/**
 * Facebook's "f" logo (Facebook Brand Asset Pack, Facebook_Logo_Primary.ai and _Secondary.ai, Dec 2024): the
 * primary one, Facebook Blue with a white "f", on the white button, and the secondary one, white with the "f"
 * left open, on the button's Facebook Blue when the pointer is over it (`brands.css`). The view box is the
 * logo's own square in the 700px artboard.
 */
function FacebookF() {
  const circle =
    'M0 0C0 138.071-111.929 250-250 250-388.071 250-500 138.071-500 0-500-117.245-419.285-215.622-310.394-242.638V-76.396H-361.946V0H-310.394V32.919C-310.394 118.011-271.886 157.451-188.346 157.451-172.508 157.451-145.179 154.346-133.999 151.24V81.986C-139.9 82.607-150.148 82.918-162.881 82.918-203.874 82.918-219.713 67.39-219.713 27.018V0H-138.054L-152.082-76.396H-219.713V-248.169C-95.927-233.218 0-127.818 0 0'
  return (
    <Svg provider="meta" viewBox="100 100 500 500">
      <g className="provider-facebook-primary">
        <path transform="matrix(1,0,0,-1,600,350)" d={circle} fill="#0866ff" />
        <path
          transform="matrix(1,0,0,-1,447.9175,426.3964)"
          d="M0 0 14.029 76.396H-67.63V103.415C-67.63 143.787-51.792 159.314-10.799 159.314 1.934 159.314 12.182 159.004 18.083 158.383V227.636C6.903 230.742-20.426 233.848-36.264 233.848-119.803 233.848-158.312 194.407-158.312 109.315V76.396H-209.864V0H-158.312V-166.242C-138.969-171.04-118.744-173.604-97.918-173.604-87.664-173.604-77.56-172.972-67.63-171.773V0Z"
          fill="#ffffff"
        />
      </g>
      <path
        className="provider-facebook-secondary"
        transform="matrix(1,0,0,-1,600,350)"
        d={circle}
        fill="#ffffff"
      />
    </Svg>
  )
}

/** Microsoft's logo (ms-symbollockup_mssymbol_19.svg, from its "Sign in with Microsoft" guidelines). */
function MicrosoftLogo() {
  return (
    <Svg provider="microsoft" viewBox="0 0 21 21">
      <rect x="1" y="1" width="9" height="9" fill="#f25022" />
      <rect x="1" y="11" width="9" height="9" fill="#00a4ef" />
      <rect x="11" y="1" width="9" height="9" fill="#7fba00" />
      <rect x="11" y="11" width="9" height="9" fill="#ffb900" />
    </Svg>
  )
}

/** A provider's mark, for its sign-in button and nowhere else. */
export function ProviderMark({ provider }: { provider: Provider }) {
  switch (provider) {
    case 'google':
      return <GoogleG />
    case 'github':
      return <Invertocat />
    case 'meta':
      return <FacebookF />
    case 'microsoft':
      return <MicrosoftLogo />
  }
}
