/**
 * The console's icons: line drawings on a 24-unit grid, stroked in the current text colour, so an icon takes the colour
 * of whatever it sits in. Drawn for Vaglio; no icon package.
 */
import type { ReactNode, SVGProps } from 'react'

type Props = SVGProps<SVGSVGElement> & { size?: number }

function Icon({ size = 18, children, ...rest }: Props & { children: ReactNode }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.8}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
      {...rest}
    >
      {children}
    </svg>
  )
}

export const IconCases = (p: Props) => (
  <Icon {...p}>
    <path d="M3 13h5l1.5 3h5L16 13h5" />
    <path d="M5.5 5h13L21 13v6a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1v-6z" />
  </Icon>
)
export const IconPolicies = (p: Props) => (
  <Icon {...p}>
    <path d="M4 5.5A2.5 2.5 0 0 1 6.5 3H20v15H6.5A2.5 2.5 0 0 0 4 20.5z" />
    <path d="M4 20.5A2.5 2.5 0 0 0 6.5 23H20v-5" />
    <path d="M9 8h7M9 12h5" />
  </Icon>
)
export const IconDraft = (p: Props) => (
  <Icon {...p}>
    <path d="M14 4l6 6L9 21H3v-6z" />
    <path d="M12.5 5.5l6 6" />
  </Icon>
)
export const IconFlask = (p: Props) => (
  <Icon {...p}>
    <path d="M9 3h6M10 3v6L4.5 18.5A1.7 1.7 0 0 0 6 21h12a1.7 1.7 0 0 0 1.5-2.5L14 9V3" />
    <path d="M7 15h10" />
  </Icon>
)
export const IconChart = (p: Props) => (
  <Icon {...p}>
    <path d="M3 3v18h18" />
    <path d="M7 15l4-5 3 3 5-7" />
  </Icon>
)
export const IconLedger = (p: Props) => (
  <Icon {...p}>
    <path d="M12 2l8 3v6c0 5-3.5 9-8 11-4.5-2-8-6-8-11V5z" />
    <path d="M8.5 12l2.5 2.5 4.5-5" />
  </Icon>
)
export const IconSearch = (p: Props) => (
  <Icon {...p}>
    <circle cx="11" cy="11" r="7" />
    <path d="M20 20l-3.5-3.5" />
  </Icon>
)
export const IconLogout = (p: Props) => (
  <Icon {...p}>
    <path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4" />
    <path d="M16 17l5-5-5-5M21 12H9" />
  </Icon>
)
export const IconCheck = (p: Props) => (
  <Icon {...p}>
    <path d="M4 12.5l5 5L20 6.5" />
  </Icon>
)
export const IconCross = (p: Props) => (
  <Icon {...p}>
    <path d="M6 6l12 12M18 6L6 18" />
  </Icon>
)
export const IconClock = (p: Props) => (
  <Icon {...p}>
    <circle cx="12" cy="12" r="9" />
    <path d="M12 7v5l3 2" />
  </Icon>
)
export const IconAlert = (p: Props) => (
  <Icon {...p}>
    <path d="M10.3 3.9L2 18a2 2 0 0 0 1.7 3h16.6a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z" />
    <path d="M12 9v4M12 17h.01" />
  </Icon>
)
export const IconAsk = (p: Props) => (
  <Icon {...p}>
    <circle cx="12" cy="12" r="9" />
    <path d="M9.5 9.5a2.5 2.5 0 1 1 3.5 2.3c-.7.3-1 .9-1 1.7M12 17h.01" />
  </Icon>
)
export const IconUser = (p: Props) => (
  <Icon {...p}>
    <circle cx="12" cy="8" r="4" />
    <path d="M4 21a8 8 0 0 1 16 0" />
  </Icon>
)
export const IconStethoscope = (p: Props) => (
  <Icon {...p}>
    <path d="M5 3v5a5 5 0 0 0 10 0V3" />
    <path d="M10 13v2a5 5 0 0 0 10 0v-1" />
    <circle cx="20" cy="12" r="2" />
  </Icon>
)
export const IconRefresh = (p: Props) => (
  <Icon {...p}>
    <path d="M20 11a8 8 0 0 0-14.6-4.5L4 8" />
    <path d="M4 3v5h5" />
    <path d="M4 13a8 8 0 0 0 14.6 4.5L20 16" />
    <path d="M20 21v-5h-5" />
  </Icon>
)
export const IconBack = (p: Props) => (
  <Icon {...p}>
    <path d="M15 18l-6-6 6-6" />
  </Icon>
)
export const IconTheme = (p: Props) => (
  <Icon {...p}>
    <circle cx="12" cy="12" r="9" />
    <path d="M12 3v18" />
    <path d="M12 3a9 9 0 0 1 0 18z" fill="currentColor" stroke="none" />
  </Icon>
)
export const IconQuote = (p: Props) => (
  <Icon {...p}>
    <path d="M4 17c2.5 0 4-1.5 4-4V7H4v6h4" />
    <path d="M14 17c2.5 0 4-1.5 4-4V7h-4v6h4" />
  </Icon>
)
export const IconDoc = (p: Props) => (
  <Icon {...p}>
    <path d="M14 3H6a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V9z" />
    <path d="M14 3v6h6M8 13h8M8 17h5" />
  </Icon>
)
export const IconSparkle = (p: Props) => (
  <Icon {...p}>
    <path d="M12 3l1.8 5.2L19 10l-5.2 1.8L12 17l-1.8-5.2L5 10l5.2-1.8z" />
    <path d="M19 16l.7 2.3L22 19l-2.3.7L19 22l-.7-2.3L16 19l2.3-.7z" />
  </Icon>
)
export const IconLock = (p: Props) => (
  <Icon {...p}>
    <rect x="4" y="11" width="16" height="10" rx="2" />
    <path d="M8 11V7a4 4 0 0 1 8 0v4" />
  </Icon>
)
export const IconMedal = (p: Props) => (
  <Icon {...p}>
    <circle cx="12" cy="15" r="6" />
    <path d="M8.5 10.5L5 3h4l3 6 3-6h4l-3.5 7.5" />
    <path d="M12 12.5l.9 1.8 2 .3-1.4 1.4.3 2-1.8-1-1.8 1 .3-2-1.4-1.4 2-.3z" />
  </Icon>
)

