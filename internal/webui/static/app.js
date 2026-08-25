"use strict";

/* compy web UI. Hash router over four views (#/configs default,
   #/configs/<name> stub, #/collector, #/settings), all data via the P2
   REST API. House rule: every API-derived string goes through
   textContent/el(), never innerHTML. */

/* ── tiny DOM helper ──────────────────────────────────────────────── */
// el(tag, opts, children): opts may set class/text/attrs/on(events). Never
// accepts raw HTML — API-derived strings always go through text or
// textContent, keeping this the one place that could introduce XSS and
// making it trivially auditable.
function el(tag, opts, children) {
  const e = document.createElement(tag);
  opts = opts || {};
  if (opts.class) e.className = opts.class;
  if (opts.text != null) e.textContent = opts.text;
  if (opts.attrs) for (const k in opts.attrs) e.setAttribute(k, opts.attrs[k]);
  if (opts.on) for (const k in opts.on) e.addEventListener(k, opts.on[k]);
  if (opts.props) for (const k in opts.props) e[k] = opts.props[k];
  for (const c of children || []) if (c) e.appendChild(c);
  return e;
}
function clear(node) { while (node.firstChild) node.removeChild(node.firstChild); }

/* ── API client ───────────────────────────────────────────────────── */
async function api(path, opts) {
  const r = await fetch(path, opts);
  const ct = r.headers.get("content-type") || "";
  const body = ct.includes("json") ? await r.json() : await r.text();
  if (!r.ok) throw new Error((body && body.error) || r.statusText);
  return body;
}
function apiJSON(path, method, obj) {
  return api(path, {
    method,
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(obj),
  });
}

/* ── error / message console ─────────────────────────────────────── */
const errorStrip = document.getElementById("error-strip");
const errorMessage = document.getElementById("error-message");
const errorLog = document.getElementById("error-log");
document.getElementById("error-dismiss").addEventListener("click", () => {
  errorStrip.classList.add("hidden");
});

// showMessage displays msg (an error or a surfaced API warning) in the dark
// console strip, verbatim; on an actual error it also appends a short log
// tail for context.
async function showMessage(msg, withLogTail) {
  errorMessage.textContent = msg;
  errorLog.textContent = "";
  errorStrip.classList.remove("hidden");
  if (withLogTail) {
    try {
      const j = await api("/api/log?lines=20");
      if (j.log) errorLog.textContent = "recent log:\n" + j.log;
    } catch (e) {
      // best-effort only
    }
  }
}
function showError(err) {
  showMessage(err && err.message ? err.message : String(err), true);
}

/* ── shared state ─────────────────────────────────────────────────── */
const state = {
  status: null,
  logFilter: "",
};

/* ── nav + router ─────────────────────────────────────────────────── */
const navButtons = Array.from(document.querySelectorAll(".nav-btn"));
for (const btn of navButtons) {
  btn.addEventListener("click", () => {
    location.hash = "#/" + btn.dataset.view;
  });
}

