<script setup lang="ts">
import type { Application, AuditEntry, AuditPage } from '~/types/api'
import { agentUrl } from '~/composables/useAgent'
import type { AgentError } from '~/utils/agentError'
import { isAbortError, toAgentError } from '~/utils/agentError'
import type { AuditFilters } from '~/utils/access'
import { AUDIT_FAMILIES, NO_AUDIT_FILTERS, auditActionLabel, auditActionProblem, auditDetailLink, auditFiltersIgnored, auditFrom, auditMore, auditOutcomeDisplay, auditQuery, auditSubject, newAuditFilters } from '~/utils/access'
import { formatAbsoluteUtc } from '~/utils/format'

/**
 * Who did what: every request that changed something on this server, or
 * tried to, newest first. Read from the agent a page at a time; nothing here
 * is polled, because a trail that moves under the reader is hard to read.
 * What is on screen can be taken away whole, as a file.
 */
const agent = useAgent()

const PAGE = 50

const applications = usePolling<Application[]>(signal => agent.get<Application[]>('/applications', { signal }), { interval: 60_000 })

const filters = reactive<AuditFilters>({ ...NO_AUDIT_FILTERS, outcomes: [] })
const SINCE = [
  { value: '', label: 'Any time' },
  { value: '24h', label: 'Last 24 hours' },
  { value: '7d', label: 'Last 7 days' },
  { value: '30d', label: 'Last 30 days' },
] as const
const KINDS = [
  { value: '', label: 'Tokens and people' },
  { value: 'token', label: 'Tokens' },
  { value: 'user', label: 'People' },
] as const
const OUTCOMES = [
  { value: 'ok', label: 'Done' },
  { value: 'refused', label: 'Refused' },
  { value: 'failed', label: 'Failed' },
] as const

const actionProblem = computed(() => auditActionProblem(filters.action))
const query = computed(() => auditQuery(filters))

const entries = shallowRef<AuditEntry[]>([])
const loading = ref(true)
const loadingOlder = ref(false)
const error = shallowRef<AgentError | null>(null)
const older = ref(false)
/** The filters of the list on screen: what an export takes away, and what Load older asks again. */
const shown = shallowRef<Record<string, string>>({})
/** The filters an agent before 0.7 ignored; the list it answered is then not what was asked for, and is not shown. */
const ignored = ref<string[]>([])
/** The agent answers `more`: it is 0.7 or newer, and can export the trail. */
const knowsExport = ref(false)
let controller: AbortController | null = null

const filtered = computed(() => Object.keys(query.value).length > 0)

