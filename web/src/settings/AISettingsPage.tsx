import { ChevronLeft } from 'lucide-react'
import { useEffect, useId, useState, type ReactNode } from 'react'
import { useNavigate } from 'react-router'
import { ApiError } from '../api/index.ts'
import { Button } from '../ui/Button.tsx'
import { ErrorBanner } from '../ui/ErrorBanner.tsx'
import { Icon } from '../ui/Icon.tsx'
import { IconButton } from '../ui/IconButton.tsx'
import { Select } from '../ui/Select.tsx'
import { Sheet } from '../ui/Sheet.tsx'
import { Skeleton } from '../ui/Skeleton.tsx'
import { Slider } from '../ui/Slider.tsx'
import { TextInput } from '../ui/TextInput.tsx'
import {
  aiKeyPolicy,
  aiProviderLabels,
  maskedKey,
  providersWithBaseURL,
  useAIModels,
  useAISettings,
  useResetAIPrompt,
  useSaveAISettings,
  useSetAIKey,
  useVerifyAIKey,
  type AIProvider,
  type AISettings,
  type AISettingsPatch,
} from './api.ts'
import styles from './AISettingsPage.module.css'
import { Confirm, LeaveGuard } from './LeaveGuard.tsx'
import { RecipeSearchCard } from './RecipeSearchCard.tsx'

// TYPE_MODEL is the Model dropdown's "Type a model id" option.
const TYPE_MODEL = '\u0000type'

// Admin · AI assistant (handoff mock 1o). Provider, model, temperature and
// system prompt are edited as a draft and saved together; the API key is
// stored (and verified) on its own, straight away. The client only ever
// sees the key's last four characters. Recipe search is its own card, saved
// on its own.
export function AISettingsPage() {
  const settings = useAISettings()
  const [aiDirty, setAIDirty] = useState(false)
  const [recipeSearchDirty, setRecipeSearchDirty] = useState(false)
  return (
    <div className={styles.page}>
      <Header />
      {settings.isPending && <Skeleton className={styles.loading} />}
      {settings.isError && (
        <ErrorBanner onRetry={() => void settings.refetch()}>
          AI settings could not load: {settings.error.message}
        </ErrorBanner>
      )}
      {settings.isSuccess && <AISettingsForm saved={settings.data} onDirtyChange={setAIDirty} />}
      <RecipeSearchCard onDirtyChange={setRecipeSearchDirty} />
      <LeaveGuard dirty={aiDirty || recipeSearchDirty} />
    </div>
  )
}

function Header() {
  const navigate = useNavigate()
  return (
    <header className={styles.header}>
      <IconButton label="Back to Settings" onClick={() => void navigate('/settings')}>
        <Icon icon={ChevronLeft} size="nav" />
      </IconButton>
      <div>
        <p className={styles.kicker}>Settings · Admin</p>
        <h1 className={styles.title}>AI assistant</h1>
      </div>
    </header>
  )
}

type Draft = {
  provider: AIProvider
  base_url: string
  model: string
  temperature: number
  system_prompt: string
}

function draftOf(saved: AISettings): Draft {
  return {
    provider: saved.provider,
    base_url: saved.base_url,
    model: saved.model,
    temperature: saved.temperature,
    system_prompt: saved.system_prompt ?? '',
  }
}

// patchOf is the PATCH body for the fields draft changes.
function patchOf(saved: AISettings, draft: Draft): AISettingsPatch {
  const before = draftOf(saved)
  const patch: AISettingsPatch = {}
  if (draft.provider !== before.provider) patch.provider = draft.provider
  if (draft.base_url !== before.base_url) patch.base_url = draft.base_url
  if (draft.model !== before.model) patch.model = draft.model
  if (draft.temperature !== before.temperature) patch.temperature = draft.temperature
  if (draft.system_prompt !== before.system_prompt) patch.system_prompt = draft.system_prompt
  return patch
}

type AISettingsFormProps = {
  saved: AISettings
  // Called with whether the form holds unsaved changes.
  onDirtyChange: (dirty: boolean) => void
}