function parseHash() {
  const raw = location.hash.replace(/^#\/?/, "");
  const parts = raw.split("/").filter(Boolean);
  if (parts[0] === "configs" && parts[1]) {
    return { view: "config", name: decodeURIComponent(parts[1]) };
  }
  if (parts[0] === "collector") return { view: "collector" };
  if (parts[0] === "settings") return { view: "settings" };
  return { view: "configs" };
}

function setNavCurrent(view) {
  const navView = view === "config" ? "configs" : view;
  for (const btn of navButtons) {
    if (btn.dataset.view === navView) btn.setAttribute("aria-current", "page");
    else btn.removeAttribute("aria-current");
  }
}

// isInputFocused reports whether the currently focused element lives inside
// the rendered view and would lose its in-progress edit if we re-rendered
// (an inline path edit, the new-configuration form, the log filter, ...).
// The background refresh checks this before touching the DOM — the P1
// lesson about never clobbering a focused input.
function isInputFocused() {
  const a = document.activeElement;
  if (!a) return false;
  const tag = a.tagName;
  if (tag !== "INPUT" && tag !== "TEXTAREA" && tag !== "SELECT") return false;
  return document.getElementById("view").contains(a);
}

const viewRoot = document.getElementById("view");

async function renderRoute() {
  const r = parseHash();
  setNavCurrent(r.view);
  try {
    if (r.view === "configs") await renderConfigsView();
    else if (r.view === "config") renderConfigStub(r.name);
    else if (r.view === "collector") await renderCollectorView();
    else await renderSettingsView();
  } catch (e) {
    showError(e);
  }
}

window.addEventListener("hashchange", renderRoute);

/* ── nav status (LED + text), refreshed independently ────────────── */
async function refreshNavStatus() {
  try {
    const s = await api("/api/status");
    state.status = s;
    document.getElementById("nav-led").classList.toggle("on", !!s.running);
    document.getElementById("nav-status-text").textContent = s.running
      ? "running" + (s.config ? " · " + s.config : "")
      : "stopped";
  } catch (e) {
    // surfaced already by whatever view fetch failed; don't double-report.
  }
}

/* ── Configurations view ──────────────────────────────────────────── */
async function renderConfigsView() {
  const [configs, status] = [await api("/api/configs"), state.status || (await api("/api/status"))];
  const active = status.config || "";

  clear(viewRoot);
  viewRoot.appendChild(el("h1", { text: "Configurations" }));
  viewRoot.appendChild(el("p", { class: "page-desc", text: "Whole collector-config documents. Exactly one is active at a time." }));

  const hasRemote = configs.some((c) => c.provenance === "remote");
  const toolbar = el("div", { class: "srow", attrs: { style: "padding:0 0 10px" } });
  toolbar.appendChild(el("button", {
    class: "pill-btn", text: "+ New configuration",
    on: { click: () => toggleNewConfigForm() },
  }));
  if (hasRemote) {
    toolbar.appendChild(el("button", {
      class: "pill-btn", text: "Sync all",
      on: {
        click: async (e) => {
          e.target.disabled = true;
          try {
            await api("/api/configs/sync-all", { method: "POST" });
          } catch (err) {
            showError(err);
          }
          await renderConfigsView();
        },
      },
    }));
  }
  viewRoot.appendChild(toolbar);

  viewRoot.appendChild(buildNewConfigForm());

  const card = el("div", { class: "card" });
  if (!configs.length) {
    card.appendChild(el("div", { class: "card-empty", text: "No configurations yet." }));
  }
  for (const c of configs) {
    card.appendChild(buildConfigRow(c, active));
  }
  viewRoot.appendChild(el("div", { class: "group" }, [card]));
}

function buildConfigRow(c, active) {
  const isActive = c.name === active;
  const title = el("div", { class: "title" }, [
    isActive ? el("span", { class: "active-marker", attrs: { "aria-label": "active" } }) : null,
    el("a", {
      text: c.name,
      class: "config-name" + (isActive ? " active" : ""),
      attrs: { href: "#/configs/" + encodeURIComponent(c.name) },
    }),
  ]);
  const metaBits = [];
  metaBits.push(el("span", { class: "chip provenance-" + c.provenance, text: c.provenance }));
  if (c.modified) metaBits.push(el("span", { class: "chip modified", text: "locally modified" }));
  if (c.meta && c.meta.active_set) metaBits.push(el("span", { class: "chip", text: "set " + c.meta.active_set }));
  const meta = el("div", { class: "desc" }, metaBits);

  const actions = el("div", { class: "actions" });

  actions.appendChild(el("button", {
    class: "pill-btn", text: "Open",
    on: { click: () => { location.hash = "#/configs/" + encodeURIComponent(c.name); } },
  }));

  if (isActive) {
    actions.appendChild(el("button", { class: "pill-btn", text: "Active", attrs: { disabled: "disabled" } }));
  } else {
    actions.appendChild(el("button", {
      class: "pill-btn", text: "Use",
      on: {
        click: async (e) => {
          e.target.disabled = true;
          try {
            await api("/api/configs/" + encodeURIComponent(c.name) + "/activate", { method: "POST" });
          } catch (err) {
            showError(err);
          }
          await renderConfigsView();
          await refreshNavStatus();
        },
      },
    }));
  }

  actions.appendChild(el("button", {
    class: "pill-btn", text: "Copy",
    on: {
      click: async () => {
        const dst = window.prompt("New configuration name (copy of " + c.name + "):");
        if (!dst) return;
        try {
          await apiJSON("/api/configs/" + encodeURIComponent(c.name) + "/copy", "POST", { dst });
        } catch (err) {
          showError(err);
        }
        await renderConfigsView();
      },
    },
  }));

  if (c.provenance === "remote" && !c.modified) {
    actions.appendChild(el("button", {
      class: "pill-btn", text: "Sync",
      on: {
        click: async () => {
          try {
            await api("/api/configs/" + encodeURIComponent(c.name) + "/sync", { method: "POST" });
          } catch (err) {
            showError(err);
          }
          await renderConfigsView();
        },
      },
    }));
  }

  const deleteBtn = el("button", { class: "danger-link", text: "Delete" });
  if (isActive) {
    deleteBtn.disabled = true;
    deleteBtn.title = "Can't delete the active configuration";
  } else {
    deleteBtn.addEventListener("click", async () => {
      if (!window.confirm('Delete configuration "' + c.name + '"? This cannot be undone.')) return;
      try {
        await api("/api/configs/" + encodeURIComponent(c.name), { method: "DELETE" });
      } catch (err) {
        showError(err);
      }
      await renderConfigsView();
    });
  }
  actions.appendChild(deleteBtn);

  return el("div", { class: "srow" }, [
    el("div", { class: "grow" }, [title, meta]),
    actions,
  ]);
}

function toggleNewConfigForm() {
  const card = document.getElementById("new-config-card");
  if (card) card.classList.toggle("hidden");
  if (card && !card.classList.contains("hidden")) {
    const nameInput = card.querySelector('input[name="name"]');
    if (nameInput) nameInput.focus();
  }
}

function buildNewConfigForm() {
  const form = el("form", { class: "form-grid" });
  const nameLabel = el("label", {}, [
    el("span", { text: "Name" }),
    el("input", { class: "field-input", attrs: { name: "name", required: "required", placeholder: "my-config" } }),
  ]);
  const urlLabel = el("label", {}, [
    el("span", { text: "Remote URL (optional)" }),
    el("input", { class: "field-input", attrs: { name: "url", type: "url", placeholder: "https://raw.githubusercontent.com/.../collector.yaml" } }),
  ]);
  const actions = el("div", { class: "form-actions" }, [
    el("button", { class: "solid-btn", attrs: { type: "submit" }, text: "Create" }),
    el("button", { class: "pill-btn", attrs: { type: "button" }, text: "Cancel", on: { click: () => toggleNewConfigForm() } }),
  ]);
  form.append(nameLabel, urlLabel, actions);
  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    const name = form.name.value.trim();
    const url = form.url.value.trim();
    if (!name) return;
    try {
      if (url) await apiJSON("/api/configs/from-url", "POST", { name, url });
      else await apiJSON("/api/configs", "POST", { name, yaml: "" });
      form.reset();
      toggleNewConfigForm();
    } catch (err) {
      showError(err);
    }
    await renderConfigsView();
  });
  return el("div", { class: "group" }, [
    el("div", { class: "card hidden", attrs: { id: "new-config-card" } }, [
      el("div", { class: "card-extra" }, [form]),
    ]),
  ]);
}

