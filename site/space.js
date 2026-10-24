// The 3D below the globe: a starfield behind the page (dark theme only), a
// small CubeSat model on each satellite's card, and cards that tilt toward the
// pointer. One WebGL renderer draws every model in turn and copies it into the
// card's own canvas, so the page never holds more than this and the globe's
// WebGL contexts. Without WebGL, or with reduced motion, the page is as before.
import * as THREE from "three";

const reduced = matchMedia("(prefers-reduced-motion: reduce)").matches;
const dark = matchMedia("(prefers-color-scheme: dark)");
const snap = window.statusSnapshot || await new Promise((r) => document.addEventListener("snapshot", (e) => r(e.detail), { once: true }));
const byNorad = new Map((snap.satellites || []).map((s) => [s.norad, s]));

try { starfield(); } catch (e) { console.warn("starfield:", e); }
try { cubesats(); } catch (e) { console.warn("cubesats:", e); }
try { story(); } catch (e) { console.warn("story:", e); }
if (!reduced) { tilt(); reveal(); }

// Stars in three layers that drift at different speeds as the page scrolls.
function starfield() {
  const canvas = document.createElement("canvas");
  canvas.className = "starfield"; canvas.setAttribute("aria-hidden", "true");
  document.body.prepend(canvas);
  const renderer = new THREE.WebGLRenderer({ canvas, alpha: true, antialias: false });
  renderer.setPixelRatio(Math.min(devicePixelRatio || 1, 1.5));
  const scene = new THREE.Scene(), camera = new THREE.PerspectiveCamera(60, 1, 0.1, 400);
  const layers = [[900, 1.2, 0x5f7090, 160], [380, 1.8, 0x9fb4d0, 110], [90, 2.6, 0xd9e6f5, 70]].map(([n, size, color, depth]) => {
    const pos = new Float32Array(3 * n);
    for (let k = 0; k < pos.length; k += 3) pos.set([(Math.random() - .5) * 260, (Math.random() - .5) * 520, -depth * (0.6 + Math.random() * 0.4)], k);
    const g = new THREE.BufferGeometry(); g.setAttribute("position", new THREE.BufferAttribute(pos, 3));
    const pts = new THREE.Points(g, new THREE.PointsMaterial({ color, size, sizeAttenuation: false, transparent: true, opacity: .9 }));
    scene.add(pts); return { pts, depth };
  });
  const size = () => { renderer.setSize(innerWidth, innerHeight, false); camera.aspect = innerWidth / innerHeight; camera.updateProjectionMatrix(); };
  size(); addEventListener("resize", size);
  const show = () => { canvas.hidden = !dark.matches; };
  show(); dark.addEventListener("change", show);
  let t = 0;
  const frame = () => {
    requestAnimationFrame(frame);
    if (canvas.hidden || document.hidden) return;
    t += reduced ? 0 : 0.0006;
    for (const { pts, depth } of layers) {
      pts.position.y = scrollY * 0.02 * (160 / depth); // nearer stars move more
      pts.rotation.z = t * (40 / depth);
    }
    renderer.render(scene, camera);
  };
  frame();
}

