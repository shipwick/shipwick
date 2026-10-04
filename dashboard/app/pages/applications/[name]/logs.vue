<script setup lang="ts">
import type { LogArchiveEntry } from '~/types/api'
import { AgentError } from '~/utils/agentError'
import type { LogView, LogViewChoice } from '~/utils/logArchive'
import { LOG_VIEWS, archiveSupported, chooseLogView, logViewOf, logsPath, positiveInt } from '~/utils/logArchive'
import { shippedLogDriver } from '~/utils/spec'
import { applicationPath } from '~/utils/tabs'

/**
 * What the application's containers wrote: now (Live), before the last one
 * ended (Previous), ever since output is kept (Archive), and wherever a text
 * occurs (Search). Each is an address. Without one in the address the tab
 * opens on what its visitor most likely came for: after a crash, the crash.
 */
const { name, detail, spec, isStatic, deployments } = useApplication()
const route = useRoute()
const router = useRouter()
const agent = useAgent()
const server = useServerInfo()

const shippedTo = computed(() => shippedLogDriver(spec.value))

// --- which of the four ----------------------------------------------------------

/** An agent before 0.7 keeps nothing: the tab is the live output, as it was. */
const supported = ref(archiveSupported(server.data.value))
watch(() => server.data.value, (s) => {
  if (s) supported.value = archiveSupported(s)
})

/** What was decided for an address that names no view; null until the newest entry has been looked at. */
const choice = shallowRef<LogViewChoice | null>(null)
const asked = computed(() => logViewOf(route.query.view))

// Decided once per visit: the view does not change under somebody who is reading it.
async function decide() {
  if (choice.value || isStatic.value || !supported.value) return
  let newest: LogArchiveEntry | null = null
  try {
    const list = await agent.get<LogArchiveEntry[]>(`/applications/${encodeURIComponent(name.value)}/logs/archive`, { query: { kind: 'replica', limit: 1 } })
    newest = list[0] ?? null
  }
  catch (cause) {
    // No archive after all, or it cannot be asked right now: the live output is what there is.
    if (cause instanceof AgentError && cause.code === 'ENDPOINT_NOT_FOUND') supported.value = false
  }
  choice.value = chooseLogView({ name: name.value, status: detail.value.status, containers: detail.value.containers.length }, newest)
}
onMounted(decide)
watch(supported, decide)
watch(name, () => {
  choice.value = null
  void decide()
})

const view = computed<LogView | null>(() => {
  if (!supported.value) return 'live'
  return asked.value ?? choice.value?.view ?? null
})

const entry = computed(() => positiveInt(route.query.entry))
const kind = computed(() => (route.query.kind === 'replica' || route.query.kind === 'run' ? route.query.kind : ''))
const deployment = computed(() => positiveInt(route.query.deployment))
const replica = computed(() => positiveInt(route.query.replica))

function setFilter(changes: { kind?: string, deployment?: number | null, replica?: number | null }) {
  const next = { kind: kind.value, deployment: deployment.value, replica: replica.value, ...changes }
  // A run has no replica.
  if (next.kind === 'run') next.replica = null
  void router.replace(logsPath(name.value, { view: 'archive', ...next }))
}

/** As many replicas as the configuration asks for, or as run now: the choice a search offers. */
const mostReplicas = computed(() => Math.max(spec.value?.replicas ?? 1, ...detail.value.containers.map(c => c.replica)))

const hasContainers = computed(() => detail.value.containers.length > 0)
const liveHeight = computed(() => (supported.value ? 'h-[calc(100dvh-23rem)] min-h-72' : 'h-[calc(100dvh-19rem)] min-h-72'))
</script>

