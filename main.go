package main

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed index.html
var indexHTML []byte

type App struct {
	mu             sync.Mutex
	espn           *ESPN
	rankType       string
	lastWeek       int
	players        []*Player
	byID           map[int]*Player
	week           int
	format         Format
	teamNames      map[int]string
	myTeam         int
	espnPicks      []Pick
	manual         []Pick // marked by hand (TeamID -1 = someone else)
	league         string
	lineup         string
	status         string
	drafted        bool
	live           bool  // ESPN says the draft is in progress
	draftDate      int64 // scheduled start, ms since epoch
	orderSet       bool  // ESPN has published the draft order
	prefs          map[int]string
	lastLeagueLoad time.Time
	wsPicks        map[int]Pick // picks seen live in the ESPN draft room (via the extension)
	clockTeam      int          // ESPN's live clock: whose turn, ms left, when we heard it
	clockMs        int
	clockAt        time.Time
	ready          bool // league + players loaded; before that, draft-room messages are only recorded
	draftMu        sync.Mutex
	pending        []string
	rosters        map[int][]RosterEntry // in-season: every team's roster
	sdata          *SeasonData           // in-season: schedule, records, live points
	nfl            map[string]NFLGame    // this NFL week: kickoff, opponent, live status per team
	injuries       map[int]Injury        // ESPN injury report by player
	fa             map[int]string        // in-season: free agents → FREEAGENT / WAIVERS
	lookedUp       map[int]bool          // unknown drafted players we've already fetched
	events         []Event
	timePerPick    int
	orderAtStart   bool // ESPN finalizes pick order when the draft starts
	news           map[int]newsEntry
	plan           []PlanPick
	planKey        string
	lastRunAt      int

	aiText, aiErr string
	aiBusy        bool
	aiPick        int
	aiWeb         bool
	autoAIPick    int
	lastAlerted   int
	pollOnce      sync.Once
}

func main() {
	loadDotEnv(".env")
	league := flag.String("league", os.Getenv("LEAGUE_ID"), "ESPN league id (or the full league URL)")
	s2 := flag.String("s2", os.Getenv("ESPN_S2"), "espn_s2 cookie (private leagues)")
	swid := flag.String("swid", os.Getenv("SWID"), "SWID cookie (private leagues), with {braces}")
	team := flag.Int("team", envInt("TEAM_ID", 0), "your ESPN team id (auto-detected from SWID)")
	season := flag.Int("season", envInt("SEASON", 2026), "season year")
	port := flag.Int("port", envInt("PORT", 8765), "local web port")
	teams := flag.Int("teams", 6, "manual mode: number of teams")
	slot := flag.Int("slot", 1, "manual mode: your draft slot (1 = first pick)")
	rounds := flag.Int("rounds", 16, "manual mode: rounds")
	openBrowser := flag.Bool("open", true, "open the dashboard in your browser")
	flag.Parse()

	a := &App{rankType: "PPR", lastWeek: 17, teamNames: map[int]string{}, myTeam: *team, prefs: map[int]string{}, wsPicks: map[int]Pick{}, lookedUp: map[int]bool{}, news: map[int]newsEntry{}}
	a.espn = NewESPN(*season, parseLeagueID(*league), *s2, strings.TrimSpace(*swid))

	// Default ESPN lineup; replaced by the real league settings when available.
	a.format = Format{Teams: *teams, Rounds: *rounds, Snake: true,
		Slots: map[string]int{"QB": 1, "RB": 2, "WR": 2, "TE": 1, "FLEX": 1, "D/ST": 1, "K": 1, "BENCH": 7}}
	for i := 1; i <= *teams; i++ {
		a.format.PickOrder = append(a.format.PickOrder, i)
		a.teamNames[i] = fmt.Sprintf("Team %d", i)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(indexHTML)
	})
	mux.HandleFunc("/api/state", a.handleState)
	mux.HandleFunc("/api/mark", a.handleMark)
	mux.HandleFunc("/api/undo", a.handleUndo)
	mux.HandleFunc("/api/team", a.handleTeam)
	mux.HandleFunc("/api/ai", a.handleAI)
	mux.HandleFunc("/api/search", a.handleSearch)
	mux.HandleFunc("/api/config", a.handleConfig)
	mux.HandleFunc("/api/fix", a.handleFix)
	mux.HandleFunc("/api/draftws", a.handleDraftWS)
	mux.HandleFunc("/api/key", a.handleKey)
	mux.HandleFunc("/api/pref", a.handlePref)

	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	u := "http://" + addr
	// Listen before loading anything, so the extension never finds draftpick
	// unreachable during a restart (messages are recorded, then replayed).
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("%v (is draftpick already running? just open %s)", err, u)
	}
	go func() { log.Fatal(http.Serve(ln, localOnly(mux))) }()

	if a.espn.LeagueID == "" {
		a.myTeam = *slot
		a.teamNames[*slot] = "You"
		a.league = "Manual mode (no league id)"
		a.status = "Manual mode: click Taken / Mine as picks happen."
	} else if err := a.loadLeague(); err != nil {
		log.Printf("league: %v", err)
		a.status = err.Error()
	}
	a.lineup = lineupDesc(a.format)

	log.Printf("loading player projections…")
	if err := a.loadPlayers(); err != nil {
		log.Fatalf("players: %v", err)
	}
	log.Printf("%d players loaded, week %d, %s scoring", len(a.players), a.week, a.rankType)

	if a.espn.LeagueID != "" {
		a.draftMu.Lock()
		a.pending = nil // anything received while loading is already in draftws.log
		a.replayDraftLog()
		a.ready = true
		a.draftMu.Unlock()
		a.pollOnce.Do(func() { go a.pollDraft() })
	}
	fmt.Printf("\n  DRAFTPICK ready → %s\n  league: %s\n  AI: %s\n\n", u, a.league, aiModel())
	if *openBrowser {
		go func() { time.Sleep(400 * time.Millisecond); exec.Command("open", u).Run() }()
	}
	go a.newsLoop()
	go a.seasonLoop()
	go a.nflLoop()
	go func() {
		for range time.Tick(10 * time.Minute) {
			if err := a.loadPlayers(); err != nil {
				log.Printf("player refresh: %v", err)
			}
		}
	}()

	select {} // the web server runs in the background (started before loading)
}

