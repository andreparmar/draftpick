package main

import (
	"fmt"
	"math"
	"sort"
)

// Weekly scoring is noisy. cv is a typical week-to-week spread for each
// position as a fraction of the projection (a projected 15-pt WR lands
// anywhere from single digits to 25+). Used only to turn projections into
// a win probability; the projections themselves are ESPN's.
var cv = map[string]float64{"QB": 0.35, "RB": 0.5, "WR": 0.55, "TE": 0.6, "K": 0.45, "D/ST": 0.65}

func variance(p *Player, proj float64) float64 {
	c := cv[p.Pos]
	if c == 0 {
		c = 0.5
	}
	return math.Pow(c*math.Max(proj, 2), 2)
}

func winProb(meanA, varA, meanB, varB float64) float64 {
	sd := math.Sqrt(varA + varB)
	if sd == 0 {
		if meanA > meanB {
			return 1
		}
		return 0
	}
	return 0.5 * (1 + math.Erf((meanA-meanB)/(sd*math.Sqrt2)))
}

var slotOrder = map[int]int{slotQB: 0, slotRB: 1, slotWR: 2, slotTE: 3, slotFlex: 4, slotOP: 5, slotDST: 6, slotK: 7}

type TeamView struct {
	ID          int         `json:"id"`
	Name        string      `json:"name"`
	Logo        string      `json:"logo"`
	Mine        bool        `json:"mine"`
	Record      Record      `json:"record"`
	Lineup      []LineupRow `json:"lineup"` // as set in ESPN, slot order
	Bench       []LineupRow `json:"bench"`
	Proj        float64     `json:"proj"`        // this week, lineup as set
	Best        float64     `json:"best"`        // this week, best possible lineup
	Live        float64     `json:"live"`        // points scored so far this week
	ProjFinal   float64     `json:"projFinal"`   // live points + what is still to come
	StillToPlay int         `json:"stillToPlay"` // starters whose game is not over
	Started     int         `json:"started"`     // starters whose game has kicked off
	ROS         float64     `json:"ros"`         // best starting lineup, rest of season (power)
	PowerRk     int         `json:"powerRk"`     // 1 = strongest roster
}

type SlotDuel struct {
	Slot   string     `json:"slot"`
	Mine   *LineupRow `json:"mine"`
	Theirs *LineupRow `json:"theirs"`
	Edge   float64    `json:"edge"` // my projection minus theirs
}

type Swap struct {
	In      *Player `json:"in"`
	Out     *Player `json:"out"`
	InProj  float64 `json:"inProj"`
	OutProj float64 `json:"outProj"`
	Before  float64 `json:"before"` // win probability now
	After   float64 `json:"after"`  // win probability after the swap
	Why     string  `json:"why"`
}

type Upcoming struct {
	Week    int     `json:"week"`
	Opp     string  `json:"opp"`
	Record  Record  `json:"record"`
	PowerRk int     `json:"powerRk"`
	OppROS  float64 `json:"oppRos"`
}

type Matchup struct {
	Week       int        `json:"week"`
	Me         *TeamView  `json:"me"`
	Opp        *TeamView  `json:"opp"`
	WinProb    float64    `json:"winProb"`
	Duels      []SlotDuel `json:"duels"`
	Swap       *Swap      `json:"swap,omitempty"`
	Favorite   bool       `json:"favorite"`
	Alerts     []string   `json:"alerts"` // e.g. their starter is out
	Upcoming   []Upcoming `json:"upcoming"`
	LiveOn     bool       `json:"liveOn"`
	LiveDetail string     `json:"liveDetail"` // e.g. "Q3 5:12" or "Final"
}

