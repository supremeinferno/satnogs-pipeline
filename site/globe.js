// The globe at the top: the Earth lit by the Sun, the shadow the alerts use (a
// cylinder behind the Earth), and the satellites where status.json says they
// are. Positions and lighting come from snapshot's SGP4 run (the "track"
// fields), every 30 seconds for six hours; this only interpolates and draws.
// Everything is Earth-fixed and one unit is one Earth radius, as in
// orbitscreen's globe. If WebGL or the CDN is missing, the page works without it.
// It runs after app.js, which loads status.json, and uses its ago() and
// batteryTitle().
import * as THREE from "three";
import { OrbitControls } from "three/addons/controls/OrbitControls.js";

const SUN = 0xffc457, SHADE = 0x8c9bff, ALERT = 0xff5d73, R = 6371;
const PATH_MIN = 50; // minutes of track drawn either side of a satellite
const $ = (id) => document.getElementById(id);
const hex = (c) => "#" + c.toString(16).padStart(6, "0");
const ease = (x) => 1 - (1 - x) ** 3;

const snap = window.statusSnapshot || await new Promise((r) => document.addEventListener("snapshot", (e) => r(e.detail), { once: true }));
const sats = (snap.satellites || []).filter((s) => s.track && s.track.length > 1);
if (snap.track_start && snap.sun && snap.sun.length > 1 && sats.length) {
  try { start(); } catch (e) { console.warn("globe:", e); $("labels").replaceChildren(); $("globe").hidden = true; }
}

// Earth-fixed position for latitude, longitude (degrees) and altitude (km),
// matching the texture layout of THREE.SphereGeometry.
function toVec(lat, lon, alt, v = new THREE.Vector3()) {
  const r = 1 + alt / R, p = lat * Math.PI / 180, l = lon * Math.PI / 180;
  return v.set(r * Math.cos(p) * Math.cos(l), r * Math.sin(p), -r * Math.cos(p) * Math.sin(l));
}

