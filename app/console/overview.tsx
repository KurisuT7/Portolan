"use client";

import { ArrowRight, Check, ChevronRight } from "lucide-react";
import { attentionItems, fleetSummary } from "../lib/fleet";
import { routeHref } from "../lib/routing";
import { byteParts, fleetTraffic, formatBytes } from "../lib/traffic";
import { useFleet } from "./data";
import { RouteGrid } from "./forwards";
import { ServerGrid } from "./servers";
import { Empty, Status } from "./ui";

export function OverviewPage() {
  const { data, errors, index, now } = useFleet();
  const summary = fleetSummary(data, index, now);
  const attention = attentionItems(data, index, errors, now);
  const problems = attention.filter((item) => item.tone !== "neutral").length;
  const unavailable = !!(errors.servers || errors.forwards || errors.probes);
  const tone = unavailable ? "neutral" : problems ? (attention.some((item) => item.tone === "bad") ? "bad" : "warn") : "good";
  const traffic = errors.traffic ? null : fleetTraffic(data.traffic);
  const trafficParts = traffic ? byteParts(traffic.rx + traffic.tx) : null;
  return (
    <>
      <div className="overview-top">
        <section className={`hero tone-${tone}`}>
          <span className="hero-state">
            <i aria-hidden="true" />
            {unavailable ? "数据未更新" : problems ? `${problems} 项需要处理` : "运行正常"}
          </span>
          <div className="hero-figures">
            <div>
              <strong>{summary.online}<small>/{summary.servers}</small></strong>
              <span>服务器在线</span>
            </div>
            <div>
              <strong>{summary.healthy}<small>/{summary.measured}</small></strong>
              <span>线路正常</span>
            </div>
            <div title={traffic ? `接收 ${formatBytes(traffic.rx)} · 发送 ${formatBytes(traffic.tx)}` : undefined}>
              <strong>{trafficParts ? <>{trafficParts.value}<small>{trafficParts.unit}</small></> : "—"}</strong>
              <span>{!traffic || traffic.calendar ? "本月流量" : "本期流量"}</span>
            </div>
          </div>
        </section>
        <section className="attention">
          <h2>需要处理</h2>
          {attention.length ? (
            <ul>
              {attention.map((item) => (
                <li key={item.key}>
                  <a href={routeHref(item.route)}>
                    <Status tone={item.tone}>{item.title}</Status>
                    <span>{item.detail}</span>
                    <ChevronRight size={15} aria-hidden="true" />
                  </a>
                </li>
              ))}
            </ul>
          ) : Object.keys(errors).length ? (
            <p className="attention-clear muted">数据未更新，暂时无法判断。</p>
          ) : (
            <p className="attention-clear"><Check size={16} aria-hidden="true" />没有需要处理的事项</p>
          )}
        </section>
      </div>
      <section className="section">
        <div className="section-header">
          <h2>线路<span className="count">{data.forwards.length}</span></h2>
          <a className="text-link" href="#/forwards">全部<ArrowRight size={14} /></a>
        </div>
        {data.forwards.length ? <RouteGrid forwards={data.forwards} /> : <Empty>{errors.forwards ? "转发数据未更新。" : "还没有转发。转发把入口服务器的端口连到目标节点。"}</Empty>}
      </section>
      <section className="section">
        <div className="section-header">
          <h2>服务器<span className="count">{data.servers.length}</span></h2>
          <a className="text-link" href="#/servers">全部<ArrowRight size={14} /></a>
        </div>
        {data.servers.length ? <ServerGrid servers={data.servers} /> : <Empty>{errors.servers ? "服务器数据未更新。" : "还没有服务器。"}</Empty>}
      </section>
    </>
  );
}
