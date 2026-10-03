import type { Deployment, DeploymentDetail } from '~/types/api'
import { isAbortError, toAgentError } from '~/utils/agentError'
import type { DeploymentProgress, ProgressAction } from '~/utils/deploymentProgress'
import { progressReducer } from '~/utils/deploymentProgress'

const POLL_INTERVAL_MS = 1000

/**
 * Follows one deployment until `completed_at` is set, the way `shipwick
 * deploy` does: poll GET /deployments/:id and narrate its events. It does not
 * stop at FAILED: a rollback may follow, and only `completed_at` ends it.
 */
export function useDeploymentProgress(onFinished?: (progress: DeploymentProgress) => void) {
  const agent = useAgent()
  const progress = shallowRef<DeploymentProgress | null>(null)
  /** Id of the deployment being polled right now; null when idle or finished. */
  const followingId = ref<number | null>(null)

  let controller: AbortController | null = null
  let timer: ReturnType<typeof setTimeout> | null = null

  function dispatch(action: ProgressAction) {
    progress.value = progressReducer(progress.value, action)
  }

  function stop() {
    if (timer !== null) {
      clearTimeout(timer)
      timer = null
    }
    controller?.abort()
    controller = null
    followingId.value = null
  }

  function finish(own: AbortController) {
    if (controller !== own) return
    controller = null
    followingId.value = null
  }

  async function poll(id: number, own: AbortController) {
    let wait = POLL_INTERVAL_MS
    try {
      const detail = await agent.get<DeploymentDetail>(`/deployments/${id}`, { signal: own.signal })
      if (own.signal.aborted) return
      dispatch({ type: 'polled', detail })
      if (detail.completed_at !== null) {
        finish(own)
        if (progress.value) onFinished?.(progress.value)
        return
      }
    }
    catch (error) {
      if (own.signal.aborted || isAbortError(error)) return
      const failure = toAgentError(error)
      dispatch({ type: 'poll_failed', message: failure.displayMessage, unreachable: failure.unreachable || failure.code === 'RUNTIME_UNAVAILABLE' })
      // Gone (application deleted) or signed out: nothing more will come.
      if (failure.status === 404 || failure.status === 401) {
        finish(own)
        return
      }
      // Anything else is waited out, an agent that is restarting included: the
      // deployment is still in flight there and resumes when the agent is back.
      wait = Math.max(wait, failure.retryAfterMs)
    }
    timer = setTimeout(() => void poll(id, own), wait)
  }

  function begin(id: number) {
    const own = new AbortController()
    controller = own
    followingId.value = id
    void poll(id, own)
  }

  /** Follows a deployment this page just started; the panel appears at once. */
  function follow(deployment: Deployment) {
    stop()
    dispatch({ type: 'dismissed' })
    dispatch({ type: 'started', deployment })
    begin(deployment.id)
  }

  /**
   * Follows a deployment known only by id (Application.in_flight_deployment_id):
   * one started from the CLI, from CI or from another tab. The panel appears
   * with the first poll.
   */
  function followId(id: number) {
    if (followingId.value === id) return
    stop()
    dispatch({ type: 'dismissed' })
    begin(id)
  }

  function dismiss() {
    stop()
    dispatch({ type: 'dismissed' })
  }

  const active = computed(() => followingId.value !== null || progress.value?.phase === 'running')

  onScopeDispose(stop)

  return { progress, active, followingId, follow, followId, dismiss }
}