// A 1U-style CubeSat: a body with solar cells on its faces, two panels
// unfolded, an antenna, and a glow ring under it for the battery's state.
function cubesats() {
  const W = 240, renderer = new THREE.WebGLRenderer({ alpha: true, antialias: true, preserveDrawingBuffer: true });
  renderer.setPixelRatio(1); renderer.setSize(W, W, false);
  const scene = new THREE.Scene(), camera = new THREE.PerspectiveCamera(30, 1, 0.1, 50);
  camera.position.set(0, 1.2, 4.3); camera.lookAt(0, -0.1, 0);
  const ambient = new THREE.AmbientLight(0xffffff, 0.5), sun = new THREE.DirectionalLight(0xfff1d6, 2.4);
  sun.position.set(3, 2, 2); scene.add(ambient, sun);

  const { sat, cellMat } = makeCubeSat();
  sat.scale.setScalar(0.82); scene.add(sat); // small enough that the panels stay in view as it turns
  const ring = new THREE.Mesh(new THREE.RingGeometry(0.75, 1.05, 48), new THREE.MeshBasicMaterial({ color: 0x3ddc97, transparent: true, opacity: 0.35, side: THREE.DoubleSide, depthWrite: false }));
  ring.rotation.x = -Math.PI / 2; ring.position.y = -0.85; scene.add(ring);

  // the battery's state from its newest reading, as app.js words it
  const ringColor = (s) => {
    if (s.stale || (s.batteries || []).some((b) => Object.values(b.anomalies || {}).some(Boolean))) return 0xffb454;
    const r = (s.batteries || []).map((b) => b.readings[b.readings.length - 1]).filter(Boolean)[0];
    if (!r) return 0x8b9cb1;
    if (r.ma != null) return r.ma > 20 ? 0x3ddc97 : r.ma < -20 ? (r.sunlit === false || r.ma > -50 ? 0x8c9bff : 0xffb454) : 0x8b9cb1;
    return r.sunlit === false ? 0x8c9bff : 0x3ddc97;
  };

  const visible = new Set();
  const io = new IntersectionObserver((es) => { for (const e of es) e.isIntersecting ? visible.add(e.target) : visible.delete(e.target); });
  const watched = new WeakSet();
  let last = 0;
  const frame = (now) => {
    requestAnimationFrame(frame);
    for (const c of document.querySelectorAll("canvas.sat3d")) if (!watched.has(c)) { watched.add(c); io.observe(c); }
    if (document.hidden || now - last < 33) return; // about 30 frames a second
    last = now;
    for (const c of visible) {
      const s = byNorad.get(Number(c.dataset.norad)); if (!s) continue;
      const lit = s.sunlit_now !== false;
      sun.intensity = lit ? 3 : 0; ambient.intensity = lit ? 0.9 : 0.25;
      ambient.color.setHex(lit ? 0xffffff : 0x7f8fd6);
      cellMat.emissive.setHex(lit ? 0x16306e : 0x060a20); // panels glow in sunlight
      ring.material.color.setHex(ringColor(s));
      ring.material.opacity = 0.28 + 0.12 * Math.sin(now / 600 + s.norad);
      const spin = reduced ? 0.6 : now / 4000 + s.norad;
      sat.rotation.set(0.15 * Math.sin(now / 3000 + s.norad), spin, 0.1);
      renderer.render(scene, camera);
      const ctx = c.getContext("2d");
      if (c.width !== W) { c.width = W; c.height = W; }
      ctx.clearRect(0, 0, W, W); ctx.drawImage(renderer.domElement, 0, 0);
    }
  };
  requestAnimationFrame(frame);
}

// A 1U CubeSat, 10 cm a side: a body with solar cells on its faces, two panels
// on hinges (pivot.rotation.z: 0 open, ±π/2 folded against the sides) and an
// antenna. Shared by the cards and the story.
function makeCubeSat() {
  const sat = new THREE.Group();
  const frameMat = new THREE.MeshStandardMaterial({ color: 0xd6dee8, metalness: 0.55, roughness: 0.35 });
  const cellMat = new THREE.MeshStandardMaterial({ color: 0x2f62d8, metalness: 0.35, roughness: 0.25, emissive: 0x000000 });
  sat.add(new THREE.Mesh(new THREE.BoxGeometry(0.9, 0.9, 0.9), frameMat));
  for (const [axis, sign] of [["x", 1], ["x", -1], ["z", 1], ["z", -1]]) { // cells on four faces
    const c = new THREE.Mesh(new THREE.PlaneGeometry(0.72, 0.72), cellMat);
    c.position[axis] = sign * 0.452;
    if (axis === "x") c.rotation.y = sign * Math.PI / 2; else if (sign < 0) c.rotation.y = Math.PI;
    sat.add(c);
  }
  const pivots = [1, -1].map((sign) => {
    const pivot = new THREE.Group(); pivot.position.set(sign * 0.46, 0.2, 0);
    const back = new THREE.Mesh(new THREE.BoxGeometry(1.2, 0.03, 0.8), frameMat);
    const cells = new THREE.Mesh(new THREE.PlaneGeometry(1.12, 0.72), cellMat);
    cells.rotation.x = -Math.PI / 2; cells.position.y = 0.017;
    const panel = new THREE.Group(); panel.add(back, cells); panel.position.x = sign * 0.62;
    pivot.add(panel); pivot.rotation.z = sign * 0.18; pivot.userData.sign = sign;
    sat.add(pivot); return pivot;
  });
  const antenna = new THREE.Mesh(new THREE.CylinderGeometry(0.012, 0.012, 0.9, 6), frameMat);
  antenna.position.set(0.25, 0.85, 0.25); sat.add(antenna);
  return { sat, cellMat, pivots, antenna };
}

