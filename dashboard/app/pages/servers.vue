<script setup lang="ts">
/**
 * `/servers` is the server's page: its status and the tabs below it. A
 * dashboard configured with several servers answers the bare address with
 * the list of them instead; `/servers?server=<name>` is that server's page.
 */
const session = useSession()

const listing = computed(() => session.multiple.value && !session.selected.value)

useHead({ title: () => (listing.value ? 'Servers' : 'Server') })
</script>

<template>
  <ServerList v-if="listing" />
  <ServerFrame v-else />
</template>
