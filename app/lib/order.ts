// Servers and forwards are shown in the order the operator arranges.

// Moves active to the slot of over among the visible items. Items hidden by a
// filter keep their slots, so the rest of the order is unchanged. visible must
// list its items in the same relative order as order.
export function moveWithin(order: string[], visible: string[], active: string, over: string) {
  const from = visible.indexOf(active);
  const to = visible.indexOf(over);
  if (from < 0 || to < 0 || from === to) return order;
  const moved = [...visible];
  moved.splice(to, 0, ...moved.splice(from, 1));
  const shown = new Set(visible);
  let next = 0;
  return order.map((id) => (shown.has(id) ? moved[next++] : id));
}

// Lists items in the order of ids; items missing from ids follow in their current order.
export function applyOrder<T extends { id: string }>(items: T[], ids: string[]) {
  const position = new Map(ids.map((id, index) => [id, index]));
  const rank = (item: T) => position.get(item.id) ?? ids.length;
  return [...items].sort((left, right) => rank(left) - rank(right));
}
