import type { LucideIcon } from 'lucide-react'
import { Calendar, Compass, House, SlidersHorizontal, Zap } from 'lucide-react'

export type NavItem = {
  to: string
  label: string
  icon: LucideIcon
  // Match the path exactly; otherwise nested paths (/plan/shopping,
  // /settings/ai) also mark the item current.
  end: boolean
}

// The primary nav, in the same order in every layout.
export const navItems: readonly NavItem[] = [
  { to: '/', label: 'Home', icon: House, end: true },
  { to: '/plan', label: 'Plan', icon: Calendar, end: false },
  { to: '/discover', label: 'Discover', icon: Compass, end: false },
  { to: '/quick', label: 'Quick', icon: Zap, end: false },
  { to: '/settings', label: 'Settings', icon: SlidersHorizontal, end: false },
]
