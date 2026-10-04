<script setup lang="ts">
import type { AgentError } from '~/utils/agentError'
import { failureTitle } from '~/utils/agentError'

/** Full-size error for a page or panel that has nothing to show. */
const props = withDefaults(defineProps<{
  error: AgentError
  /** What failed to load, e.g. "applications". */
  subject?: string
  retrying?: boolean
}>(), { subject: 'data', retrying: false })

const emit = defineEmits<{ retry: [] }>()

const title = computed(() => failureTitle(props.error, props.subject))

const explanation = computed(() => {
  if (props.error.unreachable) {
    return 'The dashboard server could not connect to the Shipwick agent. Check that the agent is running and that SHIPWICK_AGENT_URL points to it.'
  }
  if (props.error.code === 'NETWORK') return 'The browser could not reach the dashboard server. Check your connection.'
  return props.error.displayMessage
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
      <!-- Not a heading: the state stands under a page's title on one page and inside a panel on another, and is announced as an alert on both. -->
      <p class="text-base font-semibold">
        {{ title }}
      </p>
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
