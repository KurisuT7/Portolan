export type ApiServer = {
  id: string;
  name: string;
  address: string;
  ipv4_address?: string;
  ipv6_address?: string;
  egress_ipv4: boolean;
  egress_ipv6: boolean;
  region?: string;
  // The day of the month a traffic cycle starts; 1 counts calendar months.
  traffic_reset_day: number;
  agent_status: string;
  last_seen_at?: string;
  runtime?: ApiRuntime;
};

export type ApiUnit = { name: string; active_state: string; sub_state: string };

// Reported by the Agent about its own host.
export type ApiRuntime = {
  agent_version?: string;
  sing_box_version: string;
  realm_version: string;
  units: ApiUnit[];
  reported_at?: string;
};

export type ApiNode = {
  id: string;
  server_id: string;
  name: string;
  protocol: "vless-reality" | "shadowsocks" | "snell";
  listen_port: number;
  enabled: boolean;
  managed: boolean;
  source?: string;
  last_seen_at?: string;
  profile: string;
};

export type ApiForward = {
  id: string;
  ingress_server_id: string;
  name: string;
  listen_port: number;
  networks: Array<"tcp" | "udp">;
  target_host: string;
  target_port: number;
  target_server_id?: string;
  target_node_id?: string;
  engine: "sing-box" | "realm";
  enabled: boolean;
  created_at?: string;
  updated_at?: string;
};

export type ForwardInput = Pick<ApiForward, "name" | "ingress_server_id" | "listen_port" | "networks" | "engine" | "enabled"> & {
  target_node_id?: string;
  target_server_id?: string;
  target_host?: string;
  target_port?: number;
};

export type ApiForwardProbe = {
  forward_id: string;
  server_id: string;
  checked_at: string;
  attempts: number;
  successes: number;
  latency_ms: number;
  jitter_ms: number;
  loss_percent: number;
  status: "stable" | "degraded" | "down" | "unsupported";
  last_error?: string;
};

export type ProbeHistoryRange = "1h" | "6h" | "24h" | "7d";

export type ApiForwardProbeHistoryPoint = {
  checked_at: string;
  attempts: number;
  successes: number;
  latency: number;
  jitter: number;
  loss: number;
  availability: number | null;
  status: ApiForwardProbe["status"] | "unknown";
  reasons?: Array<
    "unreachable" | "packet_loss" | "high_latency" | "high_jitter"
  >;
};

export type ApiForwardProbeHistory = {
  summary: {
    availability_percent: number | null;
    avg_latency_ms: number;
    p95_latency_ms: number;
    avg_jitter_ms: number;
    loss_percent: number;
    incidents: number;
    sample_count: number;
    from: string;
    to: string;
  };
  points: ApiForwardProbeHistoryPoint[];
};

export type TrafficKind = "server" | "node" | "forward";

// Bytes received (rx) and sent (tx) in the current cycle of the server, which
// began at since; rates are bytes per second from the latest report and zero
// once that report is old.
export type ApiTrafficItem = {
  kind: TrafficKind;
  id: string;
  server_id: string;
  since: string;
  rx_bytes: number;
  tx_bytes: number;
  rx_rate: number;
  tx_rate: number;
  // Server items only: the latest traffic report and why port counters are missing.
  reported_at?: string;
  port_error?: "nft_missing" | "nft_failed";
};

export type ApiTraffic = { items: ApiTrafficItem[] };

// The last 24 hours, 30 days or 12 traffic cycles.
export type TrafficRange = "24h" | "30d" | "12m";

export type ApiTrafficPoint = { start: string; end: string; rx_bytes: number; tx_bytes: number };

// Days and traffic cycles start at midnight in the browser's time zone.
function timeZone() {
  return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
}

export type ApiJob = {
  id: string;
  server_id: string;
  type: string;
  state: "pending" | "running" | "succeeded" | "failed";
  result?: string;
  // How the snapshot compares with the release the Agent reports as active.
  applied?: "current" | "behind" | "ahead";
  created_at: string;
  started_at?: string;
  finished_at?: string;
};

export type ClientExport = { uri?: string; surge_line?: string; name?: string };

export type CoreName = "sing-box" | "realm";

export type ApiCoreTarget = { core: CoreName; version: string; updated_at: string };

export type ApiCores = { targets: ApiCoreTarget[]; jobs: ApiJob[] };

export type ApiCoreRelease = { version: string };