function AISettingsForm({ saved, onDirtyChange }: AISettingsFormProps) {
  const [draft, setDraft] = useState(() => draftOf(saved))
  const [typingModel, setTypingModel] = useState(false)
  const [confirmingReset, setConfirmingReset] = useState(false)
  const save = useSaveAISettings()
  const resetPrompt = useResetAIPrompt()
  const patch = patchOf(saved, draft)
  const dirty = Object.keys(patch).length > 0
  const providerChanged = draft.provider !== saved.provider
  // The server keeps a stored key's provider and base URL fixed until the
  // key is removed.
  const keyLocked = saved.api_key_last4 !== null && (providerChanged || draft.base_url !== saved.base_url)
  const temperatureId = useId()
  // The model list is the saved provider's, so it is asked for only while
  // that provider is chosen and has the key it needs.
  const canList =
    !providerChanged && (aiKeyPolicy[saved.provider] !== 'required' || saved.api_key_last4 !== null)
  const models = useAIModels(canList)
  const listed = canList && models.isSuccess ? models.data : []
  const typedModel = typingModel || !(draft.model === '' || listed.some((m) => m.id === draft.model))

  useEffect(() => {
    onDirtyChange(dirty)
  }, [dirty, onDirtyChange])
  useEffect(() => () => onDirtyChange(false), [onDirtyChange])

  function edit(change: Partial<Draft>) {
    save.reset()
    setDraft((current) => ({ ...current, ...change }))
  }

  function chooseProvider(provider: AIProvider) {
    setTypingModel(provider !== saved.provider)
    if (provider === saved.provider) {
      edit({ provider, base_url: saved.base_url, model: saved.model })
      return
    }
    // A fixed-URL preset refuses any other base URL, and the old model is
    // another provider's: the household types the new one.
    edit({ provider, base_url: providersWithBaseURL.has(provider) ? draft.base_url : '', model: '' })
  }

  function onSave() {
    save.mutate(patch, {
      onSuccess: (settings) => {
        setDraft(draftOf(settings))
        setTypingModel(false)
      },
    })
  }

  function onConfirmReset() {
    resetPrompt.mutate(undefined, {
      onSuccess: (settings) => {
        setDraft((current) => ({ ...current, system_prompt: settings.system_prompt ?? '' }))
        setConfirmingReset(false)
      },
    })
  }

  return (
    <>
      <div className={styles.columns}>
        <Select
          label="Provider"
          value={draft.provider}
          onChange={(event) => chooseProvider(event.target.value as AIProvider)}
        >
          {Object.entries(aiProviderLabels).map(([value, label]) => (
            <option key={value} value={value}>
              {label}
            </option>
          ))}
        </Select>
        <div className={styles.model}>
          <Select
            label="Model"
            value={typedModel ? TYPE_MODEL : draft.model}
            onChange={(event) => {
              const model = event.target.value
              setTypingModel(model === TYPE_MODEL)
              if (model !== TYPE_MODEL) edit({ model })
            }}
          >
            {!typedModel && draft.model === '' && (
              <option value="" disabled>
                Choose a model
              </option>
            )}
            {listed.map((model) => (
              <option key={model.id} value={model.id}>
                {model.display_name}
              </option>
            ))}
            <option value={TYPE_MODEL}>Type a model id</option>
          </Select>
          {canList && models.isPending && <p className={styles.note}>Loading models…</p>}
          {canList && models.isError && <p className={styles.note}>Model list unavailable: {models.error.message}</p>}
          {!canList && !providerChanged && (
            <p className={styles.note}>Add the API key to choose from the provider's models.</p>
          )}
        </div>
      </div>
      {keyLocked && <p className={styles.hint}>{keyLockedGuidance}</p>}
      {typedModel && (
        <TextInput
          label="Model id"
          className={styles.mono}
          value={draft.model}
          placeholder="e.g. claude-sonnet-4-5"
          autoComplete="off"
          spellCheck={false}
          onChange={(event) => edit({ model: event.target.value })}
        />
      )}
      {providersWithBaseURL.has(draft.provider) && (
        <TextInput
          label="Base URL"
          type="url"
          className={styles.mono}
          value={draft.base_url}
          placeholder="https://"
          autoComplete="off"
          spellCheck={false}
          onChange={(event) => edit({ base_url: event.target.value })}
        />
      )}
      <APIKey saved={saved} draftProvider={draft.provider} />

      <div className={styles.temperature}>
        <div className={styles.temperatureHeader}>
          <span id={temperatureId} className={styles.label}>
            Creativity (temperature)
          </span>
          <output htmlFor={temperatureId} className={styles.value}>
            {draft.temperature.toFixed(1)}
          </output>
        </div>
        <Slider
          aria-labelledby={temperatureId}
          aria-valuetext={`${draft.temperature.toFixed(1)}, from Familiar (0) to Adventurous (1)`}
          value={draft.temperature}
          min={0}
          max={1}
          step={0.1}
          onChange={(event) => edit({ temperature: Math.round(Number(event.target.value) * 10) / 10 })}
        />
        <div className={styles.ends} aria-hidden="true">
          <span>Familiar</span>
          <span>Adventurous</span>
        </div>
      </div>

      <PromptField value={draft.system_prompt} onChange={(system_prompt) => edit({ system_prompt })} />

      <div className={styles.status} role="status">
        {save.isSuccess && !dirty && 'Changes saved.'}
      </div>
      {save.isError && <p className={styles.error} role="alert">{saveErrorMessage(save.error)}</p>}
      {resetPrompt.isError && !confirmingReset && (
        <p className={styles.error} role="alert">
          The system prompt was not reset: {resetPrompt.error.message}
        </p>
      )}

      <div className={styles.actions}>
        <Button variant="outline" className={styles.reset} onClick={() => setConfirmingReset(true)}>
          Reset
        </Button>
        <Button className={styles.save} disabled={!dirty || save.isPending} onClick={onSave}>
          {save.isPending ? 'Saving…' : 'Save changes'}
        </Button>
      </div>

      <Confirm
        open={confirmingReset}
        title="Reset the system prompt?"
        confirmLabel={resetPrompt.isPending ? 'Resetting…' : 'Reset prompt'}
        busy={resetPrompt.isPending}
        error={resetPrompt.isError ? `The system prompt was not reset: ${resetPrompt.error.message}` : null}
        onCancel={() => setConfirmingReset(false)}
        onConfirm={onConfirmReset}
      >
        This restores MealQuest's default system prompt for everyone in the household.
      </Confirm>
    </>
  )
}

