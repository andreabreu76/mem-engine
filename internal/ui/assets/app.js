"use strict";

const POLL_MS = 2000;
const HEALTH_MS = 10000;
const FEED_KEEP = 300;

const params = new URLSearchParams(location.search);
const token = params.get("token") || "";
params.delete("token");
const rest = params.toString();
history.replaceState(null, "", location.pathname + (rest ? "?" + rest : "") + location.hash);

const $ = (id) => document.getElementById(id);
const rowHits = new WeakMap();
const state = {
  lastId: 0,
  seeded: false,
  generation: 0,
  busy: false,
  timer: 0,
  healthAt: 0,
  projects: new Map(),
  item: null,
  itemProject: "",
};

function el(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined && text !== null) node.textContent = String(text);
  return node;
}

async function api(method, path, query, body) {
  const url = new URL(path, location.origin);
  for (const [k, v] of Object.entries(query || {})) {
    if (v !== "" && v !== undefined && v !== null) url.searchParams.set(k, v);
  }
  const init = { method, headers: { "X-Mem-Token": token }, cache: "no-store" };
  if (body !== undefined) {
    init.headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(body);
  }
  const resp = await fetch(url, init);
  let data = null;
  try {
    data = await resp.json();
  } catch (_) {
    data = null;
  }
  if (!resp.ok) throw new Error((data && data.error) || resp.status + " " + resp.statusText);
  return data;
}

function showStatus(message, ok) {
  const status = $("status");
  status.classList.toggle("ok", Boolean(ok));
  status.textContent = message || "";
  status.hidden = !message;
}

function project() {
  return $("project").value;
}

function projectLabel(key) {
  if (key === "user") return "user";
  const p = state.projects.get(key);
  return p ? p.name : key;
}

function when(at) {
  const d = new Date(at);
  return isNaN(d) ? "" : d.toLocaleString();
}

function numericId(id) {
  const n = Number(String(id).replace(/^o-/, ""));
  return Number.isSafeInteger(n) ? n : 0;
}

function row(hit, current) {
  const li = el("li");
  const button = el("button", "row");
  button.type = "button";
  button.dataset.id = hit.id;
  rowHits.set(button, hit);
  if (current) button.setAttribute("aria-current", "true");
  const head = el("span", "row-head");
  head.append(el("span", "type type-" + hit.kind, hit.type || hit.kind), el("span", "title", hit.title || hit.id));
  const meta = el("span", "row-meta muted", [when(hit.at), projectLabel(hit.project), hit.id].filter(Boolean).join("  ·  "));
  button.append(head);
  if (hit.summary) button.append(el("span", "summary", hit.summary));
  button.append(meta);
  button.addEventListener("click", () => openItem(hit.id));
  li.append(button);
  return li;
}

function prependFeed(hits) {
  const feed = $("feed");
  for (const hit of hits) {
    feed.prepend(row(hit));
    state.lastId = Math.max(state.lastId, numericId(hit.id));
  }
  while (feed.children.length > FEED_KEEP) feed.lastElementChild.remove();
  $("feed-empty").hidden = feed.children.length > 0;
}

function schedule(delay) {
  clearTimeout(state.timer);
  state.timer = 0;
  if (document.visibilityState !== "visible") {
    $("feed-state").textContent = "paused";
    return;
  }
  state.timer = setTimeout(tick, delay);
}

async function fetchFeed() {
  if (state.seeded) return api("GET", "/api/feed", { after: state.lastId, project: project() });
  const recent = await api("GET", "/api/search", { project: project() });
  const observations = recent.filter((h) => h.kind === "observation");
  if (observations.length === 0) return api("GET", "/api/feed", { after: 0, project: project() });
  return observations.sort((a, b) => numericId(a.id) - numericId(b.id));
}

async function tick() {
  if (state.busy) return;
  state.busy = true;
  const generation = state.generation;
  let delay = POLL_MS;
  try {
    const hits = await fetchFeed();
    if (generation === state.generation) {
      state.seeded = true;
      prependFeed(hits);
      $("feed-state").textContent = "live";
    }
  } catch (err) {
    if (generation === state.generation) $("feed-state").textContent = "error: " + err.message;
  } finally {
    state.busy = false;
  }
  if (generation !== state.generation) delay = 0;
  if (Date.now() - state.healthAt >= HEALTH_MS) refreshHealth();
  schedule(delay);
}

function resetFeed() {
  state.generation++;
  state.lastId = 0;
  state.seeded = false;
  $("feed").replaceChildren();
  $("feed-empty").hidden = false;
  schedule(0);
}

async function refreshHealth() {
  state.healthAt = Date.now();
  const badge = $("health");
  try {
    const h = await api("GET", "/api/health");
    const bad = h.spool_pending > 0 || h.capture_errors_24h > 0;
    badge.classList.toggle("bad", bad);
    badge.classList.toggle("good", !bad);
    badge.textContent = bad ? "spool " + h.spool_pending + " · errors 24h " + h.capture_errors_24h : "healthy";
    badge.title = bad ? h.last_error || "events waiting in the spool, run mem doctor" : "spool empty, no capture errors in the last 24h";
  } catch (err) {
    badge.classList.add("bad");
    badge.classList.remove("good");
    badge.textContent = "health unknown";
    badge.title = err.message;
  }
}

async function loadProjects() {
  const select = $("project");
  try {
    const rows = await api("GET", "/api/projects");
    for (const p of rows) {
      state.projects.set(p.key, p);
      const option = el("option", "", p.name === p.key ? p.key : p.name + "  (" + p.key + ")");
      option.value = p.key;
      select.append(option);
    }
  } catch (err) {
    showStatus("Could not load projects: " + err.message);
  }
}

