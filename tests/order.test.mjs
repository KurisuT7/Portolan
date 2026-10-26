import assert from "node:assert/strict";
import test from "node:test";
import { applyOrder, moveWithin } from "../app/lib/order.ts";

test("moving a card reorders the whole list", () => {
  const order = ["a", "b", "c", "d"];
  assert.deepEqual(moveWithin(order, order, "a", "c"), ["b", "c", "a", "d"]);
  assert.deepEqual(moveWithin(order, order, "d", "a"), ["d", "a", "b", "c"]);
  assert.deepEqual(moveWithin(order, order, "b", "b"), order);
  assert.deepEqual(moveWithin(order, order, "x", "b"), order);
});

test("moving within a filtered list keeps hidden items in their slots", () => {
  const order = ["a", "x", "b", "y", "c"];
  assert.deepEqual(moveWithin(order, ["a", "b", "c"], "c", "a"), ["c", "x", "a", "y", "b"]);
  assert.deepEqual(moveWithin(order, ["a", "b", "c"], "a", "b"), ["b", "x", "a", "y", "c"]);
});

test("a saved order applies to items read before it was stored", () => {
  const items = [{ id: "a" }, { id: "b" }, { id: "c" }, { id: "new" }];
  assert.deepEqual(applyOrder(items, ["c", "a", "b"]).map((item) => item.id), ["c", "a", "b", "new"]);
  assert.deepEqual(applyOrder(items, ["b", "gone", "a"]).map((item) => item.id), ["b", "a", "c", "new"]);
});
