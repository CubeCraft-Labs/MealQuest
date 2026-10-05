import { act, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { SKIP_WAITING, UpdatePrompt, type UpdateContainer } from './UpdatePrompt.tsx'

// FakeWorker is a ServiceWorker whose state the test moves along.
class FakeWorker extends EventTarget {
  state: ServiceWorkerState = 'installing'
  postMessage = vi.fn()
  setState(state: ServiceWorkerState) {
    this.state = state
    this.dispatchEvent(new Event('statechange'))
  }
}

class FakeRegistration extends EventTarget {
  installing: FakeWorker | null = null
  waiting: FakeWorker | null = null
  // install starts a new worker, as the browser does on finding an update.
  install(): FakeWorker {
    const worker = new FakeWorker()
    this.installing = worker
    this.dispatchEvent(new Event('updatefound'))
    return worker
  }
  // finishInstall moves the installing worker to waiting.
  finishInstall(worker: FakeWorker) {
    this.installing = null
    this.waiting = worker
    worker.setState('installed')
  }
}

class FakeContainer extends EventTarget {
  controller: object | null = {}
  registration = new FakeRegistration()
  ready = Promise.resolve(this.registration)
  // takeOver makes the waiting worker the controller.
  takeOver() {
    const worker = this.registration.waiting
    this.registration.waiting = null
    this.controller = worker
    worker?.setState('activated')
    this.dispatchEvent(new Event('controllerchange'))
  }
}

async function renderPrompt(container: FakeContainer | undefined) {
  const reload = vi.fn()
  const view = render(<UpdatePrompt container={container as unknown as UpdateContainer} reload={reload} />)
  await act(async () => {
    await container?.ready
  })
  return { reload, ...view }
}

const copy = 'New version ready · Reload'

describe('UpdatePrompt', () => {
  it('shows nothing without a service worker', async () => {
    await renderPrompt(undefined)
    expect(screen.queryByRole('status')).toBeNull()
  })

  it('shows nothing while no worker is waiting', async () => {
    const container = new FakeContainer()
    await renderPrompt(container)
    act(() => {
      container.registration.install()
    })
    expect(screen.queryByRole('status')).toBeNull()
  })

  it('shows nothing for a waiting worker when the page has no controller', async () => {
    const container = new FakeContainer()
    container.controller = null
    container.registration.waiting = new FakeWorker()
    await renderPrompt(container)
    expect(screen.queryByRole('status')).toBeNull()
  })

  it('shows the toast for a worker already waiting when the page loads', async () => {
    const container = new FakeContainer()
    container.registration.waiting = new FakeWorker()
    await renderPrompt(container)
    expect(screen.getByRole('status')).toHaveTextContent(copy)
  })

  it('shows the toast when an update finishes installing', async () => {
    const container = new FakeContainer()
    await renderPrompt(container)
    act(() => container.registration.finishInstall(container.registration.install()))
    expect(screen.getByRole('status')).toHaveTextContent(copy)
  })

  it('sends SKIP_WAITING on Reload and reloads once the new worker controls the page', async () => {
    const container = new FakeContainer()
    const { reload } = await renderPrompt(container)
    const worker = container.registration.install()
    act(() => container.registration.finishInstall(worker))

    act(() => screen.getByRole('button', { name: 'Reload' }).click())
    expect(worker.postMessage).toHaveBeenCalledExactlyOnceWith(SKIP_WAITING)
    expect(screen.queryByRole('status')).toBeNull()
    expect(reload).not.toHaveBeenCalled()

    act(() => container.takeOver())
    expect(reload).toHaveBeenCalledOnce()
    // A later controller change (another tab's update) does not reload again.
    act(() => container.dispatchEvent(new Event('controllerchange')))
    expect(reload).toHaveBeenCalledOnce()
    expect(screen.queryByRole('status')).toBeNull()
  })

  it('does not reappear for the worker the household chose to reload into', async () => {
    const container = new FakeContainer()
    container.registration.waiting = new FakeWorker()
    await renderPrompt(container)
    act(() => screen.getByRole('button', { name: 'Reload' }).click())
    // The worker reports another state change before it takes over.
    act(() => container.registration.waiting?.setState('installed'))
    expect(screen.queryByRole('status')).toBeNull()
  })

  it('hides when dismissed until the next update is waiting', async () => {
    const container = new FakeContainer()
    const { reload } = await renderPrompt(container)
    const first = container.registration.install()
    act(() => container.registration.finishInstall(first))

    act(() => screen.getByRole('button', { name: 'Dismiss' }).click())
    expect(screen.queryByRole('status')).toBeNull()
    expect(first.postMessage).not.toHaveBeenCalled()
    act(() => first.setState('installed'))
    expect(screen.queryByRole('status')).toBeNull()

    act(() => container.registration.finishInstall(container.registration.install()))
    expect(screen.getByRole('status')).toHaveTextContent(copy)
    expect(reload).not.toHaveBeenCalled()
  })

  it('hides when another tab activates the waiting worker', async () => {
    const container = new FakeContainer()
    container.registration.waiting = new FakeWorker()
    const { reload } = await renderPrompt(container)
    act(() => container.takeOver())
    expect(screen.queryByRole('status')).toBeNull()
    expect(reload).not.toHaveBeenCalled()
  })

  it('removes its listeners on unmount', async () => {
    const container = new FakeContainer()
    const worker = new FakeWorker()
    container.registration.waiting = worker
    const { reload, unmount } = await renderPrompt(container)
    const removeRegistration = vi.spyOn(container.registration, 'removeEventListener')
    const removeWorker = vi.spyOn(worker, 'removeEventListener')
    act(() => screen.getByRole('button', { name: 'Reload' }).click())

    unmount()
    expect(removeRegistration).toHaveBeenCalledWith('updatefound', expect.any(Function))
    expect(removeWorker).toHaveBeenCalledWith('statechange', expect.any(Function))
    container.takeOver()
    expect(reload).not.toHaveBeenCalled()
  })
})
