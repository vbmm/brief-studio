"use strict";

const $ = (id) => document.getElementById(id);
const shell = { account: null, selected: null, loadingBrief: 0, loadingModels: 0, generating: false };
const labelDate = new Intl.DateTimeFormat("fr-FR", { dateStyle: "long", timeStyle: "short" });
const textBytes = new TextEncoder();
const idPattern = /^[A-Za-z0-9_-]{1,128}$/;
const sourceProviders = ["discord", "x"];
const sourceSettings = {};
const busySources = new Set();
const xSnapshotLimit = 16 * 1024;
const marketPath = "/api/sources/market";
const marketSnapshotLimit = 16 * 1024;
let sourceRead = 0;
let marketRead = 0;
let marketSnapshot;
let scheduleVisit = 0;
let scheduleSaving = false;
let scheduleState;
let scheduleModelsReady = false;
let scheduleBriefId = "";

class APIError extends Error {
  constructor(status, message) {
    super(message);
    this.status = status;
  }
}

async function api(path, method = "GET", body) {
  const headers = { Accept: "application/json", "X-BriefStudio-Request": "1" };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  let response;
  try {
    response = await fetch(path, {
      method, headers, credentials: "same-origin", redirect: "error", cache: "no-store",
      body: body === undefined || body instanceof Blob ? body : JSON.stringify(body),
    });
  } catch {
    throw new APIError(0, "Impossible de joindre Brief Studio. Vérifie que le lanceur est toujours ouvert.");
  }
  const payload = response.status === 204 ? null : await response.json().catch(() => null);
  if (!response.ok) {
    const message = typeof payload?.error === "string" && payload.error.trim() ? payload.error.slice(0, 600) : null;
    throw new APIError(response.status, message || "Brief Studio n’a pas pu terminer cette action.");
  }
  return payload;
}

function node(tag, content) {
  const element = document.createElement(tag);
  if (content !== undefined) element.textContent = content;
  return element;
}

function validHTTPS(value) {
  if (typeof value !== "string" || value.length > 2048) return null;
  try {
    const url = new URL(value);
    return url.protocol === "https:" && !url.username && !url.password ? url : null;
  } catch {
    return null;
  }
}

function sourceLink(text, target) {
  const url = validHTTPS(target);
  if (!url) return document.createTextNode(text);
  const link = node("a", text);
  link.href = url.href;
  link.target = "_blank";
  link.rel = "noopener noreferrer";
  return link;
}

function status(id, message = "", kind = "info") {
  const element = $(id);
  element.textContent = message;
  element.dataset.kind = kind;
  element.hidden = !message;
}

function errorMessage(error) {
  return error instanceof Error ? error.message : "Une erreur est survenue.";
}

function formatDate(value) {
  if (typeof value !== "string") return "";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "" : labelDate.format(date);
}

function fillInline(element, text, allowLinks) {
  // Model output and imported content create text nodes, never executable markup.
  const tokens = /(!)?\[([^\]\n]+)\]\(([^\s<>)]+)\)|\*\*([^*\n]+)\*\*|`([^`\n]+)`|(https:\/\/[^\s<>()\]"']+)/g;
  let cursor = 0;
  for (const token of text.matchAll(tokens)) {
    element.append(text.slice(cursor, token.index));
    if (token[2]) {
      element.append(allowLinks && !token[1] ? sourceLink(token[2], token[3]) : token[2]);
    } else if (token[4]) {
      element.append(node("strong", token[4]));
    } else if (token[5]) {
      element.append(node("code", token[5]));
    } else {
      const url = token[6].replace(/[.,;:!?]+$/, "");
      element.append(allowLinks ? sourceLink(url, url) : url, token[6].slice(url.length));
    }
    cursor = token.index + token[0].length;
  }
  element.append(text.slice(cursor));
}

function formatted(tag, text, allowLinks = true) {
  const element = node(tag);
  fillInline(element, text, allowLinks);
  return element;
}

function formatReport(brief) {
  const content = document.createDocumentFragment();
  const contents = document.createDocumentFragment();
  const localGex = typeof brief.localGex === "string" && brief.localGex.length <= 16 * 1024
    ? brief.localGex.trim() : "";
  const text = localGex ? brief.text.trimEnd() + "\n\n" + localGex : brief.text;
  const lines = text.replace(/\r\n?/g, "\n").trimStart().split("\n");
  if (/^#\s/.test(lines[0])) lines.shift();
  const heading = /^(#{1,6})\s+(.+)$/;
  const listItem = /^\s*(?:[-*+•]|(\d+)\.)\s+(.+)$/;
  const divider = /^(?:-{3,}|\*{3,}|_{3,})$/;
  const cells = (line) => line.trim().replace(/^\||\|$/g, "").split("|").map((cell) => cell.trim());
  const isTable = (i) => lines[i]?.trim().startsWith("|")
    && cells(lines[i + 1] || "").every((cell) => /^:?-{3,}:?$/.test(cell));
  const isBlock = (i) => {
    const line = lines[i];
    return !line?.trim() || heading.test(line) || listItem.test(line)
      || divider.test(line.trim()) || line.startsWith(">") || line.startsWith("```") || isTable(i);
  };

  let at = 0;
  let sections = 0;
  const addSection = (element) => {
    element.id = "section-" + (++sections);
    element.tabIndex = -1;
    const link = node("a", element.textContent);
    link.href = "#" + element.id;
    const entry = node("li");
    entry.append(link);
    contents.append(entry);
  };
  while (at < lines.length) {
    const line = lines[at];
    const title = line.match(heading);
    const item = line.match(listItem);
    if (!line.trim()) {
      at++;
    } else if (title) {
      const level = Math.max(2, Math.min(4, title[1].length));
      const element = formatted("h" + level, title[2], false);
      if (level === 2) addSection(element);
      content.append(element);
      at++;
    } else if (item) {
      const ordered = Boolean(item[1]);
      const list = node(ordered ? "ol" : "ul");
      if (ordered) list.start = Number(item[1]);
      let next;
      while ((next = lines[at]?.match(listItem)) && Boolean(next[1]) === ordered) {
        list.append(formatted("li", next[2]));
        at++;
      }
      content.append(list);
    } else if (divider.test(line.trim())) {
      content.append(node("hr"));
      at++;
    } else if (line.startsWith(">")) {
      const paragraphs = [];
      while (lines[at]?.startsWith(">")) paragraphs.push(lines[at++].replace(/^>\s?/, ""));
      content.append(formatted("blockquote", paragraphs.join("\n")));
    } else if (line.startsWith("```")) {
      const source = [];
      at++;
      while (at < lines.length && !lines[at].startsWith("```")) source.push(lines[at++]);
      if (at < lines.length) at++;
      const pre = node("pre");
      pre.append(node("code", source.join("\n")));
      content.append(pre);
    } else if (isTable(at)) {
      const table = node("table");
      const head = table.createTHead().insertRow();
      for (const cell of cells(line)) {
        const th = formatted("th", cell);
        th.scope = "col";
        head.append(th);
      }
      at += 2;
      const body = table.createTBody();
      while (lines[at]?.trim().startsWith("|")) {
        const row = body.insertRow();
        for (const cell of cells(lines[at++])) row.append(formatted("td", cell));
      }
      const wrapper = node("div");
      wrapper.className = "table-wrap";
      wrapper.append(table);
      content.append(wrapper);
    } else {
      const paragraphs = [line];
      at++;
      while (!isBlock(at)) paragraphs.push(lines[at++]);
      content.append(formatted("p", paragraphs.join(" ")));
    }
  }

  const sources = Array.isArray(brief.sources) ? brief.sources.slice(0, 100).filter(
    (source) => typeof source?.label === "string" && source.label.trim()
      && source.label.length <= 240 && validHTTPS(source.url)
  ) : [];
  if (sources.length) {
    const title = node("h2", "Sources");
    addSection(title);
    const links = node("ul");
    for (const source of sources) {
      const item = node("li");
      item.append(sourceLink(source.label, source.url));
      links.append(item);
    }
    content.append(title, links);
  }
  return { content, contents };
}

