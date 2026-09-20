<script setup lang="ts">
import type { ApplicationDetail, Deployment, RollbackRequest } from '~/types/api'
import type { AgentError } from '~/utils/agentError'
import { toAgentError } from '~/utils/agentError'
import { deploymentOrigin, formatOrigin, rollbackCandidates } from '~/utils/deployments'

const props = defineProps<{
  open: boolean
  application: ApplicationDetail
  /** The application's deployment history, newest first. */
  deployments: Deployment[]
}>()
const emit = defineEmits<{
  close: []
  started: [deployment: Deployment]
  /** The agent refused the chosen target: the history on screen is out of date. */
  stale: []
}>()

const agent = useAgent()
const selected = ref<number | null>(null)
const pending = ref(false)
const error = shallowRef<AgentError | null>(null)

/** Exactly what the agent accepts: this application's SUPERSEDED deployments. */
const candidates = computed(() => rollbackCandidates(props.deployments, props.application.name))

watch(() => props.open, (open) => {
  if (!open) return
  selected.value = candidates.value[0]?.id ?? null
  error.value = null
})

// The list is live: if the chosen target disappears from it, fall back to the newest.
watch(candidates, (list) => {
  if (selected.value !== null && !list.some(d => d.id === selected.value)) selected.value = list[0]?.id ?? null
})

const target = computed(() => candidates.value.find(d => d.id === selected.value) ?? null)

function originOf(d: Deployment): string {
  const origin = deploymentOrigin(d, props.deployments)
  return origin ? formatOrigin(origin) : ''
}

async function submit() {
  if (pending.value || !target.value) return
  pending.value = true
  error.value = null
  try {
    const body: RollbackRequest = { deployment_id: target.value.id }
    const deployment = await agent.post<Deployment>(`/applications/${encodeURIComponent(props.application.name)}/rollback`, { body })
    emit('started', deployment)
    emit('close')
  }
  catch (cause) {
    error.value = toAgentError(cause)
    if (error.value.code === 'NO_ROLLBACK_TARGET') emit('stale')
  }
  finally {
    pending.value = false
  }
}
</script>

<template>
  <UiDialog :open="props.open" :title="`Roll back ${props.application.name}`" size="md" :busy="pending" @close="emit('close')">
    <form id="rollback-form" class="space-y-4" @submit.prevent="submit">
      <template v-if="candidates.length > 0">
        <p class="text-fg-muted">
          Currently running <span class="mono text-fg">{{ props.application.version || '—' }}</span>. Choose the deployment to return to. Its whole stored configuration is deployed again, not just the image: replica by replica, health-checked, and undone if it does not come up.
        </p>
        <fieldset class="max-h-72 overflow-y-auto rounded-sm border border-line">
          <legend class="sr-only">
            Earlier successful deployments
          </legend>
          <label
            v-for="(d, index) in candidates"
            :key="d.id"
            class="flex cursor-pointer items-center gap-3 border-b border-line px-3 py-2 last:border-b-0 hover:bg-hover has-[:checked]:bg-active"
          >
            <input v-model="selected" type="radio" name="rollback-target" :value="d.id" class="accent-[var(--fg)]" :autofocus="index === 0">
            <span class="mono w-10 shrink-0 text-fg-muted">#{{ d.sequence }}</span>
            <span class="mono min-w-0 truncate font-medium" :title="d.image">{{ d.version }}</span>
            <span class="min-w-0 flex-1 truncate text-xs text-fg-subtle">{{ originOf(d) }}</span>
            <span v-if="index === 0" class="label max-sm:hidden">Previous version</span>
            <TimeAgo :time="d.started_at" class="w-16 shrink-0 text-right text-xs text-fg-muted" />
          </label>
        </fieldset>
        <p v-if="target" class="mono break-all text-xs text-fg-muted">
          {{ target.image }}
        </p>
      </template>
      <p v-else class="text-fg-muted">
        There is no earlier successful deployment of <span class="mono text-fg">{{ props.application.name }}</span> to roll back to. Failed attempts are not versions to return to.
      </p>
      <InlineError :error="error" />
    </form>
    <template #footer>
      <UiButton :disabled="pending" @click="emit('close')">
        Cancel
      </UiButton>
      <UiButton type="submit" form="rollback-form" variant="primary" :pending="pending" :disabled="!target">
        Roll back<template v-if="target">
          to {{ target.version }}
        </template>
      </UiButton>
    </template>
  </UiDialog>
</template>
