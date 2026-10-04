import { NavLink } from 'react-router'
import { Icon } from '../ui/Icon.tsx'
import { navItems } from './navItems.ts'
import styles from './Sidebar.module.css'

// Desktop nav: the MealQuest wordmark, labelled pill rows and the shared
// household card at the foot.
export function Sidebar() {
  return (
    <aside className={styles.sidebar}>
      <p className={styles.wordmark}>MealQuest</p>
      <nav aria-label="Primary" className={styles.nav}>
        {navItems.map((item) => (
          <NavLink
            key={item.to}
            to={item.to}
            end={item.end}
            className={({ isActive }) => (isActive ? `${styles.item} ${styles.active}` : styles.item)}
          >
            <Icon icon={item.icon} size="nav" />
            {item.label}
          </NavLink>
        ))}
      </nav>
      <div className={styles.household}>
        <span className={styles.avatar} aria-hidden="true">
          K
        </span>
        <p className={styles.householdText}>
          <strong>Our kitchen</strong>
          <span className={styles.householdNote}>Shared household</span>
        </p>
      </div>
    </aside>
  )
}
