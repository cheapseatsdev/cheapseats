// Mark the nav link for the current section.
for (const a of document.querySelectorAll("header nav a")) {
  const href = a.getAttribute("href");
  if (href === "/" ? location.pathname === "/" : (location.pathname.startsWith(href) || (href === "/next-steps" && location.pathname === "/quick-wins"))) a.setAttribute("aria-current", "page");
}

// The theme: light until the toggle picks another, cycling light, dark, system. The layout's head
// script applies a pick before the page paints; a pick is remembered in this browser, and back to
// light forgets it.
(function () {
  const button = document.querySelector(".theme-toggle");
  if (!button) return;
  const modes = ["light", "dark", "system"];
  let mode = document.documentElement.dataset.theme || "system";
  const show = () => {
    button.dataset.mode = mode;
    const next = modes[(modes.indexOf(mode) + 1) % modes.length];
    button.setAttribute("aria-label", `Theme: ${mode}. Switch to ${next}.`);
    button.title = button.getAttribute("aria-label");
  };
  show();
  button.addEventListener("click", () => {
    mode = modes[(modes.indexOf(mode) + 1) % modes.length];
    if (mode === "system") delete document.documentElement.dataset.theme;
    else document.documentElement.dataset.theme = mode;
    try {
      if (mode === "light") { localStorage.removeItem("cheapseats-theme"); }
      else localStorage.setItem("cheapseats-theme", mode);
    } catch {}
    show();
  });
})();

// Open the answer a link points at (#result-N), however deeply it is folded away.
function revealSource() {
  let id;
  try { id = decodeURIComponent(window.location.hash.slice(1)); } catch { return; }
  const target = document.getElementById(id);
  if (!target) return;
  for (let node = target; node; node = node.parentElement) {
    if (node instanceof HTMLDetailsElement) node.open = true;
  }
  target.scrollIntoView({ block: "start" });
}
window.addEventListener("DOMContentLoaded", revealSource);
window.addEventListener("hashchange", revealSource);

// Keep pages current without a reload: poll /api/live, and when it changes, fetch this page again
// and swap in its <main>, keeping open sections and the scroll position. Fast while a run or fix
// list is in progress, slow otherwise, paused while the tab is hidden or you're typing.
(function () {
  if (/^\/(search|login)/.test(location.pathname)) return;
  let version = null;
  let timer = null;
  const badge = document.querySelector("header .live") ?? document.createElement("span");

  // A details element's identity: its id, or its summary's leading text, under its parent's.
  function key(d) {
    const parent = d.parentElement?.closest("details");
    const first = d.querySelector(":scope > summary")?.firstChild;
    return (parent ? key(parent) + ">" : "") + (d.id || (first?.textContent ?? "").trim());
  }

  function busy() {
    const a = document.activeElement;
    return (a && /^(INPUT|TEXTAREA|SELECT)$/.test(a.tagName)) || String(window.getSelection() ?? "") !== "";
  }

  async function refresh() {
    const res = await fetch(location.href, { headers: { accept: "text/html" } });
    if (!res.ok || res.redirected) return;
    const doc = new DOMParser().parseFromString(await res.text(), "text/html");
    const fresh = doc.querySelector("main");
    const main = document.querySelector("main");
    if (!fresh || !main) return;
    const state = new Map([...main.querySelectorAll("details")].map((d) => [key(d), d.open]));
    for (const d of fresh.querySelectorAll("details")) {
      const open = state.get(key(d));
      if (open !== undefined) d.open = open;
    }
    const y = window.scrollY;
    main.replaceWith(fresh);
    document.title = doc.title;
    document.dispatchEvent(new Event("cheapseats:refreshed"));
    window.scrollTo(0, y);
  }

  async function tick() {
    timer = null;
    let next = 10000;
    if (!document.hidden) {
      try {
        const res = await fetch("/api/live", { cache: "no-store" });
        if (res.ok) {
          const live = await res.json();
          badge.hidden = !live.active;
          next = live.active ? 2500 : 10000;
          if (version === null) version = live.version;
          else if (live.version !== version && !busy()) {
            await refresh();
            version = live.version;
          }
        }
      } catch {
        // Server restarting or offline: try again later.
      }
    }
    timer = setTimeout(tick, next);
  }

  document.addEventListener("visibilitychange", () => {
    if (!document.hidden && timer) {
      clearTimeout(timer);
      tick();
    }
  });
  tick();
})();

// One click, one submit: a second click while the first request is in flight does nothing.
document.addEventListener("submit", (e) => {
  for (const b of e.target.querySelectorAll("button")) b.disabled = true;
});

