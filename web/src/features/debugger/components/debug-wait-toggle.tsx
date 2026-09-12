/**
 * *Wait for a debugger before the first invocation* — the per-function
 * switch over the `overcast:debug-wait` tag (docs/plans/
 * compute-debugger-console.md § 6). With it on, an invocation that finds no
 * debugger attached is held, after its container is up and before the event
 * is dispatched, until one attaches or the server's limit passes — so a
 * console or an editor attaching on the cold start still sees its
 * breakpoints bind before the handler runs.
 *
 * The switch flips at once and the tag call follows; a failed call flips it
 * back and says why. It is a Lambda tag, set through the `TagResource` and
 * `UntagResource` the console already speaks, so it needs the function's
 * ARN and is offered on Lambda pages only.
 */
import { useId } from "react"
import { useQueryClient } from "@tanstack/react-query"
import { Switch } from "@/components/ui/switch"
import { useResourceMutation } from "@/hooks/use-resource-mutation"
import { lambdaKeys } from "@/features/lambda/data"
import { cn } from "@/lib/utils"
import { lambda } from "@/services/api"
import type { DebuggerTarget } from "@/types"
import { debuggerKeys } from "../data"
import { useDebugTarget } from "../hooks"
import { DEBUG_WAIT_TAG, waitForDebuggerOf, withWaitForDebugger } from "../target"

export interface DebugWaitToggleProps {
  service: string
  resource: string
  /** The unqualified function ARN, which is what Lambda tags hang off. */
  resourceArn: string
  className?: string
}

/**
 * Reads the descriptor itself rather than taking it from the parent, so the
 * optimistic flip lands in the one place the switch reads from — the query
 * cache — and the parent's copy cannot lag behind it.
 */
export function DebugWaitToggle({ service, resource, resourceArn, className }: DebugWaitToggleProps) {
  const { data: target } = useDebugTarget(service, resource)
  const queryClient = useQueryClient()
  const id = useId()
  const on = target ? waitForDebuggerOf(target) : false
  const targetKey = debuggerKeys.target(service, resource)

  const { mutate, isPending } = useResourceMutation<
    void,
    Error,
    boolean,
    { previous: DebuggerTarget | null | undefined }
  >({
    options: {
      mutationFn: (next) =>
        next
          ? lambda.tagResource(resourceArn, { [DEBUG_WAIT_TAG]: "true" })
          : lambda.untagResource(resourceArn, [DEBUG_WAIT_TAG]),
      onMutate: async (next) => {
        // The poll must not land a stale descriptor over the optimistic one.
        await queryClient.cancelQueries({ queryKey: targetKey })
        const previous = queryClient.getQueryData<DebuggerTarget | null>(targetKey)
        if (previous) queryClient.setQueryData(targetKey, withWaitForDebugger(previous, next))
        return { previous }
      },
      // The hook owns onError (the toast); the rollback rides on settle,
      // which sees the error too. Either way the descriptor and the tag list
      // are asked for again, so the page ends on what the server holds.
      onSettled: (_data, error, _next, context) => {
        if (error && context?.previous !== undefined) {
          queryClient.setQueryData(targetKey, context.previous)
        }
        void queryClient.invalidateQueries({ queryKey: debuggerKeys.targets() })
        void queryClient.invalidateQueries({ queryKey: lambdaKeys.tags(resourceArn) })
      },
    },
    errorTitle: "Could not change the wait for a debugger",
  })

  if (!target) return null
  return (
    <div className={cn("flex flex-wrap items-center gap-x-3 gap-y-1", className)}>
      <div className="flex items-center gap-2">
        <Switch id={id} checked={on} onCheckedChange={(next) => mutate(next)} disabled={isPending} />
        <label htmlFor={id} className="cursor-pointer text-sm">
          Wait for a debugger before the first invocation
        </label>
      </div>
      <p className="m-0 text-xs text-fg-muted">
        Invocations wait until a debugger attaches, up to the server&rsquo;s limit, so breakpoints
        bind before the handler runs.
      </p>
    </div>
  )
}
