import { NavLink } from 'react-router'
import { Icon } from '../ui/Icon.tsx'
import { navItems } from './navItems.ts'
import styles from './Rail.module.css'

// Tablet nav: a narrow icon rail with the "M" mark on top.
export function Rail() {
  return (
    <nav aria-label="Primary" className={styles.rail}>
      <div className={styles.mark} aria-hidden="true">
        M
      </div>
      {navItems.map((item) => (
        <NavLink key={item.to} to={item.to} end={item.end} className={styles.item}>
          {({ isActive }) => (
            <>
              <span className={isActive ? `${styles.pill} ${styles.active}` : styles.pill}>
                <Icon icon={item.icon} size="nav" />
              </span>
              {item.label}
            </>
          )}
        </NavLink>
      ))}
    </nav>
  )
}
