// Omega-LB marketing site — small interactive behaviors.
// No build step, no framework: plain DOM APIs only.

document.addEventListener("DOMContentLoaded", () => {
  initTabs(".proof-tabs", ".proof-panel");
  initTabs(".roadmap-tabs", ".roadmap-panel");
  initCopyButtons();
  initMermaid();

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
