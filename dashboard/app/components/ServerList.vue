<script setup lang="ts">
import type { Server } from '~/types/api'
import type { AgentError } from '~/utils/agentError'
import { errorFromResponse, toAgentError } from '~/utils/agentError'
import { summarizeAlerts, worstAlertTone } from '~/utils/alerts'
import { pluralize } from '~/utils/format'
import { agentBase, wantedServer } from '~/utils/servers'
import { updateNotice } from '~/utils/updates'
import { REQUEST_HEADERS } from '~/composables/useSession'

/**
 * The servers this dashboard was configured with, each with its own sign-in.
 * A page about none of them: it is where one is chosen, and where a link to a
 * server the dashboard does not have ends up.
 */
const route = useRoute()
const session = useSession()

const unknown = computed(() => wantedServer(route.query.unknown) ?? '')

interface Row {
  info: Server | null
  error: AgentError | null
  loading: boolean
}
const rows = reactive(new Map<string, Row>())

/** One request per signed-in server, to name it and say how it is. Nothing is polled: this page is passed through. */
async function load(name: string) {
  const row: Row = { info: null, error: null, loading: true }
  rows.set(name, row)
  try {
    const response = await fetch(`${agentBase(name, true)}/server`, { headers: REQUEST_HEADERS, credentials: 'same-origin', cache: 'no-store' })
    if (!response.ok) throw await errorFromResponse(response)
    rows.set(name, { info: (await response.json() as { data: Server }).data, error: null, loading: false })
  }
  catch (cause) {
    rows.set(name, { info: null, error: toAgentError(cause), loading: false })
  }
}

onMounted(() => {
  for (const s of session.servers.value) if (s.authenticated) void load(s.name)
})

/** The agent refused the stored token: the proxy cleared the cookie, so the row offers a sign-in again. */
const signedIn = (name: string, authenticated: boolean) => authenticated && rows.get(name)?.error?.status !== 401

const leaving = ref<string | null>(null)
async function signOut(name: string) {
  leaving.value = name
  await session.signOut(name)
  rows.delete(name)
  leaving.value = null
}

function state(name: string): { tone: 'ok' | 'warn' | 'danger' | 'muted', text: string } {
  const row = rows.get(name)
  if (!row || row.loading) return { tone: 'muted', text: 'Asking the agent…' }
  if (row.error) {
    if (row.error.code === 'TOKEN_EXPIRED') return { tone: 'warn', text: `${row.error.message.charAt(0).toUpperCase()}${row.error.message.slice(1)}` }
    if (row.error.status === 401) return { tone: 'warn', text: 'The agent no longer accepts the stored token. Sign in again.' }
    return { tone: 'danger', text: row.error.unreachable ? 'The agent cannot be reached' : row.error.displayMessage }
  }
  const info = row.info!
  const alerts = info.alerts ?? []
  // Each server says for itself whether a newer release exists.
  const newer = updateNotice(info)
  const parts = [info.hostname, `agent ${info.agent_version}${newer ? ` (${newer.latest} is available)` : ''}`, pluralize(info.applications, 'application')]
  if (alerts.length > 0) parts.push(summarizeAlerts(alerts))
  return { tone: worstAlertTone(alerts) ?? 'ok', text: parts.join(' · ') }
}

const DOT = { ok: 'bg-ok-dot', warn: 'bg-warn-dot', danger: 'bg-danger-dot', muted: 'bg-muted-dot' } as const
const TEXT = { ok: 'text-fg-muted', warn: 'text-warn', danger: 'text-danger', muted: 'text-fg-subtle' } as const
</script>

<template>
  <div class="w-full max-w-[40rem]">
    <div class="mb-6 flex items-center gap-2">
      <AppMark :size="20" compact />
      <span class="text-lg font-semibold tracking-tight">Shipwick</span>
    </div>

    <p v-if="unknown" class="mb-4 rounded-sm border border-warn-line bg-warn-bg px-3 py-2 text-xs text-warn" role="status">
      The link you followed is for a server named <span class="mono font-medium">{{ unknown }}</span>, which this dashboard does not have. It may have been renamed or removed from <span class="mono">SHIPWICK_AGENTS</span>. These are the servers it has:
    </p>

    <section class="rounded-sm border border-line bg-bg">
      <header class="border-b border-line px-5 py-3.5">
        <h1 class="text-base font-semibold">
          Servers
        </h1>
        <p class="mt-0.5 text-fg-muted">
          Each server has its own agent and its own sign-in. Open one to see what runs on it.
        </p>
      </header>
      <ul class="divide-y divide-line">
        <li v-for="s in session.servers.value" :key="s.name" class="flex flex-wrap items-center gap-x-4 gap-y-2 px-5 py-3">
          <div class="min-w-0 flex-1 basis-56">
            <p class="flex items-center gap-2">
              <span class="size-1.5 shrink-0 rounded-full" :class="signedIn(s.name, s.authenticated) ? DOT[state(s.name).tone] : 'bg-muted-dot'" aria-hidden="true" />
              <span class="mono truncate font-medium">{{ s.name }}</span>
            </p>
            <p v-if="s.authenticated" class="mt-0.5 break-words pl-3.5 text-xs" :class="TEXT[state(s.name).tone]">
              {{ state(s.name).text }}
            </p>
            <p v-else class="mt-0.5 pl-3.5 text-xs text-fg-subtle">
              Not signed in
            </p>
          </div>
          <div class="flex shrink-0 items-center gap-2">
            <template v-if="signedIn(s.name, s.authenticated)">
              <UiButton size="sm" variant="ghost" :pending="leaving === s.name" :aria-label="`Sign out of ${s.name}`" @click="signOut(s.name)">
                Sign out
              </UiButton>
              <a :href="`/?server=${s.name}`" class="inline-flex h-7 items-center rounded-sm border border-primary bg-primary px-2.5 text-xs font-medium text-primary-fg hover:border-primary-hover hover:bg-primary-hover">Open</a>
            </template>
            <a v-else :href="`/login?server=${s.name}`" class="inline-flex h-7 items-center rounded-sm border border-line-strong bg-bg px-2.5 text-xs font-medium hover:bg-hover">Sign in</a>
          </div>
        </li>
      </ul>
    </section>

    <p class="mt-4 px-1 text-xs text-fg-muted">
      The list comes from <span class="mono text-fg">SHIPWICK_AGENTS</span> on the dashboard's server. An address carries its server (<span class="mono text-fg">?server=name</span>), so a link you share opens on the server you copied it from.
    </p>
  </div>
</template>
