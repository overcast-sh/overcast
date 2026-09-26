import * as React from "react"
import * as DropdownMenu from "@radix-ui/react-dropdown-menu"
import { cn } from "@/lib/utils"

/**
 * A dropdown menu of actions — an overflow `⋯` menu, a *Copy as* menu —
 * with the console's surface and item treatment. Radix owns the keyboard
 * (arrows, type-ahead, `esc`) and focus return.
 *
 * ```tsx
 * <Menu>
 *   <MenuTrigger asChild><Button variant="ghost" size="sm">Copy</Button></MenuTrigger>
 *   <MenuContent>
 *     <MenuItem onSelect={copyCsv}>Copy as CSV</MenuItem>
 *   </MenuContent>
 * </Menu>
 * ```
 */

const Menu = DropdownMenu.Root
const MenuTrigger = DropdownMenu.Trigger

function MenuContent({
  className,
  align = "end",
  sideOffset = 4,
  ...props
}: React.ComponentPropsWithoutRef<typeof DropdownMenu.Content>) {
  return (
    <DropdownMenu.Portal>
      <DropdownMenu.Content
        align={align}
        sideOffset={sideOffset}
        collisionPadding={8}
        className={cn(
          "z-50 min-w-44 rounded-md border border-border bg-bg-elevated p-1 shadow-lg",
          className,
        )}
        {...props}
      />
    </DropdownMenu.Portal>
  )
}

function MenuItem({
  className,
  ...props
}: React.ComponentPropsWithoutRef<typeof DropdownMenu.Item>) {
  return (
    <DropdownMenu.Item
      className={cn(
        "flex cursor-pointer items-center gap-2 rounded-sm px-2 py-1.5 text-xs text-fg outline-none select-none",
        "data-highlighted:bg-accent-muted data-highlighted:text-accent",
        "data-disabled:cursor-not-allowed data-disabled:opacity-50",
        "[&_svg]:size-3.5 [&_svg]:shrink-0",
        className,
      )}
      {...props}
    />
  )
}

function MenuSeparator({ className }: { className?: string }) {
  return <DropdownMenu.Separator className={cn("my-1 h-px bg-border", className)} />
}

export { Menu, MenuTrigger, MenuContent, MenuItem, MenuSeparator }
