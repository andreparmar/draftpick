const $ = (id) => document.getElementById(id);
const short = (s) => (s.length > 24 ? s.slice(0, 10) + "…" + s.slice(-8) : s);

async function render() {
  const { found, lastSend } = await chrome.storage.local.get(["found", "lastSend"]);
  if (found) {
    const rows = [
      ["League ID", found.leagueId],
      ["Season", String(found.season)],
      ["Team ID", found.teamId ? String(found.teamId) : "auto-detect"],
      ["SWID", found.swid],
      ["espn_s2", found.espn_s2],
    ];
    $("rows").innerHTML = rows.map(([k, v]) =>
      `<div class="row"><span class="k">${k}</span><span class="v ${v ? "" : "miss"}">${v ? short(v) : "missing — log in to ESPN"}</span></div>`
    ).join("");
  }
  if (lastSend) {
    $("msg").textContent = lastSend.ok
      ? `✓ Connected: ${lastSend.league || "league"}${lastSend.team ? " — you are " + lastSend.team : ""}`
      : `! ${lastSend.error}`;
  }
}

$("send").onclick = async () => {
  $("msg").textContent = "Sending…";
  await chrome.runtime.sendMessage("send");
  render();
};

$("copy").onclick = async () => {
  const { found } = await chrome.storage.local.get("found");
  if (!found) return;
  await navigator.clipboard.writeText(`LEAGUE_ID=${found.leagueId}\nESPN_S2=${found.espn_s2}\nSWID=${found.swid}\n`);
  $("msg").textContent = "Copied — paste into draftpick's .env file";
};

render();