// teamView builds one team's week from its ESPN roster entries.
func teamView(id int, name string, entries []RosterEntry, byID map[int]*Player, f Format, week int, live map[int]float64, rec Record, nfl map[string]NFLGame) *TeamView {
	tv := &TeamView{ID: id, Name: name, Record: rec}
	var all []*Player
	for _, e := range entries {
		p := byID[e.PlayerID]
		if p == nil {
			continue
		}
		all = append(all, p)
		row := LineupRow{Slot: slotLabel[e.SlotID], Player: p, Proj: weekProj(p), Note: note(p, week), Actual: live[p.ID]}
		if e.SlotID == slotBench || e.SlotID == slotIR {
			tv.Bench = append(tv.Bench, row)
			continue
		}
		row.Starting = true
		row.order = slotOrder[e.SlotID]
		rem := 1.0
		if g, ok := nfl[p.Team]; ok {
			rem = g.remaining()
			if g.State != "pre" {
				tv.Started++
			}
			if g.State != "post" {
				tv.StillToPlay++
			}
		} else {
			tv.StillToPlay++
		}
		row.Remaining = rem
		tv.ProjFinal += row.Actual + row.Proj*rem
		tv.Lineup = append(tv.Lineup, row)
		tv.Proj += row.Proj
		tv.Live += row.Actual
	}
	sort.SliceStable(tv.Lineup, func(i, j int) bool {
		if tv.Lineup[i].order != tv.Lineup[j].order {
			return tv.Lineup[i].order < tv.Lineup[j].order
		}
		return tv.Lineup[i].Proj > tv.Lineup[j].Proj
	})
	sort.Slice(tv.Bench, func(i, j int) bool { return tv.Bench[i].Proj > tv.Bench[j].Proj })
	tv.Best = bestLineup(all, f, weekProj)
	tv.ROS = bestLineup(all, f, func(p *Player) float64 { return p.ROS })
	return tv
}

// bestLineup: fixed positions first, then FLEX / SUPERFLEX from the rest.
func bestLineup(ps []*Player, f Format, val func(*Player) float64) float64 {
	sorted := append([]*Player(nil), ps...)
	sort.Slice(sorted, func(i, j int) bool { return val(sorted[i]) > val(sorted[j]) })
	used := map[int]bool{}
	total := 0.0
	take := func(n int, ok func(string) bool) {
		for k := 0; k < n; k++ {
			for _, p := range sorted {
				if !used[p.ID] && ok(p.Pos) {
					used[p.ID] = true
					total += val(p)
					break
				}
			}
		}
	}
	is := func(ps ...string) func(string) bool {
		return func(x string) bool {
			for _, p := range ps {
				if p == x {
					return true
				}
			}
			return false
		}
	}
	for _, pos := range []string{"QB", "RB", "WR", "TE"} {
		take(f.Slots[pos], is(pos))
	}
	take(f.Slots["FLEX"], is("RB", "WR", "TE"))
	take(f.Slots["OP"], is("QB", "RB", "WR", "TE"))
	take(f.Slots["D/ST"], is("D/ST"))
	take(f.Slots["K"], is("K"))
	return total
}

func lineupStats(rows []LineupRow) (mean, vr float64) {
	for _, r := range rows {
		mean += r.Proj
		vr += variance(r.Player, r.Proj)
	}
	return
}

var slotEligible = map[string][]string{"QB": {"QB"}, "RB": {"RB"}, "WR": {"WR"}, "TE": {"TE"}, "FLEX": {"RB", "WR", "TE"}, "SFLX": {"QB", "RB", "WR", "TE"}, "D/ST": {"D/ST"}, "K": {"K"}}

