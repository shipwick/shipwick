<script setup lang="ts">
/** Every attempt to deploy this application, successful or not, newest first. */
const { name, deployments } = useApplication()

const LIMIT = 25
</script>

<template>
  <div>
    <div class="overflow-hidden rounded-sm border border-line">
      <TableSkeleton v-if="deployments.loading.value" :rows="6" :columns="5" />
      <ErrorState
        v-else-if="deployments.error.value && !deployments.data.value"
        :error="deployments.error.value"
        subject="deployments"
        :retrying="deployments.refreshing.value"
        @retry="deployments.refresh()"
      />
      <EmptyState v-else-if="(deployments.data.value?.length ?? 0) === 0" title="No deployments yet">
        Run <span class="mono text-fg">shipwick deploy</span> from the project directory. Every attempt is kept here, successful or not.
      </EmptyState>
      <div v-else class="overflow-x-auto">
        <DeploymentsTable :deployments="deployments.data.value ?? []" :show-application="false" />
      </div>
    </div>
    <p v-if="(deployments.data.value?.length ?? 0) >= LIMIT" class="mt-3 text-xs text-fg-subtle">
      Showing the newest {{ LIMIT }}.
      <NuxtLink :to="{ path: '/deployments', query: { application: name } }" class="link">
        See further back
      </NuxtLink>
    </p>
  </div>
</template>