/** Vaglio's mark: a sieve, evidence falling in and a check mark coming out. The same drawing as the logo. */
export function Mark({ size = 36 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 96 96" aria-hidden="true" focusable="false">
      <defs>
        <linearGradient id="vmark" gradientUnits="userSpaceOnUse" x1="20" y1="14" x2="76" y2="70">
          <stop offset="0" stopColor="#f0abfc" />
          <stop offset=".5" stopColor="#c084fc" />
          <stop offset="1" stopColor="#8b5cf6" />
        </linearGradient>
        <clipPath id="vbowl">
          <path d="M22 38 A26 26 0 0 0 74 38 Z" />
        </clipPath>
      </defs>
      <rect x="4" y="4" width="88" height="88" rx="22" fill="#0b1220" />
      <rect x="4.75" y="4.75" width="86.5" height="86.5" rx="21.25" fill="none" stroke="#334155" strokeWidth="1.5" />
      <g fill="url(#vmark)">
        <circle cx="36" cy="21" r="3.6" />
        <circle cx="48" cy="15" r="3.6" />
        <circle cx="60" cy="23" r="3.6" />
      </g>
      <g clipPath="url(#vbowl)" stroke="#c4b5fd" strokeOpacity=".55" strokeWidth="1.8">
        <path d="M32 38 V66 M40 38 V66 M48 38 V66 M56 38 V66 M64 38 V66 M20 47 H76 M20 55 H76" />
      </g>
      <path d="M22 38 A26 26 0 0 0 74 38" fill="none" stroke="url(#vmark)" strokeWidth="4.5" strokeLinecap="round" />
      <path d="M16 38 H80" stroke="url(#vmark)" strokeWidth="4.5" strokeLinecap="round" />
      <path d="M38 75 l7 7 l14 -14" fill="none" stroke="#34d399" strokeWidth="5" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  )
}
