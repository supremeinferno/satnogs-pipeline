// The status page: summary tiles and a card per satellite, with charts, from
// status.json. The globe at the top is globe.js.
"use strict";
const SVGNS = "http://www.w3.org/2000/svg";
const RULE_TEXT = {
  discharging_in_sunlight: "Discharging while in sunlight: three readings, at least 30 minutes apart, all below −50 mA.",
  voltage_below_baseline: "Voltage in sunlight is clearly below this battery's own usual.",
  low_voltage: "Voltage is below the safe limit for this satellite.",
};
const RULE_ORDER = ["discharging_in_sunlight", "voltage_below_baseline", "low_voltage"];
const ICONS = {
  bolt: '<path d="M13 2L4 14h7l-1 8 9-12h-7z"/>',
  radio: '<circle cx="12" cy="12" r="2"/><path d="M16.2 7.8a6 6 0 0 1 0 8.4M7.8 16.2a6 6 0 0 1 0-8.4M19 5a10 10 0 0 1 0 14M5 19A10 10 0 0 1 5 5"/>',
  sun: '<circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/>',
  moon: '<path d="M20 14.5A8 8 0 1 1 9.5 4a6.5 6.5 0 0 0 10.5 10.5z"/>',
  check: '<path d="M5 12.5l4.5 4.5L19 7.5"/>',
  alert: '<path d="M12 3l10 18H2L12 3z"/><path d="M12 10v5M12 18.2v.1"/>',
  clock: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>',
  up: '<path d="M12 19V5M6 11l6-6 6 6"/>',
  down: '<path d="M12 5v14M6 13l6 6 6-6"/>',
  minus: '<path d="M6 12h12"/>',
};
const icon = (n) => `<svg class="icon" viewBox="0 0 24 24" aria-hidden="true">${ICONS[n]}</svg>`;

function el(tag, attrs, html) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) e.setAttribute(k, v);
  if (html !== undefined) e.innerHTML = html;
  return e;
}
function sv(tag, attrs, text) {
  const e = document.createElementNS(SVGNS, tag);
  for (const [k, v] of Object.entries(attrs || {})) e.setAttribute(k, v);
  if (text !== undefined) e.textContent = text;
  return e;
}
const esc = (s) => String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
function ago(ms) {
  const m = Math.round(ms / 60000);
  if (m < 2) return "just now";
  if (m < 120) return m + " minutes ago";
  const h = Math.round(m / 60);
  if (h < 48) return h + " hours ago";
  return Math.round(h / 24) + " days ago";
}
const volts = (mv) => (mv / 1000).toFixed(2);
const utc = (t, o) => new Date(t).toLocaleString("en-GB", Object.assign({ timeZone: "UTC" }, o));
const batteryTitle = (n) => (n === "main" ? "Battery" : "Battery " + n.toUpperCase());
function flow(ma) {
  if (ma == null) return null;
  if (Math.abs(ma) < 20) return { cls: "", icon: "minus", text: `idle, ${ma} mA` };
  return ma > 0 ? { cls: "charging", icon: "up", text: `charging, +${ma} mA` } : { cls: "discharging", icon: "down", text: `discharging, ${ma} mA` };
}
// What a battery is doing, in plain words, from its newest reading.
function story(r) {
  if (r.ma != null && r.ma > 20) return { cls: "good", text: r.sunlit === false ? "Charging" : "Charging from the Sun" };
  if (r.ma != null && r.ma < -20) return r.sunlit === false
    ? { cls: "calm", text: "Running on battery in the Earth's shadow, which is normal" }
    : { cls: "warn", text: "Using more power than its solar panels give" };
  if (r.ma != null) return { cls: "calm", text: "Resting: almost no current in or out" };
  return r.sunlit == null ? { cls: "calm", text: "Lighting not known" } : r.sunlit ? { cls: "good", text: "In sunlight" } : { cls: "calm", text: "In the Earth's shadow" };
}
let gradientId = 0;
function niceTicks(lo, hi, n) {
  const raw = (hi - lo) / n, mag = Math.pow(10, Math.floor(Math.log10(raw))), norm = raw / mag;
  const step = (norm < 1.5 ? 1 : norm < 3 ? 2 : norm < 7 ? 5 : 10) * mag, out = [];
  for (let v = Math.ceil(lo / step) * step; v <= hi + 1e-9; v += step) out.push(v);
  return { ticks: out, step };
}
function chartWidth() {
  const w = Math.min(window.innerWidth, 1120);
  const cols = window.innerWidth > 900 ? 2 : 1;
  const card = cols === 2 ? (w - 40 - 16) / 2 : w - 28;
  return Math.max(260, Math.floor(card - 42));
}

