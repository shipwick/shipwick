<script setup lang="ts">
import type { AgentError } from '~/utils/agentError'
import { forbiddenExplanation, isRole } from '~/utils/roles'

/** The result of a failed action, shown where the action was taken. */
const props = defineProps<{ error: AgentError | null }>()

// A 403 is explained from its details ("This token has the read role; deploying
// needs deploy or admin"), which reads better than the agent's lowercase line.
const headline = computed(() => (props.error ? forbiddenExplanation(props.error) || props.error.displayMessage : ''))

const hint = computed(() => {
  const error = props.error
  if (!error) return ''
  // ENDPOINT_NOT_FOUND is the agent saying "I have no such operation", as opposed
  // to NOT_FOUND, "no such application or deployment".
  if (error.code === 'ENDPOINT_NOT_FOUND') return 'This agent version does not support this action. Upgrade the agent.'
  if (error.code === 'DEPLOYMENT_IN_PROGRESS') return 'Wait for the running operation to finish, then try again.'
  if (error.code === 'NO_ROLLBACK_TARGET') return 'Only earlier deployments that served successfully can be rolled back to. The list has been refreshed.'
  if (error.code === 'FORBIDDEN') {
    const required = error.details.required
    return isRole(required) ? `Sign in with a token that has the ${required} role, or ask an admin for one.` : 'Sign in with a token whose role allows this.'
  }
  if (error.code === 'APPLICATION_RUNNING') return 'Stop the application first.'
  if (error.code === 'JOB_ALREADY_RUNNING') return 'A run of this job has not finished yet; a job runs one at a time. Open it in the run history to follow it.'
  if (error.code === 'TOKEN_EXISTS') return 'Choose another name, or revoke the existing token first.'
  if (error.code === 'STATIC_APPLICATION') return 'The files are served by the proxy as they are; there is nothing to read logs or metrics from and nothing to run a command in.'
  if (error.code === 'VOLUME_IN_USE') return 'Only volumes of deleted applications can be removed here. To replace the data of a running application, restore a backup on its page.'
  if (error.unreachable && error.agentUrl) return `Tried ${error.agentUrl}`
  return ''
})
</script>

<template>
  <div v-if="props.error" class="rounded-sm border border-danger-line bg-danger-bg px-3 py-2 text-xs text-danger" role="alert">
    <p class="font-medium">
      {{ headline }}
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
