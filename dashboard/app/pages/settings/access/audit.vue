<script setup lang="ts">
import type { Application, AuditEntry } from '~/types/api'
import type { AgentError } from '~/utils/agentError'
import { isAbortError, toAgentError } from '~/utils/agentError'
import { auditActionLabel, auditDetailLink, auditFrom, auditHasOlder, auditOutcomeDisplay, auditSubject } from '~/utils/access'
import { formatAbsoluteUtc } from '~/utils/format'

/**
 * Who did what: every request that changed something on this server, or
 * tried to, newest first. Read from the agent a page at a time; nothing here
 * is polled, because a trail that moves under the reader is hard to read.
 */
const agent = useAgent()

const PAGE = 50

const applications = usePolling<Application[]>(signal => agent.get<Application[]>('/applications', { signal }), { interval: 60_000 })

const application = ref('')
const actor = ref('')
const since = ref('')
const SINCE = [
  { value: '', label: 'Any time' },
  { value: '24h', label: 'Last 24 hours' },
  { value: '7d', label: 'Last 7 days' },
  { value: '30d', label: 'Last 30 days' },
] as const

const entries = shallowRef<AuditEntry[]>([])
const loading = ref(true)
const loadingOlder = ref(false)
const error = shallowRef<AgentError | null>(null)
const older = ref(false)
let controller: AbortController | null = null

const filtered = computed(() => application.value !== '' || actor.value.trim() !== '' || since.value !== '')

async function load(before?: number) {
  controller?.abort()
  const own = new AbortController()
  controller = own
  if (before === undefined) loading.value = true
  else loadingOlder.value = true
  error.value = null
  try {
    const page = await agent.get<AuditEntry[]>('/audit', {
      query: { application: application.value, actor: actor.value.trim(), since: since.value, limit: PAGE, before },
      signal: own.signal,
    })
    entries.value = before === undefined ? page : [...entries.value, ...page]
    older.value = auditHasOlder(page, PAGE)
  }
  catch (cause) {
    if (isAbortError(cause) || own.signal.aborted) return
    error.value = toAgentError(cause)
  }
  finally {
    if (controller === own) {
      loading.value = false
      loadingOlder.value = false
    }
  }
}

function loadOlder() {
  const last = entries.value[entries.value.length - 1]
  if (last) void load(last.id)
}

function clearFilters() {
  application.value = ''
  actor.value = ''
  since.value = ''
  void load()
}

onMounted(() => void load())
onScopeDispose(() => controller?.abort())
watch([application, since], () => void load())

const unsupported = computed(() => error.value?.code === 'ENDPOINT_NOT_FOUND')
</script>