// The last 24 hours of one satellite: sunlit or shadow, and when beacons arrived.
function timeline(sat, t1) {
  const W = chartWidth(), H = 40, t0 = t1 - 86400000;
  const x = (t) => (W * (t - t0)) / (t1 - t0);
  const svg = sv("svg", { viewBox: `0 0 ${W} ${H + 18}`, role: "img", "aria-label": `${sat.name}: sunlight and shadow over the last 24 hours, with the beacons received` });
  const hasLight = (sat.shadows || []).length > 0 || sat.sunlit_now != null;
  svg.append(sv("rect", { x: 0, y: 0, width: W, height: H, rx: 8, fill: hasLight ? "var(--sun)" : "var(--raised)" }));
  for (const [a, b] of sat.shadows || []) {
    const xa = Math.max(0, x(Date.parse(a))), xb = Math.min(W, x(Date.parse(b)));
    if (xb > xa) svg.append(sv("rect", { x: xa, y: 0, width: xb - xa, height: H, fill: "var(--night)" }));
  }
  const times = new Set();
  for (const b of sat.batteries || []) for (const r of b.readings) { const t = Date.parse(r.t); if (t >= t0 && t <= t1) times.add(Math.round(t / 60000)); }
  for (const m of times) svg.append(sv("rect", { x: x(m * 60000) - 1, y: 8, width: 2.5, height: H - 16, rx: 1, fill: "var(--accent)" }));
  for (const [i, lab] of ["24 h ago", "12 h ago", "last update"].entries()) {
    const xx = [0, W / 2, W][i];
    svg.append(sv("text", { x: xx, y: H + 14, "text-anchor": i === 0 ? "start" : i === 2 ? "end" : "middle" }, lab));
  }
  return svg;
}