function briefPath(id) {
  return "/api/briefs/" + encodeURIComponent(id);
}

function ownBrief(id, brief) {
  if (typeof id !== "string" || !idPattern.test(id) || brief?.schema !== "nq-es-brief/v1"
      || typeof brief.title !== "string" || !brief.title.trim() || brief.title.length > 240
      || typeof brief.text !== "string" || !brief.text.trim() || brief.text.length > 1024 * 1024) {
    throw new Error("Ce brief ne peut pas être affiché.");
  }
  const { content, contents } = formatReport(brief);
  shell.selected = id;
  $("brief-title").textContent = brief.title;
  const date = formatDate(brief.generatedAt);
  $("brief-date").textContent = date;
  $("brief-date").hidden = !date;
  if (date) $("brief-date").dateTime = brief.generatedAt;
  $("demo-tag").hidden = id !== "sample";
  $("brief-text").replaceChildren(content);
  $("toc").hidden = !contents.childNodes.length;
  $("toc-items").replaceChildren(contents);
  $("toc").parentElement.classList.toggle("no-toc", $("toc").hidden);
  $("export-link").href = briefPath(id) + "/export";
  $("reading").hidden = false;
  for (const entry of document.querySelectorAll(".archive-entry")) {
    if (entry.dataset.id === id) entry.setAttribute("aria-current", "page");
    else entry.removeAttribute("aria-current");
  }
}

async function selectBrief(id, focus = true) {
  const attempt = ++shell.loadingBrief;
  status("notice", "Ouverture du brief…");
  try {
    const result = await api(id === "sample" ? "/api/sample" : briefPath(id));
    if (attempt !== shell.loadingBrief) return;
    ownBrief(id, result);
    status("notice");
    if (focus) $("brief").focus();
  } catch (error) {
    if (attempt === shell.loadingBrief) status("notice", errorMessage(error), "error");
  }
}

async function refreshArchive() {
  const rows = await api("/api/briefs");
  if (!Array.isArray(rows)) throw new Error("La bibliothèque locale ne peut pas être affichée.");
  const list = document.createDocumentFragment();
  let first = null;
  for (const item of rows.slice(0, 400)) {
    if (typeof item?.id !== "string" || !idPattern.test(item.id) || item.id === "sample"
        || typeof item.title !== "string" || !item.title.trim() || item.title.length > 240) continue;
    first ||= item.id;
    const entry = node("button", item.title);
    entry.type = "button";
    entry.className = "archive-entry";
    entry.dataset.id = item.id;
    if (shell.selected === item.id) entry.setAttribute("aria-current", "page");
    const date = formatDate(item.generatedAt);
    if (date) entry.append(node("span", date));
    entry.addEventListener("click", () => { void selectBrief(item.id); });
    const row = node("li");
    row.append(entry);
    list.append(row);
  }
  $("brief-list").replaceChildren(list);
  $("empty-library").hidden = first !== null;
  return first;
}

function syncAccount(state) {
  shell.account = state;
  const identity = state.profile?.name || state.profile?.email || "Compte ChatGPT";
  $("account-button").textContent = state.connected ? String(identity).slice(0, 64) : "Compte ChatGPT";
  $("account-identity").textContent = state.connected
    ? String(state.profile?.email || identity).slice(0, 250)
    : state.hasAccount
      ? "Autorise de nouveau ton compte ChatGPT pour continuer."
      : "Connecte ton compte pour rédiger des briefs avec ton forfait ChatGPT.";
  const persistence = $("account-persistence");
  persistence.textContent = state.remembered ? "Connexion conservée sur cet ordinateur."
    : state.connected ? "Connexion active pour ce lancement seulement."
      : state.storageWarning || "";
  persistence.hidden = persistence.textContent === "";
  $("account-signin").hidden = state.connected === true;
  $("signout-button").hidden = state.hasAccount !== true;
  $("version").textContent = typeof state.version === "string" ? "v" + state.version : "";
}

