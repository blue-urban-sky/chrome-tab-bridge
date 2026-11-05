const HOST = "local.chrome_tab_bridge";
const PARK_ROOT = "Parked tabs";
const MEET_CALL = /^https:\/\/meet\.google\.com\/[a-z]{3}-[a-z]{4}-[a-z]{3}/;

let port = null;

async function profile() {
  const stored = await chrome.storage.local.get(["profileId", "label"]);
  let profileId = stored.profileId;
  if (!profileId) {
    profileId = crypto.randomUUID().slice(0, 8);
    await chrome.storage.local.set({ profileId });
  }
  let account = {};
  try {
    account = await chrome.identity.getProfileUserInfo({ accountStatus: "ANY" });
  } catch {}
  return {
    profileId,
    label: stored.label || null,
    email: account.email || "",
    gaiaId: account.id || "",
    extensionId: chrome.runtime.id,
  };
}

async function updateBadge() {
  const p = await profile();
  const name = p.label || p.profileId;
  await chrome.action.setBadgeText({ text: name.slice(0, 4) });
  await chrome.action.setBadgeBackgroundColor({ color: port ? "#1a73e8" : "#d93025" });
  await chrome.action.setTitle({ title: `Tab bridge: ${name}, ${port ? "connected" : "not connected"}` });
}

function bookmarkInfo(n) {
  return {
    id: n.id,
    parentId: n.parentId ?? "",
    index: n.index ?? 0,
    title: n.title || "",
    url: n.url || "",
    folder: !n.url,
    dateAdded: n.dateAdded ?? 0,
    dateLastUsed: n.dateLastUsed ?? 0,
    folderType: n.folderType ?? "",
    syncing: n.syncing ?? null,
    unmodifiable: n.unmodifiable ?? "",
  };
}

const hostOf = (url) => {
  try {
    return new URL(url).hostname;
  } catch {
    return "";
  }
};

const byHostThenTitle = (a, b) =>
  hostOf(a.url).localeCompare(hostOf(b.url)) || (a.title || "").localeCompare(b.title || "");

