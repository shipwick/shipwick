<script setup lang="ts">
import type { Deployment, LogMatch, LogSearchResult } from '~/types/api'
import type { AgentError } from '~/utils/agentError'
import { isAbortError, toAgentError } from '~/utils/agentError'
import { formatBytes, pluralize } from '~/utils/format'
import type { SearchWindow } from '~/utils/logArchive'
import { SEARCH_WINDOWS, groupMatches, groupSource, logsPath, searchBounds, searchSummary, searchTextProblem } from '~/utils/logArchive'

/**
 * A text looked for in what the running replicas wrote and in everything the
 * archive keeps. The agent answers a page at a time, source by source, and a
 * page may be empty while there is more to read: the search then goes on by
 * itself until it finds something or has read everything.
 */
const props = defineProps<{
  application: string
  deployments: readonly Deployment[]
  /** The most replicas the application has had, to choose one from. */
  replicas: number
}>()

const agent = useAgent()
const { announce } = useAnnounce()

const PAGE = 200

// --- the question ---------------------------------------------------------------

const text = ref('')
const when = ref<SearchWindow>('')
const from = ref('')
const to = ref('')
const deployment = ref('')
const replica = ref('')

const textProblem = computed(() => searchTextProblem(text.value))
const bounds = computed(() => searchBounds(when.value, from.value, to.value))
const ready = computed(() => textProblem.value === '' && bounds.value.problem === '')

// --- the answer -----------------------------------------------------------------

const lines = shallowRef<LogMatch[]>([])
const next = ref('')
const searching = ref(false)
/** A search has run: the box below says what it found, or that it found nothing. */
const asked = ref(false)
/** The text the results on screen were found with, to mark it in them. */
const found = ref('')
const read = reactive({ sources: 0, bytes: 0 })
const error = shallowRef<AgentError | null>(null)
let controller: AbortController | null = null
/** The question as it was asked: a later page is asked for with the same one. */
let question: Record<string, string | number | undefined> = {}

const groups = computed(() => groupMatches(lines.value))
const summary = computed(() => searchSummary(lines.value.length, groups.value.length, next.value !== ''))

async function run(more: boolean) {
  if (!more && !ready.value) return
  controller?.abort()
  const own = new AbortController()
  controller = own
  error.value = null
  searching.value = true
  if (!more) {
    question = {
      q: text.value,
      since: bounds.value.since,
      until: bounds.value.until,
      deployment: deployment.value,
      replica: replica.value,
      limit: PAGE,
    }
    found.value = text.value
    lines.value = []
    next.value = ''
    read.sources = 0
    read.bytes = 0
    asked.value = true
  }
  try {
    // An empty page that names a next one is normal: the agent reads a bounded amount for one request.
    let page: LogSearchResult
    do {
      page = await agent.get<LogSearchResult>(`/applications/${encodeURIComponent(props.application)}/logs/search`, {
        query: { ...question, cursor: next.value },
        signal: own.signal,
      })
      if (own.signal.aborted) return
      if (page.lines.length > 0) lines.value = [...lines.value, ...page.lines]
      next.value = page.next
      read.sources += page.sources
      read.bytes += page.bytes
    } while (page.lines.length === 0 && page.next !== '')
    announce(`${summary.value}.`)
  }
  catch (cause) {
    if (isAbortError(cause) || own.signal.aborted) return
    error.value = toAgentError(cause)
  }
  finally {
    if (controller === own) searching.value = false
  }
}

/** Stops a search that is going on; what it found stays, and it can be taken up where it stopped. */
function stop() {
  controller?.abort()
  controller = null
  searching.value = false
  announce(`Search stopped. ${summary.value}.`)
}

onScopeDispose(() => controller?.abort())
// Another application: nothing of the first one's search stays.
watch(() => props.application, () => {
  controller?.abort()
  lines.value = []
  next.value = ''
  asked.value = false
  error.value = null
})

const replicaChoices = computed(() => Array.from({ length: Math.max(1, props.replicas) }, (_, i) => i + 1))
</script>

