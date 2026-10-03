<script setup lang="ts">
import type { AgentError } from '~/utils/agentError'
import { limitedExplanation } from '~/utils/access'
import { loginFailedHint } from '~/utils/registries'
import { forbiddenExplanation, isRole } from '~/utils/roles'
import { missingSecrets } from '~/utils/secrets'

/** The result of a failed action, shown where the action was taken. */
const props = defineProps<{ error: AgentError | null }>()

// A 403 is explained from its details ("This token has the read role; deploying
// needs deploy or admin"), which reads better than the agent's lowercase line.
// A limited token's refusal names the applications it covers: the agent's sentence is the explanation.
const headline = computed(() => (props.error ? forbiddenExplanation(props.error) || limitedExplanation(props.error) || props.error.displayMessage : ''))

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
  if (error.code === 'TOKEN_LIMITED') return 'Sign in with a token that covers it, or ask an admin for one under Access.'
  if (error.code === 'PROMOTION_IN_PROGRESS') return 'A promotion is running on this server. It is shown under Export and standby on the server\'s page; wait for it to finish.'
  if (error.code === 'APPLICATION_RUNNING') return 'Stop the application first.'
  if (error.code === 'JOB_ALREADY_RUNNING') return 'A run of this job has not finished yet; a job runs one at a time. Open it in the run history to follow it.'
  if (error.code === 'TOKEN_EXISTS') return 'Choose another name, or revoke the existing token first.'
  if (error.code === 'STATIC_APPLICATION') return 'The files are served by the proxy as they are; there is nothing to read logs or metrics from and nothing to run a command in.'
  if (error.code === 'VOLUME_IN_USE') return 'Only volumes of deleted applications can be removed here. To replace the data of a running application, restore a backup on its page.'
  if (error.code === 'REGISTRY_LOGIN_FAILED') return loginFailedHint(error)
  if (error.code === 'KEY_ROTATION_PENDING') {
    const file = typeof error.details.key_file === 'string' ? error.details.key_file : 'encryption.key.new in the data directory of the agent'
    return `Put the key from ${file} on the server into /opt/shipwick/.env as SHIPWICK_ENCRYPTION_KEY, restart the agent, then rotate again.`
  }
  if (error.code === 'INVALID_CERTIFICATE') return 'The first field takes the chain in PEM, the certificate of the hostname itself first (fullchain.pem); the second its private key (privkey.pem).'
  if (error.code === 'TRAFFIC_UNAVAILABLE') return 'The agent reads the access log of the Caddy container in its own compose project, and there is none.'
  if (error.code === 'BACKUP_BUSY') return 'It is still being taken, verified or restored. Wait for that to finish, then try again.'
  if (error.code === 'BACKUP_NOT_USABLE') return 'Only a backup that succeeded can be verified, restored or downloaded.'
  if (error.code === 'NO_VOLUMES') return 'A backup is an archive of volumes, and this application has none. Give it some under volumes in deploy.yaml.'
  if (error.code === 'BACKUPS_NOT_ENCRYPTED') return 'Set SHIPWICK_BACKUP_PASSPHRASE in /opt/shipwick/.env on the server, then: cd /opt/shipwick && docker compose up -d'
  if (error.code === 'IMPORT_IN_PROGRESS') return 'A server takes one import at a time. Wait for the running one to finish.'
  if (error.code === 'EXPORT_IN_PROGRESS') return 'Wait for the export that is being written; it is in the list.'
  if (error.code === 'STANDBY_NOT_CONFIGURED') return 'A standby fetches exports from the bucket the first server writes them to.'
  if (error.unreachable && error.agentUrl) return `Tried ${error.agentUrl}`
  return ''
})

/** A deployment refused because a secret it refers to is not stored: the names, to offer the way to the form that stores them. */
const secrets = computed(() => (props.error?.code === 'INVALID_CONFIG' ? missingSecrets(props.error.fields) : []))
</script>

<template>
  <div v-if="props.error" class="rounded-sm border border-danger-line bg-danger-bg px-3 py-2 text-xs text-danger" role="alert">
    <p class="font-medium">
      {{ headline }}
    </p>
    <ul v-if="props.error.fields.length > 0" class="mt-1 space-y-0.5">
      <li v-for="(field, index) in props.error.fields" :key="index" class="mono break-words">
        {{ field.field }}: {{ field.message }}<template v-if="field.expected">
          (expected: {{ field.expected }})
        </template>
      </li>
    </ul>
    <p v-if="hint" class="mt-0.5">
      {{ hint }}
    </p>
    <p v-if="secrets.length > 0" class="mt-1.5">
      Store
      <template v-for="(name, index) in secrets" :key="name">
        <template v-if="index > 0">
          {{ index === secrets.length - 1 ? ' and ' : ', ' }}
        </template>
        <NuxtLink :to="{ path: '/settings/secrets', query: { name } }" class="mono font-medium underline underline-offset-2">{{ name }}</NuxtLink>
      </template>
      on the Secrets page, then try again. An admin token can add {{ secrets.length === 1 ? 'it' : 'them' }}.
    </p>
  </div>
</template>