func (a *App) loadLeague() error {
	a.mu.Lock()
	e := a.espn
	a.mu.Unlock()
	l, err := e.League()
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	st := l.Settings
	a.league = st.Name
	a.orderSet = len(st.DraftSettings.PickOrder) > 0
	if a.orderSet {
		a.format.PickOrder = st.DraftSettings.PickOrder
	} else {
		// Order not published yet: placeholder so the board still renders.
		a.format.PickOrder = nil
		for _, t := range l.Teams {
			a.format.PickOrder = append(a.format.PickOrder, t.ID)
		}
		sort.Ints(a.format.PickOrder)
	}
	a.draftDate = st.DraftSettings.Date
	a.timePerPick = st.DraftSettings.TimePerSelection
	a.orderAtStart = st.DraftSettings.OrderType == "DRAFT_START"
	a.lastLeagueLoad = time.Now()
	a.format.Teams = len(a.format.PickOrder)
	if st.Size > 0 && a.format.Teams == 0 {
		a.format.Teams = st.Size
	}
	a.format.Snake = st.DraftSettings.Type != "LINEAR"
	sc := st.RosterSettings.LineupSlotCounts
	if len(sc) > 0 {
		a.format.Slots = map[string]int{
			"QB": slotCount(sc, slotQB), "RB": slotCount(sc, slotRB), "WR": slotCount(sc, slotWR),
			"TE": slotCount(sc, slotTE), "FLEX": slotCount(sc, slotFlex), "OP": slotCount(sc, slotOP),
			"D/ST": slotCount(sc, slotDST), "K": slotCount(sc, slotK), "BENCH": slotCount(sc, slotBench),
		}
		rounds := 0
		for k, v := range sc {
			if k != strconv.Itoa(slotIR) {
				rounds += v
			}
		}
		if rounds > 0 {
			a.format.Rounds = rounds
		}
	}
	a.rankType = "STANDARD"
	for _, it := range st.ScoringSettings.ScoringItems {
		if it.StatID == 53 && it.Points > 0 { // receptions
			a.rankType = "PPR"
		}
	}
	a.teamNames = map[int]string{}
	swid := strings.ToUpper(strings.Trim(a.espn.SWID, "{}"))
	for _, t := range l.Teams {
		name := t.Name
		if name == "" {
			name = strings.TrimSpace(t.Location + " " + t.Nickname)
		}
		if name == "" {
			name = t.Abbrev
		}
		a.teamNames[t.ID] = name
		for _, o := range t.Owners {
			if swid != "" && a.myTeam == 0 && strings.ToUpper(strings.Trim(o, "{}")) == swid {
				a.myTeam = t.ID
			}
		}
	}
	a.espnPicks = nil
	a.format.Owners = map[int]int{}
	for _, p := range l.DraftDetail.Picks {
		a.format.Owners[p.OverallPickNumber] = p.TeamID
	}
	for _, p := range l.DraftDetail.Picks {
		if p.PlayerID > 0 {
			a.espnPicks = append(a.espnPicks, Pick{p.OverallPickNumber, p.PlayerID, p.TeamID})
		}
	}
	sort.Slice(a.espnPicks, func(i, j int) bool { return a.espnPicks[i].Overall < a.espnPicks[j].Overall })
	a.drafted, a.live = l.DraftDetail.Drafted, l.DraftDetail.InProgress
	a.status = ""
	log.Printf("league %q: %d teams, %d rounds, you = team %d (%s)", a.league, a.format.Teams, a.format.Rounds, a.myTeam, a.teamNames[a.myTeam])
	return nil
}

func (a *App) loadPlayers() error {
	a.mu.Lock()
	rt, lw, e := a.rankType, a.lastWeek, a.espn
	a.mu.Unlock()
	ps, week, err := e.Players(rt, lw)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.players, a.week = ps, week
	a.byID = map[int]*Player{}
	a.lookedUp = map[int]bool{} // re-fetch any deep picks on the next poll
	for _, p := range ps {
		a.byID[p.ID] = p
	}
	return nil
}

func (a *App) pollDraft() {
	fails := 0
	for {
		a.mu.Lock()
		e := a.espn
		a.mu.Unlock()
		picks, owners, drafted, inProgress, err := e.DraftPicks()
		started, refresh := false, false
		wait := 10 * time.Second
		a.mu.Lock()
		if err != nil {
			fails++
			if fails >= 3 {
				a.status = "ESPN sync issue: " + err.Error() + " — you can mark picks by hand meanwhile."
			}
		} else {
			fails = 0
			if strings.HasPrefix(a.status, "ESPN sync issue") {
				a.status = ""
			}
			if len(owners) > 0 {
				a.format.Owners = owners
			}
			wasLive := a.live
			live := a.ingest(mergePicks(picks, a.wsPicks), drafted, inProgress)
			started = live && !wasLive && !a.drafted
			soon := a.draftDate > 0 && time.Until(time.UnixMilli(a.draftDate)) < 10*time.Minute
			switch {
			case drafted:
				wait = 30 * time.Second
			case live || soon:
				wait = 2 * time.Second
			}
			// Before the draft, re-read settings: ESPN often sets the pick order late.
			refresh = !live && !drafted && (time.Since(a.lastLeagueLoad) > time.Minute || (soon && !a.orderSet))
		}
		a.mu.Unlock()
		if started {
			a.mu.Lock()
			a.addEvent("live", "The draft is live")
			a.mu.Unlock()
			fmt.Printf("\a\n>>> THE DRAFT IS LIVE — dashboard is tracking picks\n\n")
			refresh = true
		}
		a.lookupUnknown()
		if refresh {
			if err := a.loadLeague(); err != nil {
				log.Printf("league refresh: %v", err)
			}
		}
		a.maybeAlertAndAutoAI()
		time.Sleep(wait)
	}
}