/* ── Configuration editor (stub — T4 fills this in) ──────────────── */
function renderConfigStub(name) {
  clear(viewRoot);
  viewRoot.appendChild(el("h1", { text: name }));
  viewRoot.appendChild(el("p", { class: "page-desc", text: "Editor arrives in the next task." }));
  viewRoot.appendChild(el("a", { text: "← Back to configurations", attrs: { href: "#/configs" } }));
}

/* ── Collector view ───────────────────────────────────────────────── */
let lastLogText = "";

function applyLogFilter() {
  const pre = document.getElementById("log-view");
  if (!pre) return;
  const q = state.logFilter.toLowerCase();
  if (!q) {
    pre.textContent = lastLogText || "(empty)";
    return;
  }
  const lines = lastLogText.split("\n").filter((l) => l.toLowerCase().includes(q));
  pre.textContent = lines.length ? lines.join("\n") : "(no matching lines)";
}

async function renderCollectorView() {
  const status = state.status || (await api("/api/status"));

  clear(viewRoot);
  viewRoot.appendChild(el("h1", { text: "Collector" }));
  viewRoot.appendChild(el("p", { class: "page-desc", text: "The OpenTelemetry Collector compy runs for you as a background service." }));

  const statusCard = el("div", { class: "card" });
  const line = el("div", { class: "status-line" }, [
    el("span", { class: "led" + (status.running ? " on" : "") }),
    el("span", { text: status.running ? "Running" : "Stopped" }),
  ]);
  const configLine = el("div", { class: "status-meta" }, [
    el("span", {
      text: status.config
        ? "config " + status.config + (status.set ? " · set " + status.set : "")
        : "no configuration active",
    }),
  ]);
  const portsLine = el("div", { class: "status-meta" }, [
    el("span", { text: "distro " + (status.distro || "(none)") + " · grpc " + status.grpc_port + " · http " + status.http_port }),
  ]);
  const actions = el("div", { class: "srow", attrs: { style: "border-top:1px solid var(--line)" } }, [
    el("div", { class: "grow" }),
    el("div", { class: "actions" }, [
      el("button", {
        class: "pill-btn", text: "Restart",
        on: {
          click: async (e) => {
            e.target.disabled = true;
            try {
              await api("/api/service/apply", { method: "POST" });
            } catch (err) {
              showError(err);
            }
            await renderCollectorView();
            await refreshNavStatus();
          },
        },
      }),
      el("button", {
        class: "pill-btn", text: "Roll back",
        on: {
          click: async (e) => {
            e.target.disabled = true;
            if (!window.confirm("Roll back to the last known-good configuration and settings?")) {
              e.target.disabled = false;
              return;
            }
            try {
              await api("/api/service/rollback", { method: "POST" });
            } catch (err) {
              showError(err);
            }
            await renderCollectorView();
            await refreshNavStatus();
          },
        },
      }),
    ]),
  ]);
  statusCard.append(
    el("div", { class: "card-extra" }, [line, configLine, portsLine]),
    actions,
  );
  viewRoot.appendChild(el("div", { class: "group" }, [
    el("div", { class: "group-title", text: "Status" }),
    statusCard,
  ]));

  const logGroup = el("div", { class: "group" });
  logGroup.appendChild(el("div", { class: "group-title", text: "Collector log" }));
  const logCard = el("div", { class: "card" });
  const toolbar = el("div", { class: "log-toolbar", attrs: { style: "padding:10px 16px 0" } });
  const filterInput = el("input", {
    class: "field-input", attrs: { type: "search", placeholder: "Search the log", "aria-label": "Search the log" },
    props: { value: state.logFilter },
    on: {
      input: (e) => {
        state.logFilter = e.target.value;
        applyLogFilter();
      },
    },
  });
  toolbar.appendChild(filterInput);
  const pre = el("pre", { class: "code-panel", attrs: { id: "log-view" } });
  logCard.append(toolbar, el("div", { class: "card-extra" }, [pre]));
  logGroup.appendChild(logCard);
  viewRoot.appendChild(logGroup);

  try {
    const j = await api("/api/log?lines=500");
    lastLogText = j.log || "";
  } catch (err) {
    lastLogText = "";
    showError(err);
  }
  applyLogFilter();
}

