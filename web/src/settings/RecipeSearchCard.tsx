import { useEffect, useId, useState } from 'react'
import { ApiError } from '../api/index.ts'
import { Button } from '../ui/Button.tsx'
import { Card } from '../ui/Card.tsx'
import { ErrorBanner } from '../ui/ErrorBanner.tsx'
import { Segmented } from '../ui/Segmented.tsx'
import { Sheet } from '../ui/Sheet.tsx'
import { Skeleton } from '../ui/Skeleton.tsx'
import { TextInput } from '../ui/TextInput.tsx'
import {
  maskedKey,
  recipeSearchProviderLabels,
  useRecipeSearchSettings,
  useSaveRecipeSearchSettings,
  useTestRecipeSearch,
  type RecipeSearchProvider,
  type RecipeSearchSettings,
  type RecipeSearchSettingsPatch,
} from './api.ts'
import styles from './AISettingsPage.module.css'

const providerOptions = Object.entries(recipeSearchProviderLabels).map(([value, label]) => ({
  value: value as RecipeSearchProvider,
  label,
}))

type RecipeSearchCardProps = {
  // Called with whether the card holds unsaved changes.
  onDirtyChange: (dirty: boolean) => void
}

// RecipeSearchCard is where recipes are searched for: a SearXNG instance or
// Brave. The provider, SearXNG URL and a new Brave key are edited as a draft
// and saved together; the client only ever sees the stored key's last four
// characters. Test searches with the saved settings.
export function RecipeSearchCard({ onDirtyChange }: RecipeSearchCardProps) {
  const settings = useRecipeSearchSettings()
  const titleId = useId()
  return (
    <Card className={styles.card} aria-labelledby={titleId} role="region">
      <h2 id={titleId} className={styles.cardTitle}>
        Recipe search
      </h2>
      <p className={styles.hint}>Where MealQuest looks for real recipes behind each Side quest.</p>
      {settings.isPending && <Skeleton className={styles.cardLoading} />}
      {settings.isError && (
        <ErrorBanner onRetry={() => void settings.refetch()}>
          Recipe search settings could not load: {settings.error.message}
        </ErrorBanner>
      )}
      {settings.isSuccess && <RecipeSearchForm saved={settings.data} onDirtyChange={onDirtyChange} />}
    </Card>
  )
}

type Draft = {
  provider: RecipeSearchProvider
  base_url: string
  // A new Brave key to store, null when the stored one stays.
  api_key: string | null
}

// draftOf starts a household that has not set up recipe search on SearXNG,
// the sidecar every deployment runs. With no URL saved the field holds the
// deployment's SearXNG URL, which saving stores.
function draftOf(saved: RecipeSearchSettings): Draft {
  return {
    provider: saved.provider ?? 'searxng',
    base_url: saved.base_url ?? saved.default_base_url ?? '',
    api_key: null,
  }
}

// patchOf is the PATCH body for what draft changes. Only the chosen
// provider's field is sent: the other provider's is left as it is.
function patchOf(saved: RecipeSearchSettings, draft: Draft): RecipeSearchSettingsPatch {
  const patch: RecipeSearchSettingsPatch = {}
  if (draft.provider !== saved.provider) patch.provider = draft.provider
  if (draft.provider === 'searxng' && draft.base_url !== (saved.base_url ?? '')) patch.base_url = draft.base_url
  if (draft.provider === 'brave' && draft.api_key !== null) patch.api_key = draft.api_key
  return patch
}

