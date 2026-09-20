<script setup lang="ts">
import type { Application, Deployment } from '~/types/api'

useHead({ title: 'Deployments' })

const route = useRoute()
const router = useRouter()
const agent = useAgent()

const LIMIT = 100

/** The filter lives in the URL, so a filtered view can be linked to and survives reloads. */
const application = computed({
  get: () => (typeof route.query.application === 'string' ? route.query.application : ''),
  set: (value: string) => {
    void router.replace({ query: value ? { application: value } : {} })
  },
})

const deployments = usePolling<Deployment[]>(signal => agent.get<Deployment[]>('/deployments', {
  query: { application: application.value, limit: LIMIT },
  signal,
}))

// Only for the filter's options; refreshed slowly.
const apps = usePolling<Application[]>(signal => agent.get<Application[]>('/applications', { signal }), { interval: 30_000 })

const options = computed(() => {
  const names = new Set((apps.data.value ?? []).map(a => a.name))
  // A filter from the URL stays selectable even if that application is gone.
  if (application.value) names.add(application.value)
  return [...names].sort()
})

watch(application, () => void deployments.reset())
</script>

<template>
  <div>
    <PageHeader :crumbs="[{ label: 'Deployments' }]">
      <template #actions>
        <label class="flex items-center gap-2 text-xs text-fg-muted">
          Application
          <select v-model="application" class="input mono !h-7 !w-auto min-w-36 !text-xs">
            <option value="">All</option>
            <option v-for="option in options" :key="option" :value="option">{{ option }}</option>
          </select>
        </label>
      </template>
    </PageHeader>

    <PageBody>
      <StaleNotice :error="deployments.data.value ? deployments.error.value : null" :updated-at="deployments.updatedAt.value" />

      <div class="overflow-hidden rounded-sm border border-line">
        <TableSkeleton v-if="deployments.loading.value" :rows="8" :columns="6" />
        <ErrorState
          v-else-if="deployments.error.value && !deployments.data.value"
          :error="deployments.error.value"
          subject="deployments"
          :retrying="deployments.refreshing.value"
          @retry="deployments.refresh()"
        />
        <EmptyState v-else-if="(deployments.data.value?.length ?? 0) === 0" :title="application ? `No deployments of ${application}` : 'No deployments yet'">
          <template v-if="!application">
            Every deployment attempt is kept here, successful or not.
          </template>
        </EmptyState>
        <div v-else class="overflow-x-auto">
          <DeploymentsTable :deployments="deployments.data.value ?? []" :show-application="application === ''" />
        </div>
      </div>

      <p v-if="(deployments.data.value?.length ?? 0) >= LIMIT" class="mt-3 text-xs text-fg-subtle">
        Showing the newest {{ LIMIT }}. Filter by application to see further back.
      </p>
    </PageBody>
  </div>
</template>
