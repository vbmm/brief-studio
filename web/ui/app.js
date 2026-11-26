"use strict";

const $ = (id) => document.getElementById(id);
const shell = { account: null, selected: null, loadingBrief: 0, loadingModels: 0, generating: false };
const labelDate = new Intl.DateTimeFormat("fr-FR", { dateStyle: "long", timeStyle: "short" });
const textBytes = new TextEncoder();
const idPattern = /^[A-Za-z0-9_-]{1,128}$/;
const sourceProviders = ["discord", "x"];
const sourceSettings = {};
const busySources = new Set();
let sourceRead = 0;

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
      body: body === undefined ? undefined : JSON.stringify(body),
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
  const lines = brief.text.replace(/\r\n?/g, "\n").trimStart().split("\n");
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
    : "Connecte ton compte pour rédiger des briefs avec ton forfait ChatGPT.";
  $("account-signin").hidden = state.connected === true;
  $("signout-button").hidden = state.connected !== true;
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
    for (const id of ["account-button", "new-button", "sample-button", "updates-button", "sources-button"]) $(id).disabled = false;
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
    if (dialog.open && attempt === shell.loadingModels) status("composer-status", errorMessage(error), "error");
  }
}

async function generate(event) {
  event.preventDefault();
  if (shell.generating) return;
  const model = $("model-picker").value;
  const note = $("note").value.trim();
  const sourceURL = $("source-url").value.trim();
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
    const result = await api("/api/generate", "POST", { model, note, sourceURL });
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
    status("composer-status", errorMessage(error), "error");
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
  return { configured: value.configured, persistent: value.persistent, target: value.target };
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
  for (const field of ["target", "token", "persistent"]) $(`${provider}-${field}`).disabled = busy;
  $(`${provider}-save`).disabled = busy;
  $(`${provider}-test`).disabled = busy || !saved?.configured || dirty;
  $(`${provider}-remove`).disabled = busy;
  const hint = $(`${provider}-action-hint`);
  hint.textContent = saved?.configured ? "Enregistre tes modifications avant de tester." : "Enregistre la source avant de la tester.";
  hint.hidden = busy || (saved?.configured && !dirty);
  $(`source-${provider}`).setAttribute("aria-busy", String(busy));
  $("sources-close").disabled = busySources.size > 0;
}

function showSourceSettings(provider, saved) {
  sourceSettings[provider] = saved;
  $(`${provider}-target`).value = saved.target;
  $(`${provider}-persistent`).checked = saved.persistent;
  $(`${provider}-token`).value = "";
  $(`${provider}-token-hint`).textContent = saved.configured
    ? "Laisse ce champ vide pour garder le jeton actuel."
    : "Le jeton reste en mémoire jusqu’à la fermeture du lanceur par défaut.";
  const indicator = $(`${provider}-state`);
  indicator.dataset.active = String(saved.configured);
  indicator.textContent = !saved.configured ? "Désactivée"
    : saved.persistent ? "Active · sur cet ordinateur" : "Active · pour cette session";
  $(`${provider}-remove`).hidden = !saved.configured;
  sourceControls(provider);
}

async function openSources() {
  const attempt = ++sourceRead;
  $("source-grid").hidden = true;
  for (const provider of sourceProviders) {
    $(`${provider}-token`).value = "";
    status(`${provider}-status`);
  }
  status("sources-status", "Chargement des sources…");
  $("sources").showModal();
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
  if (!sourceSettings[provider]?.configured || busySources.has(provider) || sourceChanged(provider)) return;
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

$("sample-button").dataset.id = "sample";
$("sample-button").addEventListener("click", () => { void selectBrief("sample"); });
$("retry-button").addEventListener("click", () => location.reload());
$("new-button").addEventListener("click", () => { void openComposer(); });
$("composer-dismiss").addEventListener("click", () => $("composer").close());
$("composer-close").addEventListener("click", () => $("composer").close());
$("composer").addEventListener("cancel", (event) => { if (shell.generating) event.preventDefault(); });
$("generator").addEventListener("submit", (event) => { void generate(event); });
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
function forgetSourceTokens() {
  for (const provider of sourceProviders) $(`${provider}-token`).value = "";
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

void start();
