<script setup lang="ts">
import type { Application } from '~/types/api'
import { applicationStatusDisplay, sortBySeverity } from '~/utils/status'

useHead({ title: 'Applications' })

const agent = useAgent()
const router = useRouter()
const apps = usePolling<Application[]>(signal => agent.get<Application[]>('/applications', { signal }))

const query = ref('')

const rows = computed(() => {
  const needle = query.value.trim().toLowerCase()
  const list = apps.data.value ?? []
  const filtered = needle === ''
    ? list
    : list.filter(a => a.name.includes(needle) || a.domain.toLowerCase().includes(needle) || a.version.toLowerCase().includes(needle))
  return sortBySeverity(filtered)
})

function open(app: Application, event: MouseEvent) {
  if ((event.target as HTMLElement).closest('a') || window.getSelection()?.toString()) return
  void router.push(`/applications/${app.name}`)
}

function replicaTone(app: Application): string {
  if (app.replicas.desired === 0) return 'text-fg-faint'
  // Stopped on request: zero healthy replicas is the intended state, not an alarm.
  if (app.status === 'STOPPED') return 'text-fg-muted'
  if (app.replicas.healthy >= app.replicas.desired) return 'text-fg'
  return app.replicas.healthy === 0 ? 'text-danger' : 'text-warn'
}
</script>

<template>
  <div>
    <PageHeader :crumbs="[{ label: 'Applications' }]">
      <template #actions>
        <label class="relative flex items-center">
          <span class="sr-only">Filter applications</span>
          <UiIcon name="search" :size="14" class="pointer-events-none absolute left-2 text-fg-subtle" />
          <input v-model="query" type="search" placeholder="Filter" class="input !h-7 w-44 !pl-7 !text-xs" spellcheck="false" autocomplete="off">
        </label>
      </template>
    </PageHeader>

    <PageBody>
      <StaleNotice :error="apps.data.value ? apps.error.value : null" :updated-at="apps.updatedAt.value" />

      <div class="overflow-hidden rounded-sm border border-line">
        <TableSkeleton v-if="apps.loading.value" :rows="6" :columns="6" />
        <ErrorState
          v-else-if="apps.error.value && !apps.data.value"
          :error="apps.error.value"
          subject="applications"
          :retrying="apps.refreshing.value"
          @retry="apps.refresh()"
        />
        <EmptyState v-else-if="(apps.data.value?.length ?? 0) === 0" title="No applications yet">
          Applications appear here after their first deployment. In your project directory, run
          <span class="mono text-fg">shipwick init</span> and then <span class="mono text-fg">shipwick deploy</span>.
        </EmptyState>
        <EmptyState v-else-if="rows.length === 0" :title="`No application matches “${query.trim()}”`" />
        <div v-else class="overflow-x-auto">
          <table class="data-table stack">
            <thead>
              <tr>
                <th>Name</th>
                <th>Status</th>
                <th>Version</th>
                <th>Replicas</th>
                <th>Domain</th>
                <th class="right">
                  Updated
                </th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="app in rows" :key="app.name" class="clickable" @click="open(app, $event)">
                <td data-primary>
                  <NuxtLink :to="`/applications/${app.name}`" class="mono font-medium hover:underline">{{ app.name }}</NuxtLink>
                </td>
                <td data-label="Status">
                  <span class="inline-flex items-center gap-2">
                    <StatusBadge v-bind="applicationStatusDisplay(app.status)" :raw="app.status" />
                    <template v-if="app.deploying">
                      <NuxtLink
                        v-if="app.in_flight_deployment_id"
                        :to="`/deployments/${app.in_flight_deployment_id}`"
                        class="label !text-warn underline decoration-warn-line underline-offset-2"
                        title="Open the deployment in progress"
                      >{{ app.status === 'DEPLOYING' ? 'Watch' : 'Deploying' }}</NuxtLink>
                      <span v-else-if="app.status !== 'DEPLOYING'" class="label !text-warn">Deploying</span>
                    </template>
                  </span>
                </td>
                <td data-label="Version" class="mono">
                  {{ app.version || '—' }}
                </td>
                <td data-label="Replicas" class="mono" :class="replicaTone(app)">
                  <span v-if="app.replicas.desired > 0">
                    {{ app.replicas.healthy }}/{{ app.replicas.desired }} <span class="font-sans text-fg-subtle">healthy</span>
                  </span>
                  <span v-else>—</span>
                </td>
                <td data-label="Domain">
                  <a
                    v-if="app.domain"
                    :href="`https://${app.domain}`"
                    target="_blank"
                    rel="noopener noreferrer"
                    class="mono inline-flex items-center gap-1 text-fg-muted hover:text-fg hover:underline"
                  >{{ app.domain }}<UiIcon name="external" :size="12" /></a>
                  <span v-else class="text-fg-faint">—</span>
                </td>
                <td data-label="Updated" class="right text-fg-muted">
                  <TimeAgo :time="app.updated_at" />
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </PageBody>
  </div>
</template>