async function load(before?: number) {
  if (before === undefined && actionProblem.value) return
  controller?.abort()
  const own = new AbortController()
  controller = own
  if (before === undefined) loading.value = true
  else loadingOlder.value = true
  error.value = null
  const asked = before === undefined ? query.value : shown.value
  try {
    const page = await agent.getEnvelope<AuditPage>('/audit', { query: { ...asked, limit: PAGE, before }, signal: own.signal })
    shown.value = asked
    knowsExport.value = page.more !== undefined
    if (auditFiltersIgnored(page, asked)) {
      ignored.value = newAuditFilters(asked)
      entries.value = []
      older.value = false
      return
    }
    ignored.value = []
    entries.value = before === undefined ? page.data : [...entries.value, ...page.data]
    older.value = auditMore(page, PAGE)
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
  Object.assign(filters, NO_AUDIT_FILTERS, { outcomes: [] })
  void load()
}

/** What an agent before 0.7 does not know is taken out again, and the question asked without it. */
function withoutNewFilters() {
  Object.assign(filters, { family: '', action: '', outcomes: [], actorKind: '' })
  void load()
}

function toggleOutcome(value: string) {
  filters.outcomes = filters.outcomes.includes(value) ? filters.outcomes.filter(o => o !== value) : [...filters.outcomes, value]
}

onMounted(() => void load())
onScopeDispose(() => controller?.abort())
// The choices apply at once; what is typed, with Apply or Enter.
watch(() => [filters.application, filters.since, filters.family, filters.actorKind, filters.outcomes], () => void load())

const unsupported = computed(() => error.value?.code === 'ENDPOINT_NOT_FOUND')

// --- export ---------------------------------------------------------------------

/**
 * Everything that matches the filters of the list on screen, as a file: a
 * plain link through the proxy, so the browser saves it as it arrives, under
 * the agent's file name. Not offered by an agent that answered without
 * `more`: it is older than the export.
 */
const exportable = computed(() => knowsExport.value && ignored.value.length === 0 && !loading.value && !(error.value && entries.value.length === 0))
const exportUrl = (format: 'csv' | 'json') => agentUrl('/audit/export', { ...shown.value, format })
</script>

<template>
  <div class="space-y-4">
    <div v-if="unsupported" class="rounded-sm border border-line">
      <EmptyState title="This agent keeps no audit trail">
        The trail of who did what is kept by agents from 0.6 on. Upgrade the agent; until then an application's events and the deployments' "by" say part of it.
      </EmptyState>
    </div>

    <template v-else>
      <form class="space-y-3" @submit.prevent="load()">
        <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <div>
            <label for="audit-family" class="label block">What</label>
            <select id="audit-family" v-model="filters.family" class="input mt-1.5" :disabled="filters.action.trim() !== ''">
              <option v-for="family in AUDIT_FAMILIES" :key="family.value" :value="family.value">
                {{ family.label }}
              </option>
            </select>
          </div>
          <div>
            <label for="audit-application" class="label block">Application</label>
            <select id="audit-application" v-model="filters.application" class="input mono mt-1.5">
              <option value="">
                All
              </option>
              <option v-for="a in applications.data.value ?? []" :key="a.name" :value="a.name">
                {{ a.name }}
              </option>
            </select>
          </div>
          <div>
            <label for="audit-kind" class="label block">By</label>
            <select id="audit-kind" v-model="filters.actorKind" class="input mt-1.5">
              <option v-for="k in KINDS" :key="k.value" :value="k.value">
                {{ k.label }}
              </option>
            </select>
          </div>
          <div>
            <label for="audit-since" class="label block">Since</label>
            <select id="audit-since" v-model="filters.since" class="input mt-1.5">
              <option v-for="choice in SINCE" :key="choice.value" :value="choice.value">
                {{ choice.label }}
              </option>
            </select>
          </div>
        </div>

        <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto] lg:items-start">
          <div>
            <label for="audit-actor" class="label block">Who, by name</label>
            <input id="audit-actor" v-model="filters.actor" type="text" class="input mono mt-1.5" placeholder="ci, ada@example.com" autocomplete="off" autocapitalize="off" spellcheck="false" aria-describedby="audit-actor-help">
            <p id="audit-actor-help" class="mt-1.5 text-xs text-fg-muted">
              Exactly as it stands in the list.
            </p>
          </div>
          <div>
            <label for="audit-action" class="label block">One action</label>
            <input
              id="audit-action"
              v-model="filters.action"
              type="text"
              class="input mono mt-1.5"
              placeholder="token.create"
              autocomplete="off"
              autocapitalize="off"
              spellcheck="false"
              :aria-invalid="actionProblem !== '' || undefined"
              aria-describedby="audit-action-help"
            >
            <p id="audit-action-help" class="mt-1.5 text-xs" :class="actionProblem ? 'text-danger' : 'text-fg-muted'">
              {{ actionProblem || 'As the trail names it; in place of the choice under What.' }}
            </p>
          </div>
          <fieldset class="min-w-0">
            <legend class="label">
              Result
            </legend>
            <div class="mt-1.5 flex h-8 flex-wrap items-center gap-x-4 gap-y-1">
              <label v-for="o in OUTCOMES" :key="o.value" class="target flex cursor-pointer items-center gap-1.5">
                <input type="checkbox" class="accent-[var(--fg)]" :checked="filters.outcomes.includes(o.value)" @change="toggleOutcome(o.value)"> {{ o.label }}
              </label>
            </div>
            <p class="mt-1.5 text-xs text-fg-muted">
              None ticked: all three.
            </p>
          </fieldset>
        </div>

        <div class="flex flex-wrap items-center gap-2">
          <UiButton type="submit" :pending="loading && entries.length > 0" :disabled="actionProblem !== ''">
            Apply
          </UiButton>
          <UiButton v-if="filtered" variant="ghost" @click="clearFilters">
            Clear
          </UiButton>
          <span v-if="exportable" class="ml-auto flex flex-wrap items-center gap-2">
            <span class="text-xs text-fg-muted">Take {{ Object.keys(shown).length > 0 ? 'what matches' : 'the whole trail' }} away:</span>
            <a :href="exportUrl('csv')" download class="target inline-flex h-7 items-center gap-1.5 rounded-sm border border-line-strong bg-bg px-2.5 text-xs font-medium hover:bg-hover" title="Every entry that matches the filters above, not only the ones on screen: a table for a spreadsheet">
              <UiIcon name="download" :size="12" />
              Export CSV
            </a>
            <a :href="exportUrl('json')" download class="target inline-flex h-7 items-center rounded-sm border border-line-strong bg-bg px-2.5 text-xs font-medium hover:bg-hover" title="The same entries, one JSON object per line, as the API answers them">
              Export NDJSON
            </a>
          </span>
        </div>
      </form>

      <!-- An agent before 0.7 answered, and did not look at what it does not know. -->
      <div v-if="ignored.length > 0" class="rounded-sm border border-warn-line bg-warn-bg px-3 py-2.5 text-xs text-warn" role="alert">
        <p class="font-medium">
          This server's agent is older than 0.7 and cannot search the trail by {{ ignored.join(', ').replace(/, ([^,]*)$/, ' or $1') }}
        </p>
        <p class="mt-0.5">
          It ignored that part of the question, so its answer would show entries that do not match and is not shown. Search by application, name and time, or upgrade the agent.
        </p>
        <UiButton size="sm" class="mt-2" @click="withoutNewFilters">
          Search without it
        </UiButton>
      </div>

      <div v-else class="overflow-hidden rounded-sm border border-line">
        <TableSkeleton v-if="loading && entries.length === 0" :rows="6" :columns="7" />
        <ErrorState v-else-if="error && entries.length === 0" :error="error" subject="the audit trail" :retrying="loading" @retry="load()" />
        <EmptyState v-else-if="entries.length === 0" :title="filtered ? 'Nothing matches' : 'Nothing has been recorded yet'">
          <template v-if="filtered">
            No entry is of that kind, from that application, by that name and in that time. The name under Who is matched exactly, as it stands in the list.
          </template>
          <template v-else>
            Every deployment, stop, start, backup, secret, token and sign-in is written here from now on, with who asked and from where; refusals too. Entries are kept for a year.
          </template>
        </EmptyState>
        <div v-else class="overflow-x-auto">
          <table class="data-table stack" aria-label="Audit trail">
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
                    <span class="text-fg-subtle"> · </span>
                  </template><span v-if="e.target">{{ e.target }}</span><span v-if="!e.application && !e.target" class="font-sans text-fg-muted">{{ auditSubject(e) }}</span>
                </td>
                <td data-label="Result">
                  <StatusBadge v-bind="auditOutcomeDisplay(e)" :raw="`HTTP ${e.status}`" />
                  <span v-if="e.code" class="mono block text-2xs text-fg-subtle">{{ e.code }}</span>
                </td>
                <td data-label="From" class="mono whitespace-nowrap text-fg-muted" :title="e.forwarded_for ? `Reported by the proxy in front; the connection came from ${e.address}` : 'The address the agent saw'">
                  {{ auditFrom(e) }}
                </td>
                <td :data-label="e.detail ? 'Detail' : undefined" class="break-words text-fg-muted" :class="e.detail ? '' : 'cards:!hidden'">
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
            <template v-if="knowsExport">
              An export is recorded here as well, with how many entries it held.
            </template>
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
