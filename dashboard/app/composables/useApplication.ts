import type { ComputedRef, InjectionKey, Ref } from 'vue'
import type { AgentEvent, ApplicationDetail, AppSpec, Deployment } from '~/types/api'
import type { DeploymentProgress } from '~/utils/deploymentProgress'

type Polling<T> = ReturnType<typeof usePolling<T>>

/**
 * What the application page knows, shared with its tabs. The page polls the
 * application, its deployments and its events once, whichever tab is open, so
 * a tab never asks for what the header already has and the actions in the
 * header act on the same state the tab shows.
 */
export interface ApplicationContext {
  name: ComputedRef<string>
  /** The application as last answered. The tabs are only rendered once it has been. */
  detail: ComputedRef<ApplicationDetail>
  spec: ComputedRef<AppSpec | null>
  app: Polling<ApplicationDetail>
  deployments: Polling<Deployment[]>
  events: Polling<AgentEvent[]>
  /** The agent answered 404 after it had answered before: deleted elsewhere. */
  gone: ComputedRef<boolean>
  hasActive: ComputedRef<boolean>
  /** A folder served by the proxy: no containers, so nothing asks for logs, metrics or jobs. */
  isStatic: ComputedRef<boolean>
  /** A deployment or another operation holds the application. */
  busy: ComputedRef<boolean>
  /** Stopped on request and nothing is running any more: what a restore needs. */
  stoppedNow: ComputedRef<boolean>
  /** The token may deploy, stop, start, run and back up this application: its role, and for a limited token its list. */
  mayDeploy: ComputedRef<boolean>
  /** Why it may not, for the tooltip of a control that is off; undefined when it may. */
  deployHint: ComputedRef<string | undefined>
  mayAdmin: ComputedRef<boolean>
  /** The deployment being followed, or the one that just finished and has not been dismissed. */
  progress: Readonly<Ref<DeploymentProgress | null>>
  dismissProgress: () => void
  refreshAll: () => void
  setRunning: (run: boolean) => Promise<void>
}

const KEY: InjectionKey<ApplicationContext> = Symbol('shipwick:application')

export function provideApplication(context: ApplicationContext): void {
  provide(KEY, context)
}

export function useApplication(): ApplicationContext {
  const context = inject(KEY, null)
  if (!context) throw new Error('useApplication() must be used on a tab of the application page')
  return context
}
