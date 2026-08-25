"use strict";

/* compy web UI. Hash router over four views (#/configs default,
   #/configs/<name> editor, #/collector, #/settings), all data via the P2
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
  if (!r.ok) {
    const err = new Error((body && body.error) || r.statusText);
    err.status = r.status;
    throw err;
  }
  return body;
}
function apiJSON(path, method, obj) {
  return api(path, {
    method,
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(obj),
  });
}

/* ── in-page dialogs ──────────────────────────────────────────────
   compy's own window (internal/window) is a WKWebView via webview_go,
   which registers a WKUIDelegate implementing only the file-open panel —
   none of the JavaScript panel methods. WebKit's fallback for a delegate
   that doesn't implement them is to show nothing and return the "dismissed"
   answer: prompt returns null, confirm returns false. So in the
   real app every prompt/confirm-gated action (copy, del, rename, + new
   set, edit a protected config, roll back) silently did nothing, and the
   unsaved-changes guard trapped you in the editor. <dialog> is supported,
   so ask() replaces both. Deliberately plain — the redesign owns styling. */
function ask(message, initial) {
  return new Promise((resolve) => {
    const input = initial == null ? null : el("input", {
      class: "field-input", attrs: { "aria-label": message }, props: { value: initial },
    });
    const form = el("form", { attrs: { method: "dialog" } }, [
      el("p", { class: "ask-msg", text: message }),
      input,
      el("div", { class: "form-actions" }, [
        el("button", { class: "solid-btn", attrs: { value: "ok" }, text: "OK" }),
        el("button", { class: "act", attrs: { value: "cancel" }, text: "Cancel" }),
      ]),
    ]);
    const dlg = el("dialog", { class: "ask" }, [form]);
    dlg.addEventListener("close", () => {
      const ok = dlg.returnValue === "ok"; // Escape closes with "" — a cancel
      const typed = input ? input.value.trim() : "";
      dlg.remove();
      resolve(input ? (ok && typed ? typed : null) : ok);
    });
    document.body.appendChild(dlg);
    dlg.showModal();
    if (input) input.select();
  });
}
// askText prompts for a string (null = cancelled or left empty);
// askConfirm asks a yes/no question.
function askText(message, initial) { return ask(message, initial == null ? "" : initial); }
function askConfirm(message) { return ask(message, null); }

/* ── error / message console ─────────────────────────────────────── */
const errorStrip = document.getElementById("error-strip");
const errorMessage = document.getElementById("error-message");
const errorLog = document.getElementById("error-log");
document.getElementById("error-dismiss").addEventListener("click", () => {
  errorStrip.classList.add("hidden");
});

// showMessage displays msg in the console strip, verbatim. severity "error"
// (default) colors the strip's border red; "info" (a surfaced API warning, a
// distro-remove "reverted" notice — nothing actually went wrong) colors it
// amber. A log tail is appended only for a server-fault status (>= 500) — a
// 4xx is the client's own mistake (bad input, a validation failure) and
// renders with the message alone.
async function showMessage(msg, severity, status) {
  const isError = severity !== "info";
  errorMessage.textContent = msg;
  errorLog.textContent = "";
  errorStrip.classList.remove("hidden");
  errorStrip.classList.toggle("info", !isError);
  if (isError && typeof status === "number" && status >= 500) {
    try {
      const j = await api("/api/log?lines=20");
      if (j.log) errorLog.textContent = "recent log:\n" + j.log;
    } catch (e) {
      // best-effort only
    }
  }
}
function showError(err) {
  showMessage(err && err.message ? err.message : String(err), "error", err && err.status);
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
    if (r.view !== "config") resetEditor(null);
    if (r.view === "configs") await renderConfigsView();
    else if (r.view === "config") await renderConfigView(r.name);
    else if (r.view === "collector") await renderCollectorView();
    else await renderSettingsView();
  } catch (e) {
    showError(e);
  }
}

// Unsaved-changes guard: hashchange fires after the fact, so leaving with
// unsaved editor work either gets confirmed or the hash is put back (which
// re-fires hashchange — the first line swallows that one).
let navHash = location.hash;
window.addEventListener("hashchange", async () => {
  if (location.hash === navHash) return;
  if (editorHasUnsaved()) {
    // The answer arrives asynchronously, so put the hash back first (that
    // re-fires hashchange, which the line above swallows) and re-navigate
    // only once the user has said yes.
    const target = location.hash;
    location.hash = navHash;
    if (!(await askConfirm("Leave this configuration? Unsaved changes will be lost."))) return;
    resetEditor(null); // answered yes: the unsaved work is forfeit, so stop guarding it
    location.hash = target;
    return;
  }
  navHash = location.hash;
  errorStrip.classList.add("hidden"); // stale message from the old view shouldn't follow to the new one
  renderRoute();
});