function start() {
  const t0 = Date.parse(snap.track_start), step = snap.track_step_s * 1000, n = snap.sun.length;
  const tEnd = t0 + step * (n - 1);
  // Interpolates a track (rows of [lat, lon, alt, lit]) at time t, clamped to its ends.
  const sample = (rows, t) => {
    const f = Math.min(Math.max((t - t0) / step, 0), rows.length - 1), i = Math.min(Math.floor(f), rows.length - 2), k = f - i;
    const a = rows[i], b = rows[i + 1];
    let dl = b[1] - a[1]; if (dl > 180) dl -= 360; if (dl < -180) dl += 360;
    let lon = a[1] + dl * k; if (lon > 180) lon -= 360; if (lon < -180) lon += 360;
    return { lat: a[0] + (b[0] - a[0]) * k, lon, alt: a[2] === undefined ? 0 : a[2] + (b[2] - a[2]) * k, lit: (k < .5 ? a : b)[3] === 1 };
  };

  const band = $("space"), canvas = $("globe");
  const reduced = matchMedia("(prefers-reduced-motion: reduce)").matches;
  const renderer = new THREE.WebGLRenderer({ canvas, antialias: true, alpha: true });
  renderer.setPixelRatio(Math.min(window.devicePixelRatio || 1, 2));
  const world = new THREE.Scene();
  const camera = new THREE.PerspectiveCamera(34, 1, 0.01, 300);
  // The Earth and everything tied to it turn together as the page scrolls.
  const system = new THREE.Group(); world.add(system);

  // Turning, not zooming: the globe's size follows the layout, and the wheel
  // and one finger keep scrolling the page. Two fingers turn it on a phone.
  const controls = new OrbitControls(camera, canvas);
  Object.assign(controls, { enableDamping: true, enablePan: false, enableZoom: false, rotateSpeed: 0.5, autoRotate: !reduced, autoRotateSpeed: 0.3 });
  controls.touches = { ONE: null, TWO: THREE.TOUCH.DOLLY_ROTATE };
  canvas.style.touchAction = "pan-y";
  if (matchMedia("(pointer: coarse)").matches) $("scene-hint").textContent = "Two fingers to turn · tap a satellite";

  // Earth: an equirectangular canvas (ocean, graticule, land) lit by the Sun.
  const tex = document.createElement("canvas"); tex.width = 2048; tex.height = 1024;
  const texture = new THREE.CanvasTexture(tex); texture.colorSpace = THREE.SRGBColorSpace; texture.anisotropy = 4;
  paintEarth(tex, null);
  const earth = new THREE.Mesh(new THREE.SphereGeometry(1, 96, 64), new THREE.MeshPhongMaterial({ map: texture, shininess: 9, specular: 0x1c3048 }));
  system.add(earth);
  world.add(new THREE.AmbientLight(0x9fb4d0, 0.16));
  const sunLight = new THREE.DirectionalLight(0xfff1d6, 3.4);
  system.add(sunLight);
  fetch("https://cdn.jsdelivr.net/npm/world-atlas@2.0.2/land-110m.json").then((r) => r.json()).then((w) => {
    if (!window.topojson) return;
    paintEarth(tex, topojson.feature(w, w.objects.land)); texture.needsUpdate = true;
  }).catch(() => {});

  // Atmosphere: a slightly larger sphere, seen from inside, glowing edge-on.
  system.add(new THREE.Mesh(new THREE.SphereGeometry(1.07, 64, 32), new THREE.ShaderMaterial({
    side: THREE.BackSide, blending: THREE.AdditiveBlending, transparent: true, depthWrite: false,
    vertexShader: "varying vec3 n; void main() { n = normalize(normalMatrix * normal); gl_Position = projectionMatrix * modelViewMatrix * vec4(position, 1.0); }",
    fragmentShader: "varying vec3 n; void main() { float i = pow(max(0.0, 0.64 - dot(n, vec3(0.0, 0.0, 1.0))), 3.0); gl_FragColor = vec4(0.3, 0.75, 1.0, 1.0) * i * 1.7; }",
  })));

  // Stars, far away, in two sizes.
  for (const [count, size, color] of [[2200, 1.1, 0x6f7f95], [260, 2.1, 0xc9d6ea]]) {
    const pos = new Float32Array(3 * count);
    for (let k = 0; k < pos.length; k += 3) { const v = new THREE.Vector3().randomDirection().multiplyScalar(120); pos.set([v.x, v.y, v.z], k); }
    const g = new THREE.BufferGeometry(); g.setAttribute("position", new THREE.BufferAttribute(pos, 3));
    world.add(new THREE.Points(g, new THREE.PointsMaterial({ color, size, sizeAttenuation: false, transparent: true, opacity: .85 })));
  }

  // A soft glow, shared by the satellites and the Sun.
  const glow = (() => {
    const c = document.createElement("canvas"); c.width = c.height = 128; const g = c.getContext("2d");
    const grad = g.createRadialGradient(64, 64, 0, 64, 64, 64);
    grad.addColorStop(0, "rgba(255,255,255,1)"); grad.addColorStop(0.2, "rgba(255,255,255,.5)"); grad.addColorStop(1, "rgba(255,255,255,0)");
    g.fillStyle = grad; g.fillRect(0, 0, 128, 128); return new THREE.CanvasTexture(c);
  })();
  const sprite = (color, scale) => {
    const s = new THREE.Sprite(new THREE.SpriteMaterial({ map: glow, color, transparent: true, blending: THREE.AdditiveBlending, depthWrite: false }));
    s.scale.setScalar(scale); return s;
  };
  // The Sun, far off in its true direction.
  const sunGlow = sprite(0xffd88a, 26), sunCore = sprite(0xfff4dc, 5);
  system.add(sunGlow, sunCore);

  // The Earth's shadow as watch models it: a cylinder of the Earth's radius
  // pointing away from the Sun, with rings so it reads as one.
  const LEN = 4.2, shadow = new THREE.Group();
  const cyl = new THREE.Mesh(new THREE.CylinderGeometry(1, 1, LEN, 72, 1, true),
    new THREE.MeshBasicMaterial({ color: 0x4757c9, transparent: true, opacity: 0.13, side: THREE.DoubleSide, depthWrite: false }));
  cyl.position.y = LEN / 2; shadow.add(cyl);
  const ringGeo = new THREE.BufferGeometry().setFromPoints(Array.from({ length: 97 }, (_, k) => new THREE.Vector3(Math.cos(k / 96 * 2 * Math.PI), 0, Math.sin(k / 96 * 2 * Math.PI))));
  for (let d = 1; d <= 4; d++) {
    const ring = new THREE.Line(ringGeo, new THREE.LineBasicMaterial({ color: 0x8c9bff, transparent: true, opacity: 0.62 - d * 0.12, depthWrite: false }));
    ring.position.y = d; shadow.add(ring);
  }
  system.add(shadow);

  // The satellites: a dot, a glow, the path around it, a larger invisible
  // target for clicks, and a label.
  const steps = Math.round(PATH_MIN * 60000 / step);
  const hitGeo = new THREE.SphereGeometry(0.06, 8, 6), hitMat = new THREE.MeshBasicMaterial({ transparent: true, opacity: 0, depthWrite: false });
  const objs = sats.map((s) => {
    const alert = s.stale || (s.batteries || []).some((b) => Object.values(b.anomalies || {}).some(Boolean));
    const dot = new THREE.Mesh(new THREE.SphereGeometry(0.018, 16, 12), new THREE.MeshBasicMaterial({ color: SUN }));
    const halo = sprite(SUN, 0.15);
    const hit = new THREE.Mesh(hitGeo, hitMat);
    const pathGeo = new THREE.BufferGeometry();
    pathGeo.setAttribute("position", new THREE.BufferAttribute(new Float32Array(3 * (2 * steps + 2)), 3));
    pathGeo.setAttribute("color", new THREE.BufferAttribute(new Float32Array(3 * (2 * steps + 2)), 3));
    const path = new THREE.Line(pathGeo, new THREE.LineBasicMaterial({ vertexColors: true, transparent: true, blending: THREE.AdditiveBlending, depthWrite: false }));
    path.frustumCulled = false;
    system.add(dot, halo, hit, path);
    const reading = (s.batteries || []).map((b) => b.readings[b.readings.length - 1]).filter(Boolean)[0];
    const label = document.createElement("button");
    label.type = "button"; label.className = "sat-label";
    label.innerHTML = `<i></i>${esc(s.name)}${reading ? ` <small>${(reading.mv / 1000).toFixed(2)} V</small>` : ""}`;
    $("labels").append(label);
    const o = { s, alert, dot, halo, hit, path, label, pos: new THREE.Vector3(), world: new THREE.Vector3(), now: null };
    hit.userData.o = o;
    label.addEventListener("click", () => select(o));
    return o;
  });

  // Time: now, or where the slider or Play has moved it.
  let offsetMin = 0, playing = false;
  const scrub = $("scrub"), when = $("when"), play = $("play");
  const setPlaying = (p) => { playing = p; play.textContent = p ? "Pause" : "Play"; play.setAttribute("aria-pressed", String(p)); };
  const bounds = () => [Math.ceil((t0 - Date.now()) / 60000), Math.floor((tEnd - Date.now()) / 60000)];
  scrub.addEventListener("input", () => { offsetMin = Number(scrub.value); setPlaying(false); });
  play.addEventListener("click", () => { if (!playing && offsetMin >= bounds()[1] - 1) offsetMin = 0; setPlaying(!playing); });
  $("now").addEventListener("click", () => { offsetMin = 0; setPlaying(false); });
  const fmtOffset = (m) => {
    m = Math.round(m);
    if (m === 0) return "Live";
    const a = Math.abs(m), h = Math.floor(a / 60), mm = a % 60;
    return (m > 0 ? "in " : "") + (h ? `${h} h ${String(mm).padStart(2, "0")} min` : `${mm} min`) + (m < 0 ? " ago" : "");
  };

  // Selecting a satellite: the camera turns to it and a card says how it is.
  let selected = null, fly = null, lastPop = 0, follow = false, simT = Date.now(), popHTML = "";
  const pop = $("sat-pop");
  function select(o) {
    if (selected) selected.label.classList.remove("on");
    selected = o;
    if (!o) { pop.hidden = true; follow = false; return; }
    o.label.classList.add("on");
    controls.autoRotate = false;
    const len = camera.position.length(), to = o.world.clone().normalize();
    to.y = to.y * 0.8 + 0.12; to.normalize();
    fly = reduced ? null : { from: camera.position.clone().normalize(), to, len, t0: performance.now() };
    if (reduced) camera.position.copy(to).multiplyScalar(len);
    fillPop(); pop.hidden = false;
  }
  function fillPop() {
    const o = selected; if (!o) return;
    const s = o.s, p = o.now || { lit: false, alt: 0, lat: 0, lon: 0 };
    const c = o.alert ? ALERT : p.lit ? SUN : SHADE;
    // when the lighting next changes, from the track
    const f = Math.max(0, Math.ceil((simT - t0) / step));
    let change = "";
    for (let i = f; i < s.track.length; i++) {
      if ((s.track[i][3] === 1) !== p.lit) {
        const m = Math.max(1, Math.round((t0 + i * step - simT) / 60000));
        change = `${p.lit ? "enters the shadow" : "back in sunlight"} in ${m < 60 ? m + " min" : Math.floor(m / 60) + " h " + String(m % 60).padStart(2, "0") + " min"}`;
        break;
      }
    }
    if (simT > tEnd) change = "not known: this data has run out";
    else if (!change) change = p.lit ? "in sunlight until the data ends" : "in the shadow until the data ends";
    const rows = (s.batteries || []).filter((b) => b.readings.length).map((b) => {
      const r = b.readings[b.readings.length - 1];
      return `<dt>${esc(batteryTitle(b.battery))}</dt><dd>${(r.mv / 1000).toFixed(2)} V${r.ma != null ? `, ${r.ma > 0 ? "+" : ""}${r.ma} mA` : ""}</dd>`;
    }).join("");
    const html = `<button type="button" class="ghost x" aria-label="Close">×</button>
      <h3><span style="color:${hex(c)}">●</span> ${esc(s.name)}</h3><p class="sub">NORAD ${s.norad} · ${Math.round(p.alt)} km up · ${Math.abs(p.lat).toFixed(0)}° ${p.lat >= 0 ? "N" : "S"}, ${Math.abs(p.lon).toFixed(0)}° ${p.lon >= 0 ? "E" : "W"}</p>
      <dl><dt>Light</dt><dd>${p.lit ? "in sunlight" : "in the Earth's shadow"}</dd><dt>Next</dt><dd>${change}</dd>${rows}
      <dt>Last beacon</dt><dd>${s.last_beacon ? ago(Date.now() - Date.parse(s.last_beacon)) : "none yet"}</dd>
      <dt>Status</dt><dd>${o.alert ? (s.stale ? "no recent beacons" : "an alert is firing") : "all quiet"}</dd></dl>
      ${spark(s)}
      <div class="pop-actions"><button type="button" class="ghost follow" aria-pressed="${follow}">${follow ? "Following" : "Follow"}</button><button type="button" class="ghost go">Batteries and charts ↓</button></div>`;
    if (html === popHTML) return; // unchanged: keep the buttons under the pointer
    pop.innerHTML = popHTML = html;
    pop.querySelector(".follow").onclick = () => { follow = !follow; fly = null; popHTML = ""; fillPop(); };
    pop.querySelector(".x").onclick = () => select(null);
    pop.querySelector(".go").onclick = () => $("sat-" + s.norad)?.scrollIntoView({ behavior: reduced ? "auto" : "smooth", block: "start" });
  }
  document.addEventListener("click", (e) => {
    const b = e.target.closest(".show-on-globe"); if (!b) return;
    const o = objs.find((x) => x.s.norad === Number(b.dataset.norad)); if (!o) return;
    band.scrollIntoView({ behavior: reduced ? "auto" : "smooth" });
    setTimeout(() => select(o), reduced ? 0 : 450);
  });

  // Clicks and hovers on the globe itself.
  const ray = new THREE.Raycaster(), ndc = new THREE.Vector2();
  const hitAt = (e) => {
    const r = canvas.getBoundingClientRect();
    ndc.set((e.clientX - r.left) / r.width * 2 - 1, -((e.clientY - r.top) / r.height) * 2 + 1);
    ray.setFromCamera(ndc, camera);
    const h = ray.intersectObjects([earth, ...objs.map((o) => o.hit)])[0];
    return h && h.object.userData.o;
  };
  let down = null;
  canvas.addEventListener("pointerdown", (e) => {
    down = [e.clientX, e.clientY]; controls.autoRotate = false; $("scene-hint").hidden = true;
    if (follow) { follow = false; popHTML = ""; fillPop(); } // a drag takes the camera back
  });
  canvas.addEventListener("pointerup", (e) => {
    if (down && Math.hypot(e.clientX - down[0], e.clientY - down[1]) < 6) select(hitAt(e) || null);
    down = null;
  });
  canvas.addEventListener("pointermove", (e) => { if (!down && e.pointerType === "mouse") canvas.classList.toggle("pointing", !!hitAt(e)); });

  // Layout: where the globe sits and how big, from the band's size.
  let W = 1, H = 1, baseDist = 4;
  const layout = () => {
    W = band.clientWidth; H = band.clientHeight;
    renderer.setSize(W, H, false); camera.aspect = W / H;
    let cx, cy, rp;
    if (W < 900) { rp = Math.min(W * 0.36, 210); cx = W / 2; cy = Math.min(W * 0.58, 280); }
    else { rp = Math.min(H * 0.34, W * 0.23); cx = W * 0.68; cy = H * 0.46; }
    camera.setViewOffset(W, H, W / 2 - cx, H / 2 - cy, W, H);
    const k = rp * Math.tan(camera.fov * Math.PI / 360) / (H / 2);
    baseDist = 1 / Math.sin(Math.atan(k));
    camera.updateProjectionMatrix();
  };

  // Where the camera starts: side-on to the Sun, so day, night and the shadow all show.
  const s0 = sample(snap.sun, Date.now());
  camera.position.copy(toVec(s0.lat, s0.lon, 0).normalize().applyAxisAngle(new THREE.Vector3(0, 1, 0), 1.15).setY(0.36).normalize()).multiplyScalar(6);

  let visible = true;
  new IntersectionObserver(([e]) => { visible = e.isIntersecting; if (!visible && selected) select(null); }).observe(band);
  new ResizeObserver(layout).observe(band);

  const yAxis = new THREE.Vector3(0, 1, 0), sunDir = new THREE.Vector3(), tmp = new THREE.Vector3(), col = new THREE.Color();
  const occluded = (p) => { // is p behind the Earth, seen from the camera?
    const c = camera.position, d = tmp.subVectors(p, c), a = d.dot(d), b = 2 * c.dot(d), q = c.dot(c) - 1, disc = b * b - 4 * a * q;
    if (disc <= 0) return false;
    const s1 = (-b - Math.sqrt(disc)) / (2 * a);
    return s1 > 0 && s1 < 0.999;
  };
  const born = performance.now();
  let lastFrame = born;

  function frame(now) {
    requestAnimationFrame(frame);
    const dt = Math.min(now - lastFrame, 100) / 1000; lastFrame = now;
    if (!visible || document.hidden) return;
    const [lo, hi] = bounds();
    if (playing) { offsetMin += dt * 2; if (offsetMin >= hi) { offsetMin = hi; setPlaying(false); } } // 2 minutes a second
    offsetMin = Math.min(Math.max(offsetMin, lo), Math.max(hi, lo));
    scrub.min = String(lo); scrub.max = String(Math.max(hi, lo)); scrub.value = String(Math.round(offsetMin));
    when.textContent = fmtOffset(offsetMin);
    $("now").hidden = Math.round(offsetMin) === 0;
    const t = Date.now() + offsetMin * 60000;
    simT = t;
    const note = $("scene-note");
    note.hidden = t <= tEnd;
    if (!note.hidden) note.textContent = "These positions are from the last update, which is late: the globe stops where its data ends.";

    // Scroll: the system turns and the camera draws back as the band leaves.
    const p = Math.min(Math.max(-band.getBoundingClientRect().top / Math.max(band.offsetHeight, 1), 0), 1);
    system.rotation.y = p * 0.9;
    $("labels").classList.toggle("away", p > 0.35);

    const sp = sample(snap.sun, t);
    toVec(sp.lat, sp.lon, 0, sunDir).normalize();
    sunLight.position.copy(sunDir).multiplyScalar(10);
    sunGlow.position.copy(sunDir).multiplyScalar(70); sunCore.position.copy(sunGlow.position);
    shadow.quaternion.setFromUnitVectors(yAxis, tmp.copy(sunDir).negate());
    system.updateMatrixWorld();

    for (const o of objs) {
      const tr = o.s.track, q = sample(tr, t);
      o.now = q;
      toVec(q.lat, q.lon, q.alt, o.pos);
      const c = o.alert ? ALERT : q.lit ? SUN : SHADE;
      o.dot.position.copy(o.pos); o.dot.material.color.setHex(c);
      o.halo.position.copy(o.pos); o.halo.material.color.setHex(c);
      o.hit.position.copy(o.pos);
      o.label.querySelector("i").style.background = hex(c);
      o.label.title = `${o.s.name}: ${q.lit ? "in sunlight" : "in the Earth's shadow"}${o.alert ? ", needs a look" : ""}`;
      // the path: track points within PATH_MIN minutes, fading with distance in time
      const pos = o.path.geometry.attributes.position, colr = o.path.geometry.attributes.color;
      const f = (t - t0) / step, i0 = Math.max(0, Math.ceil(f - steps)), i1 = Math.min(tr.length - 1, Math.floor(f + steps));
      let k = 0;
      for (let i = i0; i <= i1; i++) {
        const r = tr[i];
        toVec(r[0], r[1], r[2], tmp); pos.setXYZ(k, tmp.x, tmp.y, tmp.z);
        const fade = Math.max(0, 1 - Math.abs(i - f) / steps) ** 1.4 * (o === selected ? 1.2 : 0.85);
        col.setHex(r[3] === 1 ? SUN : SHADE).multiplyScalar(fade); colr.setXYZ(k, col.r, col.g, col.b);
        k++;
      }
      o.path.geometry.setDrawRange(0, k); pos.needsUpdate = true; colr.needsUpdate = true;
      o.world.copy(o.pos).applyMatrix4(system.matrixWorld);
    }

    // The camera: the fly-in on load, a turn to a selected satellite, the
    // user's drags, and the distance from the layout and the scroll.
    const intro = reduced ? 1 : 1 + 1.6 * (1 - ease(Math.min(1, (now - born) / 2600)));
    if (fly) {
      const k = Math.min(1, (now - fly.t0) / 1100), e = k < .5 ? 4 * k ** 3 : 1 - (-2 * k + 2) ** 3 / 2;
      camera.position.copy(fly.from).lerp(fly.to, e).normalize();
      camera.lookAt(0, 0, 0);
      if (k === 1) fly = null;
    } else if (follow && selected) {
      // keep the selected satellite in view as it moves
      const to = tmp.copy(selected.world).normalize(); to.y = to.y * 0.8 + 0.12;
      camera.position.normalize().lerp(to.normalize(), 0.05);
      camera.lookAt(0, 0, 0);
    } else controls.update();
    camera.position.setLength(baseDist * intro * (1 + 0.5 * p));
    camera.lookAt(0, 0, 0);

    // Labels: above the dot if there is room, else below it, else higher up;
    // the ones in front of the Earth are placed first.
    const placed = [];
    // the intro text, which labels keep off on wide screens
    let textBox = null;
    if (W >= 900) {
      const br = band.getBoundingClientRect(), rs = [...band.querySelectorAll(".space-inner > :not([hidden])")].map((e) => e.getBoundingClientRect());
      if (rs.length) textBox = { l: Math.min(...rs.map((r) => r.left)) - br.left, r: Math.max(...rs.map((r) => r.right)) - br.left + 8, t: rs[0].top - br.top, b: Math.max(...rs.map((r) => r.bottom)) - br.top };
    }
    const order = objs.map((o) => { const s2 = o.world.clone().project(camera); return { o, s2, back: occluded(o.world) }; }).sort((a, b) => a.back - b.back);
    for (const { o, s2, back } of order) {
      o.label.classList.toggle("behind", back);
      o.label.hidden = s2.z > 1;
      const lw = o.label.offsetWidth, lh = o.label.offsetHeight, half = lw / 2 + 6;
      const x = Math.min(Math.max((s2.x + 1) / 2 * W, half), W - half), dotY = (1 - s2.y) / 2 * H;
      const free = (top) => top > 8 && placed.every((p) => x + lw / 2 < p.l || x - lw / 2 > p.r || top + lh < p.t || top > p.b);
      let top = [dotY - lh - 9, dotY + 9, dotY - 2 * lh - 14, dotY + lh + 14].find(free);
      if (top === undefined) top = Math.max(dotY - lh - 9, 8);
      o.label.classList.toggle("over-text", !!textBox && x - lw / 2 < textBox.r && x + lw / 2 > textBox.l && top < textBox.b && top + lh > textBox.t);
      placed.push({ l: x - lw / 2 - 3, r: x + lw / 2 + 3, t: top - 3, b: top + lh + 3 });
      o.label.style.transform = `translate(${Math.round(x - lw / 2)}px, ${Math.round(top)}px)`;
    }
    if (selected && now - lastPop > 500) { fillPop(); lastPop = now; }
    renderer.render(world, camera);
  }

  layout();
  band.classList.add("live");
  canvas.hidden = false;
  for (const id of ["scene-key", "scene-hint", "scene-bar"]) $(id).hidden = false;
  requestAnimationFrame(frame);
}

