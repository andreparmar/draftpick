package main

import (
	"fmt"
	"log"
	"sort"
	"time"
)

var slotLabel = map[int]string{slotQB: "QB", slotRB: "RB", slotWR: "WR", slotTE: "TE", slotFlex: "FLEX", slotOP: "SFLX", slotDST: "D/ST", slotK: "K", slotBench: "BN", slotIR: "IR"}

type LineupRow struct {
	Slot      string  `json:"slot"`
	Player    *Player `json:"player"`
	Proj      float64 `json:"proj"`     // projected points this week (0 if out / on bye)
	Starting  bool    `json:"starting"` // currently in your ESPN starting lineup
	Note      string  `json:"note"`
	Actual    float64 `json:"actual"`    // points scored so far this week (live on game days)
	Remaining float64 `json:"remaining"` // share of this player\'s game still to play
	order     int     // slot sort order (not sent)
}

type Waiver struct {
	Add    *Player `json:"add"`
	Status string  `json:"status"` // FREEAGENT or WAIVERS
	Drop   *Player `json:"drop"`
	Gain   float64 `json:"gain"` // rest-of-season points gained
	Why    string  `json:"why"`
}

type FARow struct {
	*Player
	Status string `json:"status"` // FREEAGENT or WAIVERS
}

type Season struct {
	Week    int         `json:"week"`
	Optimal []LineupRow `json:"optimal"`
	Bench   []LineupRow `json:"bench"`
	Current float64     `json:"current"` // projected points of your current ESPN lineup
	Best    float64     `json:"best"`    // projected points of the optimal lineup
	Changes []string    `json:"changes"` // start/sit moves to make in ESPN
	Waivers []Waiver    `json:"waivers"`
	Streams []Waiver    `json:"streams"` // this-week K / D/ST swaps
	TopFA   []FARow     `json:"topFA"`   // best available players, any position
	Teams   []*TeamView `json:"teams"`   // every team, live ESPN rosters
	Matchup *Matchup    `json:"matchup,omitempty"`
	Updated string      `json:"updated"`
}

// weekProj is what a player should score this week: 0 if ruled out.
func weekProj(p *Player) float64 {
	switch p.Injury {
	case "OUT", "INJURY_RESERVE", "SUSPENSION":
		return 0
	case "DOUBTFUL":
		return p.NextWeek * 0.25
	}
	return p.NextWeek
}

