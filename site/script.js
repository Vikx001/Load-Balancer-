// Omega-LB marketing site — advanced interactive behaviors.
// No build step, no framework: plain DOM APIs + Three.js for the WebGL hero.

function prefersReducedMotion() {
  return window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
}

function isTouchDevice() {
  return window.matchMedia && !window.matchMedia("(hover: hover)").matches;
}

document.addEventListener("DOMContentLoaded", () => {
  initTabs(".proof-tabs", ".proof-panel");
  initTabs(".roadmap-tabs", ".roadmap-panel");
  initCopyButtons();
  initMermaid();
  initNavToggle();
  initScrollProgress();
  initScrollReveal();
  initCounters();
  initScrollspy();
  initCardSpotlight();
  initHeroWebGL();
  initPlayground();
  initPipeline();
  initMagneticButtons();

  const yearEl = document.getElementById("year");
  if (yearEl) yearEl.textContent = new Date().getFullYear();
});

/**
 * Wires up a tab group: clicking a button with data-target="X" shows the
 * panel with id="X" (scoped to a given panel selector) and hides the rest.
 */
function initTabs(tabGroupSelector, panelSelector) {
  const group = document.querySelector(tabGroupSelector);
  if (!group) return;

  const buttons = Array.from(group.querySelectorAll(".tab-btn"));
  const panels = Array.from(document.querySelectorAll(panelSelector));

  buttons.forEach((btn) => {
    btn.addEventListener("click", () => {
      const targetId = btn.getAttribute("data-target");

      buttons.forEach((b) => {
        b.classList.remove("active");
        b.setAttribute("aria-selected", "false");
      });
      btn.classList.add("active");
      btn.setAttribute("aria-selected", "true");

      panels.forEach((p) => {
        p.classList.toggle("active", p.id === targetId);
      });
    });
  });
}

/**
 * Copy-to-clipboard for every .code-block that opts in via data-copy.
 */
function initCopyButtons() {
  document.querySelectorAll("[data-copy]").forEach((block) => {
    const btn = block.querySelector(".copy-btn");
    const codeEl = block.querySelector("pre code, pre");
    if (!btn || !codeEl) return;

    btn.addEventListener("click", async () => {
      const text = codeEl.textContent;
      try {
        await navigator.clipboard.writeText(text);
      } catch (err) {
        console.warn("Clipboard write failed:", err);
        return;
      }
      const original = btn.textContent;
      btn.textContent = "Copied";
      btn.classList.add("copied");
      setTimeout(() => {
        btn.textContent = original;
        btn.classList.remove("copied");
      }, 1600);
    });
  });
}

function initMermaid() {
  if (typeof mermaid === "undefined") return;
  mermaid.initialize({
    startOnLoad: true,
    theme: "dark",
    themeVariables: {
      background: "#131926",
      primaryColor: "#1a2233",
      primaryTextColor: "#dbe2ef",
      primaryBorderColor: "#232b3a",
      lineColor: "#5b8cff",
      secondaryColor: "#10151f",
      tertiaryColor: "#10151f",
    },
    securityLevel: "strict",
  });

  // After Mermaid renders, make nodes clickable and wire up the info panel.
  setTimeout(() => {
    initMermaidNodeInspector();
  }, 800);
}

/**
 * Mobile navigation toggle.
 */
function initNavToggle() {
  const toggle = document.querySelector(".nav-toggle");
  const menu = document.getElementById("nav-links");
  if (!toggle || !menu) return;

  const close = () => {
    menu.classList.remove("open");
    toggle.setAttribute("aria-expanded", "false");
  };

  toggle.addEventListener("click", () => {
    const isOpen = menu.classList.toggle("open");
    toggle.setAttribute("aria-expanded", String(isOpen));
  });

  menu.querySelectorAll("a").forEach((link) => {
    link.addEventListener("click", close);
  });

  document.addEventListener("click", (e) => {
    if (!menu.classList.contains("open")) return;
    if (menu.contains(e.target) || toggle.contains(e.target)) return;
    close();
  });

  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && menu.classList.contains("open")) {
      close();
      toggle.focus();
    }
  });
}

/**
 * Thin progress bar across the top of the viewport showing scroll depth.
 */
