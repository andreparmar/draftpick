# draftpick

A live draft and season assistant for **ESPN fantasy football**. One small Go
program runs on your computer, follows your ESPN draft as it happens, and tells
you who to take, with optional AI takes from Claude. After the draft it keeps
helping with your weekly matchup and waiver pickups.

Everything runs locally. Your ESPN login cookies stay on your machine and are
only ever sent to ESPN itself.

## Features

**During the draft**
- **Live sync.** Picks appear the moment they're made (via the Chrome extension),
  with a 3-second ESPN poll as a fallback.
- **Pick suggestions.** Ranks players by rest-of-season projected points (ESPN's
  weekly projections from the current week on), with a small injury discount,
  measured against the last rostered player at each position. Adds a scarcity
  bonus when a position will dry up before your next pick, then weights by your
  roster needs. Kicker and defense wait until your last rounds.
- **Undervalued.** Players ranked well above where ESPN's draft room lists them.
- **Claude takes (optional).** Runs automatically one pick before your turn.
  "Ask + live news" adds a web search on the top candidates (~15s).
- **Draft start.** Counts down to ESPN's scheduled time, flips to live on the
  first pick, and beeps in the terminal when the draft starts and when you're on
  the clock.
- **Board.** Every pick by round and team.
- **★ / 🚫.** Boost players you like or hide ones you don't (resets on restart).
- **Manual mode.** Mine / Taken buttons, search and undo, for when ESPN sync lags
  or you'd rather not connect a league at all.

**After the draft**
- **My team.** Your roster with this week's projections.
- **Matchup.** Your lineup as set vs. your best possible lineup, live points, and
  a win probability for the week.
- **Waivers.** Free agents that beat a player on your roster, like for like.

## Requirements

- [Go](https://go.dev/dl/) 1.24 or newer
- Google Chrome (or another Chromium browser) for the extension
- An ESPN fantasy football league
- Optional: an [Anthropic API key](https://console.anthropic.com/) for the Claude
  takes. Everything else works without one.

## Quick start

```sh
git clone https://github.com/andreparmar/draftpick.git
cd draftpick
cp .env.example .env
go build
./draftpick
```

The dashboard opens at <http://127.0.0.1:8765>. Then set up the Chrome extension
below so draftpick knows your league; it fills in `.env` for you.

## Chrome extension setup

The **Draftpick Connector** extension (in `extension/`) does two jobs: it hands
draftpick your league ID and ESPN login cookies, so you don't have to dig them
out by hand, and it relays the draft room's live pick feed for instant updates.

1. Open `chrome://extensions` in Chrome.
2. Turn on **Developer mode** (top right).
3. Click **Load unpacked** and choose the `extension` folder inside your
   `draftpick` folder.
4. Optional: click the puzzle-piece icon in the toolbar and pin
   **Draftpick Connector**.
5. Start draftpick (`./draftpick`) so it's running.
6. In Chrome, log in to ESPN and open your league. Any fantasy football page
   with `leagueId=` in the address works (your league home page is easiest).
   Reload the page if it was already open.
7. Click the extension icon. The popup shows the league ID, team and cookies it
   found. Click **Send to draftpick**.

draftpick switches to that league immediately and saves the values to your
`.env` file, so next time you can skip straight to `./draftpick`.

**On draft day**, keep the ESPN draft room open in Chrome. The extension passes
each pick to draftpick the instant it happens.

Prefer to set things up by hand? The popup's **Copy as .env** button copies the
three values, ready to paste into `.env`.

### What the extension can access

| Permission | Why |
|---|---|
| `cookies` on `espn.com` | Reads your `espn_s2` and `SWID` login cookies, which private leagues need |
| Access to `fantasy.espn.com` draft pages | Relays the draft room's live pick feed |
| Access to `127.0.0.1` | Sends the above to draftpick running on your own computer |
| `storage` | Remembers what it found, for the popup |

It talks to nothing except ESPN and your own computer: no analytics, no
third-party servers.

## Configuration

Settings live in `.env` (created from `.env.example`). Command-line flags
override them.

| `.env` | Flag | What it is |
|---|---|---|
| `LEAGUE_ID` | `-league` | Your ESPN league ID, or the full league URL |
| `ESPN_S2` | `-s2` | `espn_s2` login cookie (private leagues) |
| `SWID` | `-swid` | `SWID` login cookie, including the `{braces}` |
| `TEAM_ID` | `-team` | Your team ID (detected automatically from `SWID`) |
| `SEASON` | `-season` | Season year (default 2026) |
| `PORT` | `-port` | Local port for the dashboard (default 8765) |
| `ANTHROPIC_API_KEY` | | Enables the Claude takes (optional) |
| `DRAFT_MODEL` | | Claude model (default `claude-haiku-4-5`; `claude-sonnet-5-5` for sharper, pricier takes) |
| | `-open=false` | Don't open the browser automatically |

**Manual mode** (no league connected): `./draftpick -teams 10 -slot 3 -rounds 16`
for a 10-team league where you pick third.

## Privacy and security

- **Treat `.env` like a password.** `espn_s2` and `SWID` are your ESPN login. Never
  share them or commit them. `.env` is already in `.gitignore`.
- draftpick only listens on `127.0.0.1`, so nothing else on your network can
  reach it. It also rejects league settings sent from web pages, so a website
  you visit can't reconfigure it; only the extension (or you, locally) can.
- The only outside services it calls are ESPN, and Anthropic if you add a key.

## Troubleshooting

- **The extension says it can't reach draftpick.** Make sure `./draftpick` is
  running. The extension expects the default port 8765, so leave `PORT` unset.
- **Private league data is missing, or you get 401 errors.** Your ESPN cookies
  expired. Log in to ESPN again, reload your league page, and click **Send to
  draftpick**.
- **No Claude takes.** Add `ANTHROPIC_API_KEY` to `.env` and restart.
- **Picks lag during the draft.** Keep the ESPN draft room open in Chrome with the
  extension enabled. Without it draftpick falls back to polling every 3 seconds.

## Disclaimer

draftpick is an independent project, not affiliated with or endorsed by ESPN. It
relies on ESPN's unofficial web endpoints, which can change without notice. Use
it within ESPN's terms of service and your league's rules.

## License

[MIT](LICENSE)
