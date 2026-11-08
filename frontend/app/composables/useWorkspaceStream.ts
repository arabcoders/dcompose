import { onScopeDispose, ref } from 'vue'
import { useRealtimeStore } from '~/store/realtime'
import type { RealtimeReader, RealtimeSubscription, WorkspaceStreamMessage } from '~/types'

export const useWorkspaceStream = (
  onMessage: (message: WorkspaceStreamMessage) => void,
  onDisconnect: () => void = () => {},
  onRejected: (status: number) => void = () => {},
) => {
  const realtime = useRealtimeStore()
  const error = ref('')
  let reader: RealtimeReader | null = null
  let selection: RealtimeSubscription | null = null

  const stop = () => {
    reader?.stop()
    reader = null
    selection = null
  }

  const start = (subscription: Exclude<RealtimeSubscription, { type: 'shell' }>) => {
    if (reader && JSON.stringify(selection) === JSON.stringify(subscription)) {
      return
    }
    stop()
    error.value = ''
    selection = subscription
    reader = realtime.subscribe(subscription, {
      onMessage: (message) => {
        if (!('type' in message)) {
          error.value = ''
          onMessage(message)
        }
      },
      onDisconnect: (message) => {
        error.value = message
        onDisconnect()
      },
      onRejected,
    })
  }

  onScopeDispose(stop)
  return { error, start, stop }
}
