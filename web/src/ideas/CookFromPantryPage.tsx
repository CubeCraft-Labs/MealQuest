import { Check, ChevronLeft } from 'lucide-react'
import { useMemo, useRef, useState, type KeyboardEvent } from 'react'
import { Link, useNavigate } from 'react-router'
import { ApiError, useJob } from '../api/index.ts'
import { duration } from '../plan/PlanPage.tsx'
import { Button } from '../ui/Button.tsx'
import { Card } from '../ui/Card.tsx'
import { Chip } from '../ui/Chip.tsx'
import { EmptyState } from '../ui/EmptyState.tsx'
import { ErrorBanner } from '../ui/ErrorBanner.tsx'
import { Icon } from '../ui/Icon.tsx'
import { IconButton } from '../ui/IconButton.tsx'
import { Segmented } from '../ui/Segmented.tsx'
import { Skeleton } from '../ui/Skeleton.tsx'
import { TextInput } from '../ui/TextInput.tsx'
import {
  FROM_INGREDIENTS_JOB_KIND,
  IDEAS_KEPT,
  INGREDIENT_LIMIT,
  MAX_INGREDIENTS,
  parseIdeasResult,
  useAddIdeaToQuestLog,
  useCookIdeaTonight,
  useFindIdeas,
  type Idea,
  type IdeaRef,
  type IdeaStage,
  type Search,
} from './api.ts'
import styles from './CookFromPantryPage.module.css'

type When = 'any' | 'quick'

const WHEN = [
  { value: 'any', label: 'Any time' },
  { value: 'quick', label: 'Quick only' },
] as const satisfies readonly { value: When; label: string }[]

// findingLines says what a running search is doing, by its stage.
const findingLines: Record<Exclude<IdeaStage, 'done'>, string> = {
  suggesting: 'Thinking up ideas…',
  grounding: 'Finding recipes for them…',
}

// searchFailures says why a search failed, by its failure code. Any other
// code is shown as it is; no_ideas_found is not a failure to fix, and has
// its own empty state.
const searchFailures: Record<string, string> = {
  ai_failed: "The AI assistant couldn't come up with ideas.",
  search_not_configured: 'Recipe search is not set up. An admin can set it up in Settings.',
  search_failed: "The recipe search couldn't be reached.",
  internal_error: 'Something went wrong on the server.',
}

// fold is an ingredient as the chips compare it, so "Rice" repeats " rice".
function fold(ingredient: string): string {
  return ingredient.trim().replace(/\s+/g, ' ').toLowerCase()
}

// IdeaCard is one idea (mock 1i): its name and time, the server's line on
// what it uses and what is missing, and its two actions, each showing its
// own progress and failure. ref is the idea in its job, null while the job
// runs: the server takes an idea only once the job has succeeded, as the
// ideas may still be reordered until then.
function IdeaCard({ idea, ideaRef }: { idea: Idea; ideaRef: IdeaRef | null }) {
  const questLog = useAddIdeaToQuestLog()
  const tonight = useCookIdeaTonight()
  const noPlan = tonight.error instanceof ApiError && tonight.error.code === 'no_plan'
  return (
    <Card tone="muted" className={styles.card} role="article" aria-label={idea.name}>
      <div className={styles.cardHead}>
        <h2 className={styles.name}>{idea.name}</h2>
        {idea.minutes_total !== null && <span className={styles.time}>{duration(idea.minutes_total)}</span>}
      </div>
      <p className={styles.summary}>{idea.summary}</p>
      <div className={styles.actions}>
        {tonight.isSuccess ? (
          <span className={styles.done} role="status">
            <Icon icon={Check} /> On tonight's plan
          </span>
        ) : (
          <Button
            className={styles.action}
            disabled={ideaRef === null || tonight.isPending}
            onClick={() => tonight.mutate(ideaRef!)}
          >
            {tonight.isPending ? 'Planning…' : 'Cook tonight'}
          </Button>
        )}
        {questLog.isSuccess ? (
          <span className={styles.done} role="status">
            <Icon icon={Check} /> In the Quest log
          </span>
        ) : (
          <Button
            variant="outline"
            className={styles.action}
            disabled={ideaRef === null || questLog.isPending}
            onClick={() => questLog.mutate(ideaRef!)}
          >
            {questLog.isPending ? 'Adding…' : 'Add to Quest log'}
          </Button>
        )}
      </div>
      {noPlan ? (
        <div className={styles.noPlan} role="status">
          <span>This week has no plan yet, so there's no tonight to put it on.</span>
          <Link to="/plan" className={styles.planLink}>
            Plan this week
          </Link>
        </div>
      ) : (
        tonight.isError && (
          <p className={styles.failure} role="alert">
            Couldn't put it on tonight's plan. {tonight.error.message}
          </p>
        )
      )}
      {questLog.isError && (
        <p className={styles.failure} role="alert">
          Couldn't add it to the Quest log. {questLog.error.message}
        </p>
      )}
    </Card>
  )
}

