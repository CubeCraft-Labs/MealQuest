import type { LucideIcon } from 'lucide-react'
import styles from './Icon.module.css'

export type IconSize = 'nav' | 'inline'

export type IconProps = {
  // A lucide-react icon component, e.g. `import { House } from 'lucide-react'`.
  icon: LucideIcon
  // nav = 22 px (bottom bar, sidebar); inline = 16 px (beside text).
  size?: IconSize
  // Accessible name. Without one the icon is decorative and hidden.
  label?: string
  className?: string
}

// Lucide icon at the handoff's stroke width 2.75, sized by the stylesheet.
export function Icon({ icon: LucideGlyph, size = 'inline', label, className }: IconProps) {
  const classes = [styles.icon, styles[size], className].filter(Boolean).join(' ')
  const a11y = label === undefined ? { 'aria-hidden': true } : { role: 'img', 'aria-label': label }
  return <LucideGlyph strokeWidth={2.75} className={classes} {...a11y} />
}