function initScrollProgress() {
  const bar = document.querySelector(".scroll-progress-bar");
  if (!bar) return;

  let ticking = false;
  const update = () => {
    const scrollTop = window.scrollY;
    const docHeight = document.documentElement.scrollHeight - window.innerHeight;
    const pct = docHeight > 0 ? (scrollTop / docHeight) * 100 : 0;
    bar.style.width = `${Math.min(100, Math.max(0, pct))}%`;
    ticking = false;
  };

  window.addEventListener(
    "scroll",
    () => {
      if (!ticking) {
        requestAnimationFrame(update);
        ticking = true;
      }
    },
    { passive: true }
  );
  update();
}

/**
 * Fades/slides in elements with the .reveal class as they enter the viewport.
 */
function initScrollReveal() {
  const items = Array.from(document.querySelectorAll(".reveal"));
  if (!items.length) return;

  if (prefersReducedMotion() || typeof IntersectionObserver === "undefined") {
    items.forEach((el) => el.classList.add("revealed"));
    return;
  }

  const observer = new IntersectionObserver(
    (entries, obs) => {
      entries.forEach((entry) => {
        if (entry.isIntersecting) {
          entry.target.classList.add("revealed");
          obs.unobserve(entry.target);
        }
      });
    },
    { threshold: 0.15, rootMargin: "0px 0px -40px 0px" }
  );

  items.forEach((el, i) => {
    el.style.transitionDelay = `${Math.min(i % 5, 4) * 70}ms`;
    observer.observe(el);
  });
}

/**
 * Animates the perf-grid stat numbers counting up from 0 to their target value.
 */
function initCounters() {
  const items = Array.from(document.querySelectorAll("[data-count-to]"));
  if (!items.length) return;

  const setFinal = (el) => {
    const target = parseInt(el.getAttribute("data-count-to"), 10) || 0;
    const prefix = el.getAttribute("data-prefix") || "";
    const suffix = el.getAttribute("data-suffix") || "";
    el.textContent = `${prefix}${target}${suffix}`;
  };

  if (prefersReducedMotion() || typeof IntersectionObserver === "undefined") {
    items.forEach(setFinal);
    return;
  }

  const animate = (el) => {
    const target = parseInt(el.getAttribute("data-count-to"), 10) || 0;
    const prefix = el.getAttribute("data-prefix") || "";
    const suffix = el.getAttribute("data-suffix") || "";
    const duration = 1200;
    const start = performance.now();

    const step = (now) => {
      const t = Math.min(1, (now - start) / duration);
      const eased = 1 - Math.pow(1 - t, 3);
      const value = Math.round(target * eased);
      el.textContent = `${prefix}${value}${suffix}`;
      if (t < 1) requestAnimationFrame(step);
    };
    requestAnimationFrame(step);
  };

  const observer = new IntersectionObserver(
    (entries, obs) => {
      entries.forEach((entry) => {
        if (entry.isIntersecting) {
          animate(entry.target);
          obs.unobserve(entry.target);
        }
      });
    },
    { threshold: 0.6 }
  );

  items.forEach((el) => observer.observe(el));
}

/**
 * Highlights the nav link matching the section currently in view.
 */
function initScrollspy() {
  const links = Array.from(document.querySelectorAll(".nav-links a[href^='#']"));
  if (!links.length || typeof IntersectionObserver === "undefined") return;

  const sections = links
    .map((link) => document.querySelector(link.getAttribute("href")))
    .filter(Boolean);
  if (!sections.length) return;

  const setActive = (id) => {
    links.forEach((link) => {
      link.classList.toggle("active-link", link.getAttribute("href") === `#${id}`);
    });
  };

  const observer = new IntersectionObserver(
    (entries) => {
      entries.forEach((entry) => {
        if (entry.isIntersecting) setActive(entry.target.id);
      });
    },
    { rootMargin: "-40% 0px -50% 0px", threshold: 0 }
  );

  sections.forEach((section) => observer.observe(section));
}

/**
 * Cursor-reactive glow on .spotlight cards.
 */