<template>
  <div class="space-y-4">
    <div v-if="unsupported" class="rounded-sm border border-line">
      <EmptyState title="This agent keeps no audit trail">
        The trail of who did what is kept by agents from 0.6 on. Upgrade the agent; until then an application's events and the deployments' "by" say part of it.
      </EmptyState>
    </div>

    <template v-else>
      <form class="grid gap-3 sm:grid-cols-[minmax(0,14rem)_minmax(0,14rem)_minmax(0,12rem)_auto] sm:items-end" @submit.prevent="load()">
        <div>
          <label for="audit-application" class="label block">Application</label>
          <select id="audit-application" v-model="application" class="input mono mt-1.5">
            <option value="">
              All
            </option>
            <option v-for="a in applications.data.value ?? []" :key="a.name" :value="a.name">
              {{ a.name }}
            </option>
          </select>
        </div>
        <div>
          <label for="audit-actor" class="label block">Who</label>
          <input id="audit-actor" v-model="actor" type="text" class="input mono mt-1.5" placeholder="ci, ada@example.com" autocomplete="off" autocapitalize="off" spellcheck="false">
        </div>
        <div>
          <label for="audit-since" class="label block">Since</label>
          <select id="audit-since" v-model="since" class="input mt-1.5">
            <option v-for="choice in SINCE" :key="choice.value" :value="choice.value">
              {{ choice.label }}
            </option>
          </select>
        </div>
        <div class="flex gap-2">
          <UiButton type="submit" :pending="loading && entries.length > 0">
            Apply
          </UiButton>
          <UiButton v-if="filtered" variant="ghost" @click="clearFilters">
            Clear
          </UiButton>
        </div>
      </form>

      <div class="overflow-hidden rounded-sm border border-line">
        <TableSkeleton v-if="loading && entries.length === 0" :rows="6" :columns="7" />
        <ErrorState v-else-if="error && entries.length === 0" :error="error" subject="the audit trail" :retrying="loading" @retry="load()" />
        <EmptyState v-else-if="entries.length === 0" :title="filtered ? 'Nothing matches' : 'Nothing has been recorded yet'">
          <template v-if="filtered">
            No entry is from that application, by that name and in that time. The name under Who is matched exactly, as it stands in the list.
          </template>
          <template v-else>
            Every deployment, stop, start, backup, secret, token and sign-in is written here from now on, with who asked and from where; refusals too. Entries are kept for a year.
          </template>
        </EmptyState>
        <div v-else class="overflow-x-auto">
          <table class="data-table stack">
            <thead>
              <tr>
                <th>When</th>
                <th>Who</th>
                <th>Action</th>
                <th>On</th>
                <th>Result</th>
                <th>From</th>
                <th>Detail</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="e in entries" :key="e.id">
                <td data-primary class="whitespace-nowrap text-fg-muted" :title="formatAbsoluteUtc(e.at)">
                  <TimeAgo :time="e.at" />
                </td>
                <td data-label="Who" class="mono [overflow-wrap:anywhere] sm:min-w-[9rem]" :title="e.actor.kind === 'user' ? 'A person, signed in through the provider' : 'An API token'">
                  {{ e.actor.name }}
                </td>
                <td data-label="Action" class="whitespace-nowrap" :title="e.action">
                  {{ auditActionLabel(e.action) }}
                </td>
                <td data-label="On" class="mono [overflow-wrap:anywhere] sm:min-w-[8rem]">
                  <NuxtLink v-if="e.application" :to="`/applications/${e.application}`" class="hover:underline">{{ e.application }}</NuxtLink><template v-if="e.application && e.target">
                    <span class="text-fg-faint"> · </span>
                  </template><span v-if="e.target">{{ e.target }}</span><span v-if="!e.application && !e.target" class="font-sans text-fg-muted">{{ auditSubject(e) }}</span>
                </td>
                <td data-label="Result">
                  <StatusBadge v-bind="auditOutcomeDisplay(e)" :raw="`HTTP ${e.status}`" />
                  <span v-if="e.code" class="mono block text-2xs text-fg-subtle">{{ e.code }}</span>
                </td>
                <td data-label="From" class="mono whitespace-nowrap text-fg-muted" :title="e.forwarded_for ? `Reported by the proxy in front; the connection came from ${e.address}` : 'The address the agent saw'">
                  {{ auditFrom(e) }}
                </td>
                <td :data-label="e.detail ? 'Detail' : undefined" class="break-words text-fg-muted" :class="e.detail ? '' : 'max-sm:!hidden'">
                  <NuxtLink v-if="auditDetailLink(e.detail)" :to="auditDetailLink(e.detail)!" class="link">{{ e.detail }}</NuxtLink>
                  <template v-else>
                    {{ e.detail }}
                  </template>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-if="entries.length > 0" class="flex flex-wrap items-center justify-between gap-3 border-t border-line px-4 py-2">
          <p class="text-xs text-fg-subtle">
            {{ entries.length }} {{ entries.length === 1 ? 'entry' : 'entries' }}{{ older ? ', newest first' : '; that is all there is' }}.
            What the agent does by itself (scheduled backups, jobs) has no entry: nobody asked for it.
          </p>
          <UiButton v-if="older" size="sm" :pending="loadingOlder" @click="loadOlder">
            Load older
          </UiButton>
        </div>
        <InlineError v-if="error && entries.length > 0" :error="error" class="m-3" />
      </div>
    </template>
  </div>
</template>
