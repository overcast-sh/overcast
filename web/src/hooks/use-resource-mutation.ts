import { useMutation, type UseMutationOptions, type QueryKey } from "@tanstack/react-query"
import { useToast } from "@/components/ui/toast"

interface ResourceMutationConfig<TData, TError extends Error, TVariables, TContext> {
  /** Base mutation options (from data.ts mutation option factories). */
  options: UseMutationOptions<TData, TError, TVariables, TContext>
  /** Query key prefixes to invalidate on success. */
  invalidateKeys?: QueryKey[]
  /** Toast title shown on success. */
  successTitle?: string
  /** Derive the toast description from the mutation variables. */
  successDescription?: (variables: TVariables) => string
  /** Toast variant on success. Defaults to "success". */
  successVariant?: "default" | "success" | "danger"
  /** Toast title shown on error. Defaults to "Operation failed". */
  errorTitle?: string
  /**
   * False when the page shows the error itself, where it happened — beside
   * the input that caused it — so a toast would say it twice.
   */
  errorToast?: boolean
  /** Additional callback after invalidation + toast. */
  onSuccess?: (data: TData, variables: TVariables) => void
}

/**
 * Wires a TanStack mutation with query invalidation + toast notifications.
 *
 * Eliminates the repeated pattern of:
 *   useMutation({ ...xxxMutationOptions(), onSuccess: () => { qc.invalidate; toast; setState }, onError: () => toast })
 *
 * Usage:
 *   const deleteMut = useResourceMutation({
 *     options: deleteQueueMutationOptions(),
 *     invalidateKeys: [sqsKeys.queues()],
 *     successTitle: "Queue deleted",
 *     successDescription: (name) => name,
 *     onSuccess: () => setDeleteTarget(undefined),
 *   })
 */
export function useResourceMutation<
  TData = unknown,
  TError extends Error = Error,
  TVariables = void,
  TContext = unknown,
>(config: ResourceMutationConfig<TData, TError, TVariables, TContext>) {
  const { toast } = useToast()

  return useMutation({
    ...config.options,
    onSuccess: (data, variables, _, { client }) => {
      if (config.invalidateKeys) {
        for (const key of config.invalidateKeys) {
          void client.invalidateQueries({ queryKey: key })
        }
      }
      if (config.successTitle) {
        toast({
          title: config.successTitle,
          description: config.successDescription?.(variables),
          variant: config.successVariant ?? "success",
        })
      }
      config.onSuccess?.(data, variables)
    },
    onError: (error) => {
      if (config.errorToast === false) return
      toast({
        title: config.errorTitle ?? "Operation failed",
        description: error.message,
        variant: "danger",
      })
    },
  })
}