// ingest applies a fresh ESPN pick list: logs new picks, raises alerts,
// reconciles hand marks. Caller holds a.mu. Returns whether the draft is live.
func (a *App) ingest(picks []Pick, drafted, inProgress bool) bool {
	live := inProgress || (len(picks) > 0 && !drafted)
	for _, p := range picks[min(len(a.espnPicks), len(picks)):] {
		name, short := fmt.Sprint(p.PlayerID), fmt.Sprint(p.PlayerID)
		if pl := a.byID[p.PlayerID]; pl != nil {
			name, short = fmt.Sprintf("%s (%s %s)", pl.Name, pl.Pos, pl.Team), pl.Name
		}
		log.Printf("PICK #%d  %-22s → %s", p.Overall, a.teamNames[p.TeamID], name)
		switch {
		case p.TeamID == a.myTeam:
			fmt.Printf("\n>>> YOU DRAFTED %s — added to your team\n\n", name)
			a.addEvent("mine", "✓ "+short+" added to your team")
		case a.prefs[p.PlayerID] == "want":
			a.addEvent("target", fmt.Sprintf("⚠ Your ★ target %s went to %s (#%d)", short, a.teamNames[p.TeamID], p.Overall))
		}
	}
	a.espnPicks, a.drafted, a.live = picks, drafted, live
	a.reconcileManual()
	a.detectRun()
	return live
}

// reconcileManual drops hand-marked picks once ESPN has reported that pick
// slot: ESPN's real pick is the truth (a misclick simply disappears and the
// player becomes available again). Caller holds a.mu.
func (a *App) reconcileManual() {
	espnHas := map[int]bool{}
	for _, p := range a.espnPicks {
		espnHas[p.PlayerID] = true
	}
	kept := a.manual[:0]
	for _, m := range a.manual {
		switch {
		case espnHas[m.PlayerID]:
			// confirmed by ESPN
		case m.Overall > 0 && m.Overall <= len(a.espnPicks):
			if pl := a.byID[m.PlayerID]; pl != nil {
				log.Printf("hand-marked %s at #%d didn't match ESPN's real pick — using ESPN's", pl.Name, m.Overall)
			}
		default:
			kept = append(kept, m)
		}
	}
	a.manual = kept
}

// detectRun flags a position run (3 of the last 4 picks at one position) so
// you can grab one before they dry up. Caller holds a.mu.
func (a *App) detectRun() {
	n := len(a.espnPicks)
	if n < 4 || n-a.lastRunAt < 4 {
		return
	}
	count := map[string]int{}
	for _, p := range a.espnPicks[n-4:] {
		if pl := a.byID[p.PlayerID]; pl != nil {
			count[pl.Pos]++
		}
	}
	last := a.byID[a.espnPicks[n-1].PlayerID]
	if last != nil && count[last.Pos] >= 3 && last.Pos != "K" && last.Pos != "D/ST" {
		a.lastRunAt = n
		a.addEvent("run", fmt.Sprintf("%s run: %d of the last 4 picks were %ss. They're drying up", last.Pos, count[last.Pos], last.Pos))
	}
}

// lookupUnknown fetches drafted players missing from our pool so they still
// show on the board and count toward rosters.
func (a *App) lookupUnknown() {
	a.mu.Lock()
	var ids []int
	for _, p := range a.espnPicks {
		if a.byID[p.PlayerID] == nil && !a.lookedUp[p.PlayerID] {
			a.lookedUp[p.PlayerID] = true
			ids = append(ids, p.PlayerID)
		}
	}
	e, rt, lw, wk := a.espn, a.rankType, a.lastWeek, a.week
	a.mu.Unlock()
	if len(ids) == 0 {
		return
	}
	ps, err := e.PlayersByID(ids, rt, lw, wk)
	if err != nil {
		log.Printf("player lookup: %v", err)
		return
	}
	a.mu.Lock()
	for _, p := range ps {
		if a.byID[p.ID] == nil {
			a.players = append(a.players, p)
			a.byID[p.ID] = p
		}
	}
	a.mu.Unlock()
}

// maybeAlertAndAutoAI rings the terminal on your turn and pre-fetches a Claude
// take one pick ahead so it's ready when you're on the clock.
func (a *App) maybeAlertAndAutoAI() {
	s := a.snapshot()
	if s.MyNextPick == 0 || s.Done {
		return
	}
	a.mu.Lock()
	if s.OnClockMe && a.lastAlerted != s.CurrentPick {
		a.lastAlerted = s.CurrentPick
		top := ""
		for i, r := range s.Recs {
			if i == 3 {
				break
			}
			top += fmt.Sprintf("  %d) %s %s %s\n", i+1, r.Name, r.Pos, r.Team)
		}
		fmt.Printf("\a\n>>> YOU'RE ON THE CLOCK (pick #%d)\n%s\n", s.CurrentPick, top)
	}
	// Take one pick ahead (so it's ready), then refresh once on the clock,
	// since the pick right before mine may have taken Claude's choice.
	stage := s.MyNextPick*10 + min(s.PicksUntil, 1)
	fire := s.PicksUntil <= 1 && a.autoAIPick != stage && !a.aiBusy
	if fire {
		a.autoAIPick = stage
	}
	a.mu.Unlock()
	if fire {
		go a.runAI(false)
	}
}

