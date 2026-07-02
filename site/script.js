// Omega-LB marketing site — small interactive behaviors.
// No build step, no framework: plain DOM APIs only.

function prefersReducedMotion() {
  return window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
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
  initHeroCanvas();

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
        // Clipboard API unavailable (e.g. insecure context) — fall back silently.
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
}

/**
 * Mobile navigation toggle: shows/hides the nav-links dropdown on small
 * screens, closes on link click or outside click, keyboard-accessible
 * (Escape closes and returns focus to the toggle button).
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
 * Throttled to one update per animation frame.
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
 * Fades/slides in elements with the .reveal class as they enter the
 * viewport. Falls back to showing everything immediately if
 * IntersectionObserver is unavailable or the user prefers reduced motion.
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
 * Animates the perf-grid stat numbers counting up from 0 to their target
 * value the first time they scroll into view. Each element carries
 * data-count-to (target integer), and optional data-prefix / data-suffix.
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
      const eased = 1 - Math.pow(1 - t, 3); // ease-out cubic
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
 * Cursor-reactive glow on .spotlight cards: tracks pointer position and
 * exposes it as CSS custom properties consumed by the ::before radial
 * gradient in styles.css. No-op on touch-only devices (no hover capability).
 */
function initCardSpotlight() {
  if (window.matchMedia && !window.matchMedia("(hover: hover)").matches) return;

  document.querySelectorAll(".spotlight").forEach((card) => {
    card.addEventListener("mousemove", (e) => {
      const rect = card.getBoundingClientRect();
      card.style.setProperty("--mx", `${e.clientX - rect.left}px`);
      card.style.setProperty("--my", `${e.clientY - rect.top}px`);
    });
  });
}

/**
 * Renders a lightweight canvas animation in the hero depicting the actual
 * product concept: client requests flowing into the load balancer, which
 * routes pulses out to a pool of backend nodes. Pauses when the tab is
 * hidden and is skipped entirely when the user prefers reduced motion or
 * canvas 2D isn't supported.
 */
function initHeroCanvas() {
  const canvas = document.getElementById("hero-canvas");
  if (!canvas || prefersReducedMotion()) return;

  const ctx = canvas.getContext("2d");
  if (!ctx) return;

  const hero = canvas.closest(".hero");
  let width = 0;
  let height = 0;
  let dpr = Math.min(window.devicePixelRatio || 1, 2);
  let animationId = null;

  const CLIENT_COUNT = 3;
  const BACKEND_COUNT = 4;
  let clients = [];
  let backends = [];
  let lbNode = { x: 0, y: 0 };
  let pulses = [];
  let lastSpawn = 0;

  function layout() {
    const rect = hero.getBoundingClientRect();
    width = rect.width;
    height = rect.height;
    canvas.width = width * dpr;
    canvas.height = height * dpr;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);

    lbNode = { x: width * 0.62, y: height * 0.5 };
    clients = Array.from({ length: CLIENT_COUNT }, (_, i) => ({
      x: width * 0.08,
      y: height * (0.25 + i * 0.25),
    }));
    backends = Array.from({ length: BACKEND_COUNT }, (_, i) => ({
      x: width * 0.92,
      y: height * (0.15 + i * 0.24),
    }));
  }

  function spawnPulse(now) {
    if (now - lastSpawn < 550) return;
    lastSpawn = now;
    const from = clients[Math.floor(Math.random() * clients.length)];
    pulses.push({ from, to: lbNode, next: null, t: 0, hop: 1 });
  }

  function drawLine(a, b, alpha) {
    ctx.strokeStyle = `rgba(91, 140, 255, ${alpha})`;
    ctx.lineWidth = 1;
    ctx.beginPath();
    ctx.moveTo(a.x, a.y);
    ctx.lineTo(b.x, b.y);
    ctx.stroke();
  }

  function drawNode(p, r, color) {
    ctx.beginPath();
    ctx.arc(p.x, p.y, r, 0, Math.PI * 2);
    ctx.fillStyle = color;
    ctx.fill();
  }

  function render(now) {
    ctx.clearRect(0, 0, width, height);

    clients.forEach((c) => drawLine(c, lbNode, 0.12));
    backends.forEach((b) => drawLine(lbNode, b, 0.12));

    clients.forEach((c) => drawNode(c, 3.5, "rgba(219, 226, 239, 0.5)"));
    backends.forEach((b) => drawNode(b, 3.5, "rgba(79, 209, 165, 0.65)"));
    drawNode(lbNode, 6, "rgba(91, 140, 255, 0.9)");

    spawnPulse(now);

    pulses = pulses.filter((p) => {
      p.t += 0.02;
      if (p.t >= 1) {
        if (p.hop === 1) {
          const to = backends[Math.floor(Math.random() * backends.length)];
          p.from = lbNode;
          p.to = to;
          p.t = 0;
          p.hop = 2;
        } else {
          return false;
        }
      }
      const x = p.from.x + (p.to.x - p.from.x) * p.t;
      const y = p.from.y + (p.to.y - p.from.y) * p.t;
      drawNode({ x, y }, 2.4, p.hop === 1 ? "rgba(219, 226, 239, 0.9)" : "rgba(79, 209, 165, 0.9)");
      return true;
    });

    animationId = requestAnimationFrame(render);
  }

  layout();
  animationId = requestAnimationFrame(render);

  let resizeTimer = null;
  window.addEventListener("resize", () => {
    clearTimeout(resizeTimer);
    resizeTimer = setTimeout(layout, 150);
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