function initCardSpotlight() {
  if (isTouchDevice()) return;

  document.querySelectorAll(".spotlight").forEach((card) => {
    card.addEventListener("mousemove", (e) => {
      const rect = card.getBoundingClientRect();
      card.style.setProperty("--mx", `${e.clientX - rect.left}px`);
      card.style.setProperty("--my", `${e.clientY - rect.top}px`);
    });
  });
}

/**
 * Three.js WebGL hero: a 3D network of clients, load balancer, and backends
 * with animated traffic pulses. Falls back to the 2D canvas if WebGL fails
 * or reduced motion is preferred.
 */
function initHeroWebGL() {
  const container = document.getElementById("hero-webgl");
  if (!container || prefersReducedMotion() || typeof THREE === "undefined") return;

  let renderer, scene, camera, animationId;
  let nodes = [];
  let pulses = [];
  let lastSpawn = 0;

  function init() {
    const rect = container.getBoundingClientRect();
    const width = rect.width;
    const height = rect.height;

    scene = new THREE.Scene();
    camera = new THREE.PerspectiveCamera(60, width / height, 0.1, 1000);
    camera.position.z = 18;

    renderer = new THREE.WebGLRenderer({ antialias: true, alpha: true });
    renderer.setSize(width, height);
    renderer.setPixelRatio(Math.min(window.devicePixelRatio, 2));
    container.appendChild(renderer.domElement);

    // Ambient particles
    const particleGeo = new THREE.BufferGeometry();
    const particleCount = 120;
    const positions = new Float32Array(particleCount * 3);
    for (let i = 0; i < particleCount * 3; i++) {
      positions[i] = (Math.random() - 0.5) * 40;
    }
    particleGeo.setAttribute("position", new THREE.BufferAttribute(positions, 3));
    const particleMat = new THREE.PointsMaterial({
      color: 0x5b8cff,
      size: 0.06,
      transparent: true,
      opacity: 0.35,
    });
    scene.add(new THREE.Points(particleGeo, particleMat));

    // Nodes
    const clientGeo = new THREE.SphereGeometry(0.18, 16, 16);
    const clientMat = new THREE.MeshBasicMaterial({ color: 0xdbe2ef });
    const lbGeo = new THREE.SphereGeometry(0.38, 24, 24);
    const lbMat = new THREE.MeshBasicMaterial({ color: 0x5b8cff });
    const backendGeo = new THREE.SphereGeometry(0.22, 16, 16);
    const backendMat = new THREE.MeshBasicMaterial({ color: 0x4fd1a5 });

    const lb = new THREE.Mesh(lbGeo, lbMat);
    lb.position.set(2, 0, 0);
    scene.add(lb);
    nodes.push({ mesh: lb, role: "lb" });

    for (let i = 0; i < 3; i++) {
      const mesh = new THREE.Mesh(clientGeo, clientMat);
      const angle = (i / 3) * Math.PI * 2;
      mesh.position.set(-7 + Math.cos(angle) * 1.2, Math.sin(angle) * 2.5, 0);
      scene.add(mesh);
      nodes.push({ mesh, role: "client", target: lb.position });
    }

    for (let i = 0; i < 4; i++) {
      const mesh = new THREE.Mesh(backendGeo, backendMat);
      const y = (i - 1.5) * 2.2;
      mesh.position.set(8, y, 0);
      scene.add(mesh);
      nodes.push({ mesh, role: "backend" });
    }

    // Connection lines
    const lineMat = new THREE.LineBasicMaterial({ color: 0x5b8cff, transparent: true, opacity: 0.12 });
    nodes.filter((n) => n.role === "client").forEach((client) => {
      const geo = new THREE.BufferGeometry().setFromPoints([client.mesh.position, lb.position]);
      scene.add(new THREE.Line(geo, lineMat));
    });
    nodes.filter((n) => n.role === "backend").forEach((backend) => {
      const geo = new THREE.BufferGeometry().setFromPoints([lb.position, backend.mesh.position]);
      scene.add(new THREE.Line(geo, lineMat));
    });

    animate();
  }

  function spawnPulse(time) {
    if (time - lastSpawn < 400) return;
    lastSpawn = time;
    const clients = nodes.filter((n) => n.role === "client");
    const client = clients[Math.floor(Math.random() * clients.length)];
    const geo = new THREE.SphereGeometry(0.1, 8, 8);
    const mat = new THREE.MeshBasicMaterial({ color: 0xffffff });
    const mesh = new THREE.Mesh(geo, mat);
    mesh.position.copy(client.mesh.position);
    scene.add(mesh);
    pulses.push({ mesh, from: client.mesh.position, to: nodes.find((n) => n.role === "lb").mesh.position, t: 0, hop: 1 });
  }

  function animate(time = 0) {
    animationId = requestAnimationFrame(animate);

    // Gentle rotation
    scene.rotation.y = Math.sin(time * 0.0001) * 0.08;
    scene.rotation.x = Math.cos(time * 0.00008) * 0.04;

    spawnPulse(time);

    pulses = pulses.filter((p) => {
      p.t += 0.015;
      if (p.t >= 1) {
        if (p.hop === 1) {
          const backends = nodes.filter((n) => n.role === "backend");
          const target = backends[Math.floor(Math.random() * backends.length)].mesh.position;
          p.from = p.to;
          p.to = target;
          p.t = 0;
          p.hop = 2;
          p.mesh.material.color.setHex(0x4fd1a5);
        } else {
          scene.remove(p.mesh);
          p.mesh.geometry.dispose();
          p.mesh.material.dispose();
          return false;
        }
      }
      p.mesh.position.lerpVectors(p.from, p.to, p.t);
      return true;
    });

    renderer.render(scene, camera);
  }

  function onResize() {
    if (!renderer || !camera) return;
    const rect = container.getBoundingClientRect();
    camera.aspect = rect.width / rect.height;
    camera.updateProjectionMatrix();
    renderer.setSize(rect.width, rect.height);
  }

  function onVisibility() {
    if (document.hidden) {
      if (animationId) cancelAnimationFrame(animationId);
      animationId = null;
    } else if (!animationId) {
      animate(performance.now());
    }
  }

  try {
    init();
    window.addEventListener("resize", onResize);
    document.addEventListener("visibilitychange", onVisibility);
  } catch (err) {
    console.warn("WebGL hero failed, 2D canvas fallback remains:", err);
  }
}