// Sortable tables: click (or Enter on) a heading with data-sort to sort by that column's data-v
// values; again to reverse. The choice survives the live refresh.
(function () {
  const chosen = new Map(); // table index -> [column, direction]
  const column = (th) => Number(th.dataset.column ?? th.cellIndex);
  function sort(table, col, dir) {
    const headings = [...table.querySelectorAll("thead th[data-sort]")];
    const th = headings.find((h) => column(h) === col);
    if (!th || !table.tBodies[0]) return;
    const num = th.dataset.sort === "num";
    const body = table.tBodies[0];
    const rows = [...body.rows];
    rows.sort((a, b) => {
      const x = a.cells[col]?.dataset.v ?? "", y = b.cells[col]?.dataset.v ?? "";
      const c = num ? parseFloat(x) - parseFloat(y) : x.localeCompare(y);
      return dir === "ascending" ? c : -c;
    });
    for (const r of rows) body.appendChild(r);
    for (const h of headings) h.removeAttribute("aria-sort");
    th.setAttribute("aria-sort", dir);
  }
  function prepare() {
    document.querySelectorAll("table.sortable").forEach((table, i) => {
      for (const th of table.querySelectorAll("th[data-sort]")) th.tabIndex = 0;
      const c = chosen.get(i);
      if (c) sort(table, c[0], c[1]);
    });
  }
  function pick(th) {
    const table = th.closest("table");
    const i = [...document.querySelectorAll("table.sortable")].indexOf(table);
    const col = column(th);
    const prev = chosen.get(i);
    // Numbers start high-to-low, names A-Z.
    const first = th.dataset.sort === "num" ? "descending" : "ascending";
    const dir = prev && prev[0] === col ? (prev[1] === "ascending" ? "descending" : "ascending") : first;
    chosen.set(i, [col, dir]);
    sort(table, col, dir);
  }
  document.addEventListener("click", (e) => {
    const th = e.target.closest("table.sortable th[data-sort]");
    if (th) pick(th);
  });
  document.addEventListener("keydown", (e) => {
    const th = e.target.closest?.("table.sortable th[data-sort]");
    if (th && (e.key === "Enter" || e.key === " ")) {
      e.preventDefault();
      pick(th);
    }
  });
  window.addEventListener("DOMContentLoaded", prepare);
  document.addEventListener("cheapseats:refreshed", prepare);
})();

// Navigation menus dismiss with Escape or a click outside, keeping native details keyboard use.
document.addEventListener("keydown", (e) => {
  if (e.key !== "Escape") return;
  for (const menu of document.querySelectorAll(".nav-more[open]")) {
    if (menu.contains(document.activeElement)) menu.querySelector("summary").focus();
    menu.open = false;
  }
});
document.addEventListener("click", (e) => {
  for (const menu of document.querySelectorAll(".nav-more[open]")) {
    if (!menu.contains(e.target) || e.target.closest?.(".nav-menu a")) menu.open = false;
  }
});

// Trend charts: a crosshair that snaps to the nearest day, with that day's values; arrow keys do
// the same when the chart has focus. Text goes in with textContent: notes name prompts and models.
(function () {
  function bind(wrap) {
    const svg = wrap.querySelector("svg.trend");
    const tip = wrap.querySelector(".trend-tip");
    const cross = svg?.querySelector(".cross");
    let pts;
    try { pts = JSON.parse(wrap.querySelector(".trend-data").textContent); } catch { return; }
    if (!svg || !tip || !pts.length) return;
    let at = pts.length - 1;
    const vb = svg.viewBox.baseVal;
    function show(i) {
      at = Math.max(0, Math.min(pts.length - 1, i));
      const p = pts[at];
      cross.setAttribute("x1", p.x);
      cross.setAttribute("x2", p.x);
      cross.setAttribute("visibility", "visible");
      tip.replaceChildren();
      const value = document.createElement("div");
      const key = document.createElement("span");
      key.className = "k";
      const strong = document.createElement("b");
      strong.textContent = p.rolling;
      value.append(key, strong, document.createTextNode(" " + (p.label || "named, 7-day")));
      const lines = [p.date, "band " + p.band + " across prompts", "that day " + p.day + (p.today ? " (this set: " + p.today + ")" : "") + " · rank " + p.rank];
      tip.append(value);
      for (const l of lines) {
        const d = document.createElement("div");
        d.className = "muted";
        d.textContent = l;
        tip.append(d);
      }
      if (p.notes && p.notes.length) {
        const ul = document.createElement("ul");
        for (const n of p.notes) {
          const li = document.createElement("li");
          li.textContent = n;
          ul.append(li);
        }
        tip.append(ul);
      }
      tip.hidden = false;
      const box = svg.getBoundingClientRect();
      const px = (p.x / vb.width) * box.width;
      const left = px + 12 + tip.offsetWidth > box.width ? px - 12 - tip.offsetWidth : px + 12;
      tip.style.left = Math.max(0, left) + "px";
    }
    function hide() {
      tip.hidden = true;
      cross.setAttribute("visibility", "hidden");
    }
    svg.addEventListener("pointermove", (e) => {
      const box = svg.getBoundingClientRect();
      const x = ((e.clientX - box.left) / box.width) * vb.width;
      let best = 0;
      pts.forEach((p, i) => { if (Math.abs(p.x - x) < Math.abs(pts[best].x - x)) best = i; });
      show(best);
    });
    svg.addEventListener("pointerleave", hide);
    svg.addEventListener("focus", () => show(at));
    svg.addEventListener("blur", hide);
    svg.addEventListener("keydown", (e) => {
      if (e.key === "ArrowLeft") { e.preventDefault(); show(at - 1); }
      if (e.key === "ArrowRight") { e.preventDefault(); show(at + 1); }
      if (e.key === "Escape") hide();
    });
  }
  const all = () => document.querySelectorAll(".trend-wrap").forEach(bind);
  window.addEventListener("DOMContentLoaded", all);
  document.addEventListener("cheapseats:refreshed", all);
})();
