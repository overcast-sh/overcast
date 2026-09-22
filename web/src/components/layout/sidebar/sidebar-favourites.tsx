import { useState } from "react"
import {
  closestCenter,
  DndContext,
  KeyboardSensor,
  PointerSensor,
  useSensor,
  useSensors,
  type DragEndEvent,
} from "@dnd-kit/core"
import {
  arrayMove,
  SortableContext,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable"
import { CSS } from "@dnd-kit/utilities"
import { useDragClickGuard } from "@/hooks/use-drag-click-guard"
import { useFavourites } from "@/hooks/use-favourites"
import { restrictToVerticalAxis } from "@/lib/dnd"
import { ALL_SERVICES } from "@/lib/nav-services"
import { SidebarNavItem, type SidebarNavEntry } from "./sidebar-nav-item"

const SERVICE_BY_KEY = new Map(ALL_SERVICES.map((service) => [service.key, service]))

interface SidebarFavouritesProps {
  collapsed: boolean
  pathname: string
  isExpanded: (key: string) => boolean
  onToggleExpand: (key: string) => void
}

/** Pinned services — reorderable by drag in the expanded sidebar. */
export function SidebarFavourites({
  collapsed,
  pathname,
  isExpanded,
  onToggleExpand,
}: SidebarFavouritesProps) {
  const { favourites, reorderFavourites } = useFavourites()
  const [draggingId, setDraggingId] = useState<string | null>(null)
  // A KeyboardSensor alongside the pointer one: with only the latter, pinned services
  // could be reordered by mouse and by nothing else. dnd-kit's own coordinate getter
  // drives it — Space picks a row up, the arrows move it, Space drops it, Escape cancels
  // — and `restrictToVerticalAxis` below keeps the keyboard path on the same axis as the
  // pointer one.
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 6 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  )
  const markDragged = useDragClickGuard()

  function handleDragStart(id: string) {
    setDraggingId(id)
    // Keep the drop from activating the link under the pointer, wherever it lands.
    markDragged()
  }

  function handleDragEnd({ active, over }: DragEndEvent) {
    setDraggingId(null)
    if (over && active.id !== over.id) {
      const from = favourites.indexOf(active.id as string)
      const to = favourites.indexOf(over.id as string)
      reorderFavourites(arrayMove(favourites, from, to))
    }
  }

  if (favourites.length === 0) {
    return collapsed ? null : (
      <p className="px-2.5 py-2 font-mono text-2xs text-fg-subtle">
        Star a service to pin it here
      </p>
    )
  }

  return (
    <DndContext
      // dnd-kit renders its screen-reader announcements as a `role="status"` element
      // beside the items. Inline, that lands as a direct child of the sidebar's <ul>,
      // where only <li> is allowed — so it goes to the body instead.
      accessibility={{ container: document.body }}
      sensors={sensors}
      collisionDetection={closestCenter}
      modifiers={[restrictToVerticalAxis]}
      onDragStart={({ active }) => handleDragStart(active.id as string)}
      onDragEnd={handleDragEnd}
      onDragCancel={() => setDraggingId(null)}
    >
      <SortableContext items={favourites} strategy={verticalListSortingStrategy}>
        {favourites.map((key) => {
          const item = SERVICE_BY_KEY.get(key)
          if (!item) return null
          return (
            <SortableFavourite
              key={key}
              item={item}
              collapsed={collapsed}
              pathname={pathname}
              expanded={isExpanded(item.to)}
              onToggleExpand={onToggleExpand}
              dragging={draggingId === key}
            />
          )
        })}
      </SortableContext>
    </DndContext>
  )
}

interface SortableFavouriteProps {
  item: SidebarNavEntry
  collapsed: boolean
  pathname: string
  expanded: boolean
  onToggleExpand: (key: string) => void
  dragging: boolean
}

function SortableFavourite({ item, dragging, ...rest }: SortableFavouriteProps) {
  const { attributes, listeners, setNodeRef, setActivatorNodeRef, transform, transition } =
    useSortable({ id: item.to })

  return (
    <SidebarNavItem
      item={item}
      {...rest}
      pinnable
      sortable={{
        setRowRef: setNodeRef,
        style: {
          transform: CSS.Transform.toString(transform),
          transition,
          opacity: dragging ? 0.4 : 1,
        },
        attributes,
        listeners,
        setActivatorRef: setActivatorNodeRef,
        label: item.label,
      }}
    />
  )
}