/* ── Settings view ────────────────────────────────────────────────── */
async function renderSettingsView() {
  const [distros, settings, status] = await Promise.all([
    api("/api/distros"),
    api("/api/settings"),
    state.status ? Promise.resolve(state.status) : api("/api/status"),
  ]);

  clear(viewRoot);
  viewRoot.appendChild(el("h1", { text: "Settings" }));
  viewRoot.appendChild(el("p", { class: "page-desc", text: "Distributions, ports, and how compy wires OTEL_* into your shell." }));

  viewRoot.appendChild(buildDistrosGroup(distros));
  viewRoot.appendChild(buildPortsGroup(settings));
  viewRoot.appendChild(buildTogglesGroup(settings, status));
  viewRoot.appendChild(buildWiringGroup(settings));
}

function buildDistrosGroup(distros) {
  const card = el("div", { class: "card" });
  if (!distros.length) {
    card.appendChild(el("div", { class: "card-empty", text: "No distributions registered yet." }));
  }
  for (const d of distros) {
    card.appendChild(buildDistroRow(d));
  }
  return el("div", { class: "group" }, [
    el("div", { class: "group-title", text: "Distributions" }),
    card,
  ]);
}

function buildDistroRow(d) {
  const chips = [];
  if (d.definition) chips.push(el("span", { class: "chip", text: "definition" }));
  chips.push(el("span", { class: "chip " + (d.downloaded ? "ok" : ""), text: d.downloaded ? "downloaded" : "not downloaded" }));
  if (d.definition && !d.available) chips.push(el("span", { class: "chip warn", text: "unavailable" }));

  const radio = el("input", {
    attrs: { type: "radio", name: "distro-use", "aria-label": "Use " + d.name },
    props: { checked: !!d.selected },
    on: {
      change: async (e) => {
        e.target.disabled = true;
        try {
          await api("/api/distros/" + encodeURIComponent(d.name) + "/use", { method: "POST" });
        } catch (err) {
          showError(err);
        }
        await renderSettingsView();
        await refreshNavStatus();
      },
    },
  });

  const nameLine = el("div", { class: "title" }, [el("span", { text: d.name }), ...chips]);

  const pathInput = el("input", {
    class: "field-input path-input",
    attrs: { "aria-label": "Path for " + d.name, placeholder: "not downloaded — set a path, or Fetch" },
    props: { value: d.path || "" },
  });
  pathInput.addEventListener("change", async () => {
    const newPath = pathInput.value.trim();
    if (newPath === (d.path || "")) return;
    try {
      const res = await apiJSON("/api/distros/" + encodeURIComponent(d.name), "PUT", { path: newPath });
      if (res && res.warning) showMessage(res.warning, false);
    } catch (err) {
      showError(err);
      pathInput.value = d.path || "";
    }
    await renderSettingsView();
  });

  const actions = el("div", { class: "actions" });
  if (d.definition && !d.downloaded && d.available) {
    actions.appendChild(el("button", {
      class: "pill-btn", text: "Fetch",
      on: {
        click: async (e) => {
          e.target.disabled = true;
          e.target.textContent = "Fetching…";
          try {
            await api("/api/distros/" + encodeURIComponent(d.name) + "/fetch", { method: "POST" });
          } catch (err) {
            showError(err);
          }
          await renderSettingsView();
        },
      },
    }));
  }
  // Remove only makes sense for an actual registry entry (a custom distro,
  // or a shipped definition whose path was overridden) — a not-yet-fetched
  // shipped definition has no entry to remove (the API would 400 on it).
  if (d.path) {
    actions.appendChild(buildRemoveButton(d));
  }

  return el("div", { class: "srow" }, [
    radio,
    el("div", { class: "grow" }, [nameLine, pathInput]),
    actions,
  ]);
}