function today() {
  const d = new Date();
  return [d.getFullYear(), d.getMonth() + 1, d.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
}

function tabInfo(t) {
  return {
    id: t.id,
    windowId: t.windowId,
    index: t.index,
    groupId: t.groupId,
    pinned: t.pinned,
    active: t.active,
    audible: !!t.audible,
    discarded: t.discarded,
    lastAccessed: t.lastAccessed ?? null,
    openerTabId: t.openerTabId ?? null,
    url: t.url || t.pendingUrl || "",
    title: t.title || "",
  };
}

function groupInfo(g) {
  return { id: g.id, windowId: g.windowId, title: g.title || "", color: g.color, collapsed: g.collapsed };
}

function risks(t) {
  const reasons = [];
  if (t.pinned) reasons.push("pinned");
  if (t.audible) reasons.push("playing audio");
  if (MEET_CALL.test(t.url || "")) reasons.push("meet call");
  return reasons;
}

async function screen(ids, force) {
  const allowed = [];
  const refused = [];
  const missing = [];
  for (const id of ids) {
    let t;
    try {
      t = await chrome.tabs.get(id);
    } catch {
      missing.push(id);
      continue;
    }
    const reasons = risks(t);
    if (reasons.length && !force) refused.push({ id, reasons });
    else allowed.push(t);
  }
  return { allowed, refused, missing };
}

async function parkFolder(name) {
  const [root] = await chrome.bookmarks.getTree();
  const other = root.children.find((n) => n.folderType === "other") || root.children[1];
  const parent =
    (await chrome.bookmarks.getChildren(other.id)).find((n) => !n.url && n.title === PARK_ROOT) ||
    (await chrome.bookmarks.create({ parentId: other.id, title: PARK_ROOT }));
  const title = `${today()} ${name || ""}`.trim();
  return (
    (await chrome.bookmarks.getChildren(parent.id)).find((n) => !n.url && n.title === title) ||
    (await chrome.bookmarks.create({ parentId: parent.id, title }))
  );
}

const COMMANDS = {
  async info() {
    return { ...(await profile()), version: chrome.runtime.getManifest().version };
  },

  async label({ label }) {
    await chrome.storage.local.set({ label });
    await updateBadge();
    return profile();
  },

  async reload() {
    setTimeout(() => chrome.runtime.reload(), 200);
    return { version: chrome.runtime.getManifest().version };
  },

  async focus({ windowId }) {
    await chrome.windows.update(windowId, { focused: true, drawAttention: true });
    return { windowId };
  },

  async bookmarks() {
    const [root] = await chrome.bookmarks.getTree();
    const out = [];
    const walk = (n) => {
      out.push(bookmarkInfo(n));
      for (const c of n.children || []) walk(c);
    };
    walk(root);
    return out;
  },

  async bookmarkMove({ ids, parentId, index }) {
    const moved = [];
    for (const id of ids) {
      const props = { parentId };
      if (index != null && index >= 0) props.index = index + moved.length;
      await chrome.bookmarks.move(id, props);
      moved.push(id);
    }
    return { moved };
  },

  async bookmarkRemove({ ids, force }) {
    const removed = [];
    const refused = [];
    const failed = [];
    for (const id of ids) {
      try {
        const [node] = await chrome.bookmarks.getSubTree(id);
        if (!node.url && node.children?.length && !force) {
          refused.push({ id, reason: `folder with ${node.children.length} items` });
          continue;
        }
        if (node.url) await chrome.bookmarks.remove(id);
        else await chrome.bookmarks.removeTree(id);
        removed.push(id);
      } catch (e) {
        failed.push({ id, reason: e.message });
      }
    }
    return { removed, refused, failed };
  },

  async bookmarkCreate({ parentId, title, url }) {
    const props = { parentId, title };
    if (url) props.url = url;
    return bookmarkInfo(await chrome.bookmarks.create(props));
  },

  async bookmarkRename({ id, title }) {
    return bookmarkInfo(await chrome.bookmarks.update(id, { title }));
  },

  async list() {
    const [tabs, groups, windows] = await Promise.all([
      chrome.tabs.query({}),
      chrome.tabGroups.query({}),
      chrome.windows.getAll(),
    ]);
    return {
      profile: await profile(),
      windows: windows.map((w) => ({
        id: w.id,
        focused: w.focused,
        incognito: w.incognito,
        type: w.type,
        state: w.state,
        left: w.left,
        top: w.top,
        width: w.width,
        height: w.height,
      })),
      groups: groups.map(groupInfo),
      tabs: tabs.map(tabInfo),
    };
  },

  async open({ url, windowId, index }) {
    const props = { url, active: false };
    if (windowId != null) props.windowId = windowId;
    if (index != null) props.index = index;
    return tabInfo(await chrome.tabs.create(props));
  },

  async close({ ids, force }) {
    const { allowed, refused, missing } = await screen(ids, force);
    if (allowed.length) await chrome.tabs.remove(allowed.map((t) => t.id));
    return { closed: allowed.map((t) => t.id), refused, missing };
  },

  async move({ ids, windowId, index }) {
    const props = { index: index ?? -1 };
    if (windowId != null) props.windowId = windowId;
    const moved = await chrome.tabs.move(ids, props);
    return { moved: [].concat(moved).map(tabInfo) };
  },

  async group({ ids, groupId, title, color }) {
    const first = await chrome.tabs.get(ids[0]);
    const gid = await chrome.tabs.group(
      groupId != null ? { tabIds: ids, groupId } : { tabIds: ids, createProperties: { windowId: first.windowId } },
    );
    const update = {};
    if (title != null) update.title = title;
    if (color != null) update.color = color;
    const g = Object.keys(update).length ? await chrome.tabGroups.update(gid, update) : await chrome.tabGroups.get(gid);
    return groupInfo(g);
  },

  async updateGroup({ groupId, title, color, collapsed }) {
    const update = {};
    if (title != null) update.title = title;
    if (color != null) update.color = color;
    if (collapsed != null) update.collapsed = collapsed;
    return groupInfo(await chrome.tabGroups.update(groupId, update));
  },

  async ungroup({ ids }) {
    await chrome.tabs.ungroup(ids);
    return { ungrouped: ids };
  },

  async sort({ windowId, groupOrder }) {
    const tabs = await chrome.tabs.query({ windowId });
    const present = [
      ...new Set(
        tabs
          .filter((t) => t.groupId !== -1)
          .sort((a, b) => a.index - b.index)
          .map((t) => t.groupId),
      ),
    ];
    const order = groupOrder
      ? [...groupOrder.filter((g) => present.includes(g)), ...present.filter((g) => !groupOrder.includes(g))]
      : present;
    for (const gid of order) await chrome.tabGroups.move(gid, { index: -1 });
    const loose = tabs.filter((t) => !t.pinned && t.groupId === -1).sort(byHostThenTitle);
    for (const t of loose) {
      const moved = await chrome.tabs.move(t.id, { index: -1 });
      if (moved.groupId !== -1) await chrome.tabs.ungroup(t.id);
    }
    for (const gid of order) {
      const members = (await chrome.tabs.query({ windowId, groupId: gid })).sort((a, b) => a.index - b.index);
      const start = members[0].index;
      const sorted = [...members].sort(byHostThenTitle);
      for (let k = 0; k < sorted.length; k++) await chrome.tabs.move(sorted[k].id, { index: start + k });
    }
    return { windowId, groups: order.length, loose: loose.length };
  },

  async pin({ ids, pinned }) {
    for (const id of ids) await chrome.tabs.update(id, { pinned });
    return { ids, pinned };
  },

  async discard({ ids, force }) {
    const { allowed, refused, missing } = await screen(ids, force);
    const discarded = [];
    const failed = [];
    for (const t of allowed) {
      if (t.discarded) {
        discarded.push({ id: t.id, newId: t.id });
        continue;
      }
      if (t.active) {
        failed.push({ id: t.id, reason: "active tab" });
        continue;
      }
      try {
        const d = await chrome.tabs.discard(t.id);
        if (d) discarded.push({ id: t.id, newId: d.id });
        else failed.push({ id: t.id, reason: "chrome declined" });
      } catch (e) {
        failed.push({ id: t.id, reason: e.message });
      }
    }
    return { discarded, failed, refused, missing };
  },

  async park({ ids, name, force }) {
    const { allowed, refused, missing } = await screen(ids, force);
    if (!allowed.length) return { folder: null, parked: [], refused, missing };
    allowed.sort((a, b) => a.windowId - b.windowId || a.index - b.index);
    const folder = await parkFolder(name);
    let saved = 0;
    for (const t of allowed) {
      await chrome.bookmarks.create({ parentId: folder.id, title: t.title || t.url, url: t.url });
      saved++;
    }
    if (saved !== allowed.length) throw new Error(`saved ${saved} of ${allowed.length} bookmarks, closed nothing`);
    await chrome.tabs.remove(allowed.map((t) => t.id));
    return { folder: `${PARK_ROOT}/${folder.title}`, parked: allowed.map((t) => t.id), refused, missing };
  },

  async closed({ max }) {
    const entries = await chrome.sessions.getRecentlyClosed({ maxResults: Math.min(max || 25, 25) });
    return entries.map((e) => {
      if (e.tab) return { kind: "tab", closedAt: e.lastModified, sessionId: e.tab.sessionId, url: e.tab.url || "", title: e.tab.title || "" };
      if (e.window) return { kind: "window", closedAt: e.lastModified, sessionId: e.window.sessionId, url: "", title: `${e.window.tabs.length} tabs` };
      if (e.group) return { kind: "group", closedAt: e.lastModified, sessionId: e.group.sessionId, url: "", title: e.group.title || "" };
      return { kind: "unknown", closedAt: e.lastModified, sessionId: null, url: "", title: "" };
    });
  },

  async restore({ sessionId }) {
    const s = await chrome.sessions.restore(sessionId);
    if (s.tab) return { kind: "tab", tab: tabInfo(s.tab) };
    if (s.window) return { kind: "window", windowId: s.window.id };
    return { kind: "other" };
  },
};

async function handle(msg) {
  let reply;
  try {
    const command = COMMANDS[msg.cmd];
    if (!command) throw new Error(`unknown command: ${msg.cmd}`);
    reply = { id: msg.id, ok: true, result: await command(msg.args || {}) };
  } catch (e) {
    reply = { id: msg.id, ok: false, error: e?.message || String(e) };
  }
  port?.postMessage(reply);
}

function connect() {
  if (port) return;
  port = chrome.runtime.connectNative(HOST);
  port.onMessage.addListener(handle);
  port.onDisconnect.addListener(() => {
    console.warn("tab bridge disconnected:", chrome.runtime.lastError?.message);
    port = null;
    updateBadge();
  });
  updateBadge();
}

chrome.alarms.create("reconnect", { periodInMinutes: 1 });
chrome.alarms.onAlarm.addListener(connect);
chrome.runtime.onStartup.addListener(connect);
connect();