// Skeletons stand in for the count ideas still to come.
function Skeletons({ count }: { count: number }) {
  return Array.from({ length: count }, (_, i) => <Skeleton key={i} data-testid="skeleton-card" className={styles.skeletonCard} />)
}

// CookFromPantryPage is mock 1i, Cook from what I have: the ingredients on
// hand as chips (Enter or a comma adds one), Any time | Quick only, and
// Find ideas, which starts an ideas_from_ingredients job and shows its
// ideas as the server grounds them, skeleton cards holding the places of
// those still to come. Changing Any time | Quick only after a search asks
// again: the server, not the screen, says what is quick.
export function CookFromPantryPage() {
  const navigate = useNavigate()
  const inputRef = useRef<HTMLInputElement>(null)
  const [ingredients, setIngredients] = useState<string[]>([])
  const [draft, setDraft] = useState('')
  const [notice, setNotice] = useState<string | null>(null)
  const [when, setWhen] = useState<When>('any')
  // The search last asked for, which Retry asks again, and its job.
  const [asked, setAsked] = useState<Search | null>(null)
  const [jobId, setJobId] = useState<string | null>(null)
  const findIdeas = useFindIdeas()
  const job = useJob<unknown>(FROM_INGREDIENTS_JOB_KIND, jobId)

  // add lists entries after the chips there are, saying why it leaves out
  // any it cannot list: the server would refuse a blank, overlong or
  // repeated ingredient, or one past the most it takes.
  function add(list: string[], entries: string[]): string[] {
    const next = [...list]
    for (const entry of entries.map((e) => e.trim().replace(/\s+/g, ' '))) {
      if (entry === '') continue
      if (next.some((ingredient) => fold(ingredient) === fold(entry))) {
        setNotice(`${entry} is listed already.`)
      } else if (entry.length > INGREDIENT_LIMIT) {
        setNotice(`An ingredient can be at most ${INGREDIENT_LIMIT} characters.`)
      } else if (next.length === MAX_INGREDIENTS) {
        setNotice(`List at most ${MAX_INGREDIENTS} ingredients.`)
      } else {
        next.push(entry)
      }
    }
    return next
  }

  function onDraftChange(value: string) {
    setNotice(null)
    // A comma ends an ingredient, also within pasted text.
    const parts = value.split(',')
    const rest = parts.pop()!
    if (parts.length > 0) setIngredients(add(ingredients, parts))
    setDraft(rest)
  }

  function onDraftKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    if (event.key === 'Enter') {
      event.preventDefault()
      setNotice(null)
      setIngredients(add(ingredients, [draft]))
      setDraft('')
    } else if (event.key === 'Backspace' && draft === '' && ingredients.length > 0) {
      setNotice(null)
      setIngredients(ingredients.slice(0, -1))
    }
  }

  // search asks for ideas. Only the latest search's job is followed: mutate
  // calls back for the latest call only.
  function search(next: Search) {
    setAsked(next)
    setJobId(null)
    findIdeas.mutate(next, { onSuccess: setJobId })
  }

  // find lists what is still typed, then searches the chips.
  function find() {
    setNotice(null)
    const list = add(ingredients, [draft])
    setIngredients(list)
    setDraft('')
    if (list.length > 0) search({ ingredients: list, quickOnly: when === 'quick' })
  }

  function onWhenChange(value: When) {
    setWhen(value)
    if (asked !== null && ingredients.length > 0) search({ ingredients, quickOnly: value === 'quick' })
  }

  const retry = () => search(asked!)

  // The job's result as far as it has gone, or why it cannot be read.
  const found = useMemo(() => {
    if (job.result === null || asked === null) return { result: null, error: null }
    try {
      return { result: parseIdeasResult(`GET /api/v1/jobs/${jobId}`, asked.quickOnly, job.result), error: null }
    } catch (error) {
      return { result: null, error: error as Error }
    }
  }, [job.result, jobId, asked])

  let results = null
  if (findIdeas.isPending) {
    results = (
      <section aria-busy="true" aria-label="Ideas loading" className={styles.results}>
        <p className={styles.count}>{findingLines.suggesting}</p>
        <Skeletons count={IDEAS_KEPT} />
      </section>
    )
  } else if (findIdeas.isError) {
    results = <ErrorBanner onRetry={retry}>Couldn't start looking for ideas. {findIdeas.error.message}</ErrorBanner>
  } else if (jobId !== null && job.status === 'failed') {
    const code = job.error!.message
    results =
      code === 'no_ideas_found' ? (
        <EmptyState title="No ideas found" action={{ label: 'Retry', onClick: retry }}>
          No recipe matched {asked!.quickOnly ? 'a quick dinner from ' : ''}those ingredients. Add a few more and try again.
        </EmptyState>
      ) : (
        <ErrorBanner onRetry={retry}>Couldn't find ideas. {searchFailures[code] ?? code}</ErrorBanner>
      )
  } else if (found.error !== null) {
    results = <ErrorBanner onRetry={retry}>Couldn't read the ideas. {found.error.message}</ErrorBanner>
  } else if (jobId !== null) {
    const ideas = found.result?.ideas ?? []
    const done = job.status === 'succeeded'
    // A job not found will not be found by asking again: Retry searches
    // again. Any other failed poll is asked again.
    const lost = job.error instanceof ApiError && job.error.status === 404
    results = (
      <>
        {job.error !== null && (
          <ErrorBanner onRetry={lost ? retry : job.poll}>Couldn't check on the ideas. {job.error.message}</ErrorBanner>
        )}
        <section aria-busy={!done} aria-label="Ideas" className={styles.results}>
          <p className={styles.count}>
            {done
              ? `${ideas.length} ${ideas.length === 1 ? 'idea' : 'ideas'}`
              : findingLines[found.result?.stage === 'grounding' ? 'grounding' : 'suggesting']}
          </p>
          {ideas.map((idea, index) => (
            <IdeaCard key={`${jobId}:${index}`} idea={idea} ideaRef={done ? { jobId, index } : null} />
          ))}
          {!done && <Skeletons count={Math.max(IDEAS_KEPT - ideas.length, 0)} />}
        </section>
      </>
    )
  }

  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <IconButton label="Back to Home" onClick={() => void navigate('/')}>
          <Icon icon={ChevronLeft} size="nav" />
        </IconButton>
        <h1 className={styles.title}>Cook from what I have</h1>
      </header>
      {/* A tap anywhere on the field types in it. */}
      <div className={styles.field} onClick={() => inputRef.current?.focus()}>
        <ul className={styles.chips} aria-label="Ingredients on hand">
          {ingredients.map((ingredient, i) => (
            <li key={ingredient}>
              <Chip
                label={ingredient}
                className={styles.chip}
                onRemove={() => {
                  setNotice(null)
                  setIngredients(ingredients.filter((_, j) => j !== i))
                  inputRef.current?.focus()
                }}
              />
            </li>
          ))}
        </ul>
        <div className={styles.entry}>
          <TextInput
            ref={inputRef}
            label="Add ingredient"
            labelHidden
            placeholder="Add ingredient…"
            enterKeyHint="enter"
            autoComplete="off"
            className={styles.input}
            value={draft}
            onChange={(event) => onDraftChange(event.target.value)}
            onKeyDown={onDraftKeyDown}
          />
        </div>
      </div>
      {notice !== null && (
        <p className={styles.notice} role="status">
          {notice}
        </p>
      )}
      <div className={styles.controls}>
        <Segmented label="Which dinners" options={WHEN} value={when} onValueChange={onWhenChange} className={styles.when} />
        <Button variant="dark" className={styles.find} disabled={ingredients.length < 1 || findIdeas.isPending} onClick={find}>
          Find ideas
        </Button>
      </div>
      {results}
    </div>
  )
}
