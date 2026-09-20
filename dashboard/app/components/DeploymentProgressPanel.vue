<script setup lang="ts">
import { durationBetween, formatDuration } from '~/utils/format'
import type { DeploymentProgress } from '~/utils/deploymentProgress'
import type { ApplicationStatus } from '~/types/api'

/**
 * Live narration of one deployment, in the CLI's voice:
 *
 *   ✓ Pulled image ghcr.io/acme/my-api:1.5.0
 *   ✓ Replica 1/2 is serving 1.5.0; its 1.4.2 predecessor is retired
 *   ◌ Waiting for the replica to become healthy
 *
 * Three endings: deployed (green), failed with the previous version untouched
 * (red), and failed part-way but rolled back (amber: handled, never a success).
 */
const props = withDefaults(defineProps<{
  progress: DeploymentProgress
  /** What the application is running now, e.g. "1.4.2": after a failure, the version that still (or again) serves. */
  stillRunning?: string
  /**
   * The application's status right now. After a failure the panel says what is
   * true, not what usually is: a rollback can fail too, and then "the failed
   * deployment did not affect it" would be a lie.
   */
  appStatus?: ApplicationStatus
  dismissible?: boolean
}>(), { stillRunning: '', appStatus: undefined, dismissible: true })

/** Over, not a success, and the application is not fine: say so instead of reassuring. */
const impaired = computed(() => (props.progress.phase === 'failed' || props.progress.phase === 'rolled_back')
  && props.appStatus !== undefined && props.appStatus !== 'HEALTHY' && props.appStatus !== 'STOPPED')

const emit = defineEmits<{ dismiss: [] }>()

const now = useNow()

const elapsed = computed(() => {
  const p = props.progress
  if (p.completedAt) return formatDuration(durationBetween(p.startedAt, p.completedAt))
  const started = Date.parse(p.startedAt)
  return Number.isNaN(started) ? '' : formatDuration(Math.max(0, Math.floor((now.value - started) / 1000) * 1000))
})

/** A failure is known before the deployment is over: cleanup or a rollback is still running. */
const failing = computed(() => props.progress.phase === 'running' && props.progress.error !== '')
const rollingBack = computed(() => props.progress.phase === 'running'
  && (props.progress.status === 'ROLLBACK' || props.progress.status === 'RESTORING' || props.progress.status === 'ROLLED_BACK'))

const headline = computed(() => {
  const p = props.progress
  switch (p.phase) {
    case 'succeeded': return `Deployed ${p.version} in ${elapsed.value}`
    case 'failed': return 'Deployment failed'
    case 'rolled_back': return props.stillRunning ? `Failed — rolled back to ${props.stillRunning}` : 'Failed — rolled back'
    default:
      if (rollingBack.value) return `Deployment of ${p.version} failed — rolling back`
      if (failing.value) return `Deployment of ${p.version} failed — cleaning up`
      return `Deploying ${p.version}`
  }
})

const tone = computed(() => {
  const p = props.progress
  if (p.phase === 'succeeded') return 'ok'
  if (p.phase === 'failed') return 'danger'
  if (p.phase === 'rolled_back') return 'warn'
  return failing.value ? 'danger' : 'warn'
})

const TONE_TEXT = { ok: 'text-ok', warn: 'text-warn', danger: 'text-danger' } as const
const TONE_FRAME = { ok: 'border-line', warn: 'border-line', danger: 'border-danger-line' } as const
</script>

