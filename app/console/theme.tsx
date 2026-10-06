"use client";

import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import { Check, Monitor, Moon, Sun } from "lucide-react";
import { parseThemePreference, resolveTheme, themeBackgrounds, themeStorageKey, type ThemePreference } from "../lib/theme";

const lightQuery = "(prefers-color-scheme: light)";
const listeners = new Set<() => void>();

function readPreference(): ThemePreference {
  try {
    return parseThemePreference(localStorage.getItem(themeStorageKey));
  } catch {
    return "system";
  }
}

// Applies the preference to <html> and to the browser's theme color. Color
// transitions are suspended for the switch so every surface changes at once.
function applyTheme(preference: ThemePreference) {
  const root = document.documentElement;
  const theme = resolveTheme(preference, matchMedia(lightQuery).matches);
  if (root.getAttribute("data-theme") !== theme) {
    root.classList.add("theme-switching");
    root.setAttribute("data-theme", theme);
    void getComputedStyle(root).color;
    requestAnimationFrame(() => root.classList.remove("theme-switching"));
  }
  document.querySelectorAll('meta[name="theme-color"]').forEach((meta) => meta.setAttribute("content", themeBackgrounds[theme]));
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function setThemePreference(preference: ThemePreference) {
  try {
    if (preference === "system") localStorage.removeItem(themeStorageKey);
    else localStorage.setItem(themeStorageKey, preference);
  } catch {
    // Without storage the choice lasts for this page only.
  }
  applyTheme(preference);
  listeners.forEach((listener) => listener());
}

export function useThemePreference() {
  return useSyncExternalStore(subscribe, readPreference, () => "system" as ThemePreference);
}

// Follows system changes while the preference is "system", and choices made
// in other tabs.
export function useThemeSync() {
  useEffect(() => {
    const media = matchMedia(lightQuery);
    const update = () => {
      applyTheme(readPreference());
      listeners.forEach((listener) => listener());
    };
    const storage = (event: StorageEvent) => {
      if (event.key === themeStorageKey) update();
    };
    applyTheme(readPreference());
    media.addEventListener("change", update);
    window.addEventListener("storage", storage);
    return () => {
      media.removeEventListener("change", update);
      window.removeEventListener("storage", storage);
    };
  }, []);
}

const choices: Array<{ value: ThemePreference; label: string; icon: typeof Sun }> = [
  { value: "system", label: "跟随系统", icon: Monitor },
  { value: "light", label: "浅色", icon: Sun },
  { value: "dark", label: "深色", icon: Moon },
];

export function ThemeMenu() {
  const preference = useThemePreference();
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const close = (event: Event) => {
      if (event instanceof KeyboardEvent ? event.key === "Escape" : !root.current?.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener("pointerdown", close);
    document.addEventListener("keydown", close);
    return () => {
      document.removeEventListener("pointerdown", close);
      document.removeEventListener("keydown", close);
    };
  }, [open]);
  const current = choices.find((choice) => choice.value === preference) ?? choices[0];
  const Icon = current.icon;
  return (
    <div className="menu-anchor" ref={root}>
      <button className="icon-btn" aria-label={`外观：${current.label}`} title={`外观：${current.label}`} aria-haspopup="menu" aria-expanded={open} onClick={() => setOpen((value) => !value)}>
        <Icon size={17} />
      </button>
      {open && (
        <div className="menu" role="menu" aria-label="外观">
          {choices.map((choice) => (
            <button
              key={choice.value}
              type="button"
              role="menuitemradio"
              aria-checked={choice.value === preference}
              onClick={() => {
                setThemePreference(choice.value);
                setOpen(false);
              }}
            >
              <choice.icon size={15} aria-hidden="true" />
              {choice.label}
              {choice.value === preference && <Check size={14} className="menu-check" aria-hidden="true" />}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
