import { useSyncExternalStore } from 'react'
import { WifiOff } from 'lucide-react'
import { useApi } from '../api/index.ts'
import { Icon } from '../ui/Icon.tsx'
import styles from './OfflinePill.module.css'

// OfflinePill tells the household that what they see came from the cache
// while the browser is offline or the server cannot be reached.
export function OfflinePill() {
  const { network } = useApi()
  const offline = useSyncExternalStore(network.subscribe, network.offline)
  if (!offline) return null
  return (
    <p role="status" className={styles.pill}>
      <Icon icon={WifiOff} />
      Offline — showing saved data
    </p>
  )
}