<template>
  <section
    class="overflow-hidden rounded-sm border bg-bg"
    :class="progress.phase === 'rolled_back' ? 'border-warn-line' : TONE_FRAME[tone]"
    aria-live="polite"
  >
    <header class="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 border-b border-line bg-subtle px-4 py-2">
      <div class="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1">
        <span class="inline-flex items-center gap-1.5 font-medium" :class="TONE_TEXT[tone]">
          <UiIcon v-if="progress.phase === 'succeeded'" name="check" :size="14" />
          <UiIcon v-else-if="progress.phase === 'failed'" name="x-circle" :size="14" />
          <UiIcon v-else-if="progress.phase === 'rolled_back'" name="alert" :size="14" />
          <svg v-else class="spinner" width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" aria-hidden="true">
            <path d="M8 2a6 6 0 1 1-6 6" />
          </svg>
          {{ headline }}
        </span>
        <NuxtLink :to="`/deployments/${progress.deploymentId}`" class="mono link text-xs text-fg-muted">#{{ progress.sequence }}</NuxtLink>
        <span v-if="progress.phase === 'running'" class="mono text-xs text-fg-subtle">{{ elapsed }}</span>
      </div>
      <UiButton v-if="props.dismissible && progress.phase !== 'running'" variant="ghost" size="sm" @click="emit('dismiss')">
        Dismiss
      </UiButton>
    </header>

    <div class="px-4 py-3">
      <ol class="space-y-1">
        <li v-for="step in progress.steps" :key="step.id" class="flex items-start gap-2">
          <span class="mt-[3px] shrink-0" :class="{ 'text-ok': step.kind === 'done', 'text-warn': step.kind === 'warning', 'text-danger': step.kind === 'error' }">
            <UiIcon :name="step.kind === 'done' ? 'check' : step.kind === 'warning' ? 'alert' : 'x-circle'" :size="14" />
          </span>
          <span class="min-w-0 break-words" :class="{ 'text-warn': step.kind === 'warning', 'text-danger': step.kind === 'error' }">
            <span v-if="step.kind === 'warning'" class="sr-only">Warning: </span>{{ step.message }}
          </span>
        </li>
        <li v-if="progress.activity" class="flex items-start gap-2 text-fg-muted">
          <span class="mt-[3px] shrink-0">
            <svg class="spinner" width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" aria-hidden="true">
              <path d="M8 2a6 6 0 1 1-6 6" />
            </svg>
          </span>
          <span>{{ progress.activity }}…</span>
        </li>
      </ol>

      <div v-if="progress.error || progress.phase === 'failed' || progress.phase === 'rolled_back'" class="mt-3 border-t border-line pt-3">
        <p class="flex items-start gap-2 font-medium text-danger">
          <UiIcon name="x-circle" :size="14" class="mt-[3px]" />
          <span class="min-w-0 break-words">{{ progress.error || 'Deployment failed' }}</span>
        </p>
        <template v-if="progress.logs.length > 0">
          <p class="label mt-3">
            Last output
          </p>
          <pre
            v-for="(log, index) in progress.logs"
            :key="index"
            class="mono mt-1.5 max-h-64 overflow-auto whitespace-pre rounded-sm border border-line bg-inset px-3 py-2 text-xs leading-[1.125rem]"
            tabindex="0"
          >{{ log }}</pre>
        </template>
        <p v-if="impaired" class="mt-3 text-warn">
          <span class="mono">{{ progress.application }}</span><template v-if="props.stillRunning"> is running <span class="mono">{{ props.stillRunning }}</span>, but</template> is <span class="mono">{{ props.appStatus }}</span> right now. Shipwick keeps trying to restore it; see the replicas and events below.
        </p>
        <p v-else-if="progress.phase === 'failed' && props.stillRunning" class="mt-3 text-fg-muted">
          <span class="mono text-fg">{{ progress.application }}</span> is still running <span class="mono text-fg">{{ props.stillRunning }}</span>; the failed deployment did not affect it.
        </p>
        <p v-else-if="progress.phase === 'rolled_back'" class="mt-3 text-fg-muted">
          Some replicas had already been replaced when it failed. They were restored from the previous deployment's stored configuration<template v-if="props.stillRunning">, and <span class="mono text-fg">{{ progress.application }}</span> is running <span class="mono text-fg">{{ props.stillRunning }}</span> again</template>.
        </p>
      </div>

      <p v-if="progress.pollError" class="mt-3 text-xs text-warn" role="status">
        Lost contact while watching this deployment ({{ progress.pollError }}). It continues on the server; retrying.
      </p>
    </div>
  </section>
</template>
