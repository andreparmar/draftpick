// Every time an ESPN fantasy page loads (or the URL changes inside it), grab
// the league id from the URL + the espn_s2 / SWID cookies and send them to
// draftpick running on this Mac.
const DRAFTPICK = "http://127.0.0.1:8765/api/config";

chrome.tabs.onUpdated.addListener((_tabId, info, tab) => {
  if ((info.status === "complete" || info.url) && tab.url) capture(tab.url);
});

chrome.runtime.onMessage.addListener((msg, _sender, reply) => {
  if (msg && msg.type === "draftws") {
    queue.push({ url: msg.url, data: msg.data });
    flush();
    return false;
  }
  if (msg === "send") send().then(reply);
  return true; // async reply
});

async function capture(url) {
  let u;
  try { u = new URL(url); } catch { return; }
  if (!u.hostname.endsWith("espn.com")) return;
  const leagueId = u.searchParams.get("leagueId");
  if (!leagueId || !/^\d+$/.test(leagueId)) return;

  const [s2, swid] = await Promise.all([
    chrome.cookies.get({ url: "https://fantasy.espn.com", name: "espn_s2" }),
    chrome.cookies.get({ url: "https://fantasy.espn.com", name: "SWID" }),
  ]);
  const found = {
    leagueId,
    season: Number(u.searchParams.get("seasonId")) || new Date().getFullYear(),
    teamId: Number(u.searchParams.get("teamId")) || 0,
    espn_s2: s2 ? s2.value : "",  // keep encoded — sent verbatim as a Cookie
    swid: swid ? swid.value : "",
    at: Date.now(),
  };
  const prev = (await chrome.storage.local.get("found")).found || {};
  // Keep a known teamId if this page's URL doesn't carry one.
  if (!found.teamId && prev.leagueId === leagueId) found.teamId = prev.teamId || 0;
  await chrome.storage.local.set({ found });
  await send();
}

async function send() {
  const { found } = await chrome.storage.local.get("found");
  if (!found) return { ok: false, error: "Open your ESPN league page first." };
  let result;
  try {
    const r = await fetch(DRAFTPICK, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(found),
    });
    result = r.ok ? await r.json() : { ok: false, error: `draftpick said ${r.status}` };
  } catch {
    result = { ok: false, error: "draftpick isn't running — start ./draftpick, then click Send." };
  }
  result.at = Date.now();
  await chrome.storage.local.set({ lastSend: result });
  chrome.action.setBadgeText({ text: result.ok ? "✓" : "!" });
  chrome.action.setBadgeBackgroundColor({ color: result.ok ? "#16a34a" : "#d97706" });
  return result;
}

// Draft-room messages are delivered in order and retried until draftpick
// accepts them, so a pick is never lost if draftpick restarts mid-draft.
const queue = [];
let flushing = false;
async function flush() {
  if (flushing) return;
  flushing = true;
  while (queue.length) {
    try {
      const r = await fetch("http://127.0.0.1:8765/api/draftws", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(queue[0]) });
      if (!r.ok) throw new Error(String(r.status));
      queue.shift();
    } catch {
      await new Promise((res) => setTimeout(res, 1000));
    }
  }
  flushing = false;
}
