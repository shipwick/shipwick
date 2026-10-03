<script setup lang="ts">
/** Scheduled jobs, their runs, and one-off commands: everything that runs beside the replicas. */
const { name, detail, spec, gone, hasActive, isStatic, busy, mayDeploy } = useApplication()
</script>

<template>
  <div v-if="isStatic || !hasActive || !spec" class="rounded-sm border border-line">
    <EmptyState v-if="isStatic" :title="`${name} has nothing to run`">
      It is a folder served by the proxy. Jobs and commands run in a container of an application's image, and there is none.
    </EmptyState>
    <EmptyState v-else title="Nothing is deployed">
      Jobs and one-off commands run in a container of the deployed image. They are available after the first successful deployment.
    </EmptyState>
  </div>
  <JobsSection
    v-else-if="!gone"
    :application="name"
    :spec="spec"
    :image="detail.image"
    :may-deploy="mayDeploy"
    :busy="busy"
  />
</template>