// buildSeason: best lineup from my roster, and add/drop moves from free agents.
func buildSeason(roster []RosterEntry, fa map[int]string, byID map[int]*Player, f Format, week int) *Season {
	s := &Season{Week: week, Updated: time.Now().Format("3:04 PM")}
	var mine []*Player
	starting := map[int]bool{}
	for _, e := range roster {
		p := byID[e.PlayerID]
		if p == nil {
			continue
		}
		mine = append(mine, p)
		if e.SlotID != slotBench && e.SlotID != slotIR {
			starting[p.ID] = true
			s.Current += weekProj(p)
		}
	}
	sort.Slice(mine, func(i, j int) bool { return weekProj(mine[i]) > weekProj(mine[j]) })

	// Optimal lineup: fixed positions first, then FLEX/SUPERFLEX from the rest.
	used := map[int]bool{}
	fill := func(label string, n int, ok func(string) bool) {
		for k := 0; k < n; k++ {
			var pick *Player
			for _, p := range mine {
				if !used[p.ID] && ok(p.Pos) {
					pick = p
					break
				}
			}
			row := LineupRow{Slot: label}
			if pick != nil {
				used[pick.ID] = true
				row.Player, row.Proj, row.Starting = pick, weekProj(pick), starting[pick.ID]
				s.Best += row.Proj
				row.Note = note(pick, week)
			}
			s.Optimal = append(s.Optimal, row)
		}
	}
	is := func(ps ...string) func(string) bool {
		return func(x string) bool {
			for _, p := range ps {
				if x == p {
					return true
				}
			}
			return false
		}
	}
	fill("QB", f.Slots["QB"], is("QB"))
	fill("RB", f.Slots["RB"], is("RB"))
	fill("WR", f.Slots["WR"], is("WR"))
	fill("TE", f.Slots["TE"], is("TE"))
	fill("FLEX", f.Slots["FLEX"], is("RB", "WR", "TE"))
	fill("SFLX", f.Slots["OP"], is("QB", "RB", "WR", "TE"))
	fill("D/ST", f.Slots["D/ST"], is("D/ST"))
	fill("K", f.Slots["K"], is("K"))
	for _, p := range mine {
		if !used[p.ID] {
			s.Bench = append(s.Bench, LineupRow{Slot: "BN", Player: p, Proj: weekProj(p), Starting: starting[p.ID], Note: note(p, week)})
		}
	}
	// Moves to make in ESPN: start who should start, bench who shouldn't.
	var toStart, toBench []*Player
	for _, r := range s.Optimal {
		if r.Player != nil && !r.Starting {
			toStart = append(toStart, r.Player)
		}
	}
	for _, r := range s.Bench {
		if r.Starting {
			toBench = append(toBench, r.Player)
		}
	}
	for i, p := range toStart {
		if i < len(toBench) {
			s.Changes = append(s.Changes, fmt.Sprintf("Start %s (%.1f) over %s (%.1f%s)", p.Name, weekProj(p), toBench[i].Name, weekProj(toBench[i]), reason(toBench[i], week)))
		} else {
			s.Changes = append(s.Changes, fmt.Sprintf("Start %s (%.1f)", p.Name, weekProj(p)))
		}
	}
	for _, p := range toBench[min(len(toBench), len(toStart)):] {
		s.Changes = append(s.Changes, fmt.Sprintf("Bench %s (%.1f%s)", p.Name, weekProj(p), reason(p, week)))
	}

	// Waivers, like for like. Keep value = ESPN's rest-of-season projection
	// (it already zeroes weeks an injured player misses, so IR stashes aren't
	// dumped for their injury discount). Starters are never dropped.
	keep := func(p *Player) float64 { return p.ROS }
	bench := map[int]bool{}
	for _, r := range s.Bench {
		bench[r.Player.ID] = true
	}
	byPos := map[string][]*Player{}
	for _, p := range mine {
		byPos[p.Pos] = append(byPos[p.Pos], p)
	}
	for _, ps := range byPos {
		sort.Slice(ps, func(i, j int) bool { return keep(ps[i]) < keep(ps[j]) })
	}
	// weakest bench RB/WR: the default roster spot to free up
	var flexDrop *Player
	for _, p := range mine {
		if bench[p.ID] && (p.Pos == "RB" || p.Pos == "WR") && (flexDrop == nil || keep(p) < keep(flexDrop)) {
			flexDrop = p
		}
	}
	var avail []*Player
	for id, st := range fa {
		if p := byID[id]; p != nil && st != "" {
			avail = append(avail, p)
		}
	}
	sort.Slice(avail, func(i, j int) bool { return avail[i].Adj > avail[j].Adj })
	myKD := map[string]*Player{}
	for _, r := range s.Optimal {
		if r.Player != nil && (r.Slot == "K" || r.Slot == "D/ST") {
			myKD[r.Slot] = r.Player
		}
	}
	for _, p := range avail {
		switch p.Pos {
		case "K", "D/ST":
			// Streaming: swap your K / D/ST for a better one this week.
			if cur := myKD[p.Pos]; cur != nil && weekProj(p)-weekProj(cur) >= 2 && len(s.Streams) < 4 {
				s.Streams = append(s.Streams, Waiver{Add: p, Status: fa[p.ID], Drop: cur, Gain: weekProj(p) - weekProj(cur),
					Why: fmt.Sprintf("%.1f projected this week vs %.1f", weekProj(p), weekProj(cur))})
			}
			continue
		case "QB", "TE":
			// Only worth it if he beats a QB/TE you already roster.
			own := byPos[p.Pos]
			if len(own) == 0 {
				continue
			}
			worst := own[0]
			drop := worst
			if len(own) <= f.Slots[p.Pos] { // your only one: keep him, free a bench RB/WR instead
				drop = flexDrop
			}
			if drop == nil || p.Adj-keep(worst) < 15 || len(s.Waivers) >= 12 {
				continue
			}
			s.Waivers = append(s.Waivers, Waiver{Add: p, Status: fa[p.ID], Drop: drop, Gain: p.Adj - keep(worst),
				Why: fmt.Sprintf("+%.0f rest-of-season pts over %s", p.Adj-keep(worst), worst.Name)})
		default: // RB / WR
			if flexDrop == nil || p.Adj-keep(flexDrop) < 8 || len(s.Waivers) >= 12 {
				continue
			}
			why := fmt.Sprintf("+%.0f rest-of-season pts over %s", p.Adj-keep(flexDrop), flexDrop.Name)
			if p.Trend >= 5 {
				why += fmt.Sprintf(" · trending +%.0f%% rostered", p.Trend)
			}
			if p.Injury != "ACTIVE" {
				why += " · " + p.Injury
			}
			s.Waivers = append(s.Waivers, Waiver{Add: p, Status: fa[p.ID], Drop: flexDrop, Gain: p.Adj - keep(flexDrop), Why: why})
		}
	}
	sort.Slice(s.Waivers, func(i, j int) bool { return s.Waivers[i].Gain > s.Waivers[j].Gain })
	for _, p := range avail { // already sorted best-first
		if len(s.TopFA) == 60 {
			break
		}
		s.TopFA = append(s.TopFA, FARow{p, fa[p.ID]})
	}
	return s
}