async function start() {
  const key = new URLSearchParams(location.hash.slice(1)).get("key");
  try {
    if (key) {
      await api("/api/bootstrap", "POST", { key });
      history.replaceState(null, "", location.pathname + location.search);
    }
    const state = await api("/api/state");
    if (typeof state?.connected !== "boolean") throw new Error("État de Brief Studio indisponible.");
    syncAccount(state);
    for (const id of ["account-button", "new-button", "sample-button", "updates-button", "sources-button", "schedule-button"]) $(id).disabled = false;
    let first = null;
    let archiveError = null;
    try {
      first = await refreshArchive();
    } catch (error) {
      archiveError = error;
    }
    await selectBrief(first || "sample", false);
    const auth = new URLSearchParams(location.search).get("auth");
    if (auth === "error") {
      status("notice", state.authError || "Connexion à ChatGPT non confirmée.", "error");
    } else if (auth === "ok" && state.connected) {
      status("notice", "Compte ChatGPT connecté.");
    } else if (archiveError) {
      status("notice", errorMessage(archiveError), "error");
    }
  } catch (error) {
    const message = error.status === 401
      ? "Session locale expirée. Rouvre Brief Studio depuis le lanceur."
      : errorMessage(error);
    status("notice", message, "error");
    $("retry-button").hidden = error.status === 401;
  }
}

async function openComposer() {
  const attempt = ++shell.loadingModels;
  const dialog = $("composer");
  const connected = shell.account?.connected === true;
  const allowed = shell.account?.profile?.canUsePlan === true;
  $("generator").hidden = true;
  $("composer-cancel-actions").hidden = false;
  $("needs-account").hidden = connected;
  $("composer-signin").hidden = connected;
  status("composer-status");
  dialog.showModal();
  if (!connected) return;
  if (!allowed) {
    status("composer-status", "Ce compte ChatGPT n’autorise pas la génération dans Brief Studio.", "error");
    return;
  }
  $("gex-mode").value = "without";
  showGexCoverage();
  void refreshMarketSnapshot().catch(() => {});
  status("composer-status", "Chargement des modèles…");
  try {
    const models = await api("/api/models");
    if (!dialog.open || attempt !== shell.loadingModels) return;
    const picker = $("model-picker");
    const previous = picker.value;
    const choices = Array.isArray(models) ? models.filter(
      (model) => typeof model?.id === "string" && model.id.trim() && model.id.length < 160
        && typeof model.displayName === "string" && model.displayName.trim()
    ) : [];
    if (!choices.length) throw new Error("Aucun modèle disponible pour ce compte ChatGPT.");
    picker.replaceChildren(...choices.map((model) => {
      const option = node("option", model.displayName.slice(0, 150));
      option.value = model.id;
      return option;
    }));
    if (choices.some((choice) => choice.id === previous)) picker.value = previous;
    $("generator").hidden = false;
    $("composer-cancel-actions").hidden = true;
    $("model-picker").focus();
    status("composer-status");
  } catch (error) {
    if (dialog.open && attempt === shell.loadingModels) await showComposerError(error);
  }
}

async function showComposerError(error) {
  if (error.status === 401) {
    try {
      const state = await api("/api/state");
      syncAccount(state);
      if (!state.connected) {
        $("generator").hidden = true;
        $("needs-account").hidden = false;
        $("composer-signin").hidden = false;
        $("composer-cancel-actions").hidden = false;
      }
    } catch {
      // Keep the original error if the local browser session also expired.
    }
  }
  status("composer-status", errorMessage(error), "error");
}

async function generate(event) {
  event.preventDefault();
  if (shell.generating) return;
  const model = $("model-picker").value;
  const note = $("note").value.trim();
  const sourceURL = $("source-url").value.trim();
  const gexMode = $("gex-mode").value;
  if (!model) {
    status("composer-status", "Choisis un modèle ChatGPT.", "error");
    return;
  }
  if (textBytes.encode(note).length > 4000) {
    status("composer-status", "Raccourcis la note avant de générer.", "error");
    return;
  }
  if (sourceURL && (!note || !validHTTPS(sourceURL))) {
    status("composer-status", "Ajoute une note et un lien HTTPS valide pour citer cette source.", "error");
    return;
  }
  const controls = Array.from($("generator").querySelectorAll("button, select, input, textarea"));
  for (const control of controls) control.disabled = true;
  shell.generating = true;
  $("generator").setAttribute("aria-busy", "true");
  status("composer-status", "Collecte des données et rédaction du brief…");
  try {
    const result = await api("/api/generate", "POST", { model, note, sourceURL, gexMode });
    ++shell.loadingBrief;
    ownBrief(result?.id, result?.document);
    $("note").value = "";
    $("source-url").value = "";
    $("composer").close();
    $("brief").focus();
    status("notice", "Brief enregistré.");
    try {
      await refreshArchive();
    } catch (error) {
      status("notice", errorMessage(error), "error");
    }
  } catch (error) {
    await showComposerError(error);
    if (gexMode === "with" && error?.status === 422) void refreshMarketSnapshot().catch(() => {});
  } finally {
    shell.generating = false;
    $("generator").removeAttribute("aria-busy");
    for (const control of controls) control.disabled = false;
  }
}

async function signOut() {
  const button = $("signout-button");
  button.disabled = true;
  status("account-status", "Déconnexion…");
  let error = null;
  try {
    await api("/api/signout", "POST");
  } catch (failure) {
    error = failure;
  }
  try {
    // The local session may already be cleared even if remote revocation failed.
    syncAccount(await api("/api/state"));
  } catch (failure) {
    error ||= failure;
  } finally {
    status("account-status", error ? errorMessage(error) : "Compte déconnecté.", error ? "error" : "info");
    button.disabled = false;
  }
}

