<script setup lang="ts">
import type { AgentError } from '~/utils/agentError'
import { toAgentError } from '~/utils/agentError'
import { safeRedirect } from '~/utils/redirect'
import { wantedServer, withServer } from '~/utils/servers'
import { issuerHost } from '~/utils/access'
import { REQUEST_HEADERS } from '~/composables/useSession'

definePageMeta({ layout: 'auth' })
useHead({ title: 'Sign in' })

const route = useRoute()
const session = useSession()

const token = ref('')
const pending = ref(false)
const error = shallowRef<AgentError | null>(null)

// With several servers a sign-in is for one of them: the one the address names, else the one opened last.
const asked = wantedServer(route.query.server)
const names = computed(() => session.servers.value.map(s => s.name))
const server = ref<string>(
  (asked && names.value.includes(asked) ? asked : null)
  ?? (names.value.includes(session.remembered() ?? '') ? session.remembered()! : names.value[0] ?? ''),
)
/** The address was for a server this dashboard does not have. */
const unknown = computed(() => (session.multiple.value && asked && !names.value.includes(asked) ? asked : ''))

// Why the session ended: what the agent said about a token that expired or a session it ended, a general sentence otherwise.
const ended = computed(() => {
  if (error.value || route.query.expired !== '1') return ''
  return session.ended.value || 'Your session ended: the agent no longer accepts the stored token. Sign in again.'
})

// --- signing in through the agent's provider -----------------------------------------

interface SignInOffer {
  configured: boolean
  issuer: string
  /** The provider is configured and cannot be used: the agent's sentence. */
  problem: string
  /** Why the last sign-in of this browser did not complete; said once. */
  failure: { title: string, message: string, copy: boolean } | null
}

const offer = shallowRef<SignInOffer | null>(null)
const failure = shallowRef<SignInOffer['failure']>(null)

/** Asked of the server chosen: each agent has its own provider, or none. An agent that has none is simply not offered. */
async function askOffer() {
  const target = session.multiple.value ? server.value : null
  offer.value = null
  if (session.multiple.value && !target) return
  try {
    const response = await fetch(target ? `/api/auth?server=${encodeURIComponent(target)}` : '/api/auth', { headers: REQUEST_HEADERS, credentials: 'same-origin', cache: 'no-store' })
    if (!response.ok) return
    const body = await response.json() as SignInOffer
    if (session.multiple.value && server.value !== target) return
    offer.value = body
    if (body.failure) failure.value = body.failure
  }
  catch {
    // The token field works without it.
  }
}

onMounted(askOffer)
watch(server, askOffer)

const providerHost = computed(() => (offer.value?.issuer ? issuerHost(offer.value.issuer) : ''))
const providerLink = computed(() => (session.multiple.value ? `/auth/login?server=${encodeURIComponent(server.value)}` : '/auth/login'))

async function submit() {
  if (pending.value) return
  const value = token.value.trim()
  if (value === '') return
  pending.value = true
  error.value = null
  try {
    const target = session.multiple.value ? server.value : session.selected.value
    await session.login(value, target)
    token.value = ''
    const next = safeRedirect(route.query.redirect)
    await navigateTo(session.multiple.value ? withServer(next, target) : next, { replace: true })
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
  // Counted per address by the agent, without looking at the token: the token may well be right.
  if (e.rateLimited) return 'Too many attempts'
  if (e.code === 'TOKEN_EXPIRED') return 'Token expired'
  if (e.status === 401) return 'Token rejected'
  // Refused for where the dashboard server calls from, before the token was looked at: the token may well be right.
  if (e.applicationCaller) return 'Refused by the agent'
  if (e.unreachable) return 'Agent unreachable'
  if (e.code === 'NETWORK') return 'Dashboard server unreachable'
  return 'Could not sign in'
})
</script>

