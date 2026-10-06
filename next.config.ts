import type { NextConfig } from "next";

// The console is a client-side application. It is exported as static files
// and embedded in the panel binary, so a production host needs no Node.js.
const nextConfig: NextConfig = {
  output: "export",
};

export default nextConfig;
