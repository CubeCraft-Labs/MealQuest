import { ChevronRight, Refrigerator, Zap, type LucideIcon } from 'lucide-react'
import { Link } from 'react-router'
import { Card, Icon } from '../ui/index.ts'
import styles from './HomePage.module.css'

// Home stands in for the Home screen until its feature is built, with the
// shortcuts to the idea screens that have no other entry point.
export function HomePage() {
  return (
    <div className={styles.page}>
      <h1 className={styles.title}>Home</h1>
      <Card>
        <ul className={styles.rows}>
          <Shortcut to="/quick" icon={Zap} title="Quick dinner" />
          <Shortcut to="/cook-from-pantry" icon={Refrigerator} title="Cook from what I have" />
        </ul>
      </Card>
    </div>
  )
}

type ShortcutProps = {
  to: string
  icon: LucideIcon
  title: string
}

function Shortcut({ to, icon, title }: ShortcutProps) {
  return (
    <li className={styles.item}>
      <Link to={to} className={styles.row}>
        <span className={styles.icon}>
          <Icon icon={icon} />
        </span>
        <span className={styles.rowTitle}>{title}</span>
        <Icon icon={ChevronRight} />
      </Link>
    </li>
  )
}