/**
 * Live load-balancer playground simulation.
 */
function initPlayground() {
  const canvas = document.getElementById("playground-canvas");
  if (!canvas) return;

  const ctx = canvas.getContext("2d");
  if (!ctx) return;

  const loadInput = document.getElementById("pg-load");
  const burstBtn = document.getElementById("pg-burst");
  const backendToggles = document.querySelectorAll(".backend-toggle");
  const rpsEl = document.getElementById("pg-rps");
  const p99El = document.getElementById("pg-p99");
  const healthyEl = document.getElementById("pg-healthy");
  const droppedEl = document.getElementById("pg-dropped");

  let width = 0;
  let height = 0;
  let dpr = Math.min(window.devicePixelRatio || 1, 2);
  let animationId = null;

  const backends = [
    { id: 0, x: 0, y: 0, healthy: true, load: 0, latency: 20 },
    { id: 1, x: 0, y: 0, healthy: true, load: 0, latency: 25 },
    { id: 2, x: 0, y: 0, healthy: true, load: 0, latency: 22 },
    { id: 3, x: 0, y: 0, healthy: true, load: 0, latency: 28 },
  ];

  let requests = [];
  let stats = { total: 0, dropped: 0, latencies: [] };
  let baseLoad = 30;
  let burstMultiplier = 1;
  let burstTimer = 0;

  function layout() {
    const rect = canvas.parentElement.getBoundingClientRect();
    width = rect.width;
    height = rect.height;
    canvas.width = width * dpr;
    canvas.height = height * dpr;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);

    const cx = width * 0.5;
    const cy = height * 0.5;
    backends.forEach((b, i) => {
      const angle = (i / backends.length) * Math.PI * 2 - Math.PI / 2;
      b.x = cx + Math.cos(angle) * Math.min(width, height) * 0.32;
      b.y = cy + Math.sin(angle) * Math.min(width, height) * 0.32;
    });
  }

  function spawnRequest() {
    const load = baseLoad * burstMultiplier;
    const spawnRate = load / 60; // per frame at 60fps
    if (Math.random() > spawnRate) return;

    const healthy = backends.filter((b) => b.healthy);
    if (healthy.length === 0) {
      stats.dropped++;
      return;
    }

    // Weighted by inverse load (simple load-aware routing)
    const totalWeight = healthy.reduce((sum, b) => sum + Math.max(5, 100 - b.load), 0);
    let r = Math.random() * totalWeight;
    const target = healthy.find((b) => {
      r -= Math.max(5, 100 - b.load);
      return r <= 0;
    }) || healthy[0];

    requests.push({
      x: width * 0.5,
      y: height * 0.5,
      target,
      t: 0,
      state: "to-backend",
      latency: target.latency * (1 + target.load / 200),
    });
    stats.total++;
  }

  function updateMetrics() {
    const healthyCount = backends.filter((b) => b.healthy).length;
    const rps = Math.round(baseLoad * burstMultiplier);
    rpsEl.textContent = rps;
    healthyEl.textContent = `${healthyCount}/${backends.length}`;
    droppedEl.textContent = stats.dropped;

    // Simulate p99 from current backend loads
    const avgLoad = backends.reduce((s, b) => s + b.load, 0) / backends.length;
    const p99 = Math.round(avgLoad * 0.8 + (backends.length - healthyCount) * 40 + 5);
    p99El.textContent = `${p99}ms`;
  }

  function drawNode(x, y, r, color, label, sub) {
    ctx.beginPath();
    ctx.arc(x, y, r, 0, Math.PI * 2);
    ctx.fillStyle = color;
    ctx.fill();

    // Load ring
    ctx.beginPath();
    ctx.arc(x, y, r + 6, -Math.PI / 2, -Math.PI / 2 + Math.PI * 2 * 0.01);
    ctx.strokeStyle = "rgba(255,255,255,0.1)";
    ctx.lineWidth = 2;
    ctx.stroke();

    ctx.fillStyle = "var(--text)";
    ctx.font = "600 11px SF Mono, monospace";
    ctx.textAlign = "center";
    ctx.fillText(label, x, y + r + 20);
    if (sub) {
      ctx.fillStyle = "var(--text-dim)";
      ctx.font = "10px SF Mono, monospace";
      ctx.fillText(sub, x, y + r + 32);
    }
  }

  function render() {
    ctx.clearRect(0, 0, width, height);

    // Draw connections
    backends.forEach((b) => {
      ctx.strokeStyle = b.healthy ? "rgba(79, 209, 165, 0.15)" : "rgba(255, 107, 107, 0.15)";
      ctx.lineWidth = 1;
      ctx.beginPath();
      ctx.moveTo(width * 0.5, height * 0.5);
      ctx.lineTo(b.x, b.y);
      ctx.stroke();
    });

    // Draw LB
    drawNode(width * 0.5, height * 0.5, 18, "#5b8cff", "LB", null);

    // Draw backends
    backends.forEach((b) => {
      const color = b.healthy ? "#4fd1a5" : "#ff6b6b";
      const loadPct = Math.round(b.load);
      drawNode(b.x, b.y, 14, color, `BE-${b.id + 1}`, `${loadPct}% load`);
    });

    spawnRequest();

    // Update and draw requests
    requests = requests.filter((req) => {
      req.t += 0.04;
      if (req.state === "to-backend") {
        req.x = width * 0.5 + (req.target.x - width * 0.5) * req.t;
        req.y = height * 0.5 + (req.target.y - height * 0.5) * req.t;
        if (req.t >= 1) {
          req.t = 0;
          req.state = "processing";
          req.target.load = Math.min(100, req.target.load + 4);
          stats.latencies.push(req.latency);
        }
        ctx.fillStyle = req.target.healthy ? "#dbe2ef" : "#ff6b6b";
        ctx.beginPath();
        ctx.arc(req.x, req.y, 3, 0, Math.PI * 2);
        ctx.fill();
        return true;
      } else if (req.state === "processing") {
        if (req.t >= req.latency / 100) {
          req.t = 0;
          req.state = "returning";
        }
        return true;
      } else {
        req.x = req.target.x + (width * 0.5 - req.target.x) * req.t;
        req.y = req.target.y + (height * 0.5 - req.target.y) * req.t;
        ctx.fillStyle = "#4fd1a5";
        ctx.beginPath();
        ctx.arc(req.x, req.y, 3, 0, Math.PI * 2);
        ctx.fill();
        if (req.t >= 1) {
          req.target.load = Math.max(0, req.target.load - 3);
          return false;
        }
        return true;
      }
    });

    // Decay backend load
    backends.forEach((b) => {
      b.load = Math.max(0, b.load - 0.15);
    });

    updateMetrics();
    animationId = requestAnimationFrame(render);
  }

  // Controls
  if (loadInput) {
    loadInput.addEventListener("input", () => {
      baseLoad = parseInt(loadInput.value, 10);
    });
  }

  if (burstBtn) {
    burstBtn.addEventListener("click", () => {
      burstMultiplier = 5;
      burstTimer = 180; // frames (~3s)
    });
  }

  backendToggles.forEach((btn) => {
    btn.addEventListener("click", () => {
      const id = parseInt(btn.getAttribute("data-backend"), 10);
      backends[id].healthy = !backends[id].healthy;
      btn.classList.toggle("active", backends[id].healthy);
      btn.setAttribute("aria-pressed", String(backends[id].healthy));
    });
  });

  layout();
  render();

  window.addEventListener("resize", () => {
    clearTimeout(canvas.resizeTimer);
    canvas.resizeTimer = setTimeout(layout, 150);
  });

  document.addEventListener("visibilitychange", () => {
    if (document.hidden) {
      if (animationId) cancelAnimationFrame(animationId);
      animationId = null;
    } else if (!animationId) {
      animationId = requestAnimationFrame(render);
    }
  });
}