func (a *App) runAI(web bool) {
	a.mu.Lock()
	if a.aiBusy {
		a.mu.Unlock()
		return
	}
	a.aiBusy, a.aiWeb = true, web
	a.mu.Unlock()
	s := a.snapshot()
	text, err := askClaude(context.Background(), aiReq{Web: web, Context: aiContext(s)})
	a.mu.Lock()
	defer a.mu.Unlock()
	a.aiBusy = false
	if err != nil {
		a.aiErr = err.Error()
		if strings.Contains(a.aiErr, "no Anthropic credentials") {
			a.aiErr = "no ANTHROPIC_API_KEY — add it to the .env file and restart (everything else works without it)"
		}
		log.Printf("claude: %v", err)
		return
	}
	a.aiText, a.aiErr, a.aiPick = text, "", s.MyNextPick
}

type Event struct {
	ID   int    `json:"id"`
	Kind string `json:"kind"` // live | mine | target | run
	Text string `json:"text"`
}

// addEvent records something worth a heads-up in the UI. Caller holds a.mu.
func (a *App) addEvent(kind, text string) {
	for _, e := range a.events {
		if e.Text == text {
			return // already announced (e.g. replayed after a restart)
		}
	}
	id := 1
	if n := len(a.events); n > 0 {
		id = a.events[n-1].ID + 1
	}
	a.events = append(a.events, Event{id, kind, text})
	if len(a.events) > 30 {
		a.events = a.events[len(a.events)-30:]
	}
	log.Printf("ALERT %s", text)
}

// ---- snapshot ----

type PickView struct {
	Overall  int     `json:"overall"`
	Name     string  `json:"name"`
	Pos      string  `json:"pos"`
	Team     string  `json:"team"`
	Owner    string  `json:"owner"`
	Mine     bool    `json:"mine"`
	Manual   bool    `json:"manual"`
	PlayerID int     `json:"playerId"`
	TeamID   int     `json:"teamId"`
	Round    int     `json:"round"`
	Col      int     `json:"col"` // column on the board (draft slot)
	ROS      float64 `json:"ros"`
	Injury   string  `json:"injury"`
}

type Snapshot struct {
	League       string             `json:"league"`
	Teams        int                `json:"teams"`
	Rounds       int                `json:"rounds"`
	LineupDesc   string             `json:"lineup"`
	Week         int                `json:"week"`
	Scoring      string             `json:"scoring"`
	CurrentPick  int                `json:"currentPick"`
	OnClock      string             `json:"onClock"`
	OnClockMe    bool               `json:"onClockMe"`
	MyTeam       int                `json:"myTeam"`
	MyNextPick   int                `json:"myNextPick"`
	MyRound      int                `json:"myRound"`
	PicksUntil   int                `json:"picksUntil"`
	Mine         []*Player          `json:"mine"`
	Recs         []Rec              `json:"recs"`
	ByPos        map[string][]Rec   `json:"byPos"`
	Steals       []Rec              `json:"steals"`
	Recent       []PickView         `json:"recent"`
	TeamList     []map[string]any   `json:"teamList"`
	Status       string             `json:"status"`
	Done         bool               `json:"done"`
	Live         bool               `json:"live"`
	AI           map[string]any     `json:"ai"`
	Needs        map[string][2]int  `json:"needs"`
	Phase        string             `json:"phase"` // manual | scheduled | live | done
	DraftDate    int64              `json:"draftDate"`
	OrderSet     bool               `json:"orderSet"`
	Board        []PickView         `json:"board"`
	BoardCols    []map[string]any   `json:"boardCols"`
	Prefs        []map[string]any   `json:"prefs"`
	Slots        map[string]int     `json:"slots"`
	Events       []Event            `json:"events"`
	TimePerPick  int                `json:"timePerPick"`
	OrderAtStart bool               `json:"orderAtStart"`
	HasKey       bool               `json:"hasKey"`
	Plan         []PlanPick         `json:"plan"`
	Between      []string           `json:"between"` // teams picking before my next pick, in order
	ClockMs      int                `json:"clockMs"` // ESPN's real time left for whoever is on the clock (0 = no clock running)
	Season       *Season            `json:"season,omitempty"`
	NFL          map[string]NFLGame `json:"nfl"`
	Injuries     map[int]Injury     `json:"injuries"`
	LeagueID     string             `json:"leagueId"`
	SeasonYr     int                `json:"seasonYr"`
}

