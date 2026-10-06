export type ThemePreference = "system" | "light" | "dark";
export type Theme = "light" | "dark";

export const themeStorageKey = "portolan-theme";

// Page backgrounds, used for the browser's theme color.
export const themeBackgrounds: Record<Theme, string> = { light: "#f5f5f2", dark: "#09090a" };

export function parseThemePreference(value: unknown): ThemePreference {
  return value === "light" || value === "dark" ? value : "system";
}

export function resolveTheme(preference: ThemePreference, systemPrefersLight: boolean): Theme {
  if (preference === "system") return systemPrefersLight ? "light" : "dark";
  return preference;
}

// Runs in <head> before the first paint so the page never flashes the other
// theme. It is inlined into the exported page, and the panel's
// Content-Security-Policy allows it by its hash.
export const themeBootScript = `(function(){try{var t=localStorage.getItem("${themeStorageKey}");if(t!=="light"&&t!=="dark")t=matchMedia("(prefers-color-scheme: light)").matches?"light":"dark";document.documentElement.setAttribute("data-theme",t)}catch(e){}})()`;