async function checkUpdate() {
  const button = $("updates-check");
  const link = $("update-link");
  button.disabled = true;
  link.hidden = true;
  link.removeAttribute("href");
  status("updates-status", "Recherche d’une nouvelle version…");
  try {
    const result = await api("/api/update");
    if (result?.available === false) {
      status("updates-status", "Ta version est à jour.");
    } else {
      const destination = validHTTPS(result?.url);
      if (result?.available !== true || typeof result.tag !== "string" || !/^v?\d+\.\d+\.\d+$/.test(result.tag)
          || destination?.origin !== "https://github.com"
          || destination.pathname !== "/vbmm/brief-studio/releases/tag/" + result.tag) {
        throw new Error("La version proposée n’a pas pu être vérifiée.");
      }
      status("updates-status", "Version " + result.tag + " disponible.");
      link.href = destination.href;
      link.hidden = false;
    }
  } catch (error) {
    status("updates-status", errorMessage(error), "error");
  } finally {
    button.disabled = false;
  }
}

function readSourceSettings(state, provider) {
  const value = state?.[provider];
  if (!value || typeof value.configured !== "boolean" || typeof value.persistent !== "boolean"
      || typeof value.target !== "string" || value.target.length > 256) {
    throw new Error("Les réglages de cette source ne peuvent pas être affichés.");
  }
  const saved = { configured: value.configured, persistent: value.persistent, target: value.target };
  if (provider === "x") saved.birdAvailable = value.birdAvailable === true;
  const snapshot = value.snapshot;
  if (provider === "x" && snapshot != null) {
    if (!Number.isSafeInteger(snapshot.count) || snapshot.count < 0 || typeof snapshot.stale !== "boolean") {
      throw new Error("L’état du fichier X ne peut pas être affiché.");
    }
    saved.snapshot = snapshot;
  }
  return saved;
}

function sourceChanged(provider) {
  const saved = sourceSettings[provider];
  return saved && ($(`${provider}-target`).value.trim() !== saved.target
    || $(`${provider}-persistent`).checked !== saved.persistent || !!$(`${provider}-token`).value.trim());
}

function sourceControls(provider) {
  const saved = sourceSettings[provider];
  const busy = busySources.has(provider);
  const dirty = sourceChanged(provider);
  const snapshot = provider === "x" && saved?.snapshot;
  for (const field of ["target", "token", "persistent"]) $(`${provider}-${field}`).disabled = busy;
  $(`${provider}-save`).disabled = busy;
  $(`${provider}-test`).disabled = busy || Boolean(snapshot) || !saved?.configured || dirty;
  $(`${provider}-remove`).disabled = busy;
  const hint = $(`${provider}-action-hint`);
  hint.textContent = snapshot ? "Le fichier remplace l’API X sans appel facturable."
    : saved?.configured ? "Enregistre tes modifications avant de tester." : "Enregistre la source avant de la tester.";
  hint.hidden = busy || (!snapshot && saved?.configured && !dirty);
  if (provider === "x") {
    $("x-snapshot-pick").disabled = busy;
    $("x-snapshot-remove").disabled = busy;
  }
  $(`source-${provider}`).setAttribute("aria-busy", String(busy));
  $("sources-close").disabled = busySources.size > 0;
  $("x-bird-refresh").disabled = busySources.size > 0;
}

function showSourceSettings(provider, saved) {
  sourceSettings[provider] = saved;
  $(`${provider}-target`).value = saved.target;
  $(`${provider}-persistent`).checked = saved.persistent;
  $(`${provider}-token`).value = "";
  $(`${provider}-token-hint`).textContent = saved.configured
    ? "Laisse ce champ vide pour garder le jeton actuel."
    : "Le jeton reste en mémoire jusqu’à la fermeture du lanceur par défaut.";
  sourceIndicator(provider, saved);
  $(`${provider}-remove`).hidden = !saved.configured;
  if (provider === "x") showXSnapshot(saved);
  sourceControls(provider);
}

function sourceIndicator(provider, saved) {
  const indicator = $(`${provider}-state`);
  const snapshot = provider === "x" && saved.snapshot;
  indicator.dataset.active = String(snapshot ? !snapshot.stale : saved.configured);
  indicator.textContent = snapshot ? "Fichier local"
    : !saved.configured ? "Désactivée"
    : saved.persistent ? "Active · sur cet ordinateur" : "Active · pour cette session";
}

function showXSnapshot(saved) {
  const { snapshot, birdAvailable } = saved;
  $("x-bird-refresh").hidden = !birdAvailable;
  $("x-bird-unavailable").hidden = birdAvailable;
  const summary = $("x-snapshot-summary");
  summary.hidden = !snapshot;
  $("x-snapshot-remove").hidden = !snapshot;
  if (!snapshot) {
    summary.textContent = "";
    return;
  }
  const count = snapshot.count === 1 ? "1 publication" : `${snapshot.count} publications`;
  const date = formatDate(snapshot.latest) || formatDate(snapshot.observedAt);
  summary.dataset.stale = String(snapshot.stale);
  summary.textContent = `${snapshot.stale ? "Fichier périmé" : "Fichier récent"} · ${count}${date ? " · " + date : ""}`;
}

function readMarketSnapshot(response) {
  const snapshot = response?.snapshot;
  if (snapshot === null) return null;
  const gamma = snapshot?.gamma ?? [];
  const levels = snapshot?.levels ?? [];
  const validCoverage = (value) => Array.isArray(value) && value.length <= 2
    && value.every((symbol) => symbol === "NQ" || symbol === "ES");
  if (!snapshot || typeof snapshot.source !== "string" || !snapshot.source.trim()
      || !formatDate(snapshot.observedAt) || typeof snapshot.fresh !== "boolean"
      || !validCoverage(gamma) || !validCoverage(levels)) {
    throw new Error("L’état du fichier de marché ne peut pas être affiché.");
  }
  return { source: snapshot.source, observedAt: snapshot.observedAt, fresh: snapshot.fresh, gamma, levels };
}

