import { NavLink } from 'react-router'
import { Icon } from '../ui/Icon.tsx'
import { navItems } from './navItems.ts'
import styles from './BottomBar.module.css'

// Mobile nav: a dark pill bar floating over the bottom of the screen. Inactive
// items show only their icon; the active item adds its label on a grape pill.
export function BottomBar() {
  return (
    <nav aria-label="Primary" className={styles.bar}>
      {navItems.map((item) => (
        <NavLink key={item.to} to={item.to} end={item.end} className={styles.item}>
          {({ isActive }) => (
            <span className={isActive ? `${styles.pill} ${styles.active}` : styles.pill}>
              <Icon icon={item.icon} size="nav" />
              <span className={isActive ? styles.label : styles.hiddenLabel}>{item.label}</span>
            </span>
          )}
        </NavLink>
      ))}
    </nav>
  )
}