function chart(readings, key, sat, t0, t1, label) {
  const W = chartWidth(), H = key === "mv" ? 190 : 130, L = 46, R = 14, T = 12, B = 24;
  const pts = readings.filter((r) => r[key] != null && Date.parse(r.t) >= t0);
  if (!pts.length) return null;
  const vals = pts.map((r) => r[key]);
  let lo = Math.min(...vals), hi = Math.max(...vals);
  if (key === "ma") { lo = Math.min(lo, 0); hi = Math.max(hi, 0); }
  if (hi - lo < 100) { const c = (hi + lo) / 2; lo = c - 50; hi = c + 50; }
  const pad = (hi - lo) * 0.1; lo -= pad; hi += pad;
  const x = (t) => L + (W - L - R) * (t - t0) / (t1 - t0);
  const y = (v) => T + (H - T - B) * (hi - v) / (hi - lo);
  const svg = sv("svg", { class: "chart", viewBox: `0 0 ${W} ${H}`, role: "img", "aria-label": label });
  svg.append(sv("rect", { class: "plot", x: L, y: T, width: W - L - R, height: H - T - B, rx: 6 }));
  for (const [a, b] of sat.shadows || []) {
    const xa = Math.max(L, x(Date.parse(a))), xb = Math.min(W - R, x(Date.parse(b)));
    if (xb > xa) svg.append(sv("rect", { class: "band", x: xa, y: T, width: xb - xa, height: H - T - B }));
  }
  const { ticks, step } = niceTicks(lo, hi, 3);
  const dec = Math.max(0, Math.ceil(-Math.log10(step / 1000) - 1e-9));
  for (const v of ticks) {
    svg.append(sv("line", { class: "grid", x1: L, x2: W - R, y1: y(v), y2: y(v) }));
    svg.append(sv("text", { x: L - 7, y: y(v) + 4, "text-anchor": "end" }, key === "mv" ? (v / 1000).toFixed(dec) : Math.round(v)));
  }
  if (key === "ma") svg.append(sv("line", { class: "zero", x1: L, x2: W - R, y1: y(0), y2: y(0) }));
  const day = 86400000;
  for (let d = Math.ceil(t0 / day) * day; d < t1; d += day) {
    svg.append(sv("text", { x: x(d), y: H - 6, "text-anchor": "middle" }, utc(d, { day: "numeric", month: "short" })));
    svg.append(sv("line", { class: "grid", x1: x(d), x2: x(d), y1: T, y2: H - B }));
  }
  // One point per pass: messages less than 40 minutes apart are one pass,
  // drawn at their middle value with a thin bar from lowest to highest. The
  // newest pass is drawn at its newest message, the value in the heading.
  const passes = [];
  for (const r of pts) { const g = passes[passes.length - 1]; if (g && Date.parse(r.t) - Date.parse(g[g.length - 1].t) <= 40 * 60000) g.push(r); else passes.push([r]); }
  const mid = (a) => a.slice().sort((p, q) => p - q)[Math.floor(a.length / 2)];
  const agg = passes.map((g, i) => {
    const newest = i === passes.length - 1, end = g[g.length - 1], vs = g.map((r) => r[key]);
    return { t: newest ? Date.parse(end.t) : mid(g.map((r) => Date.parse(r.t))), v: newest ? end[key] : mid(vs), lo: Math.min(...vs), hi: Math.max(...vs), n: g.length, sunlit: (newest ? end : g[Math.floor(g.length / 2)]).sunlit, newest };
  });
  // a line through the passes with a soft gradient below it (for current:
  // green above zero, amber below)
  const id = "g" + ++gradientId, base = key === "ma" ? y(0) : H - B;
  const defs = sv("defs", {});
  const grad = sv("linearGradient", { id, x1: 0, y1: 0, x2: 0, y2: 1 });
  const stops = key === "ma"
    ? [[0, "var(--ok)", .35], [(y(0) - T) / (H - T - B), "var(--ok)", .04], [(y(0) - T) / (H - T - B), "var(--warn)", .04], [1, "var(--warn)", .35]]
    : [[0, "var(--accent)", .32], [1, "var(--accent)", 0]];
  for (const [o, c, a] of stops) grad.append(sv("stop", { offset: o, "stop-color": c, "stop-opacity": a }));
  defs.append(grad); svg.append(defs);
  const P = agg.map((a) => [x(a.t), y(a.v)]);
  if (P.length > 1) {
    const d = P.map(([a, b], i) => `${i ? "L" : "M"}${a.toFixed(1)} ${b.toFixed(1)}`).join("");
    svg.append(sv("path", { d: `${d}L${P[P.length - 1][0].toFixed(1)} ${base}L${P[0][0].toFixed(1)} ${base}Z`, fill: `url(#${id})` }));
    svg.append(sv("path", { class: "trend", d }));
  }
  const fmt = (v) => (key === "mv" ? volts(v) + " V" : Math.round(v) + " mA");
  for (const a of agg) {
    const cx = x(a.t), cy = y(a.v);
    if (a.hi > a.lo) svg.append(sv("line", { class: "range", x1: cx, x2: cx, y1: y(a.hi), y2: y(a.lo) }));
    if (a.newest) svg.append(sv("circle", { class: "halo", cx, cy, r: 11 }));
    const c = sv("circle", { class: "dot" + (a.sunlit === false ? " shade" : "") + (a.newest ? " last" : ""), cx, cy, r: a.newest ? 5 : 3.4 });
    const state = a.sunlit == null ? "" : a.sunlit ? ", in sunlight" : ", in the Earth's shadow";
    const what = a.n === 1 ? fmt(a.v) : `${a.n} radio messages, ${fmt(a.lo)} to ${fmt(a.hi)}`;
    c.append(sv("title", {}, `${utc(a.t, { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" })} UTC${state}: ${what}`));
    svg.append(c);
    if (a.newest) {
      const anchor = cx > W - 60 ? "end" : "start";
      svg.append(sv("text", { class: "val", x: anchor === "end" ? cx - 10 : cx + 10, y: cy - 9, "text-anchor": anchor }, fmt(a.v)));
    }
  }
  return svg;
}

