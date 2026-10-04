<script setup lang="ts">
import { describeBackupPlan } from '~/utils/backups'
import { formatBytes, formatCores } from '~/utils/format'
import { tidyDuration } from '~/utils/jobs'
import { limitsNotice } from '~/utils/limits'
import { describeHealth, describeLogging, describeSecurity, formatArgv, formatPathRedirect, hasProxySettings } from '~/utils/spec'
import { applicationPath } from '~/utils/tabs'
import { atLeast07 } from '~/utils/updates'

/**
 * The deploy.yaml the running version was deployed with, laid out to be read.
 * Whoever may deploy the application gets the way to the editor, where the
 * agent hands the document out to be changed; everybody else reads.
 */
const { name, spec, mayDeploy } = useApplication()
const server = useServerInfo()

/**
 * From 0.7 on the agent hands out the document of any application. Before, a
 * changed configuration was a pasted file, and only for an image the agent
 * can pull itself.
 */
const editable = computed(() => mayDeploy.value && spec.value !== null && (atLeast07(server.data.value) || (!spec.value.static && !spec.value.build)))

const envNames = computed(() => Object.keys(spec.value?.env ?? {}).sort())
const healthCheck = computed(() => describeHealth(spec.value?.health))
const process = computed(() => {
  const s = spec.value
  if (!s) return []
  const lines: { label: string, value: string }[] = []
  if (s.entrypoint?.length) lines.push({ label: 'Entrypoint', value: formatArgv(s.entrypoint) })
  if (s.command?.length) lines.push({ label: 'Command', value: formatArgv(s.command) })
  if (s.user) lines.push({ label: 'User', value: s.user })
  return lines
})
/** What the containers go without: the security block, a line for each thing it asks. Empty without the block. */
const security = computed(() => describeSecurity(spec.value?.security))
/** Said next to the limits when Docker on this server applies none of them, or not all; an agent before 0.8 does not say. */
const limitsUnenforced = computed(() => (spec.value ? limitsNotice(server.data.value?.docker?.unenforced_limits, { memory: spec.value.resources.memory_bytes ?? 0, cpu: spec.value.resources.cpu ?? 0 }) : ''))
const logging = computed(() => describeLogging(spec.value))
const loggingOptions = computed(() => Object.entries(spec.value?.logging?.options ?? {}).sort(([a], [b]) => a.localeCompare(b)))
const backupPlan = computed(() => (spec.value?.backups ? describeBackupPlan(spec.value.backups) : ''))
const stopTimeout = computed(() => (spec.value?.deploy.stop_timeout ? tidyDuration(spec.value.deploy.stop_timeout) : ''))

const proxyBlock = computed(() => (hasProxySettings(spec.value) ? spec.value!.proxy! : null))
/** What a whole-application account protects: the application's path, or everything. */
const addressPath = computed(() => spec.value?.path || 'every path')
const proxyHeaders = computed(() => Object.entries(proxyBlock.value?.headers ?? {}).sort(([a], [b]) => a.localeCompare(b)))
</script>

