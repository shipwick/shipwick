<script setup lang="ts">
import type { Application } from '~/types/api'
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

const options = computed(() => {
  const list = apps.data.value ?? []
  const names = list.map(a => a.name)
  if (application.value && !names.includes(application.value)) names.push(application.value)
  return names.sort()
})

const selected = computed(() => (apps.data.value ?? []).find(a => a.name === application.value) ?? null)

// With a single application there is nothing to choose.
watch(() => apps.data.value, (list) => {
  if (!application.value && list && list.length === 1 && list[0]) application.value = list[0].name
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

      <div v-else-if="options.length === 0" class="rounded-sm border border-line">
        <EmptyState title="No applications yet">
          Logs appear here once an application has been deployed.
        </EmptyState>
      </div>

      <template v-else>
        <LogViewer v-if="application" :key="application" :application="application" height-class="h-[calc(100dvh-14rem)] min-h-64" :initial-tail="100">
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
              <select v-model="application" class="input mono !h-7 !w-auto min-w-44 !text-xs">
                <option value="" disabled>Select…</option>
                <option v-for="option in options" :key="option" :value="option">{{ option }}</option>
              </select>
            </label>
          </div>
          <EmptyState title="Choose an application">
            Its replicas are tailed together and merged by time. Each replica keeps its own color.
          </EmptyState>
        </div>
      </template>
    </PageBody>
  </div>
</template>