var injuryText = map[string]string{"INJURY_RESERVE": "injured reserve", "OUT": "ruled out", "DOUBTFUL": "doubtful", "QUESTIONABLE": "questionable", "SUSPENSION": "suspended", "DAY_TO_DAY": "day-to-day"}

// reason explains a projection in plain words, e.g. ", injured reserve".
func reason(p *Player, week int) string {
	if n := note(p, week); n != "" {
		return ", " + n
	}
	return ""
}

func note(p *Player, week int) string {
	switch {
	case p.Bye == week:
		return "bye week"
	case p.Injury != "ACTIVE":
		if t, ok := injuryText[p.Injury]; ok {
			return t
		}
		return p.Injury
	}
	return ""
}

// seasonLoop keeps rosters and the free-agent pool fresh once the draft is done.
func (a *App) seasonLoop() {
	for {
		a.mu.Lock()
		e, done := a.espn, a.drafted
		a.mu.Unlock()
		if done && e.LeagueID != "" {
			sd, err1 := e.SeasonData()
			fa, err2 := e.FreeAgents()
			if err1 != nil || err2 != nil {
				log.Printf("season refresh: %v %v", err1, err2)
			} else {
				a.mu.Lock()
				a.rosters, a.fa, a.sdata = sd.Rosters, fa, sd
				a.mu.Unlock()
				a.lookupRostered()
			}
		}
		time.Sleep(30 * time.Second)
	}
}

// lookupRostered fetches projections for rostered players outside our pool.
func (a *App) lookupRostered() {
	a.mu.Lock()
	var ids []int
	for _, r := range a.rosters {
		for _, e := range r {
			if a.byID[e.PlayerID] == nil && !a.lookedUp[e.PlayerID] {
				a.lookedUp[e.PlayerID] = true
				ids = append(ids, e.PlayerID)
			}
		}
	}
	e, rt, lw, wk := a.espn, a.rankType, a.lastWeek, a.week
	a.mu.Unlock()
	if len(ids) == 0 {
		return
	}
	ps, err := e.PlayersByID(ids, rt, lw, wk)
	if err != nil {
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