async function runSearch(event) {
  event.preventDefault();
  const query = {
    project: project(),
    q: $("query").value.trim(),
    type: $("type").value.trim(),
    since: $("since").value,
  };
  try {
    const hits = await api("GET", "/api/search", query);
    const list = $("results");
    list.replaceChildren(...hits.map((h) => row(h)));
    $("results-title").textContent = hits.length + " result" + (hits.length === 1 ? "" : "s");
    $("results-empty").hidden = hits.length > 0;
    $("results-panel").hidden = false;
    $("feed-panel").hidden = true;
    showStatus("");
  } catch (err) {
    showStatus("Search failed: " + err.message);
  }
}

function backToFeed() {
  $("results-panel").hidden = true;
  $("feed-panel").hidden = false;
}

async function openItem(id) {
  const scope = project();
  try {
    const data = await api("GET", "/api/item/" + encodeURIComponent(id), { project: scope });
    state.item = data.item;
    state.itemProject = scope;
    renderItem(data.item, data.timeline);
    showStatus("");
    return true;
  } catch (err) {
    showStatus("Could not open " + id + ": " + err.message);
    return false;
  }
}

function renderItem(item, timeline) {
  $("item-panel").hidden = false;
  $("edit-form").hidden = true;
  $("item-title").textContent = item.title || item.id;
  const meta = [item.id, item.type || item.kind, projectLabel(item.project), when(item.at)];
  if (item.tool_name) meta.push("tool " + item.tool_name);
  if (item.session_id) meta.push("session " + item.session_id);
  $("item-meta").textContent = meta.filter(Boolean).join("  ·  ");
  $("item-tags").replaceChildren(...(item.tags || []).map((t) => el("span", "chip", t)));
  $("item-files").replaceChildren(...(item.files || []).map((f) => el("li", "", f)));
  $("item-body").textContent = item.body || item.summary || "";
  $("item-truncated").hidden = !item.truncated;
  $("item-edit").hidden = !editable(item);
  $("timeline").replaceChildren(...(timeline || []).map((h) => row(h, h.id === item.id)));
}

function closeItem() {
  state.item = null;
  $("item-panel").hidden = true;
}

function editable(item) {
  return Boolean(item) && item.kind === "memory" && !item.truncated;
}

function refreshRows(m) {
  for (const node of document.querySelectorAll(".row")) {
    if (node.dataset.id !== m.id) continue;
    const hit = { ...rowHits.get(node), type: m.type, title: m.title, summary: m.summary };
    node.parentElement.replaceWith(row(hit, node.getAttribute("aria-current") === "true"));
  }
}

function startEdit() {
  const item = state.item;
  if (!editable(item)) return;
  $("edit-type").value = item.type || "";
  $("edit-title").value = item.title || "";
  $("edit-summary").value = item.summary || "";
  $("edit-tags").value = (item.tags || []).join(", ");
  $("edit-body").value = item.body || "";
  $("edit-form").hidden = false;
  $("edit-title").focus();
}

async function saveEdit(event) {
  event.preventDefault();
  const item = state.item;
  if (!editable(item)) {
    showStatus("This memory is too large to edit here, use the update tool.");
    return;
  }
  const patch = {
    type: $("edit-type").value.trim(),
    title: $("edit-title").value.trim(),
    summary: $("edit-summary").value.trim(),
    tags: $("edit-tags").value.split(",").map((t) => t.trim()).filter(Boolean),
    body: $("edit-body").value,
  };
  let saved;
  try {
    saved = await api("PUT", "/api/memory/" + encodeURIComponent(item.id), { project: state.itemProject }, patch);
  } catch (err) {
    showStatus("Save failed: " + err.message);
    return;
  }
  refreshRows(saved);
  if (await openItem(item.id)) showStatus("Saved " + item.id + ".", true);
}

async function deleteItem() {
  const item = state.item;
  if (!item) return;
  const what = item.kind === "memory" ? "memory" : "observation";
  if (!confirm("Delete " + what + " " + item.id + " (" + (item.title || "") + ")? This cannot be undone.")) return;
  try {
    await api("DELETE", "/api/" + what + "/" + encodeURIComponent(item.id), { project: state.itemProject });
    for (const node of document.querySelectorAll(".row")) {
      if (node.dataset.id === item.id) node.parentElement.remove();
    }
    $("feed-empty").hidden = $("feed").children.length > 0;
    closeItem();
    showStatus("Deleted " + item.id + ".", true);
  } catch (err) {
    showStatus("Delete failed: " + err.message);
  }
}

async function start() {
  if (!token) {
    showStatus("Missing access token: open the URL printed by mem ui.");
    return;
  }
  $("search-form").addEventListener("submit", runSearch);
  $("back-to-feed").addEventListener("click", backToFeed);
  $("project").addEventListener("change", () => {
    backToFeed();
    closeItem();
    resetFeed();
  });
  $("item-close").addEventListener("click", closeItem);
  $("item-edit").addEventListener("click", startEdit);
  $("item-delete").addEventListener("click", deleteItem);
  $("edit-form").addEventListener("submit", saveEdit);
  $("edit-cancel").addEventListener("click", () => {
    $("edit-form").hidden = true;
  });
  document.addEventListener("visibilitychange", () => schedule(0));
  await loadProjects();
  resetFeed();
}

start();