function buildRemoveButton(d) {
  const removeBtn = el("button", { class: "danger-link", text: "Remove" });
  if (d.selected) {
    removeBtn.disabled = true;
    removeBtn.title = "Can't remove the selected distro";
  } else {
    removeBtn.addEventListener("click", async () => {
      if (!window.confirm('Remove distro "' + d.name + '"?')) return;
      try {
        const res = await api("/api/distros/" + encodeURIComponent(d.name), { method: "DELETE" });
        if (res && res.reverted) showMessage('"' + d.name + '" reverted to its shipped definition.', false);
      } catch (err) {
        showError(err);
      }
      await renderSettingsView();
    });
  }
  return removeBtn;
}

function buildPortsGroup(settings) {
  const form = el("form", { class: "form-grid" });
  const grpcLabel = el("label", {}, [
    el("span", { text: "gRPC port" }),
    el("input", { class: "field-input port-input", attrs: { name: "grpc_port", type: "number", min: "1", max: "65535" }, props: { value: settings.grpc_port } }),
  ]);
  const httpLabel = el("label", {}, [
    el("span", { text: "HTTP port" }),
    el("input", { class: "field-input port-input", attrs: { name: "http_port", type: "number", min: "1", max: "65535" }, props: { value: settings.http_port } }),
  ]);
  const hint = el("div", { class: "desc", text: "Takes effect on next apply." });
  const actions = el("div", { class: "form-actions" }, [
    el("button", { class: "solid-btn", attrs: { type: "submit" }, text: "Save" }),
    hint,
  ]);
  form.append(grpcLabel, httpLabel, actions);
  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    const grpc_port = parseInt(form.grpc_port.value, 10);
    const http_port = parseInt(form.http_port.value, 10);
    try {
      await apiJSON("/api/settings", "PUT", { grpc_port, http_port });
    } catch (err) {
      showError(err);
    }
    await renderSettingsView();
    await refreshNavStatus();
  });
  return el("div", { class: "group" }, [
    el("div", { class: "group-title", text: "Ports" }),
    el("div", { class: "card" }, [el("div", { class: "card-extra" }, [form])]),
  ]);
}