<template>
  <div v-if="isStatic" class="rounded-sm border border-line">
    <EmptyState :title="`${name} has no logs`">
      It is a folder served by the proxy: there is no container to write any. What was asked of it is under
      <NuxtLink :to="applicationPath(name, 'metrics')" class="link">
        Traffic
      </NuxtLink>.
    </EmptyState>
  </div>

  <div v-else class="space-y-4">
    <!-- Four views of the same output, each with an address: links, the current one marked. -->
    <nav v-if="supported" aria-label="Views of the logs" class="flex flex-wrap items-center gap-x-4 gap-y-2">
      <div class="inline-flex rounded-sm border border-line-strong">
        <NuxtLink
          v-for="v in LOG_VIEWS"
          :key="v.view"
          :to="logsPath(name, { view: v.view })"
          :aria-current="view === v.view ? 'page' : undefined"
          class="target flex h-7 items-center border-l border-line-strong px-3 text-xs first:rounded-l-[3px] first:border-l-0 last:rounded-r-[3px] focus-visible:relative"
          :class="view === v.view ? 'bg-active font-medium text-fg' : 'text-fg-muted hover:bg-hover hover:text-fg'"
        >
          {{ v.label }}
        </NuxtLink>
      </div>
      <p v-if="view" class="min-w-0 flex-1 basis-64 text-xs text-fg-muted">
        {{ LOG_VIEWS.find(v => v.view === view)?.about }}
      </p>
    </nav>

    <div v-if="view === null" class="space-y-2 rounded-sm border border-line px-4 py-4" role="progressbar" aria-busy="true" aria-label="Loading">
      <span class="skeleton w-64" /><span class="skeleton h-24 w-full" />
    </div>

    <template v-else-if="view === 'live'">
      <!-- A replica died not long ago and the application runs again: the way to what it said. -->
      <p v-if="supported && !asked && choice?.note" class="flex flex-wrap items-center gap-x-3 gap-y-1.5 rounded-sm border border-warn-line bg-warn-bg px-3 py-2 text-xs text-warn">
        <UiIcon name="alert" :size="14" />
        <span class="min-w-0 flex-1 basis-64">{{ choice.note }}</span>
        <NuxtLink :to="logsPath(name, { view: 'previous' })" class="notice-action">
          Its last output
          <UiIcon name="chevron-right" :size="12" />
        </NuxtLink>
      </p>
      <LogViewer v-if="hasContainers" :application="name" :height-class="liveHeight" :shipped-to="shippedTo" />
      <div v-else class="rounded-sm border border-line">
        <EmptyState title="Nothing is running to follow">
          <template v-if="detail.status === 'STOPPED'">
            The application is stopped, so it has no containers. Start it to see what it writes.
          </template>
          <template v-else>
            There are no containers to read logs from.
          </template>
          <template v-if="supported">
            What its containers wrote before they ended is under
            <NuxtLink :to="logsPath(name, { view: 'previous' })" class="link">
              Previous
            </NuxtLink>.
          </template>
          <template v-else>
            What happened before is under Events on the overview.
          </template>
        </EmptyState>
      </div>
    </template>

    <LogEntryView v-else-if="view === 'previous'" :application="name" :entry="null" :note="asked ? '' : choice?.note" />

    <template v-else-if="view === 'archive'">
      <template v-if="entry !== null">
        <p>
          <NuxtLink :to="logsPath(name, { view: 'archive', kind, deployment, replica })" class="link text-xs text-fg-muted">
            Everything that is kept
          </NuxtLink>
          <span class="mx-1.5 text-fg-faint" aria-hidden="true">/</span>
          <span class="text-xs text-fg-muted">entry {{ entry }}</span>
        </p>
        <LogEntryView :application="name" :entry="entry" />
      </template>
      <LogArchiveList
        v-else
        :application="name"
        :kind="kind"
        :deployment="deployment"
        :replica="replica"
        :deployments="deployments.data.value ?? []"
        @filter="setFilter"
      />
    </template>

    <LogSearch v-else :application="name" :deployments="deployments.data.value ?? []" :replicas="mostReplicas" />
  </div>
</template>
