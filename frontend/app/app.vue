<script setup lang="ts">
import { NuxtLayout, NuxtPage } from '#components'
import { useHead } from '#app'
import UApp from '@nuxt/ui/components/App.vue'

useHead({ titleTemplate: (title) => (title ? `${title} - DCompose` : 'DCompose') })
</script>

<template>
  <UApp>
    <NuxtLayout>
      <NuxtPage />
    </NuxtLayout>
  </UApp>
</template>
