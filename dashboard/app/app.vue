<script setup lang="ts">
const route = useRoute()
const session = useSession()

// The list of servers is about none of them: it has no sidebar to show one in.
const layout = computed(() => {
  if (session.multiple.value && !session.selected.value && route.path !== '/login') return 'auth'
  return route.meta.layout === 'auth' ? 'auth' : 'default'
})
</script>

<template>
  <NuxtLayout :name="layout">
    <NuxtPage />
  </NuxtLayout>
</template>