<template>
  <div class="space-y-4">
    <form class="space-y-3" @submit.prevent="run(false)">
      <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-[minmax(0,1fr)_11rem_13rem_8rem]">
        <div>
          <label for="log-search-text" class="label block">Text</label>
          <input
            id="log-search-text"
            v-model="text"
            type="search"
            class="input mono mt-1.5"
            placeholder="connection refused"
            autocomplete="off"
            autocapitalize="off"
            spellcheck="false"
            :aria-invalid="textProblem !== '' || undefined"
            aria-describedby="log-search-text-help"
          >
        </div>
        <div>
          <label for="log-search-window" class="label block">Written</label>
          <select id="log-search-window" v-model="when" class="input mt-1.5">
            <option v-for="choice in SEARCH_WINDOWS" :key="choice.value" :value="choice.value">
              {{ choice.label }}
            </option>
          </select>
        </div>
        <div>
          <label for="log-search-deployment" class="label block">Deployment</label>
          <select id="log-search-deployment" v-model="deployment" class="input mono mt-1.5">
            <option value="">
              All
            </option>
            <option v-for="d in props.deployments" :key="d.id" :value="String(d.id)">
              #{{ d.sequence }} · {{ d.version }}
            </option>
          </select>
        </div>
        <div>
          <label for="log-search-replica" class="label block">Replica</label>
          <select id="log-search-replica" v-model="replica" class="input mono mt-1.5">
            <option value="">
              All
            </option>
            <option v-for="r in replicaChoices" :key="r" :value="String(r)">
              {{ r }}
            </option>
          </select>
        </div>
      </div>
      <p id="log-search-text-help" class="text-xs" :class="textProblem ? 'text-danger' : 'text-fg-muted'">
        {{ textProblem || 'Found anywhere in a line, whatever its case; not a pattern. Without a text, every line of the time and place chosen.' }}
      </p>
      <div v-if="when === 'custom'" class="grid gap-3 sm:grid-cols-2 lg:max-w-xl">
        <div>
          <label for="log-search-from" class="label block">From</label>
          <input id="log-search-from" v-model="from" type="datetime-local" class="input mono mt-1.5" :aria-invalid="bounds.problem !== '' || undefined" aria-describedby="log-search-bounds-help">
        </div>
        <div>
          <label for="log-search-to" class="label block">To</label>
          <input id="log-search-to" v-model="to" type="datetime-local" class="input mono mt-1.5" :aria-invalid="bounds.problem !== '' || undefined" aria-describedby="log-search-bounds-help">
        </div>
        <p id="log-search-bounds-help" class="text-xs sm:col-span-2" :class="bounds.problem && (from !== '' || to !== '') ? 'text-danger' : 'text-fg-muted'">
          {{ bounds.problem || 'In this browser\'s time zone; the lines themselves are shown in UTC.' }}
        </p>
      </div>
      <div class="flex flex-wrap items-center gap-2">
        <UiButton type="submit" variant="primary" :pending="searching && lines.length === 0" :disabled="!ready">
          <UiIcon name="search" :size="12" />
          Search
        </UiButton>
        <UiButton v-if="searching" variant="ghost" @click="stop">
          Stop
        </UiButton>
      </div>
    </form>

    <InlineError :error="error" />

    <div v-if="asked" class="overflow-hidden rounded-sm border border-line bg-bg">
      <div class="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 border-b border-line bg-subtle px-3 py-2 text-xs text-fg-muted">
        <span class="flex items-center gap-2">
          <svg v-if="searching" class="spinner" width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" aria-hidden="true">
            <path d="M8 2a6 6 0 1 1-6 6" />
          </svg>
          <span class="font-medium text-fg">{{ searching && lines.length === 0 ? 'Searching…' : summary }}</span>
        </span>
        <span class="mono">read {{ pluralize(read.sources, 'container') }}, {{ formatBytes(read.bytes) }}</span>
      </div>

      <div
        class="mono max-h-[calc(100dvh-20rem)] min-h-24 overflow-auto bg-inset text-xs leading-[1.125rem] focus-visible:-outline-offset-2"
        role="log"
        aria-live="off"
        :aria-label="`Lines found in the logs of ${props.application}`"
        tabindex="0"
      >
        <p v-if="lines.length === 0 && !searching" class="px-3 py-3 font-sans text-fg-subtle">
          <template v-if="next !== ''">
            Nothing found so far, and the search was stopped before it had read everything.
          </template>
          <template v-else>
            No line matches in the running replicas or in what is kept. A search finds the text as it is written; try a shorter one, or a wider time.
          </template>
        </p>
        <section v-for="group in groups" :key="group.key" :aria-label="`${group.container}, ${groupSource(group)}`" class="border-b border-line last:border-b-0">
          <h3 class="sticky left-0 flex flex-wrap items-center gap-x-2 gap-y-0.5 bg-subtle px-3 py-1.5 font-sans text-xs">
            <span class="mono font-medium text-fg">{{ group.container }}</span>
            <span class="text-fg-muted">{{ groupSource(group) }}</span>
            <span class="text-fg-faint" aria-hidden="true">·</span>
            <NuxtLink v-if="group.archiveId !== null" :to="logsPath(props.application, { view: 'archive', entry: group.archiveId })" class="link text-fg-muted">
              kept as entry {{ group.archiveId }}
            </NuxtLink>
            <span v-else class="text-ok">running</span>
            <span class="mono ml-auto text-fg-subtle">{{ pluralize(group.lines.length, 'line') }}</span>
          </h3>
          <div class="py-1">
            <LogRows :lines="group.lines" :replicas="false" :highlight="found" />
          </div>
        </section>
      </div>

      <div v-if="next !== '' && !searching" class="flex flex-wrap items-center justify-between gap-3 border-t border-line px-3 py-2">
        <p class="text-xs text-fg-subtle">
          The newest come first, container by container. There is more to search.
        </p>
        <UiButton size="sm" @click="run(true)">
          Load more
        </UiButton>
      </div>
      <p v-else-if="searching && lines.length > 0" class="border-t border-line px-3 py-2 text-xs text-fg-subtle">
        Searching on…
      </p>
    </div>

    <div v-else class="rounded-sm border border-line">
      <EmptyState title="Search what the application wrote">
        The running replicas are searched first, then everything that is kept of containers that ended: crashes, replaced versions, runs of jobs. Leave the text empty to read everything of a time, a deployment or a replica.
      </EmptyState>
    </div>
  </div>
</template>