func (a *App) snapshot() *Snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	f := a.format

	// Merge ESPN picks with hand-marked ones (dedupe by player).
	taken := map[int]bool{}
	var picks []Pick
	for _, p := range a.espnPicks {
		taken[p.PlayerID] = true
		picks = append(picks, p)
	}
	manualSet := map[int]bool{}
	for _, p := range a.manual {
		if !taken[p.PlayerID] && !(p.Overall > 0 && p.Overall <= len(a.espnPicks)) {
			taken[p.PlayerID] = true
			manualSet[p.PlayerID] = true
			p.Overall = len(picks) + 1
			picks = append(picks, p)
		}
	}
	cur := len(picks) + 1
	total := f.Teams * f.Rounds

	var mine []*Player
	for _, p := range picks {
		if p.TeamID == a.myTeam {
			if pl := a.byID[p.PlayerID]; pl != nil {
				mine = append(mine, pl)
			}
		}
	}

	s := &Snapshot{League: a.league, Teams: f.Teams, Rounds: f.Rounds, LineupDesc: a.lineup, Week: a.week,
		Scoring: a.rankType, CurrentPick: cur, MyTeam: a.myTeam, Mine: mine, Status: a.status,
		Done: a.drafted || cur > total, Live: a.espn.LeagueID != "", ByPos: map[string][]Rec{}}
	if cur <= total {
		s.OnClock = a.teamNames[f.TeamForPick(cur)]
		s.OnClockMe = f.TeamForPick(cur) == a.myTeam
	}
	if next := f.NextPicks(a.myTeam, cur); len(next) > 0 && a.myTeam != 0 {
		s.MyNextPick = next[0]
		s.MyRound = (next[0]-1)/max(f.Teams, 1) + 1
		s.PicksUntil = next[0] - cur
	}

	switch {
	case a.espn.LeagueID == "":
		s.Phase = "manual"
	case s.Done:
		s.Phase = "done"
	case len(picks) > 0 || (a.live && (a.draftDate == 0 || time.Now().UnixMilli() >= a.draftDate)):
		s.Phase = "live"
	default:
		s.Phase = "scheduled"
	}
	s.DraftDate, s.OrderSet, s.Slots = a.draftDate, a.orderSet, f.Slots

	teamHave := map[int]map[string]int{}
	for _, p := range picks {
		if pl := a.byID[p.PlayerID]; pl != nil && p.TeamID > 0 {
			if teamHave[p.TeamID] == nil {
				teamHave[p.TeamID] = map[string]int{}
			}
			teamHave[p.TeamID][pl.Pos]++
		}
	}
	recs := Rank(a.players, taken, mine, f, cur, a.myTeam, a.prefs, teamHave)
	for i := range recs[:min(15, len(recs))] {
		recs[i].News = a.news[recs[i].ID].items
	}
	s.Events = a.events
	s.TimePerPick, s.OrderAtStart = a.timePerPick, a.orderAtStart
	s.HasKey = os.Getenv("ANTHROPIC_API_KEY") != ""
	// Forward-simulated draft plan; recomputed only when the draft state changes.
	key := fmt.Sprint(len(picks), a.myTeam, a.prefs, len(a.players), f.Owners[cur])
	if key != a.planKey {
		a.plan, a.planKey = Plan(a.players, taken, mine, f, cur, a.myTeam, a.prefs, teamHave, f.Rounds), key
	}
	s.Plan = a.plan
	s.LeagueID, s.SeasonYr = a.espn.LeagueID, a.espn.Season
	s.NFL = a.nfl
	// Only send injury reports for players we know (keeps the payload small).
	s.Injuries = map[int]Injury{}
	for id, inj := range a.injuries {
		if a.byID[id] != nil {
			s.Injuries[id] = inj
		}
	}
	if a.drafted && a.rosters != nil {
		s.Season = buildSeason(a.rosters[a.myTeam], a.fa, a.byID, f, a.week)
		if a.sdata != nil {
			a.addLeague(s.Season, f)
		}
		// In season, "your team" is your live ESPN roster, not your draft picks.
		var roster []*Player
		for _, e := range a.rosters[a.myTeam] {
			if p := a.byID[e.PlayerID]; p != nil {
				roster = append(roster, p)
			}
		}
		if len(roster) > 0 {
			s.Mine = roster
		}
	}
	if a.clockTeam != 0 && time.Since(a.clockAt) < 2*time.Minute {
		s.ClockMs = max(0, a.clockMs-int(time.Since(a.clockAt).Milliseconds()))
		s.OnClock = a.teamNames[a.clockTeam]
		s.OnClockMe = a.clockTeam == a.myTeam
	} else if a.clockTeam == 0 && len(a.wsPicks) > 0 {
		s.OnClockMe = false // a pick just landed; the next turn hasn't started yet
	}
	if s.MyNextPick > 0 {
		for o := cur; o < s.MyNextPick; o++ {
			s.Between = append(s.Between, a.teamNames[f.TeamForPick(o)])
		}
	}
	s.Recs = recs[:min(250, len(recs))]
	for _, r := range recs {
		if len(s.ByPos[r.Pos]) < 6 {
			s.ByPos[r.Pos] = append(s.ByPos[r.Pos], r)
		}
		if r.Steal >= 15 && r.VORP > 0 && len(s.Steals) < 8 {
			s.Steals = append(s.Steals, r)
		}
	}

	col := map[int]int{}
	for i, id := range f.PickOrder {
		col[id] = i
		s.BoardCols = append(s.BoardCols, map[string]any{"id": id, "name": a.teamNames[id], "mine": id == a.myTeam})
	}
	for _, p := range picks {
		v := PickView{Overall: p.Overall, Owner: a.teamNames[p.TeamID], Mine: p.TeamID == a.myTeam,
			Manual: manualSet[p.PlayerID], PlayerID: p.PlayerID, TeamID: p.TeamID,
			Round: (p.Overall-1)/max(f.Teams, 1) + 1, Col: col[f.TeamForPick(p.Overall)]}
		if p.TeamID == -1 {
			v.Owner = "(marked taken)"
		}
		if pl := a.byID[p.PlayerID]; pl != nil {
			v.Name, v.Pos, v.Team, v.ROS, v.Injury = pl.Name, pl.Pos, pl.Team, pl.Adj, pl.Injury
		} else {
			v.Name = fmt.Sprintf("player %d", p.PlayerID)
		}
		s.Board = append(s.Board, v)
	}
	for i := len(s.Board) - 1; i >= 0 && len(s.Recent) < 12; i-- {
		s.Recent = append(s.Recent, s.Board[i])
	}
	recByID := map[int]Rec{}
	for _, r := range recs {
		recByID[r.ID] = r
	}
	for id, pref := range a.prefs {
		if pl := a.byID[id]; pl != nil {
			st := "safe"
			switch {
			case taken[id]:
				st = "taken"
			case recByID[id].LikelyGo:
				st = "gone"
			case recByID[id].LastChance:
				st = "last"
			}
			s.Prefs = append(s.Prefs, map[string]any{"id": id, "name": pl.Name, "pos": pl.Pos, "team": pl.Team, "pref": pref, "taken": taken[id], "status": st})
		}
	}
	sort.Slice(s.Prefs, func(i, j int) bool { return s.Prefs[i]["name"].(string) < s.Prefs[j]["name"].(string) })

	have := map[string]int{}
	for _, p := range mine {
		have[p.Pos]++
	}
	s.Needs = map[string][2]int{}
	for _, pos := range []string{"QB", "RB", "WR", "TE", "D/ST", "K"} {
		s.Needs[pos] = [2]int{have[pos], f.Slots[pos]}
	}

	ids := make([]int, 0, len(a.teamNames))
	for id := range a.teamNames {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		s.TeamList = append(s.TeamList, map[string]any{"id": id, "name": a.teamNames[id]})
	}
	s.AI = map[string]any{"text": a.aiText, "err": a.aiErr, "busy": a.aiBusy, "forPick": a.aiPick, "web": a.aiWeb, "model": aiModel()}
	return s
}