// formatVerifiedDate turns key_verified_at into the handoff's "Oct 1".
export function formatVerifiedDate(timestamp: string): string {
  const date = new Date(timestamp)
  if (Number.isNaN(date.getTime())) throw new Error(`key_verified_at "${timestamp}" is not a timestamp`)
  return new Intl.DateTimeFormat('en-US', { month: 'short', day: 'numeric' }).format(date)
}

// keyLockedGuidance is how to change the provider or base URL while a key
// is stored.
const keyLockedGuidance =
  'An API key is stored. Remove it (Replace → Remove key) before changing the provider or base URL.'

// APIKey stays mounted whatever provider the draft names, so a key stored
// (and the verify that follows it) completes even if the household changes
// the provider meanwhile, and a stored key can always be removed.
function APIKey({ saved, draftProvider }: { saved: AISettings; draftProvider: AIProvider }) {
  const [replacing, setReplacing] = useState(false)
  const [key, setKey] = useState('')
  const store = useSetAIKey()
  const verify = useVerifyAIKey()
  const labelId = useId()
  const sheetTitleId = useId()
  const last4 = saved.api_key_last4
  const providerChanged = draftProvider !== saved.provider

  function open() {
    store.reset()
    setReplacing(true)
  }

  // close keeps the store mutation: a key already sent is still stored and
  // verified after the sheet is dismissed.
  function close() {
    setReplacing(false)
    setKey('')
  }

  function onStore() {
    verify.reset()
    store.mutate(key, {
      onSuccess: () => {
        close()
        verify.mutate(undefined)
      },
    })
  }

  function onRemove() {
    verify.reset()
    store.mutate('', { onSuccess: close })
  }

  const storeError = store.isError && (
    <p className={styles.error} role="alert">
      The key was not stored: {store.error.message}
    </p>
  )

  if (last4 === null && aiKeyPolicy[draftProvider] === 'none') {
    return (
      <div className={styles.key}>
        <p className={styles.hint}>{aiProviderLabels[draftProvider]} takes no API key.</p>
        {!replacing && storeError}
      </div>
    )
  }

  return (
    <div className={styles.key}>
      <span id={labelId} className={styles.label}>
        API key
      </span>
      <div className={styles.keyRow} role="group" aria-labelledby={labelId}>
        <span className={styles.keyValue}>{last4 === null ? 'No key stored' : maskedKey(last4)}</span>
        <button
          type="button"
          className={styles.replace}
          disabled={store.isPending || (last4 === null && providerChanged)}
          onClick={open}
        >
          {store.isPending ? 'Saving…' : last4 === null ? 'Add key' : 'Replace'}
        </button>
      </div>
      {last4 === null && providerChanged && (
        <p className={styles.note}>Save the provider change before adding its key.</p>
      )}
      {!replacing && storeError}
      <KeyStatus saved={saved} verify={verify} />

      <Sheet open={replacing} onClose={close} aria-labelledby={sheetTitleId}>
        <form
          className={styles.sheet}
          onSubmit={(event) => {
            event.preventDefault()
            onStore()
          }}
        >
          <h2 id={sheetTitleId} className={styles.sheetTitle}>
            {last4 === null ? 'Add API key' : 'Replace API key'}
          </h2>
          <p className={styles.sheetText}>
            The key is stored on the server for {aiProviderLabels[saved.provider]}. This device only ever sees its
            last four characters.
          </p>
          <TextInput
            label="API key"
            type="password"
            className={styles.mono}
            value={key}
            autoComplete="off"
            spellCheck={false}
            onChange={(event) => setKey(event.target.value)}
          />
          {storeError}
          <div className={styles.sheetActions}>
            {last4 !== null && (
              <Button variant="outline" disabled={store.isPending} onClick={onRemove}>
                Remove key
              </Button>
            )}
            <Button type="submit" disabled={key === '' || store.isPending}>
              {store.isPending ? 'Saving…' : 'Save and verify'}
            </Button>
          </div>
        </form>
      </Sheet>
    </div>
  )
}