<template>
  <div v-if="!spec" class="rounded-sm border border-line">
    <EmptyState title="No configuration yet">
      The configuration is the deploy.yaml of the version that runs, and nothing has been deployed successfully.
    </EmptyState>
  </div>
  <div v-else class="space-y-8">
    <div class="flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
      <p class="min-w-0 flex-1 basis-72 text-fg-muted">
        The deploy.yaml of the version that runs. Values of environment variables and passwords are never shown.
      </p>
      <UiButton v-if="editable" :to="{ path: '/deploy', query: { application: name } }">
        Change the configuration
      </UiButton>
    </div>

    <!-- A static configuration is a folder and a hostname; the container settings do not exist for it. -->
    <UiPanel v-if="spec.static" title="Configuration">
      <dl class="facts">
        <div>
          <dt class="label">
            Folder
          </dt>
          <dd class="mono mt-0.5">
            {{ spec.static.dir }}/ <span class="font-sans text-fg-muted">— served by the proxy, no container. Relative to deploy.yaml on the machine that deploys.</span>
          </dd>
        </div>
        <div>
          <dt class="label">
            Fallback page
          </dt>
          <dd class="mono mt-0.5">
            <template v-if="spec.static.fallback">
              {{ spec.static.fallback }} <span class="font-sans text-fg-muted">— answered with 200 for a path that names no file, so a single-page application can route it.</span>
            </template>
            <span v-else class="font-sans text-fg-muted">None: a path that names no file is a 404.</span>
          </dd>
        </div>
      </dl>
    </UiPanel>

    <UiPanel v-else title="Configuration">
      <dl class="facts sm:grid-cols-2 lg:grid-cols-3">
        <div>
          <dt class="label">
            Health check
          </dt>
          <dd class="mono mt-0.5">
            <template v-if="healthCheck">
              {{ healthCheck.check }} <span class="text-fg-muted">{{ healthCheck.schedule }}</span>
            </template>
            <template v-else>
              None: replicas only need to stay up
            </template>
          </dd>
        </div>
        <div>
          <dt class="label">
            Limits per replica
          </dt>
          <dd class="mono mt-0.5">
            CPU {{ formatCores(spec.resources.cpu) }} · memory {{ spec.resources.memory_bytes ? formatBytes(spec.resources.memory_bytes) : 'unlimited' }}
            <span v-if="limitsUnenforced" class="mt-1 flex items-start gap-2 font-sans text-xs text-warn">
              <UiIcon name="alert" :size="14" class="mt-px" />
              <span class="min-w-0">{{ limitsUnenforced }}</span>
            </span>
          </dd>
        </div>
        <div>
          <dt class="label">
            Port · replicas
          </dt>
          <dd class="mono mt-0.5">
            {{ spec.port ?? '—' }} · {{ spec.replicas }}
          </dd>
        </div>
        <div>
          <dt class="label">
            Restart policy
          </dt>
          <dd class="mono mt-0.5">
            {{ spec.restart.policy }}
          </dd>
        </div>
        <div>
          <dt class="label">
            Deployment strategy
          </dt>
          <dd class="mono mt-0.5">
            {{ spec.deploy.strategy }}
            <UiTooltip v-if="stopTimeout" class="text-fg-muted" text="deploy.stop_timeout: how long a replica gets to finish after it is asked to stop, before it is killed">· stop timeout {{ stopTimeout }}</UiTooltip>
          </dd>
        </div>
        <div v-if="backupPlan">
          <dt class="label">
            Backups
          </dt>
          <dd class="mt-0.5 break-words">
            {{ backupPlan }}
            <NuxtLink :to="applicationPath(name, 'backups')" class="link ml-1 text-xs text-fg-muted">
              See them
            </NuxtLink>
          </dd>
        </div>
        <div v-if="spec.init">
          <dt class="label">
            Init process
          </dt>
          <dd class="mt-0.5">
            <span class="mono">init: true</span>
            <span class="text-fg-muted">— replicas, jobs and commands run under an init process: a stop signal reaches the application at once, and processes it leaves behind are cleaned up.</span>
          </dd>
        </div>
        <div v-if="security.length > 0">
          <dt class="label">
            <UiTooltip text="security in deploy.yaml: what the replicas, jobs and commands of this application go without. Every key takes away; none adds.">Security</UiTooltip>
          </dt>
          <dd class="mt-0.5">
            <ul class="space-y-0.5">
              <li v-for="line in security" :key="line" class="[overflow-wrap:anywhere]">
                {{ line }}
              </li>
            </ul>
          </dd>
        </div>
        <div v-if="process.length > 0">
          <dt class="label">
            Process
          </dt>
          <dd class="mt-0.5 space-y-0.5">
            <div v-for="line in process" :key="line.label" class="flex gap-2">
              <span class="w-20 shrink-0 text-fg-muted">{{ line.label }}</span>
              <span class="mono min-w-0 [overflow-wrap:anywhere]">{{ line.value }}</span>
            </div>
          </dd>
        </div>
        <div v-if="spec.pre_deploy">
          <dt class="label">
            <UiTooltip text="pre_deploy: runs in a one-off container before any replica is replaced; the deployment fails if it does">Before each deployment</UiTooltip>
          </dt>
          <dd class="mono mt-0.5 break-all">
            {{ formatArgv(spec.pre_deploy.command) }} <span class="text-fg-muted">timeout {{ tidyDuration(spec.pre_deploy.timeout) }}</span>
          </dd>
        </div>
        <div v-if="logging">
          <dt class="label">
            Logging
          </dt>
          <dd class="mono mt-0.5">
            {{ logging }}
            <details v-if="loggingOptions.length > 0" class="mt-1 font-sans text-xs">
              <summary class="cursor-pointer select-none text-fg-subtle hover:text-fg">
                Options
              </summary>
              <dl class="mono mt-1 grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-0.5">
                <template v-for="[key, value] in loggingOptions" :key="key">
                  <dt class="text-fg-muted">
                    {{ key }}
                  </dt>
                  <dd class="break-all">
                    {{ value }}
                  </dd>
                </template>
              </dl>
            </details>
          </dd>
        </div>
        <div>
          <dt class="label">
            <UiTooltip :text="envNames.length ? 'Values are never returned by the agent' : null">Environment</UiTooltip>
          </dt>
          <dd class="mono mt-0.5 break-words">
            {{ envNames.length ? envNames.join(', ') : 'No variables' }}
          </dd>
        </div>
      </dl>
    </UiPanel>

    <!-- The proxy block of deploy.yaml, read-only. Passwords are masked by the agent and are not shown at all. -->
    <UiPanel v-if="proxyBlock || spec.path" title="Proxy">
      <dl class="facts sm:grid-cols-2">
        <div v-if="spec.path">
          <dt class="label">
            Path
          </dt>
          <dd class="mt-0.5">
            <span class="mono">{{ spec.path }}</span>
            <span class="text-fg-muted">
              — {{ proxyBlock?.strip_prefix ? 'removed before a request reaches the application, which sees / where the visitor asked for' : 'passed on as it is; the application sees' }}
              <span class="mono">{{ spec.path }}</span>. Other applications may serve the rest of the domain.
            </span>
          </dd>
        </div>
        <div v-if="proxyHeaders.length > 0">
          <dt class="label">
            Response headers
          </dt>
          <dd class="mono mt-0.5 space-y-0.5">
            <div v-for="[header, value] in proxyHeaders" :key="header" class="break-all">
              <span class="text-fg-muted">{{ header }}:</span> {{ value }}
            </div>
          </dd>
        </div>
        <div v-if="proxyBlock?.basic_auth?.length">
          <dt class="label">
            Password protection
          </dt>
          <dd class="mt-0.5 space-y-0.5">
            <div v-for="(account, index) in proxyBlock.basic_auth" :key="index" class="flex flex-wrap items-baseline gap-x-2">
              <span class="mono break-all">{{ account.path || addressPath }}</span>
              <span class="text-fg-muted">asks for the password of</span>
              <span class="mono break-all">{{ account.username }}</span>
            </div>
            <p class="text-xs text-fg-subtle">
              Passwords are stored encrypted and never returned.
            </p>
          </dd>
        </div>
        <div v-if="proxyBlock?.redirects?.length">
          <dt class="label">
            Redirects
          </dt>
          <dd class="mono mt-0.5 space-y-0.5">
            <div v-for="redirect in proxyBlock.redirects" :key="redirect.from" class="break-all">
              {{ formatPathRedirect(redirect) }}
            </div>
          </dd>
        </div>
      </dl>
    </UiPanel>

    <p class="text-xs text-fg-subtle">
      <template v-if="editable">
        To change it, edit the file in the project and run <span class="mono text-fg-muted">shipwick deploy</span>, or
        <NuxtLink :to="{ path: '/deploy', query: { application: name } }" class="link">
          change it here
        </NuxtLink>: the server hands the document out, with a reference where a value came from a stored secret and <span class="mono text-fg-muted">"********"</span> where it did not.
      </template>
      <template v-else-if="mayDeploy">
        To change it, edit the file in the project and run <span class="mono text-fg-muted">shipwick deploy</span>.
      </template>
      <template v-else>
        It is changed by whoever may deploy this application, with <span class="mono text-fg-muted">shipwick deploy</span> or from this page.
      </template>
    </p>
  </div>
</template>