function buildTogglesGroup(settings, status) {
  const menuRow = el("div", { class: "srow" }, [
    el("div", { class: "grow" }, [
      el("div", { class: "title", text: "Show distro switcher in menu bar" }),
      el("div", { class: "desc", text: "Off by default — most people never need to swap collector binaries from the tray." }),
    ]),
    el("span", { class: "switch" }, [
      el("input", {
        attrs: { type: "checkbox", "aria-label": "Show distro switcher in menu bar" },
        props: { checked: !!settings.menu_distro_swap },
        on: {
          change: async (e) => {
            try {
              await apiJSON("/api/settings", "PUT", { menu_distro_swap: e.target.checked });
            } catch (err) {
              showError(err);
            }
          },
        },
      }),
      el("span", { class: "track" }),
    ]),
  ]);
  const osEnvRow = el("div", { class: "srow" }, [
    el("div", { class: "grow" }, [
      el("div", { class: "title", text: "Set variables system-wide" }),
      el("div", { class: "desc", text: "Injects OTEL_* into the login session (launchctl setenv) so newly started apps pick them up with no shell setup. Already-running apps are unaffected." }),
    ]),
    el("span", { class: "switch" }, [
      el("input", {
        attrs: { type: "checkbox", "aria-label": "Set variables system-wide" },
        props: { checked: !!status.os_env },
        on: {
          change: async (e) => {
            try {
              await apiJSON("/api/os-env", "POST", { on: e.target.checked });
            } catch (err) {
              showError(err);
            }
            await refreshNavStatus();
          },
        },
      }),
      el("span", { class: "track" }),
    ]),
  ]);
  return el("div", { class: "group" }, [
    el("div", { class: "group-title", text: "Menu bar & environment" }),
    el("div", { class: "card" }, [menuRow, osEnvRow]),
  ]);
}

function copyButton(sourceId) {
  return el("button", {
    class: "pill-btn", text: "Copy",
    on: {
      click: (e) => {
        const text = document.getElementById(sourceId).textContent;
        navigator.clipboard.writeText(text).catch(() => {});
        e.target.textContent = "Copied";
        setTimeout(() => { e.target.textContent = "Copy"; }, 1500);
      },
    },
  });
}

function buildWiringGroup(settings) {
  const httpEndpoint = "http://127.0.0.1:" + settings.http_port;
  const grpcEndpoint = "127.0.0.1:" + settings.grpc_port;

  const epHTTP = el("code", { text: httpEndpoint, attrs: { id: "ep-http" } });
  const epGRPC = el("code", { text: grpcEndpoint, attrs: { id: "ep-grpc" } });
  const evalLine = el("code", { text: 'eval "$(compy env)"', attrs: { id: "env-line" } });

  const endpointsCard = el("div", { class: "card" }, [
    el("div", { class: "srow" }, [
      el("div", { class: "grow" }, [el("div", { class: "title", text: "OTLP over HTTP" }), el("div", { class: "desc" }, [epHTTP])]),
      copyButton("ep-http"),
    ]),
    el("div", { class: "srow" }, [
      el("div", { class: "grow" }, [el("div", { class: "title", text: "OTLP over gRPC" }), el("div", { class: "desc" }, [epGRPC])]),
      copyButton("ep-grpc"),
    ]),
  ]);

  const shellCard = el("div", { class: "card" }, [
    el("div", { class: "srow" }, [
      el("div", { class: "grow" }, [
        el("div", { class: "title", text: "Current shell" }),
        el("div", { class: "desc" }, [document.createTextNode("Sets OTEL_* variables for this shell only: "), evalLine]),
      ]),
      copyButton("env-line"),
    ]),
    el("div", { class: "srow" }, [
      el("div", { class: "grow" }, [
        el("div", { class: "title", text: "Single command" }),
        el("div", { class: "desc" }, [document.createTextNode("Runs one program with the variables set: "), el("code", { text: "compy run -- <command>" })]),
      ]),
    ]),
  ]);

  return el("div", { class: "group" }, [
    el("div", { class: "group-title", text: "Wiring" }),
    endpointsCard,
    el("div", { attrs: { style: "height:12px" } }),
    shellCard,
  ]);
}

/* ── background refresh ───────────────────────────────────────────── */
async function tick() {
  await refreshNavStatus();
  if (isInputFocused()) return; // never clobber a focused input mid-edit
  await renderRoute();
}

/* ── boot ─────────────────────────────────────────────────────────── */
renderRoute();
refreshNavStatus();
setInterval(tick, 5000);