// The scroll story: one CubeSat pinned to the screen while four short
// chapters pass. The scroll position drives everything, so it plays at the
// reader's pace: panels unfold, the Sun comes up, the Earth slides in front of
// it (the shadow the alerts are about), and radio waves go out.
function story() {
  const sec = document.getElementById("story"); if (!sec) return;
  const canvas = sec.querySelector("canvas"), steps = [...sec.querySelectorAll(".story-step")], dots = [...sec.querySelectorAll(".story-dots i")];
  const renderer = new THREE.WebGLRenderer({ canvas, antialias: true, alpha: true });
  renderer.setPixelRatio(Math.min(devicePixelRatio || 1, 2));
  const scene = new THREE.Scene(), camera = new THREE.PerspectiveCamera(32, 1, 0.1, 200);
  camera.position.set(0, 0.5, 7.5); camera.lookAt(0, 0.1, 0);
  const ambient = new THREE.AmbientLight(0x9fb4d0, 0.3), sun = new THREE.DirectionalLight(0xfff1d6, 0);
  sun.position.set(6, 4, 3); scene.add(ambient, sun);
  const { sat, cellMat, pivots, antenna } = makeCubeSat();
  sat.scale.setScalar(0.95); scene.add(sat);

  const glowTex = (() => {
    const c = document.createElement("canvas"); c.width = c.height = 128; const g = c.getContext("2d");
    const grad = g.createRadialGradient(64, 64, 0, 64, 64, 64);
    grad.addColorStop(0, "rgba(255,255,255,1)"); grad.addColorStop(0.18, "rgba(255,255,255,.55)"); grad.addColorStop(1, "rgba(255,255,255,0)");
    g.fillStyle = grad; g.fillRect(0, 0, 128, 128); return new THREE.CanvasTexture(c);
  })();
  const sunGlow = new THREE.Sprite(new THREE.SpriteMaterial({ map: glowTex, color: 0xffd88a, transparent: true, blending: THREE.AdditiveBlending, depthWrite: false }));
  const SUN_AT = new THREE.Vector3(3.4, 2.3, -12);
  sunGlow.position.copy(SUN_AT); scene.add(sunGlow);
  // the Earth, a dark sphere with a thin blue rim, that slides in front of the Sun
  const earth = new THREE.Group();
  earth.add(new THREE.Mesh(new THREE.SphereGeometry(6, 64, 48), new THREE.MeshBasicMaterial({ color: 0x03070f })));
  earth.add(new THREE.Mesh(new THREE.SphereGeometry(6.12, 64, 48), new THREE.ShaderMaterial({
    side: THREE.BackSide, blending: THREE.AdditiveBlending, transparent: true, depthWrite: false,
    vertexShader: "varying vec3 n; void main() { n = normalize(normalMatrix * normal); gl_Position = projectionMatrix * modelViewMatrix * vec4(position, 1.0); }",
    fragmentShader: "varying vec3 n; void main() { float i = pow(max(0.0, 0.7 - dot(n, vec3(0.0, 0.0, 1.0))), 4.0); gl_FragColor = vec4(0.3, 0.7, 1.0, 1.0) * i * 0.75; }",
  })));
  scene.add(earth);
  // radio waves: rings that grow out from the antenna and fade
  const waves = [0, 1, 2].map(() => {
    const m = new THREE.Mesh(new THREE.RingGeometry(0.96, 1, 64), new THREE.MeshBasicMaterial({ color: 0x4cc9f0, transparent: true, opacity: 0, side: THREE.DoubleSide, depthWrite: false }));
    scene.add(m); return m;
  });

  const smooth = (a, b, x) => { const t = Math.min(Math.max((x - a) / (b - a), 0), 1); return t * t * (3 - 2 * t); };
  let W = 1, H = 1, visible = false;
  const layout = () => {
    W = sec.clientWidth; H = innerHeight;
    renderer.setSize(W, H, false); camera.aspect = W / H;
    // the model to the right of the text on wide screens, above it on phones
    if (W >= 900) camera.setViewOffset(W, H, -W * 0.2, 0, W, H); else camera.setViewOffset(W, H, 0, H * 0.17, W, H);
    sat.scale.setScalar(W >= 900 ? 0.95 : 0.62);
    camera.updateProjectionMatrix();
  };
  layout(); addEventListener("resize", layout);
  new IntersectionObserver(([e]) => { visible = e.isIntersecting; }).observe(sec);
  const tmp = new THREE.Vector3(), dark = new THREE.Color(0x060a20), lit = new THREE.Color(0x16306e);

  const frame = (now) => {
    requestAnimationFrame(frame);
    if (!visible || document.hidden) return;
    const rect = sec.getBoundingClientRect();
    const p = Math.min(Math.max(-rect.top / Math.max(rect.height - H, 1), 0), 1), seg = p * 4, i = Math.min(3, Math.floor(seg));
    // chapter 1: the panels unfold as it turns in
    const open = smooth(0.04, 0.2, p);
    for (const pv of pivots) pv.rotation.z = pv.userData.sign * (0.18 * open + (Math.PI / 2) * (1 - open));
    sat.rotation.set(0.3 + 0.08 * Math.sin(now / 2400), -0.9 + p * Math.PI * 1.6 + (reduced ? 0 : now / 9000), 0.12);
    // chapter 2: sunlight; chapter 3: the Earth covers the Sun
    const sunUp = smooth(0.24, 0.36, p), eclipse = smooth(0.5, 0.62, p), light = sunUp * (1 - eclipse);
    // before the Sun it is softly lit; only the eclipse makes it dark
    sun.intensity = 3.2 * light; ambient.intensity = 0.55 + 0.35 * light - 0.38 * eclipse;
    ambient.color.setHex(eclipse > 0.5 ? 0x7f8fd6 : 0xffffff);
    cellMat.emissive.copy(dark).lerp(lit, light);
    sunGlow.scale.setScalar(3 + 9 * sunUp); sunGlow.material.opacity = sunUp * (1 - eclipse);
    // the Earth slides in from the right until it covers the Sun
    earth.position.set(SUN_AT.x + 2.2 + 16 * (1 - eclipse), SUN_AT.y + 1.6, SUN_AT.z - 1);
    // chapter 4: radio waves from the antenna
    const talk = smooth(0.74, 0.82, p);
    antenna.getWorldPosition(tmp);
    waves.forEach((w, k) => {
      const f = ((now / 1800) + k / 3) % 1;
      w.position.copy(tmp); w.lookAt(camera.position);
      w.scale.setScalar(0.15 + f * 1.8); w.material.opacity = talk * (1 - f) * 0.7;
    });
    // the words: each chapter fades in and out over its quarter of the scroll
    steps.forEach((el, j) => {
      const local = seg - j;
      const o = j === i ? (j === 0 ? 1 : smooth(0, 0.18, local)) * (j === 3 ? 1 : 1 - smooth(0.82, 1, local)) : 0;
      el.style.opacity = o.toFixed(3);
      el.style.transform = `translateY(${((1 - o) * 24).toFixed(1)}px)`;
    });
    dots.forEach((d, j) => d.classList.toggle("on", j === i));
    renderer.render(scene, camera);
  };
  sec.classList.add("live");
  requestAnimationFrame(frame);
}