// The first battery's voltage over the last three days, as a small SVG: a line,
// and each beacon as a dot coloured by its lighting.
function spark(s) {
  const b = (s.batteries || []).find((x) => x.readings.length > 1); if (!b) return "";
  const t1 = Date.now(), t0 = t1 - 3 * 86400000;
  const rs = b.readings.filter((r) => Date.parse(r.t) >= t0); if (rs.length < 2) return "";
  const lo = Math.min(...rs.map((r) => r.mv)), hi = Math.max(...rs.map((r) => r.mv)), span = Math.max(hi - lo, 20);
  const W = 254, H = 44, X = (r) => (Date.parse(r.t) - t0) / (t1 - t0) * (W - 6) + 3, Y = (r) => H - 4 - (r.mv - lo) / span * (H - 8);
  const line = rs.map((r, i) => `${i ? "L" : "M"}${X(r).toFixed(1)} ${Y(r).toFixed(1)}`).join("");
  const dots = rs.map((r) => `<circle cx="${X(r).toFixed(1)}" cy="${Y(r).toFixed(1)}" r="2" fill="${r.sunlit === false ? "#8c9bff" : "#ffc457"}"/>`).join("");
  return `<p class="spark-cap">${esc(batteryTitle(b.battery))}, last 3 days: ${(lo / 1000).toFixed(2)} to ${(hi / 1000).toFixed(2)} V</p>
    <svg class="spark" viewBox="0 0 ${W} ${H}" role="img" aria-label="${esc(s.name)} voltage over three days"><path d="${line}" fill="none" stroke="#4cc9f0" stroke-width="1.4" opacity=".7"/>${dots}</svg>`;
}

