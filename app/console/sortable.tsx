"use client";

import { type ReactNode } from "react";
import { closestCenter, DndContext, KeyboardSensor, MouseSensor, TouchSensor, useSensor, useSensors, type Announcements, type UniqueIdentifier } from "@dnd-kit/core";
import { rectSortingStrategy, SortableContext, sortableKeyboardCoordinates, useSortable } from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { GripHorizontal } from "lucide-react";

type Item = { id: string; name: string };

// A card grid the operator can rearrange while sorting is on. Cards stay inert
// meanwhile, so a press anywhere on a card drags it instead of following its
// link; touch screens start a drag after a short hold so swiping still scrolls.
export function SortableGrid<T extends Item>({ className, items, sorting, onMove, children }: {
  className: string;
  items: T[];
  sorting: boolean;
  onMove: (active: string, over: string) => void;
  children: (item: T) => ReactNode;
}) {
  const sensors = useSensors(
    useSensor(MouseSensor, { activationConstraint: { distance: 4 } }),
    useSensor(TouchSensor, { activationConstraint: { delay: 180, tolerance: 8 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );
  const names = new Map(items.map((item, position) => [item.id, { name: item.name, position: position + 1 }]));
  const describe = (id: UniqueIdentifier) => names.get(String(id))?.name ?? "";
  const place = (id: UniqueIdentifier | undefined) => (id == null ? undefined : names.get(String(id))?.position);
  const announcements: Announcements = {
    onDragStart: ({ active }) => `已拿起 ${describe(active.id)}`,
    onDragOver: ({ active, over }) => (over ? `${describe(active.id)} 移到第 ${place(over.id)} 位` : undefined),
    onDragEnd: ({ active, over }) => (over ? `${describe(active.id)} 放在第 ${place(over.id)} 位` : `${describe(active.id)} 回到原位`),
    onDragCancel: ({ active }) => `已取消，${describe(active.id)} 回到原位`,
  };
  return (
    <DndContext
      sensors={sensors}
      collisionDetection={closestCenter}
      accessibility={{ announcements, screenReaderInstructions: { draggable: "按空格键拿起，用方向键移动，再按空格键放下；按 Esc 取消。" } }}
      onDragEnd={({ active, over }) => {
        if (over && active.id !== over.id) onMove(String(active.id), String(over.id));
      }}
    >
      <SortableContext items={items.map((item) => item.id)} strategy={rectSortingStrategy} disabled={!sorting}>
        <div className={`${className}${sorting ? " is-sorting" : ""}`}>
          {items.map((item) => (
            <SortableCard key={item.id} id={item.id} label={item.name} sorting={sorting}>
              {children(item)}
            </SortableCard>
          ))}
        </div>
      </SortableContext>
    </DndContext>
  );
}

function SortableCard({ id, label, sorting, children }: { id: string; label: string; sorting: boolean; children: ReactNode }) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({ id });
  return (
    <div
      ref={setNodeRef}
      className={`sortable${isDragging ? " is-dragging" : ""}`}
      style={{ transform: CSS.Translate.toString(transform), transition }}
      {...(sorting ? { ...attributes, ...listeners, "aria-label": label } : {})}
    >
      <span className="sort-grip" aria-hidden="true"><GripHorizontal size={16} /></span>
      <div className="sortable-body" inert={sorting}>{children}</div>
    </div>
  );
}
