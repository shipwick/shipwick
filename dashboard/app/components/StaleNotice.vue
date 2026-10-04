<script setup lang="ts">
import type { AgentError } from '~/utils/agentError'
import { formatRelativeTime } from '~/utils/format'

/**
 * Shown above data that could not be refreshed: the last good data stays on
 * screen (no layout jump), and this line says it is no longer live.
 */
const props = defineProps<{ error: AgentError | null, updatedAt: number | null }>()

const now = useNow()
const age = computed(() => (props.updatedAt === null ? '' : formatRelativeTime(new Date(props.updatedAt).toISOString(), now.value)))

// Said once when the data goes stale and once when it is live again. The
// notice counts the seconds: as a live region it would be read at every one.
const { announce } = useAnnounce()
watch(() => (props.error ? (props.error.unreachable ? 'unreachable' : props.error.dockerSilent ? 'docker' : 'failed') : null), (state, before) => {
  if (state === 'unreachable') announce('Agent unreachable. Showing the last data, retrying.')
  else if (state === 'docker') announce('Docker does not answer on the server. Showing the last data, retrying.')
  else if (state === 'failed') announce('Refresh failed. Showing the last data, retrying.')
  else if (before) announce('Connected again: the data is live.')
}, { immediate: true })
</script>

<template>
  <div
    v-if="props.error"
    class="mb-4 flex flex-wrap items-center gap-x-2 gap-y-1 rounded-sm border border-warn-line bg-warn-bg px-3 py-2 text-xs text-warn"
  >
    <UiIcon name="alert" :size="14" />
    <span class="font-medium">{{ props.error.unreachable ? 'Agent unreachable.' : 'Refresh failed.' }}</span>
    <span>
      Showing data from <span class="mono">{{ age }}</span>, retrying.
      <span v-if="props.error.agentUrl" class="mono">{{ props.error.agentUrl }}</span>
      <span v-else>{{ props.error.displayMessage }}</span>
    </span>
  </div>
</template>
