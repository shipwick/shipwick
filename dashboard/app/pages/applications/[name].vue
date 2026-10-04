<script setup lang="ts">
import type { AgentEvent, ApplicationDetail, Deployment } from '~/types/api'
import type { AgentError } from '~/utils/agentError'
import { toAgentError } from '~/utils/agentError'
import { deployHint as whyNoDeploy, readOnlyReason } from '~/utils/access'
import { rollbackCandidates } from '~/utils/deployments'
import { headline as describeState } from '~/utils/diagnosis'
import { tidyDuration } from '~/utils/jobs'
import { roleHint } from '~/utils/roles'
import { addressOf, applicationUrl } from '~/utils/spec'
import { applicationStatusDisplay } from '~/utils/status'
import { applicationPath, applicationTabs, tabTitle } from '~/utils/tabs'

/**
 * The frame of an application's page: who it is and how it is, the actions,
 * and the tabs. Each tab is a page of its own below this one and reads what
 * is polled here through useApplication().
 */
const route = useRoute()
const router = useRouter()
const agent = useAgent()
const access = useAccess()

const name = computed(() => String(route.params.name))
const path = computed(() => `/applications/${encodeURIComponent(name.value)}`)


// --- data ---------------------------------------------------------------------

// Follow a deployment in flight more closely: replicas come and go within seconds.
const deployingNow = ref(false)
const app = usePolling<ApplicationDetail>(signal => agent.get<ApplicationDetail>(path.value, { signal }), {
  interval: () => (deployingNow.value ? 2000 : 5000),
})
watch(() => app.data.value?.deploying, (deploying) => {
  deployingNow.value = Boolean(deploying)
})
const deployments = usePolling<Deployment[]>(signal => agent.get<Deployment[]>('/deployments', {
  query: { application: name.value, limit: 25 },
  signal,
}))
const events = usePolling<AgentEvent[]>(signal => agent.get<AgentEvent[]>(`${path.value}/events`, { query: { limit: 30 }, signal }))

const gone = computed(() => app.error.value?.notFound === true)
const hasActive = computed(() => Boolean(app.data.value?.active_deployment))
// A folder served by the proxy: no containers, so the agent answers 409 STATIC_APPLICATION to logs, metrics, jobs and run. Do not ask.
const isStatic = computed(() => app.data.value?.static === true)

watch(name, () => {
  void app.reset()
  void deployments.reset()
  void events.reset()
})

function refreshAll() {
  void app.refresh()
  void deployments.refresh()
  void events.refresh()
}

// --- live deployment progress ---------------------------------------------------

const progress = useDeploymentProgress(() => refreshAll())
let dismissedId: number | null = null

// A deployment started elsewhere (CLI, CI, another tab) is picked up as well:
// the application itself says which deployment is in flight.
watch(() => app.data.value, (a) => {
  if (!a || progress.active.value) return
  const id = a.in_flight_deployment_id
  if (id !== null && id !== progress.progress.value?.deploymentId && id !== dismissedId) progress.followId(id)
})

