"use client";

import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { CornerDownLeft, Search } from "lucide-react";
import { serverEndpoint, serverHost } from "../lib/endpoints";
import { protocolShort } from "../lib/nodes";
import type { Route } from "../lib/routing";
import { useFleet } from "./data";
import { navigate } from "./hooks";

export type PaletteAction = "server" | "node" | "forward";
type Item = { key: string; group: string; label: string; hint?: string; run: () => void };

export function CommandPalette({ onClose, onAction }: { onClose: () => void; onAction: (action: PaletteAction) => void }) {
  const { data, index } = useFleet();
  const ref = useRef<HTMLDialogElement>(null);
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  useEffect(() => {
    const dialog = ref.current!;
    dialog.showModal();
    return () => dialog.close();
  }, []);

  const items = useMemo(() => {
    const go = (route: Route) => () => navigate(route);
    const all: Item[] = [
      { key: "a:server", group: "操作", label: "添加服务器", run: () => onAction("server") },
      { key: "a:node", group: "操作", label: "添加节点", run: () => onAction("node") },
      { key: "a:forward", group: "操作", label: "添加转发", run: () => onAction("forward") },
      ...data.servers.map((server) => ({ key: `s:${server.id}`, group: "服务器", label: server.name, hint: serverHost(server) || server.region, run: go({ page: "server", id: server.id }) })),
      ...data.forwards.map((forward) => ({
        key: `f:${forward.id}`,
        group: "转发",
        label: forward.name,
        hint: serverEndpoint(index.servers.get(forward.ingress_server_id), forward.listen_port),
        run: go({ page: "forward", id: forward.id }),
      })),
      ...data.nodes.map((node) => ({
        key: `n:${node.id}`,
        group: "节点",
        label: node.name,
        hint: `${protocolShort[node.protocol]} :${node.listen_port} · ${index.servers.get(node.server_id)?.name ?? ""}`,
        run: go({ page: "server", id: node.server_id }),
      })),
    ];
    const needle = query.trim().toLowerCase();
    if (!needle) return all.filter((item) => item.group !== "节点").slice(0, 12);
    return all.filter((item) => `${item.label} ${item.hint ?? ""}`.toLowerCase().includes(needle)).slice(0, 30);
  }, [data, index, onAction, query]);

  const current = Math.min(active, Math.max(0, items.length - 1));
  function choose(item: Item | undefined) {
    if (!item) return;
    onClose();
    item.run();
  }
  function keyboard(event: KeyboardEvent<HTMLInputElement>) {
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      const step = event.key === "ArrowDown" ? 1 : -1;
      setActive((current + step + items.length) % Math.max(1, items.length));
    }
    if (event.key === "Enter") {
      event.preventDefault();
      choose(items[current]);
    }
  }

  let group = "";
  return (
    <dialog
      ref={ref}
      className="palette"
      aria-label="搜索与命令"
      onCancel={(event) => { event.preventDefault(); onClose(); }}
      onClick={(event) => { if (event.target === ref.current) onClose(); }}
    >
      <label className="palette-input">
        <Search size={17} aria-hidden="true" />
        <input
          autoFocus
          value={query}
          placeholder="搜索服务器、转发、节点，或输入操作"
          onChange={(event) => { setQuery(event.target.value); setActive(0); }}
          onKeyDown={keyboard}
          role="combobox"
          aria-expanded="true"
          aria-controls="palette-list"
          aria-activedescendant={items[current] ? `palette-${items[current].key}` : undefined}
        />
        <kbd>Esc</kbd>
      </label>
      <ul className="palette-list" id="palette-list" role="listbox">
        {items.map((item, position) => {
          const heading = item.group !== group ? item.group : "";
          group = item.group;
          return (
            <li key={item.key} role="presentation">
              {heading && <span className="palette-group">{heading}</span>}
              <button
                type="button"
                id={`palette-${item.key}`}
                role="option"
                aria-selected={position === current}
                onMouseMove={() => setActive(position)}
                onClick={() => choose(item)}
              >
                <span>{item.label}</span>
                {item.hint && <span className="palette-hint">{item.hint}</span>}
                {position === current && <CornerDownLeft size={14} aria-hidden="true" />}
              </button>
            </li>
          );
        })}
        {!items.length && <li className="palette-empty">没有匹配的结果</li>}
      </ul>
    </dialog>
  );
}