function showGexCoverage() {
  const fresh = marketSnapshot?.fresh;
  const mode = $("gex-mode").value;
  const gamma = fresh ? marketSnapshot.gamma : [];
  const levels = fresh ? marketSnapshot.levels : [];
  const guidance = levels.length
    ? `Niveaux importés disponibles pour ${levels.join(" et ")}.`
    : "Les niveaux futures importés sont utilisables quel que soit le mode GEX.";
  let message = guidance;
  if (mode === "public") {
    message = "FlashAlpha est consulté à la génération. Relevé différé de la séance précédente sur NQ et ES, consultable dans Brief Studio.";
    if (levels.length) message += " " + guidance;
  } else if (mode === "with" && gamma.length > 0) {
    const missing = ["NQ", "ES"].filter((symbol) => !gamma.includes(symbol));
    const coverage = missing.length ? `GEX pour ${gamma[0]} seulement. ` : "GEX disponible pour NQ et ES. ";
    message = coverage + guidance;
  } else if (mode === "with") {
    message = marketSnapshot === undefined ? "La disponibilité du GEX sera vérifiée avant la génération."
      : "Aucun GEX récent. Importe un fichier dans Sources ou sélectionne Sans GEX.";
  }
  $("gex-coverage").textContent = message;
}

function scheduleDate(value, timeZone) {
  if (!timeZone) return formatDate(value);
  try {
    const date = new Date(value);
    if (typeof value !== "string" || Number.isNaN(date.getTime())) return "";
    const moment = new Intl.DateTimeFormat("fr-FR", {
      dateStyle: "long", timeStyle: "short", timeZone,
    }).format(date);
    return `${moment} (${timeZone})`;
  } catch {
    return formatDate(value);
  }
}

function readSchedule(value) {
  const config = value?.config;
  if (!config || typeof config.enabled !== "boolean" || typeof config.time !== "string"
      || typeof config.timeZone !== "string" || typeof config.weekdaysOnly !== "boolean"
      || typeof config.model !== "string" || !["", "without", "with", "public"].includes(config.gexMode)
      || typeof value.canEnable !== "boolean" || typeof value.needsConsent !== "boolean") {
    throw new Error("L’état de la planification ne peut pas être affiché.");
  }
  return { config, canEnable: value.canEnable, needsConsent: value.needsConsent, next: value.next, last: value.last };
}

function scheduleControls() {
  const available = scheduleState?.canEnable === true;
  $("schedule-form").hidden = !available;
  $("schedule-fields").disabled = scheduleSaving || !scheduleModelsReady;
  $("schedule-save").hidden = !available;
  $("schedule-save").disabled = scheduleSaving || !scheduleModelsReady;
  $("schedule-pause").hidden = !scheduleState?.config.enabled;
  $("schedule-pause").disabled = scheduleSaving;
  $("schedule-close").disabled = scheduleSaving;
  $("schedule").setAttribute("aria-busy", String(scheduleSaving));
}

function showSchedule(next) {
  scheduleState = next;
  const { config, canEnable, needsConsent, last } = next;
  $("schedule-summary").hidden = false;
  $("schedule-state").textContent = config.enabled
    ? needsConsent ? "Planification suspendue. Autorisation ChatGPT à renouveler."
      : "Planification active"
    : "Planification en pause";
  if (!canEnable) $("schedule-state").textContent += " · Connexion ChatGPT éligible requise pour l’activer.";
  const nextDate = scheduleDate(next.next, config.timeZone);
  $("schedule-next").textContent = nextDate ? `Prochain brief · ${nextDate}` : "";
  $("schedule-next").hidden = !nextDate;
  const lastDate = scheduleDate(last?.at, config.timeZone);
  const labels = {
    running: "En cours", success: "Brief enregistré", failed: "Échec", blocked: "Bloqué",
    interrupted: "Interrompu", cancelled: "Annulé", missed: "Horaire manqué",
  };
  const outcome = Object.hasOwn(labels, last?.status) ? labels[last.status] : "";
  const message = typeof last?.message === "string" ? last.message.trim().slice(0, 600) : "";
  $("schedule-last").textContent = lastDate
    ? ["Dernière tentative", lastDate, outcome, message].filter(Boolean).join(" · ") : "";
  $("schedule-last").hidden = !lastDate;
  scheduleBriefId = typeof last?.briefId === "string" && /^[a-f0-9]{32}$/.test(last.briefId) ? last.briefId : "";
  $("schedule-last-brief").hidden = !scheduleBriefId;
  $("schedule-time").value = config.time || "13:30";
  $("schedule-zone").value = config.timeZone || Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  $("schedule-days").value = config.weekdaysOnly ? "weekdays" : "daily";
  $("schedule-gex").value = config.gexMode || "without";
  $("schedule-consent").checked = false;
  if (scheduleModelsReady && config.model) $("schedule-model").value = config.model;
  scheduleControls();
}

async function openSchedule() {
  const visit = ++scheduleVisit;
  const dialog = $("schedule");
  scheduleState = undefined;
  scheduleModelsReady = false;
  scheduleBriefId = "";
  $("schedule-summary").hidden = true;
  $("schedule-consent").checked = false;
  scheduleControls();
  status("schedule-status", "Chargement de la planification…");
  dialog.showModal();
  let current;
  try {
    current = readSchedule(await api("/api/schedule"));
    if (!dialog.open || visit !== scheduleVisit) return;
    showSchedule(current);
    if (!current.canEnable) {
      status("schedule-status");
      return;
    }
    status("schedule-status", "Chargement des modèles…");
    const models = await api("/api/models");
    if (!dialog.open || visit !== scheduleVisit || !scheduleState?.canEnable) return;
    const choices = Array.isArray(models) ? models.filter(
      (model) => typeof model?.id === "string" && model.id.trim() && model.id.length < 160
        && typeof model.displayName === "string" && model.displayName.trim()
    ) : [];
    if (!choices.length) throw new Error("Aucun modèle disponible pour ce compte ChatGPT.");
    const picker = $("schedule-model");
    const model = scheduleState.config.model;
    const priorModelAvailable = !model || choices.some((choice) => choice.id === model);
    const options = choices.map((choice) => {
      const option = node("option", choice.displayName.slice(0, 150));
      option.value = choice.id;
      return option;
    });
    if (!priorModelAvailable) {
      const placeholder = node("option", "Choisir un modèle");
      placeholder.value = "";
      options.unshift(placeholder);
    }
    picker.replaceChildren(...options);
    picker.value = priorModelAvailable ? model || choices[0].id : "";
    scheduleModelsReady = true;
    scheduleControls();
    if (!scheduleSaving && current === scheduleState) {
      status("schedule-status", priorModelAvailable ? "" : "Le modèle enregistré est indisponible. Choisis un modèle pour réactiver ce programme.", priorModelAvailable ? "info" : "error");
    }
  } catch (error) {
    if (dialog.open && visit === scheduleVisit && !scheduleSaving && current === scheduleState) {
      status("schedule-status", errorMessage(error), "error");
    }
  }
}

