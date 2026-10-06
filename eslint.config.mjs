import { defineConfig, globalIgnores } from "eslint/config";
import nextVitals from "eslint-config-next/core-web-vitals";
import nextTs from "eslint-config-next/typescript";

const eslintConfig = defineConfig([
  ...nextVitals,
  ...nextTs,
  // Generated output and dependencies.
  globalIgnores([
    ".next/**",
    ".vinext/**",
    "dist/**",
    "internal/webui/dist/**",
    "node_modules/**",
    "out/**",
    "release/**",
    "next-env.d.ts",
  ]),
]);

export default eslintConfig;
