<script setup lang="ts">
import type { AgentError } from '~/utils/agentError'
import { toAgentError } from '~/utils/agentError'
import { safeRedirect } from '~/utils/redirect'

definePageMeta({ layout: 'auth' })
useHead({ title: 'Sign in' })

const route = useRoute()
const session = useSession()

const token = ref('')
const pending = ref(false)
const error = shallowRef<AgentError | null>(null)
const expired = computed(() => route.query.expired === '1' && !error.value)

async function submit() {
  if (pending.value) return
  const value = token.value.trim()
  if (value === '') return
  pending.value = true
  error.value = null
  try {
    await session.login(value)
    token.value = ''
    await navigateTo(safeRedirect(route.query.redirect), { replace: true })
  }
  catch (cause) {
    error.value = toAgentError(cause)
  }
  finally {
    pending.value = false
  }
}

const errorTitle = computed(() => {
  const e = error.value
  if (!e) return ''
  if (e.status === 401) return 'Token rejected'
  if (e.unreachable) return 'Agent unreachable'
  if (e.code === 'NETWORK') return 'Dashboard server unreachable'
  return 'Could not sign in'
})
</script>

<template>
  <div class="w-full max-w-[22rem]">
    <div class="mb-6 flex items-center gap-2">
      <AppMark :size="20" />
      <span class="text-lg font-semibold tracking-tight">Shipwick</span>
    </div>

    <form class="rounded-sm border border-line bg-bg p-5" novalidate @submit.prevent="submit">
      <h1 class="text-base font-semibold">
        Sign in to the agent
      </h1>
      <p class="mt-1 text-fg-muted">
        Authenticate with the agent's API token.
      </p>

      <p v-if="expired" class="mt-4 rounded-sm border border-warn-line bg-warn-bg px-3 py-2 text-xs text-warn" role="status">
        Your session ended: the agent no longer accepts the stored token. Sign in again.
      </p>

      <label for="token" class="label mt-5 block">Agent token</label>
      <input
        id="token"
        v-model="token"
        type="password"
        name="token"
        class="input mono mt-1.5"
        autocomplete="current-password"
        autocapitalize="off"
        autocorrect="off"
        spellcheck="false"
        autofocus
        required
        :aria-invalid="error?.status === 401 || undefined"
        aria-describedby="token-help"
      >

      <div v-if="error" class="mt-3 rounded-sm border border-danger-line bg-danger-bg px-3 py-2 text-xs text-danger" role="alert">
        <p class="font-medium">
          {{ errorTitle }}
        </p>
        <p class="mt-0.5">
          {{ error.message }}
        </p>
        <p v-if="error.unreachable" class="mt-1">
          Check that the agent is running and that <span class="mono">SHIPWICK_AGENT_URL</span> points to it.
        </p>
      </div>

      <UiButton type="submit" variant="primary" class="mt-4 w-full" :pending="pending" :disabled="token.trim() === ''">
        Sign in
      </UiButton>
    </form>

    <div id="token-help" class="mt-4 space-y-2 px-1 text-xs text-fg-muted">
      <p>
        The token is the value of <span class="mono text-fg">SHIPWICK_AGENT_TOKEN</span> on the server. If it was not set, the agent generated one on first start and printed it once:
        <span class="mono text-fg">docker logs shipwick-agent</span>
      </p>
      <p>
        It is kept in an httpOnly cookie and is never readable by scripts in this page. It grants full control of the server; sign out on shared machines.
      </p>
    </div>
  </div>
</template>
