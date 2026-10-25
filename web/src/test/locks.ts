// FakeLocks is the browser's Web Locks for jsdom, which has none: one
// device's exclusive locks, shared by every "tab" (each its own copy of a
// module) a test opens. close is a tab closing while it holds a lock: the
// browser lets the lock go though its callback never finishes.
export class FakeLocks {
  private holders = new Map<string, symbol>()
  private waiting = new Map<string, Array<() => void>>()

  // held says whether some tab holds name.
  held(name: string): boolean {
    return this.holders.has(name)
  }

  // close lets name go as its holding tab closes.
  close(name: string) {
    if (!this.holders.has(name)) throw new Error(`no tab holds the lock ${name}`)
    this.release(name)
  }

  request<T>(name: string, ...rest: unknown[]): Promise<T> {
    if (rest.length !== 1 || typeof rest[0] !== 'function') throw new Error('FakeLocks takes only request(name, callback)')
    const callback = rest[0] as (lock: Lock) => Promise<T> | T
    return new Promise<T>((resolve, reject) => {
      const run = () => {
        const holder = Symbol(name)
        this.holders.set(name, holder)
        Promise.resolve()
          .then(() => callback({ name, mode: 'exclusive' }))
          .then(resolve, reject)
          .finally(() => {
            if (this.holders.get(name) === holder) this.release(name)
          })
      }
      if (this.holders.has(name)) this.queue(name).push(run)
      else run()
    })
  }

  private queue(name: string): Array<() => void> {
    let queue = this.waiting.get(name)
    if (queue === undefined) this.waiting.set(name, (queue = []))
    return queue
  }

  private release(name: string) {
    this.holders.delete(name)
    const next = this.waiting.get(name)?.shift()
    if (next !== undefined) next()
  }
}

// installLocks gives navigator a new FakeLocks, as each test starts.
export function installLocks(): FakeLocks {
  const locks = new FakeLocks()
  Object.defineProperty(navigator, 'locks', { value: locks, configurable: true })
  return locks
}

// deviceLocks is the FakeLocks this test's tabs share.
export function deviceLocks(): FakeLocks {
  if (!(navigator.locks instanceof FakeLocks)) throw new Error('navigator.locks is not the FakeLocks of src/test/setup.ts')
  return navigator.locks
}
