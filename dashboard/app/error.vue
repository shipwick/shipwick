<script setup lang="ts">
import type { NuxtError } from '#app'

const props = defineProps<{ error: NuxtError }>()

const notFound = computed(() => props.error.statusCode === 404)
useHead({ title: notFound.value ? 'Not found' : 'Error' })
</script>

<template>
  <div class="flex min-h-dvh items-start justify-center bg-subtle px-4 pt-[16vh]">
    <div class="w-full max-w-[26rem] rounded-sm border border-line bg-bg p-5">
      <p class="mono text-xs text-fg-subtle">
        {{ props.error.statusCode }}
      </p>
      <h1 class="mt-1 text-base font-semibold">
        {{ notFound ? 'Page not found' : 'Something went wrong' }}
      </h1>
      <p class="mt-1 text-fg-muted">
        {{ notFound ? 'There is no page at this address.' : props.error.message }}
      </p>
      <UiButton class="mt-4" size="sm" @click="clearError({ redirect: '/' })">
        Go to overview
      </UiButton>
    </div>
  </div>
</template>
