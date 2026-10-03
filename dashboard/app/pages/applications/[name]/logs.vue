<script setup lang="ts">
import { shippedLogDriver } from '~/utils/spec'
import { applicationPath } from '~/utils/tabs'

/** The replicas' output, tailed together. The Logs page shows the same for any application. */
const { name, detail, spec, isStatic } = useApplication()

const shippedTo = computed(() => shippedLogDriver(spec.value))
</script>

<template>
  <div>
    <LogViewer
      v-if="!isStatic && detail.containers.length > 0"
      :application="name"
      height-class="h-[calc(100dvh-19rem)] min-h-72"
      :shipped-to="shippedTo"
    />
    <div v-else class="rounded-sm border border-line">
      <EmptyState v-if="isStatic" :title="`${name} has no logs`">
        It is a folder served by the proxy: there is no container to write any. What was asked of it is under
        <NuxtLink :to="applicationPath(name, 'metrics')" class="link">
          Traffic
        </NuxtLink>.
      </EmptyState>
      <EmptyState v-else title="No logs to read">
        <template v-if="detail.status === 'STOPPED'">
          The application is stopped, so it has no containers. Start it to see what it writes; what happened before is under Events on the overview.
        </template>
        <template v-else>
          There are no containers to read logs from.
        </template>
      </EmptyState>
    </div>
  </div>
</template>
