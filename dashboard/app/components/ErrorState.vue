<script setup lang="ts">
import type { AgentError } from '~/utils/agentError'

/** Full-size error for a page or panel that has nothing to show. */
const props = withDefaults(defineProps<{
  error: AgentError
  /** What failed to load, e.g. "applications". */
  subject?: string
  retrying?: boolean
}>(), { subject: 'data', retrying: false })

const emit = defineEmits<{ retry: [] }>()

const title = computed(() => {
  if (props.error.unreachable) return 'Agent unreachable'
  if (props.error.code === 'NETWORK') return 'Dashboard server unreachable'
  if (props.error.notFound) return 'Not found'
  return `Could not load ${props.subject}`
})

const explanation = computed(() => {
  if (props.error.unreachable) {
    return 'The dashboard server could not connect to the Shipwick agent. Check that the agent is running and that SHIPWICK_AGENT_URL points to it.'
  }
  if (props.error.code === 'NETWORK') return 'The browser could not reach the dashboard server. Check your connection.'
  return props.error.message
})

const cause = computed(() => {
  const value = props.error.details.cause
  return typeof value === 'string' ? value : ''
})
</script>

<template>
  <div class="flex flex-col items-start gap-3 px-4 py-8 sm:px-6" role="alert">
    <div class="flex items-center gap-2 text-danger">
      <UiIcon name="alert" />
      <h3 class="text-base font-semibold">
        {{ title }}
      </h3>
    </div>
    <p class="max-w-prose text-fg-muted">
      {{ explanation }}
    </p>
    <dl v-if="props.error.agentUrl || cause" class="grid grid-cols-[auto_1fr] items-baseline gap-x-4 gap-y-1 text-xs">
      <template v-if="props.error.agentUrl">
        <dt class="label">
          Tried
        </dt>
        <dd class="mono break-all">
          {{ props.error.agentUrl }}
        </dd>
      </template>
      <template v-if="cause">
        <dt class="label">
          Cause
        </dt>
        <dd class="mono break-all">
          {{ cause }}
        </dd>
      </template>
    </dl>
    <UiButton size="sm" :pending="props.retrying" @click="emit('retry')">
      <UiIcon name="refresh" :size="14" />
      Retry
    </UiButton>
  </div>
</template>
