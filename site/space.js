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
if (!reduced) tilt();

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

  const sat = new THREE.Group(); sat.scale.setScalar(0.82); scene.add(sat); // small enough that the panels stay in view as it turns
  const frameMat = new THREE.MeshStandardMaterial({ color: 0xd6dee8, metalness: 0.55, roughness: 0.35 });
  const cellMat = new THREE.MeshStandardMaterial({ color: 0x2f62d8, metalness: 0.35, roughness: 0.25, emissive: 0x000000 });
  const body = new THREE.Mesh(new THREE.BoxGeometry(0.9, 0.9, 0.9), frameMat); sat.add(body);
  for (const [axis, sign] of [["x", 1], ["x", -1], ["z", 1], ["z", -1]]) { // cells on four faces
    const c = new THREE.Mesh(new THREE.PlaneGeometry(0.72, 0.72), cellMat);
    c.position[axis] = sign * 0.452;
    if (axis === "x") c.rotation.y = sign * Math.PI / 2; else if (sign < 0) c.rotation.y = Math.PI;
    sat.add(c);
  }
  for (const sign of [1, -1]) { // two unfolded panels
    const panel = new THREE.Group();
    const back = new THREE.Mesh(new THREE.BoxGeometry(1.2, 0.03, 0.8), frameMat);
    const cells = new THREE.Mesh(new THREE.PlaneGeometry(1.12, 0.72), cellMat);
    cells.rotation.x = -Math.PI / 2; cells.position.y = 0.017;
    panel.add(back, cells); panel.position.set(sign * 1.08, 0.2, 0); panel.rotation.z = sign * 0.18;
    sat.add(panel);
  }
  const antenna = new THREE.Mesh(new THREE.CylinderGeometry(0.012, 0.012, 0.9, 6), frameMat);
  antenna.position.set(0.25, 0.85, 0.25); sat.add(antenna);
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