/** The narration is on the overview: a deployment started from another tab is watched there. */
function onStarted(deployment: Deployment) {
  progress.follow(deployment)
  refreshAll()
  if (route.path !== path.value) void router.push(path.value)
  window.scrollTo({ top: 0, behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' })
}

function dismissProgress() {
  dismissedId = progress.progress.value?.deploymentId ?? null
  progress.dismiss()
}

// --- actions ------------------------------------------------------------------

const dialog = ref<'deploy' | 'rollback' | 'stop' | 'delete' | null>(null)
const actionPending = ref(false)
const actionError = shallowRef<AgentError | null>(null)

const busy = computed(() => Boolean(app.data.value?.deploying) || progress.active.value)
const stopped = computed(() => app.data.value?.desired_state === 'stopped')
const rollbackTargets = computed(() => rollbackCandidates(deployments.data.value ?? [], name.value))

// What the signed-in token may do; the agent decides for real and a refusal is rendered where it happens.
// A deploy token may be limited to some applications: it is asked about this one, not only about its role.
const mayDeploy = computed(() => access.canDeploy(name.value))
const mayAdmin = computed(() => access.can('admin'))
const deployHint = computed(() => whyNoDeploy(access.token.value, name.value))
const readOnly = computed(() => readOnlyReason(access.token.value, name.value))

async function setRunning(run: boolean) {
  actionPending.value = true
  actionError.value = null
  try {
    app.data.value = await agent.post<ApplicationDetail>(`${path.value}/${run ? 'start' : 'stop'}`)
    dialog.value = null
    void events.refresh()
  }
  catch (cause) {
    actionError.value = toAgentError(cause)
  }
  finally {
    actionPending.value = false
  }
}

function closeDialog() {
  dialog.value = null
  actionError.value = null
}

// --- presentation -------------------------------------------------------------

const status = computed(() => (app.data.value ? applicationStatusDisplay(app.data.value.status) : null))
const spec = computed(() => app.data.value?.spec ?? null)
const state = computed(() => (app.data.value ? describeState(app.data.value) : ''))

const tabs = computed(() => applicationTabs({
  name: name.value,
  static: isStatic.value,
  volumes: (spec.value?.volumes?.length ?? 0) > 0,
}))

// Each tab is a page: its name is in the title.
useHead({ title: () => tabTitle(tabs.value, route.path, name.value) })

/** What the stop dialog names: the address visitors use, path included. */
const address = computed(() => (app.data.value?.domain ? addressOf(app.data.value.domain, app.data.value.path) : ''))
const addressUrl = computed(() => (app.data.value ? applicationUrl(app.data.value) : null))
const stopTimeout = computed(() => (spec.value?.deploy.stop_timeout ? tidyDuration(spec.value.deploy.stop_timeout) : ''))

provideApplication({
  name,
  detail: computed(() => app.data.value!),
  spec,
  app,
  deployments,
  events,
  gone,
  hasActive,
  isStatic,
  busy,
  stoppedNow: computed(() => stopped.value && app.data.value?.replicas.running === 0),
  mayDeploy,
  deployHint,
  mayAdmin,
  progress: progress.progress,
  dismissProgress,
  refreshAll,
  setRunning,
})
</script>

<template>
  <div>
    <PageHeader :crumbs="[{ label: 'Applications', to: '/applications' }, { label: name, mono: true }]">
      <template v-if="app.data.value" #actions>
        <UiButton
          variant="primary"
          size="sm"
          :disabled="busy || !hasActive || !mayDeploy"
          :hint="deployHint ?? (!hasActive ? 'Nothing deployed yet. The first deployment needs shipwick deploy.' : busy ? 'A deployment is in progress' : 'Deploy another version of the image with the same configuration')"
          @click="dialog = 'deploy'"
        >
          Deploy
        </UiButton>
        <UiButton
          size="sm"
          :disabled="busy || rollbackTargets.length === 0 || !mayDeploy"
          :hint="deployHint ?? (rollbackTargets.length === 0 ? 'No earlier successful deployment to go back to' : 'Go back to an earlier deployment')"
          @click="dialog = 'rollback'"
        >
          Roll back
        </UiButton>
        <UiButton
          v-if="stopped"
          size="sm"
          :disabled="busy || !hasActive || !mayDeploy"
          :hint="deployHint"
          :pending="actionPending && dialog === null"
          @click="setRunning(true)"
        >
          <UiIcon name="play" :size="12" />
          Start
        </UiButton>
        <UiButton v-else size="sm" :disabled="busy || !hasActive || !mayDeploy" :hint="deployHint" @click="dialog = 'stop'">
          <UiIcon name="pause" :size="12" />
          Stop
        </UiButton>
        <UiButton variant="danger" size="sm" :disabled="busy || !mayAdmin" :hint="mayAdmin ? undefined : roleHint('admin')" @click="dialog = 'delete'">
          <UiIcon name="trash" :size="12" />
          Delete
        </UiButton>
      </template>
    </PageHeader>

    <PageBody>
      <!-- Loading -->
      <div v-if="app.loading.value && !app.data.value" class="space-y-3" role="progressbar" aria-busy="true" aria-label="Loading">
        <span class="skeleton h-6 w-40" />
        <span class="skeleton w-24" />
        <span class="skeleton w-64" />
      </div>

      <!-- Not found / unreachable with nothing to show -->
      <div v-else-if="!app.data.value && app.error.value" class="rounded-sm border border-line">
        <EmptyState v-if="gone" :title="`No application named ${name}`">
          It may have been deleted. <NuxtLink to="/applications" class="link">
            Back to applications
          </NuxtLink>
        </EmptyState>
        <ErrorState v-else :error="app.error.value" subject="the application" :retrying="app.refreshing.value" @retry="refreshAll" />
      </div>

      <div v-else-if="app.data.value && status" class="space-y-6">
        <!-- Who it is and how it is: the same on every tab -->
        <div class="min-w-0">
          <div class="flex flex-wrap items-center gap-x-4 gap-y-1">
            <p class="mono truncate text-xl font-semibold">
              {{ app.data.value.name }}
            </p>
            <StatusBadge :tone="status.tone" :label="status.label" :raw="app.data.value.status" size="md" />
            <UiTooltip v-if="app.data.value.deploying && app.data.value.status !== 'DEPLOYING'" :to="path" class="label !text-warn underline decoration-warn-line underline-offset-2" text="Watch the deployment on the overview">
              Deploying
            </UiTooltip>
          </div>
          <p class="mt-1.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-fg-muted">
            <span>{{ state }}</span>
            <template v-if="app.data.value.active_deployment">
              <span class="text-fg-faint" aria-hidden="true">·</span>
              <span>
                <span class="mono text-fg">{{ app.data.value.version || '—' }}</span>
                deployed <TimeAgo :time="app.data.value.active_deployment.completed_at ?? app.data.value.active_deployment.started_at" />
                as <NuxtLink :to="`/deployments/${app.data.value.active_deployment.id}`" class="mono link">#{{ app.data.value.active_deployment.sequence }}</NuxtLink>
              </span>
            </template>
            <template v-if="address">
              <span class="text-fg-faint" aria-hidden="true">·</span>
              <a v-if="addressUrl" :href="addressUrl" target="_blank" rel="noopener noreferrer" class="mono link inline-flex min-w-0 items-center gap-1 break-all">{{ address }}<UiIcon name="external" :size="12" /><span class="sr-only">(opens in a new tab)</span></a>
              <span v-else class="mono break-all">{{ address }}</span>
            </template>
          </p>
          <!-- Said once, in words: a disabled button's tooltip is out of reach on a phone. -->
          <p v-if="readOnly" class="mt-2 flex items-start gap-1.5 text-xs text-fg-subtle">
            <UiIcon name="lock" :size="12" class="mt-[3px]" />
            {{ readOnly }}
          </p>
        </div>

        <UiTabs :tabs="tabs" :label="`Sections of ${name}`" />

        <StaleNotice v-if="!gone" :error="app.error.value" :updated-at="app.updatedAt.value" class="!mb-0" />
        <p v-if="gone" class="rounded-sm border border-danger-line bg-danger-bg px-3 py-2 text-xs text-danger" role="alert">
          This application no longer exists on the agent. <NuxtLink to="/applications" class="underline">
            Back to applications
          </NuxtLink>
        </p>
        <InlineError v-if="dialog === null" :error="actionError" />

        <NuxtPage />
      </div>
    </PageBody>

    <template v-if="app.data.value">
      <DeployDialog :open="dialog === 'deploy'" :application="app.data.value" @close="closeDialog" @started="onStarted" />
      <RollbackDialog
        :open="dialog === 'rollback'"
        :application="app.data.value"
        :deployments="deployments.data.value ?? []"
        @close="closeDialog"
        @started="onStarted"
        @stale="deployments.refresh()"
      />
      <ConfirmDialog
        :open="dialog === 'stop'"
        :title="`Stop ${name}`"
        confirm-label="Stop application"
        danger
        :pending="actionPending"
        :error="actionError"
        @close="closeDialog"
        @confirm="setRunning(false)"
      >
        <template v-if="isStatic">
          <span class="mono text-fg">{{ address }}</span> answers 503 instead of the files<template v-if="app.data.value.redirects?.length">
            (its redirects keep working)
          </template>.
        </template>
        <template v-else>
          All replicas are stopped<template v-if="app.data.value.domain">
            and <span class="mono text-fg">{{ address }}</span> stops answering<template v-if="app.data.value.redirects?.length">
              (its redirects keep working)
            </template>
          </template>.
        </template>
        The application stays stopped, across agent restarts too, until you start it again. Nothing is deleted.
        <template v-if="stopTimeout && !isStatic">
          Each replica gets {{ stopTimeout }} to finish what it is doing, so this can take that long to answer.
        </template>
      </ConfirmDialog>
      <DeleteDialog :open="dialog === 'delete'" :name="name" @close="closeDialog" @deleted="router.replace('/applications')" />
    </template>
  </div>
</template>