async function changeSchedule(enabled) {
  if (scheduleSaving || !scheduleState || (enabled && (!scheduleState.canEnable || !scheduleModelsReady))) return;
  const body = { enabled };
  if (enabled) {
    body.time = $("schedule-time").value;
    body.timeZone = $("schedule-zone").value.trim();
    body.weekdaysOnly = $("schedule-days").value === "weekdays";
    body.model = $("schedule-model").value;
    body.gexMode = $("schedule-gex").value;
    body.consent = $("schedule-consent").checked;
    if (!body.consent) {
      status("schedule-status", "Coche l’autorisation de génération récurrente pour activer ce programme.", "error");
      return;
    }
    if (!/^([01][0-9]|2[0-3]):[0-5][0-9]$/.test(body.time) || !body.model || !body.timeZone) {
      status("schedule-status", "Choisis une heure, un fuseau horaire et un modèle.", "error");
      return;
    }
    $("schedule-consent").checked = false;
  }
  const visit = scheduleVisit;
  scheduleSaving = true;
  scheduleControls();
  status("schedule-status", enabled ? "Activation…" : "Mise en pause…");
  try {
    const current = readSchedule(await api("/api/schedule", "POST", body));
    if (!$("schedule").open || visit !== scheduleVisit) return;
    showSchedule(current);
    status("schedule-status", enabled ? "Programme enregistré." : "Planification mise en pause.");
  } catch (error) {
    if ($("schedule").open && visit === scheduleVisit) status("schedule-status", errorMessage(error), "error");
  } finally {
    scheduleSaving = false;
    scheduleControls();
  }
}

function marketControls() {
  const busy = busySources.has("market");
  $("market-import").disabled = busy;
  $("market-remove").disabled = busy;
  $("market-test-public").disabled = busySources.has("market-public");
  $("source-market").setAttribute("aria-busy", String(busy));
  $("sources-close").disabled = busySources.size > 0;
  $("x-bird-refresh").disabled = busySources.size > 0;
}

function showMarketSnapshot(snapshot) {
  marketSnapshot = snapshot;
  const indicator = $("market-state");
  indicator.dataset.active = String(snapshot?.fresh === true);
  indicator.textContent = !snapshot ? "Aucun fichier" : snapshot.fresh ? "Fichier récent" : "Fichier périmé";
  const summary = $("market-summary");
  summary.hidden = !snapshot;
  summary.textContent = snapshot
    ? `${snapshot.source} · ${formatDate(snapshot.observedAt)} · GEX ${snapshot.gamma.join(" / ") || "absent"} · Niveaux ${snapshot.levels.join(" / ") || "absents"}`
    : "";
  summary.dataset.stale = String(snapshot?.fresh === false);
  $("market-import").textContent = snapshot ? "Remplacer le fichier" : "Importer un fichier";
  $("market-remove").hidden = !snapshot;
  showGexCoverage();
}

async function refreshMarketSnapshot() {
  const attempt = ++marketRead;
  try {
    const snapshot = readMarketSnapshot(await api(marketPath));
    if (attempt === marketRead) showMarketSnapshot(snapshot);
  } catch (error) {
    if (attempt === marketRead) throw error;
  }
}

async function changeMarketSnapshot(file) {
  if (busySources.has("market")) return;
  const invalidFile = "Choisis un fichier JSON au format du modèle et de 16 Ko maximum.";
  if (file && (!/\.json$/i.test(file.name) || file.size === 0 || file.size > marketSnapshotLimit)) {
    status("market-status", invalidFile, "error");
    return;
  }
  ++marketRead;
  busySources.add("market");
  marketControls();
  status("market-status", file ? "Importation du fichier…" : "Retrait du fichier…");
  try {
    const body = file ? { action: "import", snapshot: JSON.parse(await file.text()) } : { action: "remove" };
    if (file && (!body.snapshot || Array.isArray(body.snapshot) || typeof body.snapshot !== "object"
        || textBytes.encode(JSON.stringify(body)).length > marketSnapshotLimit)) {
      throw new SyntaxError(invalidFile);
    }
    const snapshot = readMarketSnapshot(await api(marketPath, "POST", body));
    showMarketSnapshot(snapshot);
    status("market-status", file ? "Fichier de marché importé." : "Fichier de marché retiré.");
  } catch (error) {
    const message = error instanceof SyntaxError ? invalidFile : errorMessage(error);
    status("market-status", message, "error");
  } finally {
    busySources.delete("market");
    marketControls();
    $("market-import").focus();
  }
}

