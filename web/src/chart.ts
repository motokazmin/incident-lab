import type { Sample } from "./api";
import { h } from "./dom";

const NS = "http://www.w3.org/2000/svg";
const W = 360, H = 110, L = 34, R = 6, T = 8, B = 16;
export const WINDOW = 180;

function svg(tag: string, attrs: Record<string, string | number>): SVGElement {
  const el = document.createElementNS(NS, tag);
  for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, String(v));
  return el;
}

function niceMax(v: number): number {
  for (const m of [10, 25, 50, 100, 250, 500, 1000, 2500]) if (v <= m) return m;
  return Math.ceil(v / 1000) * 1000;
}

export interface Marker { t: number; label: string }

/** Один показатель во времени, ось одна. Значения видны в заголовке и подсказке. */
export class Chart {
  readonly el: HTMLElement;
  private readonly plot = svg("svg", { viewBox: `0 0 ${W} ${H}`, class: "plot", role: "img" }) as SVGSVGElement;
  private readonly value = h("span", { class: "chart-value" }, "—");
  private readonly tip = h("div", { class: "tip", hidden: true });
  private rows: Sample[] = [];
  private x0 = 0;
  private yMax = 1;

  constructor(
    title: string,
    hint: string,
    private readonly read: (s: Sample) => number | undefined,
    private readonly format: (v: number) => string,
    private readonly fixedMax?: number,
  ) {
    this.plot.setAttribute("aria-label", title);
    this.el = h("figure", { class: "chart" },
      h("figcaption", {}, h("span", {}, title), this.value),
      h("small", { class: "chart-hint" }, hint), this.plot, this.tip);
    this.plot.addEventListener("pointermove", (e) => this.hover(e));
    this.plot.addEventListener("pointerleave", () => this.leave());
  }

  private sx(t: number) { return L + ((t - this.x0) / WINDOW) * (W - L - R); }
  private sy(v: number) { return H - B - (Math.min(v, this.yMax) / this.yMax) * (H - T - B); }

  /** Секунды без значения (нет запросов, нет замера) на графике пропускаются. */
  draw(all: Sample[], now: number, markers: Marker[]) {
    const rows = all.filter((r) => this.read(r) !== undefined);
    this.rows = rows;
    this.x0 = now - WINDOW;
    const vals = rows.map((r) => this.read(r)!);
    this.yMax = this.fixedMax ?? niceMax(Math.max(1, ...vals));
    if (rows.length) this.value.textContent = this.format(vals[vals.length - 1]);

    const kids: SVGElement[] = [];
    for (const f of [0, 0.5, 1]) {
      const v = this.yMax * f, y = this.sy(v);
      kids.push(svg("line", { x1: L, x2: W - R, y1: y, y2: y, class: f === 0 ? "axis" : "grid" }));
      const lab = svg("text", { x: L - 5, y: y + 3, class: "tick", "text-anchor": "end" });
      lab.textContent = String(Math.round(v));
      kids.push(lab);
    }
    for (const m of markers) {
      if (m.t < this.x0) continue;
      const x = this.sx(m.t);
      kids.push(svg("line", { x1: x, x2: x, y1: T, y2: H - B, class: "marker" }));
      const lab = svg("text", { x: Math.min(x + 4, W - R - 34), y: T + 7, class: "marker-label" });
      lab.textContent = m.label;
      kids.push(lab);
    }
    for (const [off, text] of [[WINDOW, "−3 мин"], [0, "сейчас"]] as const) {
      const lab = svg("text", {
        x: off ? L : W - R, y: H - 4, class: "tick", "text-anchor": off ? "start" : "end",
      });
      lab.textContent = text;
      kids.push(lab);
    }
    const pts = rows.filter((r) => r.t >= this.x0).map((r) => `${this.sx(r.t).toFixed(1)},${this.sy(this.read(r)!).toFixed(1)}`);
    if (pts.length > 1) kids.push(svg("polyline", { points: pts.join(" "), class: "series" }));
    this.plot.replaceChildren(...kids);
  }

  private hover(e: PointerEvent) {
    if (!this.rows.length) return;
    const box = this.plot.getBoundingClientRect();
    const px = ((e.clientX - box.left) / box.width) * W;
    const t = this.x0 + ((px - L) / (W - L - R)) * WINDOW;
    const row = this.rows.reduce((a, b) => (Math.abs(b.t - t) < Math.abs(a.t - t) ? b : a));
    const x = this.sx(row.t);
    this.leave();
    const hair = svg("line", { x1: x, x2: x, y1: T, y2: H - B, class: "hair" });
    const dot = svg("circle", { cx: x, cy: this.sy(this.read(row)!), r: 3.5, class: "dot" });
    hair.dataset.hover = dot.dataset.hover = "1";
    this.plot.append(hair, dot);
    const time = new Date(row.t * 1000).toLocaleTimeString("ru-RU");
    this.tip.replaceChildren(h("strong", {}, this.format(this.read(row)!)), h("span", {}, time));
    this.tip.hidden = false;
    this.tip.style.left = `${Math.min((x / W) * box.width + 8, box.width - 96)}px`;
  }

  private leave() {
    this.plot.querySelectorAll("[data-hover]").forEach((n) => n.remove());
    this.tip.hidden = true;
  }
}