// buildMatchup compares my lineup with this week's opponent and finds the
// single bench swap (if any) that raises my chance to win.
func buildMatchup(me, opp *TeamView, week int) *Matchup {
	m := &Matchup{Week: week, Me: me, Opp: opp}
	mMean, mVar := lineupStats(me.Lineup)
	oMean, oVar := lineupStats(opp.Lineup)
	m.LiveOn = me.Started+opp.Started > 0
	if m.LiveOn {
		// Once games start: what's banked is certain, only the rest can swing.
		mMean, mVar = me.ProjFinal, liveVar(me.Lineup)
		oMean, oVar = opp.ProjFinal, liveVar(opp.Lineup)
	}
	m.WinProb = winProb(mMean, mVar, oMean, oVar)
	m.Favorite = m.WinProb >= 0.5

	for i := 0; i < max(len(me.Lineup), len(opp.Lineup)); i++ {
		d := SlotDuel{}
		if i < len(me.Lineup) {
			d.Mine = &me.Lineup[i]
			d.Slot = me.Lineup[i].Slot
			d.Edge += me.Lineup[i].Proj
		}
		if i < len(opp.Lineup) {
			d.Theirs = &opp.Lineup[i]
			if d.Slot == "" {
				d.Slot = opp.Lineup[i].Slot
			}
			d.Edge -= opp.Lineup[i].Proj
		}
		m.Duels = append(m.Duels, d)
	}

	// Win-probability optimizer: try every healthy bench player in every slot
	// he can fill. Keep a swap only if it raises the chance to win.
	best := m.WinProb
	for i, s := range me.Lineup {
		for _, b := range me.Bench {
			if b.Proj <= 0 || !contains(slotEligible[s.Slot], b.Player.Pos) {
				continue
			}
			nm := mMean - s.Proj + b.Proj
			nv := mVar - variance(s.Player, s.Proj) + variance(b.Player, b.Proj)
			p := winProb(nm, nv, oMean, oVar)
			if p > best+0.003 {
				best = p
				in, out := b.Player, me.Lineup[i].Player
				why := "projects more points"
				if b.Proj < s.Proj {
					if m.Favorite {
						why = "a steadier player protects your lead"
					} else {
						why = "more upside gives you a better shot as the underdog"
					}
				}
				m.Swap = &Swap{In: in, Out: out, InProj: b.Proj, OutProj: s.Proj, Before: m.WinProb, After: p, Why: why}
			}
		}
	}

	for _, r := range opp.Lineup {
		switch {
		case r.Proj == 0 && r.Note != "":
			m.Alerts = append(m.Alerts, fmt.Sprintf("Their %s %s is %s and projects 0", r.Slot, r.Player.Name, r.Note))
		case r.Player.Injury == "QUESTIONABLE" || r.Player.Injury == "DOUBTFUL":
			m.Alerts = append(m.Alerts, fmt.Sprintf("Their %s %s is %s", r.Slot, r.Player.Name, injuryText[r.Player.Injury]))
		}
	}
	if opp.Best-opp.Proj > 1 {
		m.Alerts = append(m.Alerts, fmt.Sprintf("If they fix their lineup they project %.1f, not %.1f", opp.Best, opp.Proj))
	}
	return m
}

func liveVar(rows []LineupRow) float64 {
	v := 0.0
	for _, r := range rows {
		v += variance(r.Player, r.Proj*r.Remaining)
	}
	return v
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// addLeague fills every team's view, power ranks, this week's matchup and
// the next few opponents. Caller holds a.mu.
func (a *App) addLeague(s *Season, f Format) {
	sd := a.sdata
	byTeam := map[int]*TeamView{}
	for id, entries := range a.rosters {
		tv := teamView(id, a.teamNames[id], entries, a.byID, f, a.week, sd.Live, sd.Records[id], a.nfl)
		tv.Mine = id == a.myTeam
		tv.Logo = sd.Logos[id]
		byTeam[id] = tv
		s.Teams = append(s.Teams, tv)
	}
	sort.Slice(s.Teams, func(i, j int) bool { return s.Teams[i].ROS > s.Teams[j].ROS })
	for i, t := range s.Teams {
		t.PowerRk = i + 1
	}
	me := byTeam[a.myTeam]
	if me == nil {
		return
	}
	oppOf := func(g Game) int {
		switch a.myTeam {
		case g.Home:
			return g.Away
		case g.Away:
			return g.Home
		}
		return 0
	}
	for _, g := range sd.Games { // this week
		if o := byTeam[oppOf(g)]; g.Period == sd.Period && o != nil {
			s.Matchup = buildMatchup(me, o, a.week)
			for _, r := range append(append([]LineupRow{}, me.Lineup...), o.Lineup...) {
				if g, ok := a.nfl[r.Player.Team]; ok && g.State == "in" {
					s.Matchup.LiveDetail = g.Detail
					break
				}
			}
			if s.Matchup.LiveOn && s.Matchup.LiveDetail == "" && me.StillToPlay+o.StillToPlay == 0 {
				s.Matchup.LiveDetail = "Final"
			}
		}
	}
	if s.Matchup == nil {
		return
	}
	for _, g := range sd.Games { // the next three weeks
		if o := byTeam[oppOf(g)]; o != nil && g.Period > sd.Period && g.Period <= sd.Period+3 {
			s.Matchup.Upcoming = append(s.Matchup.Upcoming, Upcoming{Week: g.Period, Opp: o.Name, Record: o.Record, PowerRk: o.PowerRk, OppROS: o.ROS})
		}
	}
	if s.Matchup != nil {
		sort.Slice(s.Matchup.Upcoming, func(i, j int) bool { return s.Matchup.Upcoming[i].Week < s.Matchup.Upcoming[j].Week })
	}
}