function RecipeSearchForm({ saved, onDirtyChange }: { saved: RecipeSearchSettings } & RecipeSearchCardProps) {
  const [draft, setDraft] = useState(() => draftOf(saved))
  const save = useSaveRecipeSearchSettings()
  const test = useTestRecipeSearch()
  const patch = patchOf(saved, draft)
  const dirty = Object.keys(patch).length > 0

  useEffect(() => {
    onDirtyChange(dirty)
  }, [dirty, onDirtyChange])
  useEffect(() => () => onDirtyChange(false), [onDirtyChange])

  // An edit clears the last save and test results: they describe the saved
  // settings, not the draft.
  function edit(change: Partial<Draft>) {
    save.reset()
    test.reset()
    setDraft((current) => ({ ...current, ...change }))
  }

  function onSave() {
    save.mutate(patch, {
      onSuccess: (settings) => {
        setDraft(draftOf(settings))
        test.reset()
      },
    })
  }

  return (
    <>
      <Segmented
        label="Recipe search provider"
        options={providerOptions}
        value={draft.provider}
        onValueChange={(provider) => edit({ provider })}
      />
      {draft.provider === 'searxng' ? (
        <TextInput
          label="SearXNG URL"
          type="url"
          className={styles.mono}
          value={draft.base_url}
          placeholder="https://"
          autoComplete="off"
          spellCheck={false}
          onChange={(event) => edit({ base_url: event.target.value })}
        />
      ) : (
        <BraveKey
          last4={saved.api_key_last4}
          newKey={draft.api_key}
          onKey={(api_key) => edit({ api_key })}
        />
      )}

      <div className={styles.status} role="status">
        {save.isSuccess && !dirty && 'Recipe search saved.'}
        {test.isSuccess && `Found ${test.data.count} ${test.data.count === 1 ? 'recipe' : 'recipes'} for '${test.data.query}'`}
      </div>
      {save.isError && (
        <p className={styles.error} role="alert">
          {saveErrorMessage(save.error)}
        </p>
      )}
      {test.isError && (
        <p className={styles.error} role="alert">
          {testErrorMessage(test.error)}
        </p>
      )}
      {dirty && saved.provider !== null && <p className={styles.note}>Save your changes to test them.</p>}

      <div className={styles.cardActions}>
        <Button
          variant="outline"
          disabled={dirty || saved.provider === null || test.isPending}
          onClick={() => test.mutate()}
        >
          {test.isPending ? 'Testing…' : 'Test'}
        </Button>
        <Button disabled={!dirty || save.isPending} onClick={onSave}>
          {save.isPending ? 'Saving…' : 'Save recipe search'}
        </Button>
      </div>
    </>
  )
}

type BraveKeyProps = {
  last4: string | null
  newKey: string | null
  onKey: (key: string | null) => void
}

// BraveKey shows the stored key masked, or the new one waiting to be saved,
// and takes a new key in a sheet. The key is sent only when the card is
// saved.
function BraveKey({ last4, newKey, onKey }: BraveKeyProps) {
  const [replacing, setReplacing] = useState(false)
  const [key, setKey] = useState('')
  const labelId = useId()
  const sheetTitleId = useId()

  function close() {
    setReplacing(false)
    setKey('')
  }

  let shown = 'No key stored'
  if (newKey !== null) shown = maskedKey(newKey.slice(-4))
  else if (last4 !== null) shown = maskedKey(last4)

  return (
    <div className={styles.key}>
      <span id={labelId} className={styles.label}>
        Brave API key
      </span>
      <div className={styles.keyRow} role="group" aria-labelledby={labelId}>
        <span className={styles.keyValue}>{shown}</span>
        <button type="button" className={styles.replace} onClick={() => setReplacing(true)}>
          {last4 === null && newKey === null ? 'Add key' : 'Replace'}
        </button>
      </div>
      {newKey !== null && (
        <div className={styles.keyStatus}>
          <p className={styles.note}>New key not saved yet</p>
          <button type="button" className={styles.link} onClick={() => onKey(null)}>
            Discard
          </button>
        </div>
      )}

      <Sheet open={replacing} onClose={close} aria-labelledby={sheetTitleId}>
        <form
          className={styles.sheet}
          onSubmit={(event) => {
            event.preventDefault()
            onKey(key)
            close()
          }}
        >
          <h2 id={sheetTitleId} className={styles.sheetTitle}>
            {last4 === null ? 'Add Brave API key' : 'Replace Brave API key'}
          </h2>
          <p className={styles.sheetText}>
            The key is stored on the server when you save recipe search. This device only ever sees its last four
            characters.
          </p>
          <TextInput
            label="Brave API key"
            type="password"
            className={styles.mono}
            value={key}
            autoComplete="off"
            spellCheck={false}
            onChange={(event) => setKey(event.target.value)}
          />
          <div className={styles.sheetActions}>
            <Button variant="outline" onClick={close}>
              Cancel
            </Button>
            <Button type="submit" disabled={key === ''}>
              Use this key
            </Button>
          </div>
        </form>
      </Sheet>
    </div>
  )
}

function saveErrorMessage(error: Error): string {
  if (error instanceof ApiError && error.code === 'invalid_settings') return `Not saved: ${error.message}.`
  return `Not saved: ${error.message}`
}

// testErrorMessage says why POST /settings/recipe-search/test failed, by
// the error code the server names (internal/server/recipesearch.go).
function testErrorMessage(error: Error): string {
  if (error instanceof ApiError && error.code === 'not_configured') {
    return 'Test failed: recipe search is not set up yet.'
  }
  return `Test failed: ${error.message}`
}