type KeyStatusProps = {
  saved: AISettings
  verify: ReturnType<typeof useVerifyAIKey>
}

// KeyStatus is the line under the key: verifying, the verify error, when it
// was verified, or that it has not been.
function KeyStatus({ saved, verify }: KeyStatusProps) {
  if (saved.api_key_last4 === null) return null
  if (verify.isPending) return <p className={styles.note}>Verifying…</p>
  if (verify.isError) {
    return (
      <div className={styles.keyStatus}>
        <p className={styles.error} role="alert">
          {verifyErrorMessage(verify.error)}
        </p>
        <VerifyButton verify={verify}>Try again</VerifyButton>
      </div>
    )
  }
  if (saved.key_verified_at !== null) {
    return <p className={styles.verified}>Verified · {formatVerifiedDate(saved.key_verified_at)}</p>
  }
  return (
    <div className={styles.keyStatus}>
      <p className={styles.note}>Not verified yet</p>
      <VerifyButton verify={verify}>Verify</VerifyButton>
    </div>
  )
}

function VerifyButton({ verify, children }: { verify: KeyStatusProps['verify']; children: ReactNode }) {
  return (
    <button type="button" className={styles.link} onClick={() => verify.mutate(undefined)}>
      {children}
    </button>
  )
}

// verifyErrorMessage says why POST /settings/ai/verify failed, by the
// error code the server names (internal/server/aisettings.go).
export function verifyErrorMessage(error: Error): string {
  if (!(error instanceof ApiError)) return `Verification failed: ${error.message}`
  switch (error.code) {
    case 'auth_failed':
      return 'Verification failed: the provider rejected this key.'
    case 'rate_limited':
      return 'Verification failed: the provider is rate limiting requests. Try again shortly.'
    case 'timeout':
      return 'Verification failed: the provider did not answer in time.'
    case 'settings_changed':
      return 'Verification failed: the settings changed while verifying. Try again.'
    default:
      return `Verification failed: ${error.message}`
  }
}

// saveErrorMessage says why a save failed. The server refuses to move a
// stored key to another provider or base URL; it names how to fix that.
function saveErrorMessage(error: Error): string {
  if (error instanceof ApiError && error.code === 'invalid_settings') {
    const guidance = error.message.includes('while an API key is stored') ? ` ${keyLockedGuidance}` : ''
    return `Not saved: ${error.message}.${guidance}`
  }
  return `Not saved: ${error.message}`
}

function PromptField({ value, onChange }: { value: string; onChange: (value: string) => void }) {
  const id = useId()
  return (
    <div className={styles.prompt}>
      <label htmlFor={id} className={styles.label}>
        System prompt
      </label>
      <textarea
        id={id}
        className={styles.textarea}
        value={value}
        rows={8}
        spellCheck={false}
        onChange={(event) => onChange(event.target.value)}
      />
    </div>
  )
}
