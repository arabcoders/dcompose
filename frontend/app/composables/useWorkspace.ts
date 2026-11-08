import { storeToRefs } from 'pinia'
import { useWorkspaceStore } from '~/store/workspace'

export const useWorkspace = () => {
  const store = useWorkspaceStore()
  const { nodes, ready, error, loading, streamError } = storeToRefs(store)
  return { nodes, ready, error, loading, streamError, load: store.load }
}