// Sections fade up as they scroll into view.
function reveal() {
  const io = new IntersectionObserver((es) => {
    for (const e of es) if (e.isIntersecting) { e.target.classList.add("in"); io.unobserve(e.target); setTimeout(() => e.target.classList.remove("reveal", "in"), 900); }
  }, { rootMargin: "0px 0px -8% 0px" });
  const watch = () => { for (const el of document.querySelectorAll(".tile, .card, .step, .how h2, footer")) if (!el.dataset.revealed) { el.dataset.revealed = "1"; el.classList.add("reveal"); io.observe(el); } };
  watch(); new MutationObserver(watch).observe(document.getElementById("cards"), { childList: true });
}

// Cards lean a few degrees toward the pointer.
function tilt() {
  if (!matchMedia("(pointer: fine)").matches) return;
  document.addEventListener("pointermove", (e) => {
    const card = e.target.closest?.(".card, .step, .tile");
    for (const c of document.querySelectorAll(".tilted")) if (c !== card) { c.classList.remove("tilted"); c.style.transform = ""; }
    if (!card) return;
    const r = card.getBoundingClientRect(), dx = (e.clientX - r.left) / r.width - .5, dy = (e.clientY - r.top) / r.height - .5;
    const k = card.classList.contains("card") ? 2 : 5;
    card.classList.add("tilted");
    card.style.transform = `perspective(900px) rotateX(${(-dy * k).toFixed(2)}deg) rotateY(${(dx * k).toFixed(2)}deg)`;
  });
}
