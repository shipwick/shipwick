<script setup lang="ts">
import type { AgentError } from '~/utils/agentError'

/** The result of a failed action, shown where the action was taken. */
const props = defineProps<{ error: AgentError | null }>()

const hint = computed(() => {
  const error = props.error
  if (!error) return ''
  // ENDPOINT_NOT_FOUND is the agent saying "I have no such operation", as opposed
  // to NOT_FOUND, "no such application or deployment".
  if (error.code === 'ENDPOINT_NOT_FOUND') return 'This agent version does not support this action. Upgrade the agent.'
  if (error.code === 'DEPLOYMENT_IN_PROGRESS') return 'Wait for the running operation to finish, then try again.'
  if (error.code === 'NO_ROLLBACK_TARGET') return 'Only earlier deployments that served successfully can be rolled back to. The list has been refreshed.'
  if (error.unreachable && error.agentUrl) return `Tried ${error.agentUrl}`
  return ''
})
</script>

<template>
  <div v-if="props.error" class="rounded-sm border border-danger-line bg-danger-bg px-3 py-2 text-xs text-danger" role="alert">
    <p class="font-medium">
      {{ props.error.message }}
    </p>
    <ul v-if="props.error.fields.length > 0" class="mt-1 space-y-0.5">
      <li v-for="field in props.error.fields" :key="field.field" class="mono">
        {{ field.field }}: {{ field.message }}<template v-if="field.expected">
          (expected: {{ field.expected }})
        </template>
      </li>
    </ul>
    <p v-if="hint" class="mt-0.5">
      {{ hint }}
    </p>
  </div>
</template>
