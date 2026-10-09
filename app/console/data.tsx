"use client";

import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from "react";
import { ApiError, PortolanApi, type ApiSession } from "../lib/api";
import type { Fleet, FleetIndex } from "../lib/fleet";

export const api = new PortolanApi();

export type Resource = keyof Fleet;
export type ResourceErrors = Partial<Record<Resource, string>>;

const resources: Resource[] = ["servers", "nodes", "forwards", "probes", "config", "cores", "traffic"];
const empty: Fleet = { servers: [], nodes: [], forwards: [], probes: [], config: [], cores: { targets: [], jobs: [] }, traffic: { items: [] } };

export const resourceNames: Record<Resource, string> = {
  servers: "服务器",
  nodes: "节点",
  forwards: "转发",
  probes: "延迟检测",
  config: "配置状态",
  cores: "核心版本",
  traffic: "流量",
};

export function errorText(error: unknown) {
  return error instanceof Error ? error.message : "操作失败，请重试";
}

export type Session = "checking" | "login" | "ready" | "error";

export function useConsole() {
  const [data, setData] = useState<Fleet>(empty);
  const [errors, setErrors] = useState<ResourceErrors>({});
  const [lastSuccess, setLastSuccess] = useState<Partial<Record<Resource, number>>>({});
  const [session, setSession] = useState<Session>("checking");
  const [info, setInfo] = useState<ApiSession | null>(null);
  const [connectionError, setConnectionError] = useState("");
  const [refreshing, setRefreshing] = useState(false);
  // fetchedAt is the reference clock for freshness checks; syncedAt marks the last complete read.
  const [fetchedAt, setFetchedAt] = useState(0);
  const [syncedAt, setSyncedAt] = useState(0);
  const inflight = useRef<AbortController | null>(null);

  const clear = useCallback(() => {
    setData(empty);
    setErrors({});
    setLastSuccess({});
    setSyncedAt(0);
  }, []);

  const refresh = useCallback(async () => {
    inflight.current?.abort();
    const controller = new AbortController();
    inflight.current = controller;
    setRefreshing(true);
    const results = await Promise.allSettled(resources.map((key) => api[key](controller.signal)));
    if (controller.signal.aborted) return false;
    setRefreshing(false);
    if (results.some((result) => result.status === "rejected" && result.reason instanceof ApiError && result.reason.status === 401)) {
      clear();
      setSession("login");
      return false;
    }
    const now = api.panelNow();
    const loaded: Partial<Fleet> = {};
    const failures: ResourceErrors = {};
    const succeeded: Partial<Record<Resource, number>> = {};
    results.forEach((result, position) => {
      const key = resources[position];
      if (result.status === "fulfilled") {
        Object.assign(loaded, { [key]: result.value });
        succeeded[key] = now;
      } else {
        failures[key] = errorText(result.reason);
      }
    });
    setData((previous) => ({ ...previous, ...loaded }));
    setErrors(failures);
    setLastSuccess((previous) => ({ ...previous, ...succeeded }));
    setFetchedAt(now);
    if (!Object.keys(failures).length) setSyncedAt(now);
    return true;
  }, [clear]);

  const connect = useCallback(async (signal?: AbortSignal) => {
    try {
      setInfo(await api.session(signal));
      if (await refresh()) {
        setConnectionError("");
        setSession("ready");
      }
    } catch (error) {
      if (signal?.aborted) return;
      setConnectionError(errorText(error));
      setSession(error instanceof ApiError && error.status === 401 ? "login" : "error");
    }
  }, [refresh]);

  useEffect(() => {
    const controller = new AbortController();
    // Start after commit; connect only updates state once the session request settles.
    queueMicrotask(() => void connect(controller.signal));
    return () => {
      controller.abort();
      inflight.current?.abort();
    };
  }, [connect]);

  useEffect(() => {
    if (session !== "ready") return;
    const refreshVisible = () => {
      if (document.visibilityState === "visible") void refresh();
    };
    const timer = setInterval(refreshVisible, 15_000);
    document.addEventListener("visibilitychange", refreshVisible);
    return () => {
      clearInterval(timer);
      document.removeEventListener("visibilitychange", refreshVisible);
    };
  }, [refresh, session]);

  async function login(token: string, code = "") {
    setInfo(await api.login(token, code));
    if (await refresh()) setSession("ready");
  }

  async function logout() {
    await api.logout();
    inflight.current?.abort();
    clear();
    setInfo(null);
    setSession("login");
  }

  // Reads the session again after a security setting changed.
  async function reloadSession() {
    setInfo(await api.session());
  }

  return { data, errors, lastSuccess, session, info, connectionError, refreshing, fetchedAt, syncedAt, refresh, connect, login, logout, reloadSession };
}

export type ConsoleControl = ReturnType<typeof useConsole>;

export type FleetContextValue = {
  data: Fleet;
  index: FleetIndex;
  errors: ResourceErrors;
  now: number;
  refresh: () => Promise<boolean>;
  // Version of the running panel.
  version: string;
  // Version of the Agent this panel installs, compared with the version each Agent reports.
  agentVersion: string;
};

const FleetContext = createContext<FleetContextValue | null>(null);

export function FleetProvider({ value, children }: { value: FleetContextValue; children: ReactNode }) {
  return <FleetContext.Provider value={value}>{children}</FleetContext.Provider>;
}

export function useFleet() {
  const value = useContext(FleetContext);
  if (!value) throw new Error("useFleet must be used inside FleetProvider");
  return value;
}