// ---- handlers ----

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (a *App) handleState(w http.ResponseWriter, r *http.Request) { writeJSON(w, a.snapshot()) }

func (a *App) handleMark(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID   int  `json:"id"`
		Mine bool `json:"mine"`
	}
	json.NewDecoder(r.Body).Decode(&in)
	cur := a.snapshot().CurrentPick
	a.mu.Lock()
	team := a.format.TeamForPick(cur) // hand-marked picks go to whoever is on the clock
	if in.Mine {
		team = a.myTeam
	} else if team == a.myTeam {
		team = -1
	}
	a.manual = append(a.manual, Pick{Overall: cur, PlayerID: in.ID, TeamID: team})
	a.mu.Unlock()
	a.maybeAlertAndAutoAI()
	writeJSON(w, map[string]bool{"ok": true})
}

// handleFix edits hand-marked picks: remove one, or move it to another team.
func (a *App) handleFix(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID     int  `json:"id"`
		Remove bool `json:"remove"`
		TeamID int  `json:"teamId"`
	}
	json.NewDecoder(r.Body).Decode(&in)
	a.mu.Lock()
	for i, p := range a.manual {
		if p.PlayerID != in.ID {
			continue
		}
		if in.Remove {
			a.manual = append(a.manual[:i], a.manual[i+1:]...)
		} else {
			a.manual[i].TeamID = in.TeamID
		}
		break
	}
	a.mu.Unlock()
	writeJSON(w, map[string]bool{"ok": true})
}

// handlePref stars ("want") or bans ("avoid") a player; "" clears it.
func (a *App) handlePref(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID   int    `json:"id"`
		Pref string `json:"pref"`
	}
	json.NewDecoder(r.Body).Decode(&in)
	a.mu.Lock()
	if in.Pref == "want" || in.Pref == "avoid" {
		a.prefs[in.ID] = in.Pref
	} else {
		delete(a.prefs, in.ID)
	}
	a.mu.Unlock()
	writeJSON(w, map[string]bool{"ok": true})
}

func (a *App) handleUndo(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	if n := len(a.manual); n > 0 {
		a.manual = a.manual[:n-1]
	}
	a.mu.Unlock()
	writeJSON(w, map[string]bool{"ok": true})
}

func (a *App) handleTeam(w http.ResponseWriter, r *http.Request) {
	var in struct{ ID int }
	json.NewDecoder(r.Body).Decode(&in)
	a.mu.Lock()
	a.myTeam = in.ID
	a.mu.Unlock()
	writeJSON(w, map[string]bool{"ok": true})
}

func (a *App) handleAI(w http.ResponseWriter, r *http.Request) {
	go a.runAI(r.URL.Query().Get("web") == "1")
	writeJSON(w, map[string]bool{"ok": true})
}

func (a *App) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	a.mu.Lock()
	defer a.mu.Unlock()
	taken := map[int]bool{}
	for _, p := range a.espnPicks {
		taken[p.PlayerID] = true
	}
	for _, p := range a.manual {
		taken[p.PlayerID] = true
	}
	var out []*Player
	for _, p := range a.players {
		if !taken[p.ID] && q != "" && strings.Contains(strings.ToLower(p.Name), q) {
			out = append(out, p)
			if len(out) == 12 {
				break
			}
		}
	}
	writeJSON(w, out)
}

// ---- helpers ----

var leagueIDRe = regexp.MustCompile(`leagueId=(\d+)`)

func parseLeagueID(s string) string {
	s = strings.TrimSpace(s)
	if m := leagueIDRe.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	if u, err := url.Parse(s); err == nil && u.Query().Get("leagueId") != "" {
		return u.Query().Get("leagueId")
	}
	return s
}

func lineupDesc(f Format) string {
	var parts []string
	for _, k := range []string{"QB", "RB", "WR", "TE", "FLEX", "OP", "D/ST", "K", "BENCH"} {
		if v := f.Slots[k]; v > 0 {
			name := k
			if k == "OP" {
				name = "SUPERFLEX"
			}
			parts = append(parts, fmt.Sprintf("%d %s", v, name))
		}
	}
	return strings.Join(parts, ", ")
}

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return v
	}
	return def
}

// loadDotEnv reads KEY=VALUE lines so setup is just editing one file.
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.Trim(strings.TrimSpace(v), `"'`)
		if os.Getenv(k) == "" && v != "" {
			os.Setenv(k, v)
		}
	}
}