<template>
  <div class="w-full max-w-[22rem]">
    <div class="mb-6 flex items-center gap-2">
      <AppMark :size="20" compact />
      <span class="text-lg font-semibold tracking-tight">Shipwick</span>
    </div>

    <form class="rounded-sm border border-line bg-bg p-5" novalidate @submit.prevent="submit">
      <h1 class="text-base font-semibold">
        Sign in
      </h1>
      <p class="mt-1 text-fg-muted">
        <template v-if="session.multiple.value">
          Choose a server and paste an API token of its agent. Each server has its own sign-in.
        </template>
        <template v-else-if="offer?.configured">
          Use your account at the sign-in provider, or paste an API token of this server's agent.
        </template>
        <template v-else>
          Paste an API token of this server's agent.
        </template>
      </p>

      <p v-if="session.problem.value" class="mt-4 rounded-sm border border-danger-line bg-danger-bg px-3 py-2 text-xs text-danger" role="alert">
        The dashboard's server could not say which servers it has: {{ session.problem.value }}
      </p>
      <p v-if="unknown" class="mt-4 rounded-sm border border-warn-line bg-warn-bg px-3 py-2 text-xs text-warn" role="status">
        The link you followed is for a server named <span class="mono font-medium">{{ unknown }}</span>, which this dashboard does not have.
      </p>
      <p v-if="ended" class="mt-4 rounded-sm border border-warn-line bg-warn-bg px-3 py-2 text-xs text-warn" role="status">
        {{ ended }}
      </p>

      <template v-if="session.multiple.value">
        <label for="server" class="label mt-5 block">Server</label>
        <select id="server" v-model="server" name="server" class="input mono mt-1.5">
          <option v-for="s in session.servers.value" :key="s.name" :value="s.name">
            {{ s.name }}{{ s.authenticated ? ' (signed in)' : '' }}
          </option>
        </select>
      </template>

      <!-- Why the sign-in through the provider did not complete. What is meant for an admin is easy to copy. -->
      <div v-if="failure && !error" class="mt-4 rounded-sm border border-danger-line bg-danger-bg px-3 py-2 text-xs text-danger" role="alert">
        <p class="font-medium">
          {{ failure.title }}
        </p>
        <p v-if="failure.message" class="mt-1 break-words" :class="failure.copy ? 'select-all rounded-sm border border-danger-line bg-bg px-2 py-1.5 text-fg' : ''">
          {{ failure.message }}
        </p>
      </div>

      <!-- People sign in with their account at the company's provider; a token is for the CLI, CI and the first admin. -->
      <template v-if="offer?.configured">
        <a
          v-if="!offer.problem"
          :href="providerLink"
          class="target mt-5 flex h-8 w-full items-center justify-center rounded-sm border border-primary bg-primary px-3 text-sm font-medium text-primary-fg hover:border-primary-hover hover:bg-primary-hover"
        >Sign in with {{ providerHost }}</a>
        <template v-else>
          <span class="mt-5 flex h-8 w-full items-center justify-center rounded-sm border border-line-strong px-3 text-sm font-medium opacity-45" aria-disabled="true">Sign in with the provider</span>
          <p class="mt-1.5 break-words text-xs text-warn" role="status">
            The sign-in provider cannot be used right now: {{ offer.problem }}
          </p>
        </template>
        <p class="mt-4 flex items-center gap-3 text-xs text-fg-subtle" aria-hidden="true">
          <span class="h-px flex-1 bg-line" />or with a token<span class="h-px flex-1 bg-line" />
        </p>
      </template>

      <label for="token" class="label block" :class="offer?.configured ? 'mt-3' : session.multiple.value ? 'mt-4' : 'mt-5'">API token</label>
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
        :aria-describedby="error ? 'token-error token-help' : 'token-help'"
      >

      <div v-if="error" id="token-error" class="mt-3 rounded-sm border border-danger-line bg-danger-bg px-3 py-2 text-xs text-danger" role="alert">
        <p class="font-medium">
          {{ errorTitle }}
        </p>
        <p class="mt-0.5">
          {{ error.displayMessage }}
        </p>
        <p v-if="error.unreachable" class="mt-1">
          Check that the agent is running and that <span class="mono">{{ session.multiple.value ? 'SHIPWICK_AGENTS' : 'SHIPWICK_AGENT_URL' }}</span> points to it.
        </p>
      </div>

      <UiButton type="submit" :variant="offer?.configured ? 'secondary' : 'primary'" class="mt-4 w-full" :pending="pending" :disabled="token.trim() === ''">
        {{ offer?.configured ? 'Sign in with the token' : 'Sign in' }}
      </UiButton>

      <p v-if="session.multiple.value" class="mt-3 text-center text-xs">
        <a href="/servers" class="link text-fg-muted">All servers</a>
      </p>
    </form>

    <div id="token-help" class="mt-4 space-y-2 px-1 text-xs text-fg-muted">
      <p>
        The first token is the value of <span class="mono text-fg">SHIPWICK_AGENT_TOKEN</span> on the server. If it was not set, the agent generated one on first start and printed it once:
        <span class="mono text-fg">docker logs shipwick-agent</span>
      </p>
      <p>
        A token an admin created for you, here under Access or with <span class="mono text-fg">shipwick token create</span>, works too, with what its role allows. It is kept in an httpOnly cookie and is never readable by scripts in this page; sign out on shared machines.
      </p>
    </div>
  </div>
</template>