// Same guard for a browser-level reload/close: unsaved editor work triggers
// the native "leave site?" prompt. Browsers ignore custom strings and show
// their own wording, but a returnValue is still required to arm it.
window.addEventListener("beforeunload", (e) => {
  if (!editorHasUnsaved()) return;
  e.preventDefault();
  e.returnValue = "Leave this configuration? Unsaved changes will be lost.";
});

/* ── nav status (LED + text), refreshed independently ────────────── */
async function refreshNavStatus() {
  try {
    const s = await api("/api/status");
    state.status = s;
    document.getElementById("nav-led").classList.toggle("on", !!s.running);
    document.getElementById("nav-status-text").textContent = s.running
      ? "running" + (s.config ? " · " + s.config + (s.set ? " · " + s.set : "") : "")
      : "stopped";
  } catch (e) {
    // surfaced already by whatever view fetch failed; don't double-report.
  }
}

/* ── Configurations view ──────────────────────────────────────────── */
async function renderConfigsView() {
  const [configs, status] = await Promise.all([api("/api/configs"), api("/api/status")]);
  const active = status.config || "";

  clear(viewRoot);
  viewRoot.appendChild(el("h1", { text: "Configurations" }));
  viewRoot.appendChild(el("p", { class: "page-desc", text: "Whole collector-config documents. Exactly one is active at a time." }));

  const hasRemote = configs.some((c) => c.provenance === "remote");
  const toolbar = el("div", { class: "srow", attrs: { style: "padding:0 0 10px" } });
  toolbar.appendChild(el("button", {
    class: "primary-act", text: "+ new",
    on: { click: () => toggleNewConfigForm() },
  }));
  if (hasRemote) {
    toolbar.appendChild(el("button", {
      class: "primary-act", text: "sync all",
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

  const table = el("div", { class: "dtable configs-table" });
  table.appendChild(el("div", { class: "dtable-head" }, [
    el("div", {}), el("div", { text: "Name" }), el("div", { text: "Source" }), el("div", { text: "State" }),
    el("div", { class: "col-actions", text: "Actions" }),
  ]));
  if (!configs.length) {
    table.appendChild(el("div", { class: "card-empty", text: "No configurations yet." }));
  }
  for (const c of configs) {
    table.appendChild(buildConfigRow(c, active));
  }
  viewRoot.appendChild(el("div", { class: "group" }, [
    el("div", { class: "card" }, [el("div", { class: "dtable-scroll" }, [table])]),
  ]));

  const activeCount = configs.filter((c) => c.name === active).length;
  viewRoot.appendChild(el("div", {
    class: "footer-line",
    text: configs.length + (configs.length === 1 ? " configuration" : " configurations") + " · " + activeCount + " active",
  }));
}

// pendingActivate is the configuration whose activation is in flight, or
// null. It lives here rather than on the clicked button because the 5s
// background refresh re-renders the whole table: a `disabled` flag set on
// the DOM node is gone within five seconds, so an activation that takes a
// while (validate + launchctl + a probe that waits up to 5s for the
// collector to listen) looked like a dot that simply does nothing.
let pendingActivate = null;

// activateConfig posts to the same endpoint the old "use" action called.
async function activateConfig(name) {
  if (pendingActivate) return; // one at a time; a second click must not re-fire
  pendingActivate = name;
  await renderConfigsView(); // show the pending marker before we start waiting
  try {
    await api("/api/configs/" + encodeURIComponent(name) + "/activate", { method: "POST" });
  } catch (err) {
    showError(err);
  } finally {
    pendingActivate = null;
  }
  await renderConfigsView();
  await refreshNavStatus();
}

function buildActivationCell(c, isActive) {
  if (c.name === pendingActivate) {
    return el("span", {
      class: "activate-dot pending", text: "◌",
      attrs: { "aria-label": "Activating " + c.name, title: "Activating…" },
    });
  }
  if (isActive) {
    return el("span", {
      class: "activate-dot on", text: "●",
      attrs: { "aria-label": c.name + " is active", title: "active" },
    });
  }
  return el("button", {
    class: "activate-dot", text: "○",
    attrs: { "aria-label": "Activate " + c.name, title: "Activate" },
    on: { click: () => activateConfig(c.name) },
  });
}

function buildConfigRow(c, active) {
  const isActive = c.name === active;

  const name = el("a", {
    text: c.name,
    class: "config-name",
    attrs: { href: "#/configs/" + encodeURIComponent(c.name) },
  });

  let sourceText = c.provenance;
  if (c.modified) sourceText += " · modified";
  if (c.meta && c.meta.active_set) sourceText += " · set " + c.meta.active_set;
  const source = el("div", { class: "state-muted", text: sourceText });

  const state = el("div", {
    class: isActive ? "state-active" : "state-muted",
    text: isActive ? "active" : "—",
  });

  const actions = el("div", { class: "col-actions" });

  actions.appendChild(el("a", {
    class: "act", text: "open",
    attrs: { href: "#/configs/" + encodeURIComponent(c.name) },
  }));

  actions.appendChild(el("button", {
    class: "act", text: "copy",
    on: {
      click: async () => {
        const dst = await askText("New configuration name (copy of " + c.name + "):", "");
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
      class: "act", text: "sync",
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

  if (isActive) {
    actions.appendChild(el("span", { class: "act", text: "del", attrs: { title: "Can't delete the active configuration" } }));
  } else {
    actions.appendChild(el("button", {
      class: "act danger", text: "del",
      on: {
        click: async () => {
          if (!(await askConfirm('Delete configuration "' + c.name + '"? This cannot be undone.'))) return;
          try {
            await api("/api/configs/" + encodeURIComponent(c.name), { method: "DELETE" });
          } catch (err) {
            showError(err);
          }
          await renderConfigsView();
        },
      },
    }));
  }

  return el("div", { class: "dtable-row" + (isActive ? " is-active" : "") }, [
    buildActivationCell(c, isActive),
    el("div", { class: "col-name" }, [name]), source, state, actions,
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
    el("button", { class: "act", attrs: { type: "button" }, text: "cancel", on: { click: () => toggleNewConfigForm() } }),
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

/* ── Configuration editor ─────────────────────────────────────────── */
// Editor state that must survive a re-render of the view: which config it
// belongs to, whether the YAML card is unfolded / unlocked, the CodeMirror
// instance and its dirty flag, per-set unsaved cell values, and the last
// validate result. Cleared whenever we navigate to a different config.
const editor = {
  name: null,
  revealed: false,
  editable: false,
  cm: null,
  cmDirty: false,
  drafts: {},        // set name -> {var: value} typed but not saved
  validate: null,    // {ok:true} | {ok:false, msg:"..."}
  validateFresh: false, // scroll the result into view once, after a save
};

function resetEditor(name) {
  editor.name = name || null;
  editor.revealed = false;
  editor.editable = false;
  editor.cm = null;
  editor.cmDirty = false;
  editor.drafts = {};
  editor.validate = null;
  editor.validateFresh = false;
}

// editorHasUnsaved reports unsaved YAML edits or unsaved variable cells.
function editorHasUnsaved() {
  return editor.cmDirty || Object.keys(editor.drafts).length > 0;
}
// editorBusy: the config view is up with a live CodeMirror or unsaved work.
// The background tick treats this exactly like a focused input — re-rendering
// would destroy the editor instance (and the user's typing) under them.
function editorBusy() {
  return !!editor.name && parseHash().view === "config" && (!!editor.cm || editorHasUnsaved());
}

async function renderConfigView(name) {
  if (editor.name !== name) resetEditor(name);
  const [detail, distros] = await Promise.all([
    api("/api/configs/" + encodeURIComponent(name)),
    api("/api/distros"),
  ]);
  const info = detail.info || {};
  const meta = info.meta || {};

  clear(viewRoot);
  editor.cm = null; // the old instance goes with the cleared DOM
  viewRoot.appendChild(el("a", { class: "back-link", text: "← Configurations", attrs: { href: "#/configs" } }));
  viewRoot.appendChild(el("h1", { text: name }));
  viewRoot.appendChild(el("p", { class: "page-desc", text: "Properties, variable sets, and the collector YAML itself." }));

  viewRoot.appendChild(buildPropertiesGroup(name, info, meta, distros));
  viewRoot.appendChild(buildVariablesGroup(name, info, meta));
  viewRoot.appendChild(buildYamlGroup(name, info, detail.yaml || ""));
}

function buildPropertiesGroup(name, info, meta, distros) {
  const prov = info.provenance || "local";
  const sourceText = prov + (info.modified ? " · modified" : "");

  const urlInput = el("input", {
    class: "field-input path-input",
    attrs: { type: "url", "aria-label": "Remote URL", placeholder: "https://… (empty = local configuration)" },
    props: { value: meta.remote_url || "" },
  });
  urlInput.addEventListener("change", async () => {
    const v = urlInput.value.trim();
    if (v === (meta.remote_url || "")) return;
    try {
      await apiJSON("/api/configs/" + encodeURIComponent(name) + "/meta", "PUT", { remote_url: v });
    } catch (err) {
      showError(err);
    }
    await renderConfigView(name);
  });

  const selected = distros.find((d) => d.selected);
  const distroSelect = el("select", { class: "field-input", attrs: { "aria-label": "Default distribution" } }, [
    el("option", { text: "Global default" + (selected ? " (" + selected.name + ")" : ""), attrs: { value: "" } }),
    ...distros.map((d) => el("option", { text: d.name, attrs: { value: d.name } })),
  ]);
  distroSelect.value = meta.distro || "";
  distroSelect.addEventListener("change", async () => {
    try {
      await apiJSON("/api/configs/" + encodeURIComponent(name) + "/meta", "PUT", { distro: distroSelect.value });
    } catch (err) {
      showError(err);
      distroSelect.value = meta.distro || "";
    }
    await renderConfigView(name);
    await refreshNavStatus();
  });

  const syncActions = el("div", { class: "actions" });
  if (prov === "remote" && !info.modified) {
    syncActions.appendChild(el("button", {
      class: "act", text: "sync",
      on: {
        click: async (e) => {
          e.target.disabled = true;
          try {
            await api("/api/configs/" + encodeURIComponent(name) + "/sync", { method: "POST" });
          } catch (err) {
            showError(err);
          }
          await renderConfigView(name);
        },
      },
    }));
  } else if (prov === "remote" && info.modified) {
    syncActions.appendChild(el("button", {
      class: "act danger", text: "discard local edits & re-sync",
      on: {
        click: async (e) => {
          if (!(await askConfirm("Refetch " + name + " from its remote URL? Your local edits to this configuration are discarded."))) return;
          e.target.disabled = true;
          try {
            await api("/api/configs/" + encodeURIComponent(name) + "/resync", { method: "POST" });
          } catch (err) {
            showError(err);
          }
          resetEditor(name);
          await renderConfigView(name);
        },
      },
    }));
  }

  const bar = el("div", { class: "srow props-bar" }, [
    el("span", { class: "state-muted props-source", text: sourceText }),
    urlInput,
    distroSelect,
    syncActions,
  ]);
  return el("div", { class: "group" }, [el("div", { class: "card" }, [bar])]);
}

/* ── variables card ───────────────────────────────────────────────── */
function draftFor(set) {
  if (!editor.drafts[set]) editor.drafts[set] = {};
  return editor.drafts[set];
}

function buildVariablesGroup(name, info, meta) {
  const vars = info.vars || [];
  const sets = meta.variable_sets || {};
  const setNames = Object.keys(sets).sort();
  const active = meta.active_set || "";

  const card = el("div", { class: "card" });
  if (!vars.length) {
    card.appendChild(el("div", { class: "card-extra vars-empty" }, [
      el("div", { class: "title", text: "No variables in this configuration." }),
      el("div", { class: "desc", text: "Write ${env:NAME} (optionally ${env:NAME:-default}) anywhere in the YAML and it shows up here. A trailing comment on the same line becomes the description:" }),
      el("pre", { class: "code-panel", text: 'exporters:\n  otlphttp:\n    endpoint: ${env:BACKEND_URL:-https://localhost:4318} # where to ship the data\n    headers:\n      authorization: ${env:API_KEY} # your backend API key' }),
    ]));
  } else {
    card.appendChild(buildVarsTable(name, vars, sets, setNames, active));
  }

  const footer = el("div", { class: "srow" }, [
    el("div", { class: "grow" }, [el("div", { class: "desc", text: setNames.length ? "One column per set; the active set (amber) is the one the collector runs with." : "Variable sets hold values for these variables — dev, prod, a customer, …" })]),
    el("div", { class: "actions" }, [
      el("button", {
        class: "primary-act", text: "+ new set",
        on: {
          click: async () => {
            const set = await askText("Name for the new variable set:", "");
            if (!set) return;
            try {
              await apiJSON("/api/configs/" + encodeURIComponent(name) + "/sets/" + encodeURIComponent(set), "PUT", { values: {} });
            } catch (err) {
              showError(err);
            }
            await renderConfigView(name);
          },
        },
      }),
    ]),
  ]);
  card.appendChild(footer);

  return el("div", { class: "group" }, [el("div", { class: "group-title", text: "Variables" }), card]);
}

function buildVarsTable(name, vars, sets, setNames, active) {
  const table = el("table", { class: "vars-table" });
  const headRow = el("tr", {}, [el("th", { class: "var-col", text: "Variable" })]);
  for (const set of setNames) headRow.appendChild(buildSetHeader(name, set, sets[set] || {}, set === active));
  if (!setNames.length) headRow.appendChild(el("th", { class: "desc", text: "no sets yet" }));
  table.appendChild(el("thead", {}, [headRow]));

  const body = el("tbody");
  for (const v of vars) {
    const row = el("tr", {}, [
      el("th", { class: "var-col" }, [
        el("code", { text: v.name }),
        v.description ? el("div", { class: "desc", text: v.description }) : null,
      ]),
    ]);
    for (const set of setNames) {
      const stored = (sets[set] || {})[v.name];
      const draft = editor.drafts[set];
      const val = draft && Object.prototype.hasOwnProperty.call(draft, v.name) ? draft[v.name] : (stored != null ? stored : "");
      const input = el("input", {
        class: "field-input cell-input",
        attrs: {
          "aria-label": v.name + " in " + set,
          placeholder: v.has_default ? String(v.default) : "(no default)",
        },
        props: { value: val },
        on: { input: (e) => { draftFor(set)[v.name] = e.target.value; } },
      });
      row.appendChild(el("td", { class: set === active ? "active-col" : "" }, [input]));
    }
    if (!setNames.length) row.appendChild(el("td", {}, [el("span", { class: "desc", text: v.has_default ? "default: " + v.default : "unset" })]));
    body.appendChild(row);
  }
  table.appendChild(body);
  return el("div", { class: "vars-scroll" }, [table]);
}

function buildSetHeader(name, set, stored, isActive) {
  const base = "/api/configs/" + encodeURIComponent(name) + "/sets/" + encodeURIComponent(set);

  const radio = el("input", {
    attrs: { type: "radio", name: "active-set", "aria-label": "Use set " + set },
    props: { checked: isActive },
    on: {
      change: async () => {
        try {
          await api(base + "/use", { method: "POST" });
        } catch (err) {
          showError(err);
        }
        await renderConfigView(name);
        await refreshNavStatus();
      },
    },
  });

  const saveBtn = el("button", {
    class: "act", text: "save",
    on: {
      click: async (e) => {
        e.target.disabled = true;
        // The PUT replaces the whole set: stored values plus everything the
        // user typed (an empty string is a legal value; a cell never touched
        // and never stored simply stays absent).
        const values = Object.assign({}, stored, editor.drafts[set] || {});
        try {
          await apiJSON(base, "PUT", { values });
          delete editor.drafts[set];
        } catch (err) {
          showError(err);
        }
        await renderConfigView(name);
      },
    },
  });

  const renameBtn = el("button", {
    class: "act", text: "rename",
    on: {
      click: async () => {
        const to = await askText("Rename variable set:", set);
        if (!to || to === set) return;
        try {
          await apiJSON(base + "/rename", "POST", { to });
          delete editor.drafts[set];
        } catch (err) {
          showError(err);
        }
        await renderConfigView(name);
      },
    },
  });

  const delBtn = el("button", { class: "act danger", text: "del" });
  if (isActive) {
    delBtn.disabled = true;
    delBtn.title = "Can't delete the active set";
  } else {
    delBtn.addEventListener("click", async () => {
      if (!(await askConfirm('Delete variable set "' + set + '"?'))) return;
      try {
        await api(base, { method: "DELETE" });
        delete editor.drafts[set];
      } catch (err) {
        showError(err);
      }
      await renderConfigView(name);
    });
  }

  return el("th", { class: "set-col" + (isActive ? " active-col" : "") }, [
    el("div", { class: "set-head" }, [radio, el("span", { class: "set-name", text: set })]),
    el("div", { class: "set-actions" }, [saveBtn, renameBtn, delBtn]),
  ]);
}

/* ── YAML card ────────────────────────────────────────────────────── */
// Edit protection: a shipped or remote configuration that hasn't been
// touched starts collapsed, then read-only, and only becomes editable after
// an explicit confirm — everything else is editable straight away.
function buildYamlGroup(name, info, yaml) {
  const prov = info.provenance || "local";
  const protectedCfg = (prov === "shipped" || prov === "remote") && !info.modified;
  if (!protectedCfg) {
    editor.revealed = true;
    editor.editable = true;
  }

  const card = el("div", { class: "card" });
  const actions = el("div", { class: "actions" });
  const header = el("div", { class: "srow" }, [
    el("div", { class: "grow" }, [
      el("div", { class: "title", text: "collector.yaml" }),
      el("div", {
        class: "desc",
        text: editor.editable
          ? "Saving validates the configuration with its own distro and active set."
          : protectedCfg && editor.revealed
            ? "Read-only: this configuration still matches its " + (prov === "remote" ? "remote source" : "shipped original") + "."
            : "",
      }),
    ]),
    actions,
  ]);
  card.appendChild(header);

  if (!editor.revealed) {
    actions.appendChild(el("button", {
      class: "act", text: "show yaml",
      on: {
        click: () => { editor.revealed = true; renderConfigView(name).catch(showError); },
      },
    }));
    return el("div", { class: "group" }, [el("div", { class: "group-title", text: "Configuration" }), card]);
  }

  const host = el("div", { class: "cm-host" });
  card.appendChild(host);

  if (!editor.editable) {
    actions.appendChild(el("button", {
      class: "act", text: "edit",
      on: {
        click: async () => {
          const msg = prov === "remote"
            ? "Editing detaches this configuration from its remote source. Sync will stop; \"Discard local edits & re-sync\" brings it back."
            : "Editing makes this configuration yours. Your version will be kept on future compy updates. Continue?";
          if (!(await askConfirm(msg))) return;
          editor.editable = true;
          renderConfigView(name).catch(showError);
        },
      },
    }));
  } else {
    actions.appendChild(el("button", {
      class: "solid-btn", text: "Save & validate",
      on: { click: (e) => saveAndValidate(name, e.target) },
    }));
  }

  if (editor.validate) {
    const result = el("div", { class: "card-extra validate-result" }, [
      editor.validate.ok
        ? el("span", { class: "validate-ok", text: "valid" })
        : el("pre", { class: "code-panel validate-error", text: editor.validate.msg }),
    ]);
    card.appendChild(result);
    if (editor.validateFresh) {
      // it lives below a half-viewport-tall editor; bring it into view once.
      editor.validateFresh = false;
      queueMicrotask(() => result.scrollIntoView({ block: "end" }));
    }
  }

  // CodeMirror is created after the card is in the document so it measures
  // itself correctly.
  queueMicrotask(() => {
    if (!host.isConnected) return;
    editor.cm = CodeMirror(host, {
      value: yaml,
      mode: "yaml",
      lineNumbers: true,
      readOnly: !editor.editable,
      lineWrapping: true,
      viewportMargin: 20,
    });
    editor.cmDirty = false;
    editor.cm.on("change", () => { editor.cmDirty = true; });
  });

  return el("div", { class: "group" }, [el("div", { class: "group-title", text: "Configuration" }), card]);
}

async function saveAndValidate(name, btn) {
  if (!editor.cm) return;
  btn.disabled = true;
  btn.textContent = "Saving…";
  const base = "/api/configs/" + encodeURIComponent(name);
  try {
    await api(base + "/yaml", {
      method: "PUT",
      headers: { "Content-Type": "text/plain" },
      body: editor.cm.getValue(),
    });
    editor.cmDirty = false;
  } catch (err) {
    showError(err);
    await renderConfigView(name);
    return;
  }
  try {
    await api(base + "/validate", { method: "POST" });
    editor.validate = { ok: true };
  } catch (err) {
    editor.validate = { ok: false, msg: err && err.message ? err.message : String(err) };
  }
  editor.validateFresh = true;
  await renderConfigView(name); // the modified flag just changed
}

/* ── Collector view ───────────────────────────────────────────────── */
let lastLogText = "";

// logLines is how much of the collector log the view loads — and, since the
// search box filters exactly that text client-side, how far the search
// reaches. It used to be 500, so searching for anything older than the last
// 500 lines answered "(no matching lines)" about a line that is right there
// in the log. 2000 is the API's own cap (webui.maxLogLines); the note beside
// the search box says how much was actually loaded, so a miss is legible
// instead of a lie.
const logLines = 2000;

function applyLogFilter() {
  const pre = document.getElementById("log-view");
  if (!pre) return;
  const note = document.getElementById("log-note");
  const all = lastLogText ? lastLogText.split("\n") : [];
  const q = state.logFilter.toLowerCase();
  const lines = q ? all.filter((l) => l.toLowerCase().includes(q)) : all;
  pre.textContent = lines.length ? lines.join("\n") : (q ? "(no matching lines)" : "(empty)");
  if (note) {
    note.textContent = q
      ? lines.length + " of the " + all.length + " loaded lines match"
      : all.length + " lines loaded";
  }
}

async function renderCollectorView() {
  const status = await api("/api/status");

  const prevLog = document.getElementById("log-view");
  const prevScroll = prevLog
    ? { top: prevLog.scrollTop, atBottom: prevLog.scrollHeight - prevLog.scrollTop - prevLog.clientHeight < 4 }
    : null;
  clear(viewRoot);
  viewRoot.appendChild(el("h1", { text: "Collector" }));
  viewRoot.appendChild(el("p", { class: "page-desc", text: "The OpenTelemetry Collector compy runs for you as a background service." }));

  const statusCard = el("div", { class: "card" });
  const table = el("table", { class: "def-table" }, [
    el("tr", {}, [el("th", { text: "state" }), el("td" , {}, [
      el("span", { class: "led" + (status.running ? " on" : ""), attrs: { style: "margin-right:7px" } }),
      el("span", { text: status.running ? "running" : "stopped" }),
    ])]),
    el("tr", {}, [el("th", { text: "config" }), el("td", {
      // Always name the set, "(none)" included: which variables the running
      // collector actually got is not something to leave the reader guessing.
      text: status.config
        ? status.config + " · set " + (status.set || "(none)")
        : "no configuration active",
    })]),
    el("tr", {}, [el("th", { text: "distro" }), el("td", { text: status.distro || "(none)" })]),
    el("tr", {}, [el("th", { text: "ports" }), el("td", { text: "grpc " + status.grpc_port + " · http " + status.http_port })]),
  ]);
  const actions = el("div", { class: "srow" }, [
    el("div", { class: "grow" }),
    el("div", { class: "col-actions" }, [
      el("button", {
        class: "act", text: "restart",
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
        class: "act", text: "roll back",
        on: {
          click: async (e) => {
            e.target.disabled = true;
            if (!(await askConfirm("Roll back to the last known-good configuration and settings?"))) {
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
  statusCard.append(table, actions);
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
  toolbar.appendChild(el("span", { class: "desc", attrs: { id: "log-note" } }));
  const pre = el("pre", { class: "code-panel", attrs: { id: "log-view" } });
  logCard.append(toolbar, el("div", { class: "card-extra" }, [pre]));
  logGroup.appendChild(logCard);
  viewRoot.appendChild(logGroup);

  try {
    const j = await api("/api/log?lines=" + logLines);
    lastLogText = j.log || "";
  } catch (err) {
    lastLogText = "";
    showError(err);
  }
  applyLogFilter();
  // The background refresh rebuilds this whole view every 5s, which used to
  // snap the log back to the top mid-read; carry the reading position over.
  if (prevScroll) pre.scrollTop = prevScroll.atBottom ? pre.scrollHeight : prevScroll.top;
}

/* ── Settings view ────────────────────────────────────────────────── */
async function renderSettingsView() {
  const [distros, settings, status] = await Promise.all([
    api("/api/distros"),
    api("/api/settings"),
    api("/api/status"),
  ]);

  clear(viewRoot);
  viewRoot.appendChild(el("h1", { text: "Settings" }));
  viewRoot.appendChild(el("p", { class: "page-desc", text: "Collector distributions and menu bar / environment behavior." }));

  viewRoot.appendChild(buildDistrosGroup(distros));
  viewRoot.appendChild(buildTogglesGroup(settings, status));
}

function buildDistrosGroup(distros) {
  const table = el("div", { class: "dtable distros-table" });
  table.appendChild(el("div", { class: "dtable-head" }, [
    el("div", { text: "Name" }), el("div", { text: "Path" }), el("div", { text: "State" }),
    el("div", { class: "col-actions", text: "Actions" }),
  ]));
  if (!distros.length) {
    table.appendChild(el("div", { class: "card-empty", text: "No distributions registered yet." }));
  }
  for (const d of distros) {
    table.appendChild(buildDistroRow(d));
  }
  const toolbar = el("div", { class: "srow", attrs: { style: "padding:0 0 10px" } });
  toolbar.appendChild(el("button", {
    class: "primary-act", text: "+ add distribution",
    on: { click: () => toggleAddDistroForm() },
  }));
  return el("div", { class: "group" }, [
    el("div", { class: "group-title", text: "Distributions" }),
    toolbar,
    buildAddDistroForm(),
    el("div", { class: "card" }, [el("div", { class: "dtable-scroll" }, [table])]),
  ]);
}

function toggleAddDistroForm() {
  const card = document.getElementById("add-distro-card");
  if (card) card.classList.toggle("hidden");
  if (card && !card.classList.contains("hidden")) {
    const nameInput = card.querySelector('input[name="name"]');
    if (nameInput) nameInput.focus();
  }
}

function buildAddDistroForm() {
  const form = el("form", { class: "form-grid" });
  const nameLabel = el("label", {}, [
    el("span", { text: "Name" }),
    el("input", { class: "field-input", attrs: { name: "name", required: "required", placeholder: "my-distro" } }),
  ]);
  const pathLabel = el("label", {}, [
    el("span", { text: "Path" }),
    el("input", { class: "field-input path-input", attrs: { name: "path", required: "required", placeholder: "/path/to/otelcol" } }),
  ]);
  const actions = el("div", { class: "form-actions" }, [
    el("button", { class: "solid-btn", attrs: { type: "submit" }, text: "Add" }),
    el("button", { class: "act", attrs: { type: "button" }, text: "cancel", on: { click: () => toggleAddDistroForm() } }),
  ]);
  form.append(nameLabel, pathLabel, actions);
  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    const name = form.name.value.trim();
    const path = form.path.value.trim();
    if (!name || !path) return;
    try {
      const res = await apiJSON("/api/distros", "POST", { name, path });
      if (res && res.warning) showMessage(res.warning, "info");
      form.reset();
      toggleAddDistroForm();
    } catch (err) {
      showError(err);
    }
    await renderSettingsView();
  });
  return el("div", { class: "card hidden", attrs: { id: "add-distro-card" } }, [
    el("div", { class: "card-extra" }, [form]),
  ]);
}

function buildDistroRow(d) {
  let nameText = d.name;
  if (d.definition) nameText += " · definition";

  const pathInput = el("input", {
    class: "field-input path-input",
    attrs: { "aria-label": "Path for " + d.name, placeholder: "not downloaded — set a path, or fetch" },
    props: { value: d.path || "" },
  });
  pathInput.addEventListener("change", async () => {
    const newPath = pathInput.value.trim();
    if (newPath === (d.path || "")) return;
    try {
      const res = await apiJSON("/api/distros/" + encodeURIComponent(d.name), "PUT", { path: newPath });
      if (res && res.warning) showMessage(res.warning, "info");
    } catch (err) {
      showError(err);
      pathInput.value = d.path || "";
    }
    await renderSettingsView();
  });

  // "downloaded" only means anything for a shipped definition compy fetches
  // itself; a binary the user pointed at is simply there.
  let stateText = d.definition ? (d.downloaded ? "downloaded" : "not downloaded") : "ready";
  let stateClass = "state-muted";
  if (d.definition && !d.available) { stateText = "unavailable"; stateClass = "state-warn"; }
  if (d.selected) { stateText = "selected"; stateClass = "state-active"; }
  const state = el("div", { class: stateClass, text: stateText });

  const actions = el("div", { class: "col-actions" });
  if (d.definition && !d.downloaded && d.available) {
    actions.appendChild(el("button", {
      class: "act", text: "fetch",
      on: {
        click: async (e) => {
          e.target.disabled = true;
          e.target.textContent = "fetching…";
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
  // "available" is false only for a shipped definition with no build for
  // this platform — offering "use" there could only ever fail.
  if (!d.selected && d.available) {
    actions.appendChild(el("button", {
      class: "act", text: "use",
      on: {
        click: async (e) => {
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
    }));
  }
  // Remove only makes sense for an actual distros.json entry (a custom
  // distro, or a shipped definition whose path was overridden) —
  // user_entry is false for a shipped definition merely downloaded to its
  // default path, which has no registry entry to remove (the API 400s).
  if (d.user_entry) {
    actions.appendChild(buildRemoveButton(d));
  }

  return el("div", { class: "dtable-row" + (d.selected ? " is-active" : "") }, [
    el("div", { class: "col-name", text: nameText }),
    el("div", { class: "col-path" }, [pathInput]),
    state,
    actions,
  ]);
}

function buildRemoveButton(d) {
  const removeBtn = el("button", { class: "act danger", text: "remove" });
  if (d.selected) {
    removeBtn.disabled = true;
    removeBtn.title = "Can't remove the selected distro";
  } else {
    removeBtn.addEventListener("click", async () => {
      if (!(await askConfirm('Remove distro "' + d.name + '"?'))) return;
      try {
        const res = await api("/api/distros/" + encodeURIComponent(d.name), { method: "DELETE" });
        if (res && res.reverted) showMessage('"' + d.name + '" reverted to its shipped definition.', "info");
      } catch (err) {
        showError(err);
      }
      await renderSettingsView();
    });
  }
  return removeBtn;
}

function buildTogglesGroup(settings, status) {
  const menuRow = el("div", { class: "srow" }, [
    el("div", { class: "grow" }, [
      el("div", { class: "title", text: "Show distro switcher in menu bar" }),
      el("div", { class: "desc", text: "Off by default — most people never need to swap collector binaries from the tray." }),
    ]),
    el("input", {
      class: "sq-check",
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
  ]);
  const osEnvRow = el("div", { class: "srow" }, [
    el("div", { class: "grow" }, [
      el("div", { class: "title", text: "Set variables system-wide" }),
      el("div", { class: "desc", text: "Injects OTEL_* into the login session (launchctl setenv) so newly started apps pick them up with no shell setup. Already-running apps are unaffected." }),
    ]),
    el("input", {
      class: "sq-check",
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
  ]);
  return el("div", { class: "group" }, [
    el("div", { class: "group-title", text: "Menu bar & environment" }),
    el("div", { class: "card" }, [menuRow, osEnvRow]),
  ]);
}

/* ── background refresh ───────────────────────────────────────────── */
async function tick() {
  await refreshNavStatus();
  // never clobber a focused input mid-edit, nor a live editor
  if (isInputFocused() || editorBusy()) return;
  await renderRoute();
}

/* ── boot ─────────────────────────────────────────────────────────── */
renderRoute();
refreshNavStatus();
setInterval(tick, 5000);