async function testPublicGex() {
  if (busySources.has("market-public")) return;
  busySources.add("market-public");
  marketControls();
  status("market-public-status", "Vérification du relevé FlashAlpha…");
  try {
    const snapshot = readMarketSnapshot(await api(marketPath + "/public/test", "POST", {}));
    if (!snapshot?.gamma.length) throw new Error("Aucun relevé GEX public trouvé.");
    const date = formatDate(snapshot.observedAt);
    const detail = `${snapshot.source} · ${date} · ${snapshot.gamma.join(" / ")}`;
    status("market-public-status", snapshot.fresh ? detail : `Relevé périmé · ${detail}`, snapshot.fresh ? "info" : "empty");
  } catch (error) {
    status("market-public-status", errorMessage(error), "error");
  } finally {
    busySources.delete("market-public");
    marketControls();
    $("market-test-public").focus();
  }
}

async function openSources() {
  const attempt = ++sourceRead;
  $("source-grid").hidden = true;
  for (const provider of sourceProviders) {
    $(`${provider}-token`).value = "";
    status(`${provider}-status`);
  }
  $("x-snapshot-file").value = "";
  $("market-file").value = "";
  status("x-snapshot-status");
  status("market-status");
  status("market-public-status");
  status("sources-status", "Chargement des sources…");
  $("sources").showModal();
  void refreshMarketSnapshot().catch((error) => {
    if ($("sources").open && attempt === sourceRead) {
      if (error?.status === 422) {
        // A corrupt stored file can still be removed through the same action.
        showMarketSnapshot(null);
        $("market-state").textContent = "Fichier invalide";
        $("market-remove").hidden = false;
      } else if (marketSnapshot === undefined) {
        $("market-state").textContent = "État indisponible";
      }
      status("market-status", errorMessage(error), "error");
    }
  });
  try {
    const state = await api("/api/sources");
    const values = sourceProviders.map((provider) => readSourceSettings(state, provider));
    if (!$("sources").open || attempt !== sourceRead) return;
    sourceProviders.forEach((provider, index) => showSourceSettings(provider, values[index]));
    $("source-grid").hidden = false;
    status("sources-status");
  } catch (error) {
    if ($("sources").open && attempt === sourceRead) {
      const message = error?.status === 401
        ? "Session locale expirée. Rouvre Brief Studio depuis le lanceur."
        : "Impossible de charger les sources. Ferme cette fenêtre et réessaie.";
      status("sources-status", message, "error");
    }
  }
}

function sourceFailure(error) {
  // Provider diagnostics stay out of the page in case they contain credentials.
  switch (error?.status) {
    case 0: return "Impossible de joindre Brief Studio. Vérifie que le lanceur est ouvert.";
    case 400: return "Vérifie les paramètres saisis et le format du jeton.";
    case 401: return "Jeton refusé : vérifie ou remplace le jeton de la source.";
    case 402: return "Quota ou crédit d’API épuisé chez le fournisseur.";
    case 403: return "Accès refusé : vérifie les permissions du jeton ou du salon.";
    case 409: return error.message === "Une opération est déjà en cours."
      ? "Une autre opération est en cours. Réessaie dans un instant."
      : "Cette source doit être configurée et active avant le test.";
    case 429: return "Limite de requêtes du fournisseur atteinte. Réessaie plus tard.";
    case 504: return "Le fournisseur a mis trop de temps à répondre. Réessaie plus tard.";
    default: return "La source est momentanément indisponible. Réessaie plus tard.";
  }
}

async function saveSource(provider, remove = false) {
  if (!sourceSettings[provider] || busySources.has(provider)) return;
  const tokenField = $(`${provider}-token`);
  const body = remove ? { target: "", token: "", persistent: false, remove: true }
    : { target: $(`${provider}-target`).value.trim(), token: tokenField.value.trim(),
        persistent: $(`${provider}-persistent`).checked };
  if (!remove && !sourceSettings[provider].configured && !body.token) {
    status(`${provider}-status`, "Saisis un jeton pour activer cette source.", "error");
    tokenField.focus();
    return;
  }
  tokenField.value = "";
  busySources.add(provider);
  sourceControls(provider);
  status(`${provider}-status`, remove ? "Déconnexion…" : "Enregistrement…");
  try {
    const updated = await api(`/api/sources/${provider}`, "POST", body);
    showSourceSettings(provider, readSourceSettings(updated, provider));
    status(`${provider}-status`, remove ? "Source déconnectée. Jeton effacé." : "Source enregistrée. Tu peux vérifier l’accès avec Tester.");
  } catch (error) {
    status(`${provider}-status`, sourceFailure(error), "error");
  } finally {
    busySources.delete(provider);
    sourceControls(provider);
    (remove && $(`${provider}-remove`).hidden ? $(`${provider}-target`) : $(`${provider}-save`)).focus();
  }
}

async function testSource(provider) {
  if (!sourceSettings[provider]?.configured || sourceSettings[provider].snapshot
      || busySources.has(provider) || sourceChanged(provider)) return;
  busySources.add(provider);
  sourceControls(provider);
  status(`${provider}-status`, "Vérification de la source enregistrée…");
  try {
    const result = await api(`/api/sources/${provider}/test`, "POST", {});
    if (!Number.isSafeInteger(result?.count) || result.count < 0) throw new Error("Réponse invalide.");
    if (result.count === 0) {
      const hint = provider === "discord" ? " Vérifie l’accès à l’historique du salon si tu attends des messages." : "";
      status(`${provider}-status`, `Aucun contenu récent trouvé.${hint}`, "empty");
      return;
    }
    const kind = provider === "discord"
      ? (result.count === 1 ? "message récent" : "messages récents")
      : (result.count === 1 ? "publication récente" : "publications récentes");
    const latest = formatDate(result.latest);
    status(`${provider}-status`, `Accès vérifié. ${result.count} ${kind}.${latest ? " Dernière observation le " + latest + "." : ""}`);
  } catch (error) {
    status(`${provider}-status`, sourceFailure(error), "error");
  } finally {
    busySources.delete(provider);
    sourceControls(provider);
    $(`${provider}-test`).focus();
  }
}

