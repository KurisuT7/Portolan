"use client";

import { useMemo, useSyncExternalStore } from "react";
import { parseRoute, routeHref, type Route } from "../lib/routing";

function subscribeHash(callback: () => void) {
  window.addEventListener("hashchange", callback);
  return () => window.removeEventListener("hashchange", callback);
}

export function useRoute() {
  const hash = useSyncExternalStore(subscribeHash, () => window.location.hash, () => "");
  return useMemo(() => parseRoute(hash), [hash]);
}

export function navigate(route: Route) {
  window.location.hash = routeHref(route);
}
