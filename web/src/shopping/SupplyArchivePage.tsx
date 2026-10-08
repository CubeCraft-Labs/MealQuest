import { ChevronLeft } from 'lucide-react'
import { useState } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router'
import { useBreakpoint } from '../shell/useBreakpoint.ts'
import { Button } from '../ui/Button.tsx'
import { EmptyState } from '../ui/EmptyState.tsx'
import { ErrorBanner } from '../ui/ErrorBanner.tsx'
import { Icon } from '../ui/Icon.tsx'
import { IconButton } from '../ui/IconButton.tsx'
import { Segmented } from '../ui/Segmented.tsx'
import { Skeleton } from '../ui/Skeleton.tsx'
import { useSupplyArchive, WEEK_PARAM, type ArchiveGroup, type ArchivePeriod, type ArchivePost } from './api.ts'
import styles from './SupplyArchivePage.module.css'
import { SUPPLY_ARCHIVE, SupplyRunPage } from './SupplyRunPage.tsx'

// PAGE is how many posts the Supply archive shows at first, and how many
// more each "Load older" shows.
export const PAGE = 8

// CHIPS is how many of a post's dinners it names; a "+N" chip counts the rest.
const CHIPS = 2

const PERIODS = [
  { value: 'week', label: 'Week' },
  { value: 'month', label: 'Month' },
  { value: 'year', label: 'Year' },
] as const satisfies readonly { value: ArchivePeriod; label: string }[]

// archivePost is the Supply archive screen opened on the post of the week
// starting on Monday weekStart.
export function archivePost(weekStart: string): string {
  return `${SUPPLY_ARCHIVE}?${new URLSearchParams({ [WEEK_PARAM]: weekStart })}`
}

function plural(n: number, one: string, many: string): string {
  return `${n} ${n === 1 ? one : many}`
}

// PostCard is one week's Supply run (mock 1m): the week label as its kicker,
// the AI's title — the week label while it has none — the counts and its
// first dinners as chips. The current week's post is ringed in grape. It
// opens the week's list, read-only.
function PostCard({ post }: { post: ArchivePost }) {
  const rest = post.dinners.length - CHIPS
  return (
    <Link to={archivePost(post.weekStart)} className={[styles.post, post.current && styles.current].filter(Boolean).join(' ')}>
      <span className={styles.kicker}>
        {post.label}
        {post.current && ' · Current'}
      </span>
      <span className={styles.postTitle}>{post.title ?? post.label}</span>
      <span className={styles.meta}>
        {plural(post.dinnerCount, 'dinner', 'dinners')} · {plural(post.itemCount, 'item', 'items')} · {post.onHandCount} already on
        hand
      </span>
      {post.dinners.length > 0 && (
        <span className={styles.chips}>
          {post.dinners.slice(0, CHIPS).map((name, i) => (
            <span key={i} className={styles.chip}>
              {name}
            </span>
          ))}
          {rest > 0 && <span className={styles.chip}>+{rest}</span>}
        </span>
      )}
    </Link>
  )
}

function Posts({ posts }: { posts: ArchivePost[] }) {
  return (
    <ul className={styles.posts}>
      {posts.map((post) => (
        <li key={post.weekStart}>
          <PostCard post={post} />
        </li>
      ))}
    </ul>
  )
}

// shownGroups is groups cut to their first shown posts, newest first, without
// the periods left with none.
function shownGroups(groups: ArchiveGroup[], shown: number): ArchiveGroup[] {
  const out: ArchiveGroup[] = []
  let left = shown
  for (const group of groups) {
    if (left === 0) break
    const posts = group.posts.slice(0, left)
    out.push({ ...group, posts })
    left -= posts.length
  }
  return out
}

// ArchivePosts is the Supply archive as the server groups it by by: one post
// per week, under the Bagel heading of each period the server sends — by
// week, the week's label. It shows PAGE posts and "Load older" shows PAGE
// more.
function ArchivePosts({ by }: { by: ArchivePeriod }) {
  const archive = useSupplyArchive(by)
  const [shown, setShown] = useState(PAGE)

  if (archive.isError) {
    return <ErrorBanner onRetry={() => void archive.refetch()}>Couldn't load the Supply archive. {archive.error.message}</ErrorBanner>
  }
  if (archive.isPending) {
    return (
      <div aria-busy="true" aria-label="Supply archive loading" className={styles.posts}>
        {Array.from({ length: 3 }, (_, i) => (
          <Skeleton key={i} data-testid="skeleton-post" className={styles.skeletonPost} />
        ))}
      </div>
    )
  }
  const total = archive.data.reduce((n, group) => n + group.posts.length, 0)
  if (total === 0) {
    return <EmptyState title="Nothing archived yet">Each week's Supply run becomes a post here once it's made.</EmptyState>
  }
  const groups = shownGroups(archive.data, shown)
  return (
    <>
      {groups.map((group) => (
        <section key={group.key} aria-label={group.heading} className={styles.period}>
          <h2 className={styles.heading}>{group.heading}</h2>
          <Posts posts={group.posts} />
        </section>
      ))}
      {shown < total && (
        <Button variant="outline" className={styles.older} onClick={() => setShown(shown + PAGE)}>
          Load older
        </Button>
      )}
    </>
  )
}

// SupplyArchive is the Supply archive (mock 1m, and the right panel of 2f):
// the Week | Month | Year control over the posts. On mobile and tablet a
// back button returns to the Supply run.
function SupplyArchive({ panel }: { panel: boolean }) {
  const navigate = useNavigate()
  const [by, setBy] = useState<ArchivePeriod>('week')
  return (
    <div className={styles.archive}>
      <header className={styles.header}>
        {!panel && (
          <IconButton label="Back to the Supply run" onClick={() => void navigate('/plan/shopping')}>
            <Icon icon={ChevronLeft} size="nav" />
          </IconButton>
        )}
        <h1 className={styles.title}>Supply archive</h1>
      </header>
      <Segmented label="Group the Supply archive by" options={PERIODS} value={by} onValueChange={setBy} className={styles.periods} />
      <ArchivePosts key={by} by={by} />
    </div>
  )
}

// SupplyArchivePage is /plan/shopping/archive. A post opens it on its week
// (?week=), whose list the Supply run screen shows read-only and tagged
// Archived, whatever its status: even the current week's post is only
// viewed here, never changed. On desktop (mock 2f) the archive is a panel at
// the right of that list, this week's editable Supply run when no post is
// open; on mobile and tablet it is the archive or, with a post open, the
// post's list.
export function SupplyArchivePage() {
  const layout = useBreakpoint()
  const [params] = useSearchParams()
  if (layout === 'desktop') {
    return (
      <div className={styles.split}>
        <div className={styles.list}>
          <SupplyRunPage readOnly={params.has(WEEK_PARAM)} />
        </div>
        <aside className={styles.panel} aria-label="Supply archive">
          <SupplyArchive panel />
        </aside>
      </div>
    )
  }
  return params.has(WEEK_PARAM) ? <SupplyRunPage readOnly /> : <SupplyArchive panel={false} />
}