async function changeXSnapshot({ file, useBird = false } = {}) {
  if (!sourceSettings.x || busySources.has("x")) return;
  if (useBird && (!sourceSettings.x.birdAvailable || busySources.size > 0)) return;
  if (file && (!/\.json$/i.test(file.name) || file.size === 0 || file.size > xSnapshotLimit)) {
    status("x-snapshot-status", "Choisis un fichier JSON non vide de 16 Ko maximum.", "error");
    return;
  }
  busySources.add("x");
  sourceControls("x");
  status("x-snapshot-status", useBird ? "Actualisation avec Bird…"
    : file ? "Importation du fichier X…" : "Retrait du fichier X…");
  try {
    const state = useBird ? await api("/api/sources/x/bird", "POST", {})
      : await api("/api/sources/x/snapshot", "POST", file ?? { remove: true });
    const saved = readSourceSettings(state, "x");
    // Updating the snapshot does not overwrite unfinished API edits.
    sourceSettings.x.snapshot = saved.snapshot;
    sourceSettings.x.birdAvailable = saved.birdAvailable;
    sourceIndicator("x", sourceSettings.x);
    showXSnapshot(sourceSettings.x);
    status("x-snapshot-status", useBird ? "Publications X actualisées avec Bird." : file ? "Fichier X importé."
      : saved.configured ? "Fichier retiré. L’API X est de nouveau active." : "Fichier X retiré.");
  } catch (error) {
    let message = error?.status === 400 || error?.status === 413 || error?.status === 422
      ? "Ce fichier ne correspond pas au format d’import X."
      : "La modification du fichier X a échoué. Réessaie.";
    if (useBird) {
      message = error?.status === 409 ? "Une autre opération est en cours. Réessaie dans un instant."
        : error?.status === 503 ? "Connecteur Bird local indisponible. Tu peux importer un fichier JSON."
        : "Bird n’a pas pu actualiser les publications. Vérifie la connexion X du connecteur, puis réessaie.";
    }
    status("x-snapshot-status", message, "error");
  } finally {
    busySources.delete("x");
    sourceControls("x");
    $(useBird && sourceSettings.x.birdAvailable ? "x-bird-refresh" : "x-snapshot-pick").focus();
  }
}

$("sample-button").dataset.id = "sample";
$("sample-button").addEventListener("click", () => { void selectBrief("sample"); });
$("retry-button").addEventListener("click", () => location.reload());
$("new-button").addEventListener("click", () => { void openComposer(); });
$("composer-dismiss").addEventListener("click", () => $("composer").close());
$("composer-close").addEventListener("click", () => $("composer").close());
$("composer").addEventListener("cancel", (event) => { if (shell.generating) event.preventDefault(); });
$("generator").addEventListener("submit", (event) => { void generate(event); });
$("gex-mode").addEventListener("change", showGexCoverage);
$("account-button").addEventListener("click", () => {
  status("account-status");
  $("account").showModal();
});
$("account-close").addEventListener("click", () => $("account").close());
$("signout-button").addEventListener("click", () => { void signOut(); });
$("updates-button").addEventListener("click", () => {
  $("updates-version").textContent = "Version installée " + (shell.account?.version || "inconnue");
  $("updates").showModal();
});
$("updates-close").addEventListener("click", () => $("updates").close());
$("updates-check").addEventListener("click", () => { void checkUpdate(); });
$("sources-button").addEventListener("click", () => { void openSources(); });
$("schedule-button").addEventListener("click", () => { void openSchedule(); });
$("schedule-close").addEventListener("click", () => $("schedule").close());
$("schedule").addEventListener("cancel", (event) => { if (scheduleSaving) event.preventDefault(); });
$("schedule-form").addEventListener("submit", (event) => {
  event.preventDefault();
  void changeSchedule(true);
});
$("schedule-pause").addEventListener("click", () => { void changeSchedule(false); });
$("schedule-last-brief").addEventListener("click", () => {
  if (!scheduleBriefId || scheduleSaving) return;
  const id = scheduleBriefId;
  $("schedule").close();
  void refreshArchive().catch(() => {});
  void selectBrief(id);
});
if (typeof Intl.supportedValuesOf === "function") {
  $("schedule-zones").replaceChildren(...["UTC", ...Intl.supportedValuesOf("timeZone")].map((zone) => {
    const option = node("option");
    option.value = zone;
    return option;
  }));
}
function forgetSourceTokens() {
  for (const provider of sourceProviders) $(`${provider}-token`).value = "";
  $("x-snapshot-file").value = "";
  $("market-file").value = "";
}
$("sources-close").addEventListener("click", () => {
  forgetSourceTokens();
  $("sources").close();
});
$("sources").addEventListener("cancel", (event) => {
  if (busySources.size) event.preventDefault();
  else forgetSourceTokens();
});
window.addEventListener("pagehide", forgetSourceTokens);
for (const provider of sourceProviders) {
  $(`source-${provider}`).addEventListener("input", () => {
    sourceControls(provider);
    status(`${provider}-status`);
  });
  $(`source-${provider}`).addEventListener("submit", (event) => {
    event.preventDefault();
    void saveSource(provider);
  });
  $(`${provider}-test`).addEventListener("click", () => { void testSource(provider); });
  $(`${provider}-remove`).addEventListener("click", () => { void saveSource(provider, true); });
}
$("x-snapshot-pick").addEventListener("click", () => $("x-snapshot-file").click());
$("x-bird-refresh").addEventListener("click", () => { void changeXSnapshot({ useBird: true }); });
$("x-snapshot-file").addEventListener("change", (event) => {
  const file = event.target.files[0];
  event.target.value = "";
  if (file) void changeXSnapshot({ file });
});
$("x-snapshot-remove").addEventListener("click", () => { void changeXSnapshot(); });
$("market-import").addEventListener("click", () => $("market-file").click());
$("market-file").addEventListener("change", (event) => {
  const file = event.target.files[0];
  event.target.value = "";
  if (file) void changeMarketSnapshot(file);
});
$("market-remove").addEventListener("click", () => { void changeMarketSnapshot(); });
$("market-test-public").addEventListener("click", () => { void testPublicGex(); });

void start();
