<script setup lang="ts">
import type { KeyRotation } from '~/types/api'
import type { AgentError } from '~/utils/agentError'
import { toAgentError } from '~/utils/agentError'
import { pluralize } from '~/utils/format'
import { roleHint } from '~/utils/roles'

/**
 * Rotating the key the agent encrypts stored values with. Nothing is deployed
 * and nothing restarts. Where the key is set in the agent's environment the
 * agent cannot replace it there: the answer then carries the new key, once,
 * and this panel shows it until it is dismissed and holds it nowhere else.
 */
const props = defineProps<{ admin: boolean }>()

const agent = useAgent()

const confirming = ref(false)
const pending = ref(false)
const error = shallowRef<AgentError | null>(null)
const rotation = shallowRef<KeyRotation | null>(null)
const copied = ref(false)
const result = ref<HTMLElement | null>(null)
useFocusWhenShown(result)
const { announce } = useAnnounce()

async function rotate() {
  if (pending.value) return
  pending.value = true
  error.value = null
  try {
    rotation.value = await agent.post<KeyRotation>('/server/rotate-key')
    copied.value = false
    confirming.value = false
  }
  catch (cause) {
    error.value = toAgentError(cause)
  }
  finally {
    pending.value = false
  }
}

function close() {
  confirming.value = false
  error.value = null
}

const fromEnvironment = computed(() => rotation.value?.key_source === 'environment' && Boolean(rotation.value.key))
const line = computed(() => (rotation.value?.key ? `SHIPWICK_ENCRYPTION_KEY=${rotation.value.key}` : ''))
const counts = computed(() => (rotation.value ? `${pluralize(rotation.value.values, 'stored value')} and ${pluralize(rotation.value.deployments, 'deployment')} re-encrypted` : ''))

async function copy() {
  try {
    await navigator.clipboard.writeText(line.value)
    copied.value = true
  }
  catch {
    // No clipboard access (http, or denied): the line is selectable right there.
    announce('Could not copy. Select the line and copy it by hand.')
  }
}

// The button that rotates is back where the result was: it takes the focus Done had.
const start = ref<HTMLElement | null>(null)
function done() {
  rotation.value = null
  void nextTick(() => start.value?.querySelector('button')?.focus())
}

// The key leaves the page with the panel; nothing else ever held it.
onBeforeUnmount(() => {
  rotation.value = null
})
</script>

<template>
  <UiPanel title="Encryption key">
    <div v-if="rotation" ref="result" tabindex="-1" class="space-y-3 px-4 py-4 focus-visible:-outline-offset-2" role="status">
      <p class="flex items-center gap-2 font-medium text-ok">
        <UiIcon name="check" :size="14" />
        Rotated the encryption key: {{ counts }}
      </p>
      <template v-if="fromEnvironment">
        <p class="text-fg-muted">
          The agent's key is set in its environment, which it cannot change. Put the new key in <span class="mono text-fg">/opt/shipwick/.env</span> on the server before the agent restarts:
        </p>
        <div class="flex flex-wrap items-center gap-2">
          <code class="mono select-all break-all rounded-sm border border-line bg-inset px-2.5 py-1.5 text-sm">{{ line }}</code>
          <UiButton size="sm" @click="copy">
            <UiIcon name="copy" :size="12" />
            {{ copied ? 'Copied' : 'Copy' }}
          </UiButton>
        </div>
        <p class="text-fg-muted">
          Copy it now: it will not be shown again. With the old key there the agent refuses to start. Until it has started with the new one it keeps a copy in <span class="mono text-fg">{{ rotation.key_file }}</span> on the server, and removes it then.
        </p>
      </template>
      <p v-else class="text-fg-muted">
        The new key is in <span class="mono text-fg">{{ rotation.key_file }}</span> on the server. Back it up: database backups made from now on need it, earlier ones the old key.
      </p>
      <UiButton size="sm" variant="ghost" @click="done">
        Done
      </UiButton>
    </div>
    <div v-else ref="start" class="flex flex-wrap items-center justify-between gap-x-6 gap-y-3 px-4 py-3">
      <p class="max-w-prose text-fg-muted">
        Environment values, secrets, registry passwords and certificate keys are stored encrypted with one key. Rotating has the agent generate a new one and re-encrypt everything under it; nothing is deployed and nothing restarts.
      </p>
      <UiButton size="sm" :disabled="!props.admin" :title="props.admin ? undefined : roleHint('admin')" @click="confirming = true">
        <UiIcon name="key" :size="12" />
        Rotate encryption key
      </UiButton>
    </div>

    <ConfirmDialog
      :open="confirming"
      title="Rotate the encryption key"
      confirm-label="Rotate key"
      :pending="pending"
      :error="error"
      @close="close"
      @confirm="rotate"
    >
      The agent replaces the key and re-encrypts every stored value under the new one. Running applications are not touched. A copy of the old key no longer opens the database: back the new one up afterwards. Backups of the database made before the rotation still need the old key.
    </ConfirmDialog>
  </UiPanel>
</template>
