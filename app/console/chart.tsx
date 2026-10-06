"use client";

import { useEffect, useRef, useState, type KeyboardEvent, type PointerEvent } from "react";
import type { ApiForwardProbeHistory } from "../lib/api";
import { chartScale, formatProbeWindow, getProbePointWindow, hasLatency, latencyRuns, latencySegments, probeStatusLabel, sampleIndexAt } from "../lib/quality";

const height = 236;
const pad = { top: 24, right: 10, bottom: 46, left: 46 };
const plotHeight = height - pad.top - pad.bottom;
const stripY = pad.top + plotHeight + 10;

export function LatencyChart({ history, selected, onSelect }: {
  history: ApiForwardProbeHistory;
  selected: number | null;
  onSelect: (index: number | null) => void;
}) {
  const box = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(0);
  useEffect(() => {
    const observer = new ResizeObserver(([entry]) => setWidth(Math.floor(entry.contentRect.width)));
    observer.observe(box.current!);
    return () => observer.disconnect();
  }, []);

  const points = history.points;
  const count = points.length;
  const plotWidth = Math.max(0, width - pad.left - pad.right);
  const bucket = count ? plotWidth / count : 0;
  const measured = points.filter(hasLatency);
  const { step, maximum } = chartScale(Math.max(20, ...measured.map((point) => point.latency * 1.1)));
  const ticks = Array.from({ length: Math.round(maximum / step) + 1 }, (_, position) => position * step);
  const x = (index: number) => pad.left + (index + 0.5) * bucket;
  const y = (latency: number) => pad.top + plotHeight - Math.min(1, latency / maximum) * plotHeight;
  const from = Date.parse(history.summary.from);
  const to = Date.parse(history.summary.to);
  const timeTicks = width < 520 ? 3 : 5;
  const multiDay = to - from > 86_400_000;
  const active = selected != null && selected < count ? selected : null;
  const activePoint = active == null ? undefined : points[active];
  const activeWindow = active == null ? null : getProbePointWindow(points, active, history.summary.to);

  function inspect(event: PointerEvent<SVGSVGElement>) {
    const bounds = event.currentTarget.getBoundingClientRect();
    onSelect(sampleIndexAt((event.clientX - bounds.left - pad.left) / Math.max(1, plotWidth), count));
  }
  function keyboard(event: KeyboardEvent<SVGSVGElement>) {
    const moves: Record<string, number> = { ArrowLeft: -1, ArrowRight: 1 };
    if (!count || !(event.key in moves || event.key === "Home" || event.key === "End")) return;
    event.preventDefault();
    const current = active ?? count - 1;
    if (event.key === "Home") onSelect(0);
    else if (event.key === "End") onSelect(count - 1);
    else onSelect(Math.min(count - 1, Math.max(0, current + moves[event.key])));
  }

  return (
    <div className="chart" ref={box}>
      {width > 0 && (
        <svg
          width={width}
          height={height}
          role="slider"
          tabIndex={0}
          aria-label="延迟历史，方向键选择时段"
          aria-valuemin={0}
          aria-valuemax={Math.max(0, count - 1)}
          aria-valuenow={active ?? Math.max(0, count - 1)}
          aria-valuetext={activePoint && activeWindow ? `${formatProbeWindow(activeWindow.start, activeWindow.end)} ${probeStatusLabel(activePoint.status)}` : "最近时段"}
          onPointerMove={inspect}
          onPointerDown={inspect}
          onPointerLeave={() => onSelect(null)}
          onKeyDown={keyboard}
          onBlur={() => onSelect(null)}
        >
          {ticks.map((tick) => (
            <g key={tick}>
              <line className="chart-grid" x1={pad.left} x2={pad.left + plotWidth} y1={y(tick)} y2={y(tick)} />
              <text className="chart-axis" x={pad.left - 8} y={y(tick) + 4} textAnchor="end">{tick}</text>
            </g>
          ))}
          <text className="chart-axis" x={pad.left - 8} y={11} textAnchor="end">ms</text>
          {points.map((point, index) => point.status === "down" && (
            <rect key={point.checked_at} className="chart-outage" x={pad.left + index * bucket} y={pad.top} width={Math.max(1, bucket)} height={plotHeight} />
          ))}
          {!measured.length && (
            <text className="chart-empty" x={pad.left + plotWidth / 2} y={pad.top + plotHeight / 2} textAnchor="middle">此时段没有成功的 TCP 建连</text>
          )}
          <defs>
            <linearGradient id="chart-fill" x1="0" x2="0" y1="0" y2="1">
              <stop offset="0" className="chart-fill-top" />
              <stop offset="1" className="chart-fill-bottom" />
            </linearGradient>
          </defs>
          <g transform={`translate(${pad.left} ${pad.top})`}>
            {latencyRuns(points).filter((run) => run.length > 1).map((run) => (
              <path
                key={`area-${run[0]}`}
                className="chart-area"
                d={`M${((run[0] + 0.5) * bucket).toFixed(1)},${plotHeight} ${run.map((index) => `L${((index + 0.5) * bucket).toFixed(1)},${(y(points[index].latency) - pad.top).toFixed(1)}`).join(" ")} L${((run.at(-1)! + 0.5) * bucket).toFixed(1)},${plotHeight} Z`}
              />
            ))}
            {latencyRuns(points).map((run) => run.length === 1 && (
              <circle key={run[0]} className="chart-dot" cx={(run[0] + 0.5) * bucket} cy={y(points[run[0]].latency) - pad.top} r={2.2} />
            ))}
            {latencySegments(points, plotWidth, plotHeight, maximum).map((path) => <path key={path} className="chart-line" d={path} />)}
          </g>
          {points.map((point, index) => (
            <rect
              key={point.checked_at}
              className={`chart-cell cell-${point.status}`}
              x={pad.left + index * bucket + (bucket > 3 ? 0.5 : 0)}
              y={stripY}
              width={Math.max(1, bucket - (bucket > 3 ? 1 : 0))}
              height={6}
              rx={1}
            />
          ))}
          {Array.from({ length: timeTicks }, (_, position) => {
            const fraction = position / (timeTicks - 1);
            const label = new Date(from + (to - from) * fraction).toLocaleString("zh-CN", {
              ...(multiDay ? { month: "2-digit", day: "2-digit" } : {}),
              hour: "2-digit",
              minute: "2-digit",
              hour12: false,
            });
            return (
              <text key={position} className="chart-axis" x={pad.left + plotWidth * fraction} y={height - 10} textAnchor={position === 0 ? "start" : position === timeTicks - 1 ? "end" : "middle"}>
                {label}
              </text>
            );
          })}
          {active != null && (
            <g className="chart-cursor">
              <line x1={x(active)} x2={x(active)} y1={pad.top} y2={stripY + 6} />
              {activePoint && hasLatency(activePoint) && <circle cx={x(active)} cy={y(activePoint.latency)} r={4} />}
            </g>
          )}
        </svg>
      )}
    </div>
  );
}