function renderBattery(b, sat, t0, t1) {
  const rs = b.readings, last = rs[rs.length - 1];
  const box = el("div", { class: "batt" });
  const f = flow(last.ma), st = story(last);
  // the gauge: where the newest voltage sits between the lowest and highest this battery has shown
  const lo = Math.min(...rs.map((r) => r.mv)), hi = Math.max(...rs.map((r) => r.mv));
  const level = hi - lo < 10 ? 50 : Math.round(100 * (last.mv - lo) / (hi - lo));
  box.append(el("div", { class: "bhead" },
    `<h3>${esc(batteryTitle(b.battery))}</h3><div class="big">${volts(last.mv)}<span>volts</span></div>` +
    (f ? `<span class="flow ${f.cls}">${icon(f.icon)}${f.text}</span>` : "")));
  box.append(el("p", { class: `story ${st.cls}` }, `At its last radio message, ${ago(Date.now() - Date.parse(last.t))}: ${st.text.charAt(0).toLowerCase() + st.text.slice(1)}.`));
  box.append(el("div", { class: "gauge", role: "img", "aria-label": `${level}% of the way from its lowest (${volts(lo)} V) to its highest (${volts(hi)} V) seen` },
    `<div class="cell"><i style="width:${100 - Math.max(4, level)}%"></i></div><div class="tip"></div>` +
    `<div class="gauge-scale"><span>lowest ${volts(lo)} V</span><span>${level}% of usual range</span><span>highest ${volts(hi)} V</span></div>`));
  box.append(el("p", { class: "sub" }, `Last radio message ${ago(Date.now() - Date.parse(last.t))}. ${rs.length} messages in ${((d) => d + (d === 1 ? " day" : " days"))(Math.max(1, Math.round((t1 - Date.parse(rs[0].t)) / 86400000)))}.`));
  const vc = chart(rs, "mv", sat, t0, t1, `${sat.name} ${batteryTitle(b.battery)} voltage, last three days`);
  if (vc) { const bx = el("div", { class: "chartbox" }, '<p class="cap">Voltage over three days <span>(each dot is one pass over a ground station; darker bands: the Earth\'s shadow)</span></p>'); bx.append(vc); box.append(bx); }
  if (rs.some((r) => r.ma != null)) {
    const cc = chart(rs, "ma", sat, t0, t1, `${sat.name} ${batteryTitle(b.battery)} current, last three days`);
    if (cc) { const bx = el("div", { class: "chartbox" }, '<p class="cap">Current <span>(above the line: charging; below: using the battery)</span></p>'); bx.append(cc); box.append(bx); }
  }
  const fired = RULE_ORDER.filter((r) => b.anomalies && b.anomalies[r]);
  if (fired.length) box.append(el("ul", { class: "rules" }, fired.map((r) => `<li>${icon("alert")}<span>${RULE_TEXT[r]}</span></li>`).join("")));
  else box.append(el("p", { class: "quiet" }, `${icon("check")}No warning for this battery.`));
  const rows = rs.slice(-10).reverse().map((r) => `<tr><td>${utc(r.t, { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" })}</td><td>${volts(r.mv)} V</td><td>${r.ma == null ? "n/a" : r.ma + " mA"}</td><td>${r.sunlit == null ? "unknown" : r.sunlit ? "sunlight" : "shadow"}</td></tr>`).join("");
  box.append(el("details", {}, `<summary>See the numbers: the latest 10 radio messages</summary><table><thead><tr><th>Time (UTC)</th><th>Voltage</th><th>Current</th><th>Light</th></tr></thead><tbody>${rows}</tbody></table>`));
  return box;
}