// Ocean, graticule and land, equirectangular. Coastlines that cross ±180°
// (eastern Russia, Fiji, Antarctica) are unwrapped and drawn three times,
// shifted by ±360°, as orbitscreen's map does.
function paintEarth(c, land) {
  const g = c.getContext("2d"), W = c.width, H = c.height;
  const P = (lat, lon) => [(lon + 180) / 360 * W, (90 - lat) / 180 * H];
  const ocean = g.createLinearGradient(0, 0, 0, H);
  ocean.addColorStop(0, "#123a5c"); ocean.addColorStop(0.5, "#16486f"); ocean.addColorStop(1, "#123a5c");
  g.fillStyle = ocean; g.fillRect(0, 0, W, H);
  g.strokeStyle = "rgba(160, 200, 255, .07)"; g.lineWidth = 1.5; g.beginPath();
  for (let lon = -180; lon <= 180; lon += 30) { const [x] = P(0, lon); g.moveTo(x, 0); g.lineTo(x, H); }
  for (let lat = -60; lat <= 60; lat += 30) { const [, y] = P(lat, 0); g.moveTo(0, y); g.lineTo(W, y); }
  g.stroke();
  if (!land) return;
  g.fillStyle = "#3f7a8f"; g.strokeStyle = "#7fb8d4"; g.lineWidth = 1.4; g.beginPath();
  for (const f of land.features) {
    const polys = f.geometry.type === "Polygon" ? [f.geometry.coordinates] : f.geometry.coordinates;
    for (const poly of polys) for (const ring of poly) {
      let prev = null;
      const un = ring.map(([lon, lat]) => { if (prev !== null) lon += 360 * Math.round((prev - lon) / 360); prev = lon; return [lon, lat]; });
      for (const shift of [-360, 0, 360]) {
        un.forEach(([lon, lat], k) => { const [x, y] = P(lat, lon + shift); k ? g.lineTo(x, y) : g.moveTo(x, y); });
        g.closePath();
      }
    }
  }
  g.fill(); g.stroke();
}

function esc(s) { return String(s).replace(/[&<>"']/g, (ch) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[ch]); }
