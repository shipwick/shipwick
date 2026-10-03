<script setup lang="ts">
import type { Application, ApplicationDetail } from '~/types/api'
import { hasNoLogs, logApplications } from '~/utils/logs'
import { shippedLogDriver } from '~/utils/spec'
import { applicationStatusDisplay } from '~/utils/status'

useHead({ title: 'Logs' })

const route = useRoute()
const router = useRouter()
const agent = useAgent()

const apps = usePolling<Application[]>(signal => agent.get<Application[]>('/applications', { signal }), { interval: 15_000 })

/** The selected application lives in the URL: /logs?application=my-api */
const application = computed({
  get: () => (typeof route.query.application === 'string' ? route.query.application : ''),
  set: (value: string) => {
    void router.replace({ query: value ? { application: value } : {} })
  },
})

// A static application has no containers and so no logs: it is not offered.
const options = computed(() => logApplications(apps.data.value ?? [], application.value))
/** One was named in the URL all the same (an old link, a typed address). */
const staticSelected = computed(() => hasNoLogs(apps.data.value ?? [], application.value))

const selected = computed(() => (apps.data.value ?? []).find(a => a.name === application.value) ?? null)

// The list items carry no spec; the detail says whether the logs are shipped
// to a remote driver, in which case the viewer warns that it shows Docker's copy.
const detail = usePolling<ApplicationDetail | null>(
  signal => (application.value ? agent.get<ApplicationDetail>(`/applications/${encodeURIComponent(application.value)}`, { signal }) : Promise.resolve(null)),
  { interval: 60_000, enabled: () => application.value !== '' && !staticSelected.value },
)
watch(application, () => void detail.reset())
const shippedTo = computed(() => shippedLogDriver(detail.data.value?.spec))

// With a single application that has logs there is nothing to choose.
watch(options, (names) => {
  if (!application.value && names.length === 1 && names[0]) application.value = names[0]
})
</script>

<template>
  <div>
    <PageHeader :crumbs="[{ label: 'Logs' }]" />
    <PageBody>
      <div v-if="apps.loading.value && !apps.data.value" class="rounded-sm border border-line">
        <TableSkeleton :rows="8" :columns="2" />
      </div>

      <div v-else-if="apps.error.value && !apps.data.value" class="rounded-sm border border-line">
        <ErrorState :error="apps.error.value" subject="applications" :retrying="apps.refreshing.value" @retry="apps.refresh()" />
      </div>

      <div v-else-if="options.length === 0 && !staticSelected" class="rounded-sm border border-line">
        <EmptyState v-if="(apps.data.value?.length ?? 0) === 0" title="No applications yet">
          Logs appear here once an application has been deployed.
        </EmptyState>
        <EmptyState v-else title="No application has logs">
          Every application on this server is a folder served by the proxy. Logs are what containers write, and there are none.
        </EmptyState>
      </div>

      <template v-else>
        <LogViewer v-if="application && !staticSelected" :key="application" :application="application" height-class="h-[calc(100dvh-14rem)] min-h-64" :initial-tail="100" :shipped-to="shippedTo">
          <template #leading>
            <label class="flex items-center gap-1.5 text-xs text-fg-muted">
              <span class="sr-only">Application</span>
              <select v-model="application" class="input mono !h-7 !w-auto min-w-36 !text-xs">
                <option v-for="option in options" :key="option" :value="option">{{ option }}</option>
              </select>
            </label>
            <StatusBadge v-if="selected" v-bind="applicationStatusDisplay(selected.status)" :raw="selected.status" />
          </template>
        </LogViewer>

        <div v-else class="rounded-sm border border-line">
          <div class="flex flex-wrap items-center gap-3 border-b border-line bg-subtle px-3 py-2">
            <label class="flex items-center gap-2 text-xs text-fg-muted">
              Application
              <select :value="staticSelected ? '' : application" class="input mono !h-7 !w-auto min-w-44 !text-xs" @change="application = ($event.target as HTMLSelectElement).value">
                <option value="" disabled>Select…</option>
                <option v-for="option in options" :key="option" :value="option">{{ option }}</option>
              </select>
            </label>
          </div>
          <EmptyState v-if="staticSelected" :title="`${application} has no logs`">
            It is a folder served by the proxy: there is no container to write any. What was asked of it is under Traffic on
            <NuxtLink :to="`/applications/${application}`" class="link">its page</NuxtLink>.
          </EmptyState>
          <EmptyState v-else title="Choose an application">
            Its replicas are tailed together and merged by time. Each replica keeps its own color.
          </EmptyState>
        </div>
      </template>
    </PageBody>
  </div>
</template>