export type CreateServerResponse = {
  server: ApiServer;
  enrollment_token: string;
  expires_in_seconds: number;
  enrollment_hint: string;
};

export type ApiSession = {
  csrf_token: string;
  expires_at: string;
  version: string;
  totp_enabled: boolean;
  // "dbip" when the region database requires DB-IP attribution.
  geoip_provider: string;
};

export type TotpSetup = { secret: string; uri: string; qr: { size: number; rows: string[] } };

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
    // Stable reason from the panel, such as "totp_required" or "login_locked".
    public code = "",
    public retryAfter = 0,
  ) {
    super(message);
  }
}

export class PortolanApi {
  private csrf = "";

  async session(signal?: AbortSignal) {
    const data = await this.request<ApiSession>("/api/v1/session", {
      signal,
    });
    this.csrf = data.csrf_token;
    return data;
  }

  async login(token: string, code = "") {
    const data = await this.request<ApiSession>(
      "/api/v1/session",
      {
        method: "POST",
        body: JSON.stringify({ token, code }),
      },
      false,
    );
    this.csrf = data.csrf_token;
    return data;
  }

  async setupTotp() {
    return this.request<TotpSetup>("/api/v1/security/totp/setup", { method: "POST" });
  }

  async enableTotp(code: string) {
    await this.request<void>("/api/v1/security/totp", { method: "POST", body: JSON.stringify({ code }) });
  }

  async disableTotp(code: string) {
    await this.request<void>("/api/v1/security/totp", { method: "DELETE", body: JSON.stringify({ code }) });
  }

  async logout() {
    await this.request<void>("/api/v1/session", { method: "DELETE" });
    this.csrf = "";
  }

  async servers(signal?: AbortSignal) {
    return (
      (
        await this.request<{ items: ApiServer[] }>("/api/v1/servers", {
          signal,
        })
      ).items ?? []
    );
  }

  async nodes(signal?: AbortSignal) {
    return (
      (await this.request<{ items: ApiNode[] }>("/api/v1/nodes", { signal }))
        .items ?? []
    );
  }

  async forwards(signal?: AbortSignal) {
    return (
      (
        await this.request<{ items: ApiForward[] }>("/api/v1/forwards", {
          signal,
        })
      ).items ?? []
    );
  }

  async probes(signal?: AbortSignal) {
    return (
      (
        await this.request<{ items: ApiForwardProbe[] }>(
          "/api/v1/forward-probes",
          { signal },
        )
      ).items ?? []
    );
  }

  async probeHistory(
    id: string,
    range: ProbeHistoryRange,
    signal?: AbortSignal,
  ) {
    return this.request<ApiForwardProbeHistory>(
      `/api/v1/forwards/${encodeURIComponent(id)}/probe-history?range=${encodeURIComponent(range)}`,
      { signal },
    );
  }

  async traffic(signal?: AbortSignal) {
    return this.request<ApiTraffic>(`/api/v1/traffic?tz=${encodeURIComponent(timeZone())}`, { signal });
  }

  async trafficHistory(kind: TrafficKind, id: string, range: TrafficRange, signal?: AbortSignal) {
    const collection = { server: "servers", node: "nodes", forward: "forwards" }[kind];
    const query = `range=${range}&tz=${encodeURIComponent(timeZone())}`;
    return (await this.request<{ points: ApiTrafficPoint[] }>(`/api/v1/${collection}/${encodeURIComponent(id)}/traffic?${query}`, { signal })).points ?? [];
  }

  async config(signal?: AbortSignal) {
    return (await this.request<{ items: ApiJob[] }>("/api/v1/config-status", { signal })).items ?? [];
  }

  async cores(signal?: AbortSignal) {
    return this.request<ApiCores>("/api/v1/cores", { signal });
  }

  async coreReleases(core: CoreName) {
    return (await this.request<{ items: ApiCoreRelease[] }>(`/api/v1/cores/${core}/releases`)).items ?? [];
  }

  // The panel downloads and verifies both architectures before answering.
  async setCoreTarget(core: CoreName, version: string) {
    return this.request<ApiCoreTarget>(`/api/v1/cores/${core}`, { method: "PUT", body: JSON.stringify({ version }) }, true, 180_000);
  }

  async rolloutCore(core: CoreName) {
    return this.request<{ queued: number }>(`/api/v1/cores/${core}/rollout`, { method: "POST" });
  }

  async updateServerCore(id: string, core: CoreName) {
    return this.request<ApiJob>(`/api/v1/servers/${encodeURIComponent(id)}/cores/${core}`, { method: "POST" });
  }

