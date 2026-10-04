import { EmptyState } from '../ui/EmptyState.tsx'
import styles from './FullScreen.module.css'

// Shown when the server refuses a signed-in account that is not in this
// household. Nothing else renders: the account can do nothing here.
export function NotInHouseholdPage() {
  return (
    <main className={styles.screen}>
      <EmptyState title="Not in this household">
        You are signed in, but this account is not in this household. Ask someone in Our kitchen to add it.
      </EmptyState>
    </main>
  )
}