// handleConfig receives league id + cookies from the Chrome extension and
// switches the running tool onto that league (and saves them to .env).
func (a *App) handleConfig(w http.ResponseWriter, r *http.Request) {
	// Only the extension (or curl) may configure us — not arbitrary web pages.
	if o := r.Header.Get("Origin"); o != "" && !strings.HasPrefix(o, "chrome-extension://") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var in struct {
		LeagueID string `json:"leagueId"`
		Season   int    `json:"season"`
		TeamID   int    `json:"teamId"`
		S2       string `json:"espn_s2"`
		SWID     string `json:"swid"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || parseLeagueID(in.LeagueID) == "" {
		http.Error(w, "need leagueId", http.StatusBadRequest)
		return
	}
	in.LeagueID = parseLeagueID(in.LeagueID)
	if _, err := strconv.Atoi(in.LeagueID); err != nil {
		http.Error(w, "bad leagueId", http.StatusBadRequest)
		return
	}

	a.mu.Lock()
	if in.Season == 0 {
		in.Season = a.espn.Season
	}
	same := a.espn.LeagueID == in.LeagueID && a.espn.S2 == in.S2 && a.espn.SWID == in.SWID &&
		a.espn.Season == in.Season && (in.TeamID == 0 || in.TeamID == a.myTeam)
	a.mu.Unlock()
	if same {
		writeJSON(w, map[string]any{"ok": true, "changed": false, "league": a.snapshot().League})
		return
	}

	a.mu.Lock()
	a.espn = NewESPN(in.Season, in.LeagueID, in.S2, in.SWID)
	a.myTeam = in.TeamID // 0 → auto-detect from SWID in loadLeague
	a.espnPicks, a.manual, a.drafted = nil, nil, false
	a.status = "Connecting to your ESPN league…"
	a.mu.Unlock()

	if err := a.loadLeague(); err != nil {
		a.mu.Lock()
		a.status = err.Error()
		a.mu.Unlock()
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	a.mu.Lock()
	a.lineup = lineupDesc(a.format)
	league, team := a.league, a.teamNames[a.myTeam]
	a.mu.Unlock()
	go func() {
		if err := a.loadPlayers(); err != nil {
			log.Printf("players: %v", err)
		}
	}()
	a.pollOnce.Do(func() { go a.pollDraft() })
	if err := saveDotEnv(".env", map[string]string{"LEAGUE_ID": in.LeagueID, "ESPN_S2": in.S2, "SWID": in.SWID}); err != nil {
		log.Printf(".env: %v", err)
	}
	log.Printf("extension connected league %s (%s), you = %s", in.LeagueID, league, team)
	writeJSON(w, map[string]any{"ok": true, "changed": true, "league": league, "team": team})
}

// saveDotEnv updates (or adds) keys in .env, leaving other lines alone.
func saveDotEnv(path string, kv map[string]string) error {
	data, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	done := map[string]bool{}
	for i, l := range lines {
		k, _, ok := strings.Cut(strings.TrimSpace(l), "=")
		if v, want := kv[strings.TrimSpace(k)]; ok && want && !strings.HasPrefix(strings.TrimSpace(l), "#") {
			lines[i] = k + "=" + v
			done[k] = true
		}
	}
	for _, k := range []string{"LEAGUE_ID", "ESPN_S2", "SWID", "ANTHROPIC_API_KEY"} {
		if v, ok := kv[k]; ok && !done[k] {
			lines = append(lines, k+"="+v)
		}
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}

type newsEntry struct {
	items []NewsItem
	at    time.Time
}

// newsLoop keeps ESPN news fresh for the players you're most likely to pick
// (top recommendations + your ★ list), a few at a time.
func (a *App) newsLoop() {
	for {
		s := a.snapshot()
		want := []int{}
		for i, r := range s.Recs {
			if i == 15 {
				break
			}
			want = append(want, r.ID)
		}
		for _, p := range s.Prefs {
			want = append(want, p["id"].(int))
		}
		a.mu.Lock()
		e := a.espn
		var stale []int
		for _, id := range want {
			if n, ok := a.news[id]; !ok || time.Since(n.at) > 10*time.Minute {
				stale = append(stale, id)
			}
		}
		a.mu.Unlock()
		for i, id := range stale {
			if i == 6 {
				break
			}
			items, err := e.News(id)
			if err != nil {
				continue
			}
			a.mu.Lock()
			a.news[id] = newsEntry{items, time.Now()}
			a.mu.Unlock()
		}
		time.Sleep(15 * time.Second)
	}
}

// handleKey saves the Anthropic API key from the dashboard (to .env and the
// running process), so Claude works without a restart.
func (a *App) handleKey(w http.ResponseWriter, r *http.Request) {
	if o := r.Header.Get("Origin"); o != "" && !strings.HasPrefix(o, "http://127.0.0.1:") && !strings.HasPrefix(o, "http://localhost:") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var in struct{ Key string }
	json.NewDecoder(r.Body).Decode(&in)
	key := strings.TrimSpace(in.Key)
	if !strings.HasPrefix(key, "sk-ant-") {
		writeJSON(w, map[string]any{"ok": false, "error": "That doesn't look like an Anthropic key (starts with sk-ant-)"})
		return
	}
	os.Setenv("ANTHROPIC_API_KEY", key)
	if err := saveDotEnv(".env", map[string]string{"ANTHROPIC_API_KEY": key}); err != nil {
		log.Printf(".env: %v", err)
	}
	a.mu.Lock()
	a.aiErr = ""
	a.mu.Unlock()
	go a.runAI(false)
	writeJSON(w, map[string]any{"ok": true})
}

// localOnly rejects state-changing requests from other websites. The
// dashboard itself, the Chrome extension, and curl are allowed.
func localOnly(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); r.Method == http.MethodPost && o != "" &&
			!strings.HasPrefix(o, "http://127.0.0.1:") && !strings.HasPrefix(o, "http://localhost:") &&
			!strings.HasPrefix(o, "chrome-extension://") {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// mergePicks combines the ESPN API's pick list (empty until the draft ends)
// with picks seen live in the draft room. Same pick number: the API wins.
func mergePicks(api []Pick, ws map[int]Pick) []Pick {
	by := map[int]Pick{}
	for _, p := range ws {
		by[p.Overall] = p
	}
	for _, p := range api {
		by[p.Overall] = p
	}
	out := make([]Pick, 0, len(by))
	for _, p := range by {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Overall < out[j].Overall })
	return out
}

var draftWSLog *os.File

// handleDraftWS receives the ESPN draft room's live messages from the
// extension. Every message is logged raw; SELECTED lines become picks.
func (a *App) handleDraftWS(w http.ResponseWriter, r *http.Request) {
	var in struct {
		URL  string `json:"url"`
		Data string `json:"data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	if draftWSLog == nil {
		draftWSLog, _ = os.OpenFile("draftws.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	}
	if draftWSLog != nil {
		fmt.Fprintf(draftWSLog, "%s %s | %s\n", time.Now().Format("15:04:05"), in.URL, in.Data)
	}
	a.draftMu.Lock()
	if a.ready {
		a.applyDraftData(in.Data)
	} else {
		a.pending = append(a.pending, in.Data)
	}
	a.draftMu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// parseInit decodes the draft room's binary INIT snapshot. Picks are stored
// as big-endian int32 runs: league id, team id, overall pick, player id.
func parseInit(b64 string, leagueID, total int) []Pick {
	// Keep only base64 characters (the snapshot is padded with '#').
	b64 = strings.Map(func(r rune) rune {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '+' || r == '/' {
			return r
		}
		return -1
	}, b64)
	b64 = b64[:len(b64)/4*4]
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil && len(b) == 0 {
		log.Printf("draft room snapshot: %v", err)
		return nil
	}
	i32 := func(o int) int { return int(int32(binary.BigEndian.Uint32(b[o : o+4]))) }
	real := func(p int) bool { return p > 1000 || p < -1000 } // ESPN ids are large; D/ST negative
	// Find the pick table: pick #1 with a real player, then walk it. Records
	// are a fixed stride apart (league, team, pick #, player, ...).
	for o := 0; o+16 <= len(b); o++ {
		if i32(o) != leagueID || i32(o+8) != 1 || !real(i32(o+12)) {
			continue
		}
		for stride := 16; stride <= 96; stride++ {
			var out []Pick
			for k := 0; ; k++ {
				q := o + k*stride
				if q+16 > len(b) || i32(q) != leagueID || i32(q+8) != k+1 || k+1 > total {
					break
				}
				if p := i32(q + 12); real(p) {
					out = append(out, Pick{Overall: k + 1, PlayerID: p, TeamID: i32(q + 4)})
				} else {
					break // first unmade pick: the table ends here
				}
			}
			if len(out) >= 2 || (len(out) == 1 && i32(o+stride) == leagueID) {
				return out
			}
		}
	}
	return nil
}

// applyDraftData turns draft-room messages into picks (INIT = full list,
// SELECTED = the next pick). Used live and when replaying draftws.log.
func (a *App) applyDraftData(data string) {
	a.mu.Lock()
	leagueID, _ := strconv.Atoi(a.espn.LeagueID)
	total := a.format.Teams * a.format.Rounds
	a.mu.Unlock()
	var added []Pick
	for _, line := range strings.Split(data, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "INIT" {
			// Full draft snapshot sent when the draft room loads: every pick so far.
			if ps := parseInit(f[1], leagueID, total); len(ps) > 0 {
				a.mu.Lock()
				a.wsPicks = map[int]Pick{}
				a.mu.Unlock()
				added = append(added, ps...)
				log.Printf("draft room snapshot: %d picks so far", len(ps))
			}
			continue
		}
		if len(f) >= 3 && (f[0] == "SELECTING" || f[0] == "CLOCK") {
			team, ms := f[1], f[2]
			if f[0] == "CLOCK" && len(f) >= 4 {
				team = f[3]
			}
			t, e1 := strconv.Atoi(team)
			left, e2 := strconv.Atoi(ms)
			if e1 == nil && e2 == nil {
				a.mu.Lock()
				a.clockTeam, a.clockMs, a.clockAt = t, left, time.Now()
				a.mu.Unlock()
			}
			continue
		}
		if len(f) >= 1 && f[0] == "SELECTED" {
			a.mu.Lock()
			a.clockTeam = 0 // pick is in: nobody's clock is running until the next SELECTING
			a.mu.Unlock()
		}
		// "SELECTED <team> <player> <slot> <member>": the next pick, in order.
		if len(f) < 3 || f[0] != "SELECTED" {
			continue
		}
		team, e1 := strconv.Atoi(f[1])
		player, e2 := strconv.Atoi(f[2])
		// Real ESPN ids are large; team defenses (D/ST) are large negatives.
		if e1 != nil || e2 != nil || (player > -1000 && player < 1000) {
			continue
		}
		added = append(added, Pick{Overall: -1, PlayerID: player, TeamID: team})
	}
	if len(added) > 0 {
		a.mu.Lock()
		for _, p := range added {
			if p.Overall < 0 {
				dup, next := false, 1
				for _, q := range mergePicks(a.espnPicks, a.wsPicks) {
					if q.PlayerID == p.PlayerID {
						dup = true
					}
					if q.Overall >= next {
						next = q.Overall + 1
					}
				}
				if dup {
					continue
				}
				// If this team isn't the one due, a pick was missed: use this
				// team's next slot so numbering (and who's on the clock) stays right.
				// The draft room's INIT snapshot later fills the missing player.
				for o := next; o < next+a.format.Teams*2 && o <= total; o++ {
					if a.format.TeamForPick(o) == p.TeamID {
						next = o
						break
					}
				}
				p.Overall = next
			}
			a.wsPicks[p.Overall] = p
		}
		a.live = true
		a.ingest(mergePicks(a.espnPicks, a.wsPicks), a.drafted, true)
		a.mu.Unlock()
		go a.lookupUnknown()
		a.maybeAlertAndAutoAI()
	}
}

// replayDraftLog rebuilds the live draft from the recorded draft-room
// messages, so a restart never loses picks.
func (a *App) replayDraftLog() {
	f, err := os.ReadFile("draftws.log")
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(f), "\n") {
		if i := strings.Index(line, " | "); i >= 0 {
			a.applyDraftData(line[i+3:])
		}
	}
	a.mu.Lock()
	n := len(a.wsPicks)
	a.mu.Unlock()
	if n > 0 {
		log.Printf("restored %d live draft picks from draftws.log", n)
	}
}