  async syncServer(id: string) {
    return this.request<{ status: string }>(`/api/v1/servers/${encodeURIComponent(id)}/sync`, { method: "POST" });
  }

  async createNode(payload: Record<string, unknown>) {
    return this.request<{
      id: string;
      client: ClientExport;
      export_pending?: boolean;
    }>("/api/v1/nodes", {
      method: "POST",
      body: JSON.stringify(payload),
    });
  }

  async createServer(payload: Record<string, unknown>) {
    return this.request<CreateServerResponse>("/api/v1/servers", {
      method: "POST",
      body: JSON.stringify(payload),
    });
  }

  async renewEnrollment(id: string) {
    return this.request<CreateServerResponse>(
      `/api/v1/servers/${encodeURIComponent(id)}/enrollment`,
      {
        method: "POST",
      },
    );
  }

  async updateForward(id: string, payload: ForwardInput) {
    return this.request<ApiForward>(`/api/v1/forwards/${encodeURIComponent(id)}`, {
      method: "PUT", body: JSON.stringify(payload),
    });
  }

  async createForward(payload: ForwardInput) {
    return this.request<ApiForward>("/api/v1/forwards", {
      method: "POST",
      body: JSON.stringify(payload),
    });
  }

  async probeForward(id: string) {
    return this.request<ApiForwardProbe>(
      `/api/v1/forwards/${encodeURIComponent(id)}/probe`,
      {
        method: "POST",
      },
    );
  }

  async deleteNode(id: string) {
    await this.request<void>(`/api/v1/nodes/${encodeURIComponent(id)}`, {
      method: "DELETE",
    });
  }

  async deleteForward(id: string) {
    await this.request<void>(`/api/v1/forwards/${encodeURIComponent(id)}`, {
      method: "DELETE",
    });
  }

  async exportNode(id: string, endpoint?: { address: string; port: number; name: string }) {
    const query = endpoint ? `?${new URLSearchParams({ address: endpoint.address, port: String(endpoint.port), name: endpoint.name })}` : "";
    return this.request<ClientExport>(`/api/v1/nodes/${encodeURIComponent(id)}/export${query}`);
  }

  async updateServer(id: string, payload: Record<string, unknown>) {
    return this.request<ApiServer>(
      `/api/v1/servers/${encodeURIComponent(id)}`,
      { method: "PATCH", body: JSON.stringify(payload) },
    );
  }

  async deleteServer(id: string) {
    await this.request<void>(`/api/v1/servers/${encodeURIComponent(id)}`, {
      method: "DELETE",
    });
  }

  private async request<T>(
    path: string,
    init: RequestInit = {},
    includeCSRF = true,
    timeoutMs = 12_000,
  ): Promise<T> {
    const headers = new Headers(init.headers);
    if (init.body) headers.set("Content-Type", "application/json");
    if (
      includeCSRF &&
      this.csrf &&
      init.method &&
      !["GET", "HEAD"].includes(init.method)
    ) {
      headers.set("X-CSRF-Token", this.csrf);
    }
    const controller = new AbortController();
    const timeout = window.setTimeout(() => controller.abort(), timeoutMs);
    try {
      const response = await fetch(path, {
        ...init,
        headers,
        credentials: "same-origin",
        signal: init.signal ? AbortSignal.any([init.signal, controller.signal]) : controller.signal,
      });
      if (!response.ok) {
        let message = `请求失败（${response.status}）`;
        let code = "";
        let retryAfter = 0;
        try {
          const body = (await response.json()) as { error?: string; code?: string; retry_after?: number };
          if (body.error) message = body.error;
          code = body.code ?? "";
          retryAfter = body.retry_after ?? 0;
        } catch (error) {
          if (controller.signal.aborted || init.signal?.aborted) throw error;
        }
        throw new ApiError(response.status, message, code, retryAfter);
      }
      if (response.status === 204) return undefined as T;
      return (await response.json()) as T;
    } catch (error) {
      if (init.signal?.aborted) throw error;
      if (error instanceof ApiError) throw error;
      if (controller.signal.aborted) throw new ApiError(0, "连接超时，请重试");
      if (error instanceof SyntaxError) throw new ApiError(0, "面板返回了无法读取的数据");
      throw new ApiError(0, "无法连接面板，请稍后重试");
    } finally {
      window.clearTimeout(timeout);
    }
  }
}
