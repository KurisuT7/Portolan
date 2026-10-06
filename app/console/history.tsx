"use client";

import { createContext, useContext, useEffect, useState, type ReactNode } from "react";
import type { ApiForward, ApiForwardProbeHistory } from "../lib/api";
import { api } from "./data";

const HistoryContext = createContext<Map<string, ApiForwardProbeHistory>>(new Map());

// 24-hour histories feed the route sparklines; they change slowly, so they refresh every five minutes.
export function HistoryProvider({ forwards, children }: { forwards: ApiForward[]; children: ReactNode }) {
  const [histories, setHistories] = useState<Map<string, ApiForwardProbeHistory>>(new Map());
  const ids = forwards.filter((forward) => forward.enabled && forward.networks.includes("tcp")).map((forward) => forward.id).join(",");
  useEffect(() => {
    if (!ids) return;
    const controller = new AbortController();
    const list = ids.split(",");
    async function load() {
      const results = await Promise.allSettled(list.map((id) => api.probeHistory(id, "24h", controller.signal)));
      if (controller.signal.aborted) return;
      setHistories((previous) => {
        const next = new Map(previous);
        results.forEach((result, position) => {
          if (result.status === "fulfilled") next.set(list[position], result.value);
        });
        return next;
      });
    }
    const first = setTimeout(load, 0);
    const timer = setInterval(load, 300_000);
    return () => {
      controller.abort();
      clearTimeout(first);
      clearInterval(timer);
    };
  }, [ids]);
  return <HistoryContext.Provider value={histories}>{children}</HistoryContext.Provider>;
}

export function useHistory(id: string) {
  return useContext(HistoryContext).get(id);
}
