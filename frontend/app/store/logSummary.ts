import { defineStore } from 'pinia'
import { ref, watch } from 'vue'
import { useAuthStore } from '~/store/auth'
import type { LogEntry } from '~/types'

export const useLogSummaryStore = defineStore('logSummary', () => {
  const auth = useAuthStore()
  const entries = ref<Array<LogEntry>>([])
  const cursor = ref(0)
  const reset = () => {
    entries.value = []
    cursor.value = 0
  }
  watch(
    () => auth.authenticated,
    (authenticated) => {
      if (!authenticated) {
        reset()
      }
    },
    { flush: 'sync' },
  )

  const set = (incoming: Array<LogEntry>, latest: number) => {
    if (!auth.authenticated) {
      return
    }
    entries.value = incoming.slice(-5)
    cursor.value = latest
  }

  const append = (entry: LogEntry) => {
    if (!auth.authenticated) {
      return
    }
    entries.value = [...entries.value, entry].slice(-5)
    cursor.value = Math.max(cursor.value, entry.id)
  }

  return { entries, cursor, set, append, reset }
})