/**
 * Interactive 5-layer pipeline: hover/click to show details.
 */
function initPipeline() {
  const nodes = document.querySelectorAll(".pipeline-node.layer");
  const detail = document.getElementById("pipeline-detail");
  const defaultView = detail?.querySelector(".pipeline-detail-default");
  const contentView = document.getElementById("pipeline-detail-content");
  if (!nodes.length || !detail || !defaultView || !contentView) return;

  const data = {
    1: {
      title: "01 — Consistent Hash Ring",
      desc: "MurmurHash3 maps each client to a vnode on the ring. Demand-aware redistribution (β = 1.25) shrinks the slice of overloaded backends without breaking sticky sessions.",
      equation: "slot = murmur3(client_id) mod (backends × vnodes)",
      bullets: [
        "150 vnodes per backend",
        "Sticky client → backend mapping",
        "Bounded-load redistribution under imbalance",
      ],
    },
    2: {
      title: "02 — CBF Safety Projection",
      desc: "A Control Barrier Function projects the routing weights onto a safe simplex. If any backend would exceed 80% utilisation, the decision is mathematically pulled back.",
      equation: "w_i = max(0, 1 − 0.42·cpu − 0.31·lat − 10·err) × h_i",
      bullets: ["Caps utilisation at 80%", "OSQP quadratic projection", "Failsafe even if upstream is unsafe"],
    },
    3: {
      title: "03 — KAN Interpretable Policy",
      desc: "A Kolmogorov–Arnold Network with B-spline edges scores backends. Unlike a black-box NN, it can express its decision as a readable symbolic formula.",
      equation: "score(x) = Σ_i φ_i(Σ_j ψ_ij(x_j))",
      bullets: ["Hot-reloaded ONNX model", "Symbolic fallback when no model is present", "Interpretable by design"],
    },
    4: {
      title: "04 — DQN Adaptive Rate Limiting",
      desc: "An ε-greedy DQN chooses Expand, Hold, or Throttle for each backend's token bucket, clamped to configured [min_rps, max_rps].",
      equation: "action = argmax_a Q(s, a)  (with ε exploration)",
      bullets: ["Per-backend token buckets", "Clamped to config bounds", "Learns from latency/SLA feedback"],
    },
    5: {
      title: "05 — Proactive Pre-distribution",
      desc: "A 30-second load-slope lookahead rebalances vnode counts before saturation happens, instead of reacting after the fact.",
      equation: "Δvnode ∝ slope(load) · (capacity − forecast)",
      bullets: ["30-second lookahead", "Pre-emptive vnode migration", "Reduces p99 spikes"],
    },
  };

  function showLayer(id) {
    const d = data[id];
    if (!d) return;
    defaultView.hidden = true;
    contentView.hidden = false;
    document.getElementById("pd-title").textContent = d.title;
    document.getElementById("pd-desc").textContent = d.desc;
    document.getElementById("pd-equation").textContent = d.equation;
    const ul = document.getElementById("pd-bullets");
    ul.innerHTML = "";
    d.bullets.forEach((b) => {
      const li = document.createElement("li");
      li.textContent = b;
      ul.appendChild(li);
    });

    nodes.forEach((n) => n.classList.toggle("active", n.getAttribute("data-layer") === id));
  }

  nodes.forEach((node) => {
    node.addEventListener("mouseenter", () => showLayer(node.getAttribute("data-layer")));
    node.addEventListener("click", () => showLayer(node.getAttribute("data-layer")));
  });
}