function renderSatellite(sat, snapMs) {
  const card = el("article", { class: "card", id: "sat-" + sat.norad });
  const anyAlert = (sat.batteries || []).some((b) => Object.values(b.anomalies || {}).some(Boolean));
  const pill = anyAlert ? ["warn", "alert", "Alert"] : sat.stale ? ["warn", "clock", "No recent beacons"] : ["ok", "check", "All quiet"];
  card.append(el("header", { class: "card-head" }, `<div><h2>${esc(sat.name)}</h2><span class="norad">NORAD ${sat.norad}</span><br><button type="button" class="show-on-globe" data-norad="${sat.norad}">Show on the globe</button></div><span class="pill ${pill[0]}">${icon(pill[1])}${pill[2]}</span>`));
  const sunny = sat.sunlit_now;
  const verdict = anyAlert ? "Something needs a look: see the alert below." : sat.stale ? "No radio message for a while, so how it is doing is not known." : "Its batteries look normal.";
  const where = sunny == null ? "" : sunny ? " At the last update it was in sunlight." : " At the last update it was in the Earth's shadow.";
  card.append(el("p", { class: "verdict" }, verdict + where));
  const last = sat.last_beacon ? `Last radio message ${ago(Date.now() - Date.parse(sat.last_beacon))}` : "No radio message yet";
  card.append(el("div", { class: "light" },
    `<div class="orb ${sunny ? "sun" : "moon"}">${icon(sunny ? "sun" : "moon")}</div>` +
    `<div><strong>${sunny == null ? "Lighting unknown" : sunny ? "In sunlight at the last update" : "In the Earth's shadow at the last update"}</strong><small>${last}</small></div>`));
  const tl = el("div", { class: "timeline" }); tl.append(timeline(sat, snapMs)); card.append(tl);
  card.append(el("p", { class: "key" }, '<span><i style="background:var(--sun)"></i>sunlight</span><span><i style="background:var(--night)"></i>Earth\'s shadow</span><span><i style="background:var(--accent)"></i>a radio message heard</span>'));
  if (sat.stale && sat.last_beacon) card.append(el("p", { class: "note" }, `Nothing heard for longer than ${sat.stale_after_hours} hours, the longest silence this satellite normally has.`));
  if (sat.error) card.append(el("p", { class: "note" }, "Note: " + esc(sat.error)));
  const bats = (sat.batteries || []).filter((b) => b.readings && b.readings.length);
  if (!bats.length) { card.append(el("p", { class: "quiet" }, "No battery readings yet.")); return card; }
  const first = Math.min(...bats.map((b) => Date.parse(b.readings[0].t)));
  const t0 = Math.max(first - 3 * 3600000, snapMs - 3 * 86400000);
  for (const b of bats) card.append(renderBattery(b, sat, t0, snapMs));
  return card;
}

function renderTiles(snap) {
  const sats = snap.satellites;
  const batteries = sats.reduce((n, s) => n + (s.batteries || []).length, 0);
  const beacons = sats.reduce((n, s) => n + Math.max(0, ...(s.batteries || []).map((b) => b.readings.length)), 0);
  const alerts = sats.reduce((n, s) => n + (s.batteries || []).filter((b) => Object.values(b.anomalies || {}).some(Boolean)).length + (s.stale ? 1 : 0), 0);
  const tile = (ic, n, l, cls) => `<div class="tile ${cls || ""}">${icon(ic)}<div class="n">${n}</div><div class="l">${l}</div></div>`;
  const lit = sats.filter((x) => x.sunlit_now).length;
  document.getElementById("tiles").innerHTML =
    tile("sun", `${lit} of ${sats.length}`, "satellites in sunlight at the last update") + tile("bolt", batteries, "batteries watched") +
    tile("radio", beacons.toLocaleString("en-GB"), "radio messages heard in two weeks") +
    (alerts ? tile("alert", alerts, alerts === 1 ? "thing needs a look" : "things need a look", "bad") : tile("check", "All quiet", "no warnings right now", "good"));
}

let snapshot = null;
function render() {
  if (!snapshot) return;
  const snapMs = Date.parse(snapshot.generated_at);
  document.getElementById("updated").textContent = `Updated ${utc(snapMs, { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" })} UTC, ${ago(Date.now() - snapMs)}. It refreshes every hour.`;
  renderTiles(snapshot);
  document.getElementById("cards").replaceChildren(...snapshot.satellites.map((s) => renderSatellite(s, snapMs)));
}
async function main() {
  try {
    const res = await fetch("status.json", { cache: "no-store" });
    if (!res.ok) throw new Error("status.json: HTTP " + res.status);
    snapshot = await res.json();
    render();
    window.statusSnapshot = snapshot; // for the globe below
    document.dispatchEvent(new CustomEvent("snapshot", { detail: snapshot }));
  } catch (e) {
    document.getElementById("updated").textContent = "";
    document.getElementById("cards").replaceChildren();
    const box = document.getElementById("error");
    box.style.display = "block"; box.textContent = "Could not load the data: " + e.message;
  }
}
let resizeTimer;
window.addEventListener("resize", () => { clearTimeout(resizeTimer); resizeTimer = setTimeout(render, 200); });
main();