/**
 * Make Mermaid-rendered architecture nodes clickable and show an info panel.
 */
function initMermaidNodeInspector() {
  const info = document.getElementById("arch-info");
  if (!info) return;

  const descriptions = {
    "Operator": "A human operator inspecting the system via curl or the Streamlit dashboard.",
    "Browser": "The client generating HTTP traffic against the Omega proxy.",
    "curl": "The client generating HTTP traffic against the Omega proxy.",
    "Streamlit": "Live dashboard on port 8501 showing KPIs, routing decisions, and fault simulation.",
    "omega-lb.yaml": "YAML configuration file defining backends, tokens, rate limits, and algorithm weights.",
    "live_metrics.json": "Rolling metrics file consumed by the dashboard for live charts and gauges.",
    "Load": "demo/loadgen.py produces synthetic traffic to exercise the routing pipeline.",
    "Omega": "The userspace Python proxy on port 8080 that runs the 5-layer routing pipeline.",
    "Admin": "Go admin API on port 9000 for runtime control, protected by bearer token.",
    "Metrics": "Rolling collector that aggregates backend health, latency, and throughput.",
    "Hash": "Layer 1: maps clients to backends with 150 vnodes per backend.",
    "Health": "Layer 2: health checks + Control Barrier Function safety projection.",
    "KAN": "Layer 3: Kolmogorov–Arnold Network interpretable scoring.",
    "DQN": "Layer 4: adaptive per-backend token buckets.",
    "Proactive": "Layer 5: 30-second lookahead vnode rebalancing.",
    "backend-1": "Example backend instance in the pool.",
    "backend-2": "Example backend instance in the pool.",
    "backend-3": "Example backend instance in the pool.",
    "backend-4": "Example backend instance in the pool.",
  };

  function findDescription(firstLine) {
    const direct = descriptions[firstLine];
    if (direct) return direct;
    const key = Object.keys(descriptions).find((k) => firstLine.includes(k) || k.includes(firstLine));
    return key ? descriptions[key] : "Component of the Omega-LB runtime architecture.";
  }

  const mermaidNodes = document.querySelectorAll(".mermaid-wrap.interactive .node");
  mermaidNodes.forEach((node) => {
    node.style.cursor = "pointer";
    node.addEventListener("click", () => {
      const label = node.querySelector(".nodeLabel");
      const html = label ? label.innerHTML : node.textContent;
      const lines = html.split(/<br\s*\/?>/i).map((s) => s.trim()).filter(Boolean);
      const firstLine = lines[0] || "Component";
      const desc = findDescription(firstLine);
      info.innerHTML = `<h4>${firstLine}</h4><p>${desc}</p>`;
    });
  });
}

/**
 * Magnetic button effect: buttons subtly move toward the cursor on desktop.
 */
function initMagneticButtons() {
  if (isTouchDevice()) return;

  document.querySelectorAll(".magnetic").forEach((btn) => {
    btn.addEventListener("mousemove", (e) => {
      const rect = btn.getBoundingClientRect();
      const x = e.clientX - rect.left - rect.width / 2;
      const y = e.clientY - rect.top - rect.height / 2;
      btn.style.transform = `translate(${x * 0.15}px, ${y * 0.15}px)`;
    });

    btn.addEventListener("mouseleave", () => {
      btn.style.transform = "translate(0, 0)";
    });
  });
}
