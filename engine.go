package main

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

type Player struct {
	ID            int     `json:"id"`
	Name          string  `json:"name"`
	Pos           string  `json:"pos"`
	PosID         int     `json:"-"`
	Team          string  `json:"team"`
	Injury        string  `json:"injury"`
	ROS           float64 `json:"ros"`      // ESPN projected points, current week → end of season
	Adj           float64 `json:"adj"`      // ROS after injury haircut
	NextWeek      float64 `json:"nextWeek"` // projection for the upcoming week
	ActualPPG     float64 `json:"actualPpg"`
	Games         int     `json:"games"`
	Bye           int     `json:"bye"`
	ESPNRank      int     `json:"espnRank"` // what the ESPN draft room shows your friends
	ADP           float64 `json:"adp"`
	Owned         float64 `json:"owned"`
	Trend         float64 `json:"trend"`             // change in % rostered this week (ESPN)
	Outlook       string  `json:"outlook,omitempty"` // ESPN analyst note for this week
	SeasonOutlook string  `json:"seasonOutlook,omitempty"`
	ValueRank     int     `json:"valueRank"` // our overall rank by value over replacement
	VORP          float64 `json:"vorp"`
}

type Pick struct {
	Overall  int `json:"overall"`
	PlayerID int `json:"playerId"`
	TeamID   int `json:"teamId"`
}

type Rec struct {
	*Player
	Score      float64    `json:"score"`
	VONA       float64    `json:"vona"`
	Steal      int        `json:"steal"` // ESPN rank minus our rank (positive = undervalued)
	Tags       []string   `json:"tags"`
	LikelyGo   bool       `json:"likelyGone"`
	LastChance bool       `json:"lastChance"`
	CanWait    bool       `json:"canWait"`
	News       []NewsItem `json:"news,omitempty"`
}

type Format struct {
	Teams     int
	Rounds    int
	Slots     map[string]int // "QB","RB","WR","TE","K","D/ST","FLEX","OP"
	PickOrder []int          // team ids in round-1 order
	Snake     bool
	Owners    map[int]int // ESPN's pick schedule: overall pick → team (handles traded picks)
}

func (f Format) TeamForPick(overall int) int {
	if t, ok := f.Owners[overall]; ok && t > 0 {
		return t
	}
	n := len(f.PickOrder)
	if n == 0 {
		return 0
	}
	r := (overall - 1) / n
	i := (overall - 1) % n
	if f.Snake && r%2 == 1 {
		i = n - 1 - i
	}
	return f.PickOrder[i]
}

// NextPicks returns the overall pick numbers >= from that belong to team.
func (f Format) NextPicks(team, from int) []int {
	var out []int
	for o := from; o <= f.Teams*f.Rounds; o++ {
		if f.TeamForPick(o) == team {
			out = append(out, o)
		}
	}
	return out
}

var flexPos = map[string]bool{"RB": true, "WR": true, "TE": true}

// Replacement computes, per position, the value of the last player who would
// start league-wide. Anything above that is real value (VORP).
func Replacement(all []*Player, f Format) map[string]float64 {
	byPos := map[string][]*Player{}
	for _, p := range all {
		byPos[p.Pos] = append(byPos[p.Pos], p)
	}
	for _, ps := range byPos {
		sort.Slice(ps, func(i, j int) bool { return ps[i].Adj > ps[j].Adj })
	}
	demand := map[string]int{}
	for _, pos := range []string{"QB", "RB", "WR", "TE", "K", "D/ST"} {
		demand[pos] = f.Teams * f.Slots[pos]
	}
	// Hand FLEX (and superflex) spots to the best leftover eligible players.
	fill := func(n int, elig func(string) bool) {
		for k := 0; k < n; k++ {
			best, bestV := "", -1.0
			for pos, ps := range byPos {
				if elig(pos) && demand[pos] < len(ps) && ps[demand[pos]].Adj > bestV {
					best, bestV = pos, ps[demand[pos]].Adj
				}
			}
			if best == "" {
				return
			}
			demand[best]++
		}
	}
	fill(f.Teams*f.Slots["OP"], func(p string) bool { return p == "QB" || flexPos[p] })
	fill(f.Teams*f.Slots["FLEX"], func(p string) bool { return flexPos[p] })

	repl := map[string]float64{}
	for pos, ps := range byPos {
		i := demand[pos]
		if i >= len(ps) {
			i = len(ps) - 1
		}
		if i >= 0 {
			repl[pos] = ps[i].Adj
		}
	}
	return repl
}

// Rank scores every available player for *my* next pick.
func Rank(all []*Player, taken map[int]bool, mine []*Player, f Format, currentPick, myTeam int, prefs map[int]string, teams map[int]map[string]int) []Rec {
	repl := Replacement(all, f)
	deepF := f
	deepF.Teams = f.Teams * 3 / 2
	deep := Replacement(all, deepF)
	for _, p := range all {
		p.VORP = p.Adj - repl[p.Pos]
	}
	byValue := append([]*Player(nil), all...)
	sort.Slice(byValue, func(i, j int) bool { return byValue[i].VORP > byValue[j].VORP })
	for i, p := range byValue {
		p.ValueRank = i + 1
	}

	var avail []*Player
	for _, p := range all {
		if !taken[p.ID] {
			avail = append(avail, p)
		}
	}

	// Opponent model: replay every pick before mine (and up to my following
	// pick). Friends draft off ESPN's list, but skip positions they've already
	// filled (nobody takes a 2nd QB/TE early) and leave K/DST to the end.
	opp := append([]*Player(nil), avail...)
	sort.Slice(opp, func(i, j int) bool { return espnOrder(opp[i]) < espnOrder(opp[j]) })

	next := f.NextPicks(myTeam, currentPick)
	goneBefore, goneByNext := map[int]bool{}, map[int]bool{}
	if len(next) > 0 {
		end := next[0]
		if len(next) > 1 {
			end = next[1]
		}
		sim := map[int]map[string]int{}
		used := map[int]bool{}
		for o := currentPick; o < end; o++ {
			t := f.TeamForPick(o)
			if t == myTeam {
				continue
			}
			if sim[t] == nil {
				sim[t] = map[string]int{}
				for k, v := range teams[t] {
					sim[t][k] = v
				}
			}
			p := simPick(opp, used, sim[t], f)
			if p == nil {
				break
			}
			used[p.ID] = true
			sim[t][p.Pos]++
			if o < next[0] {
				goneBefore[p.ID] = true
			}
			goneByNext[p.ID] = true
		}
	}

	// Roster needs.
	have := map[string]int{}
	for _, p := range mine {
		have[p.Pos]++
	}
	flexUsed := 0
	for pos := range flexPos {
		if extra := have[pos] - f.Slots[pos]; extra > 0 {
			flexUsed += extra
		}
	}
	roundsLeft := f.Rounds - len(mine)
	openStarters := 0
	for _, pos := range []string{"QB", "RB", "WR", "TE", "K", "D/ST"} {
		if d := f.Slots[pos] - have[pos]; d > 0 {
			openStarters += d
		}
	}
	if d := f.Slots["FLEX"] - flexUsed; d > 0 {
		openStarters += d
	}
	kdstNeeded := 0
	for _, pos := range []string{"K", "D/ST"} {
		if f.Slots[pos] > have[pos] {
			kdstNeeded += f.Slots[pos] - have[pos]
		}
	}

	recs := make([]Rec, 0, len(avail))
	for _, p := range avail {
		r := Rec{Player: p}
		// VONA: how much worse is the best same-position player likely left at my following pick?
		nextBest := repl[p.Pos]
		for _, q := range avail {
			if q.ID != p.ID && q.Pos == p.Pos && !goneByNext[q.ID] && q.Adj > nextBest {
				nextBest = q.Adj
			}
		}
		r.VONA = p.Adj - nextBest

		// Starters are scored against the starter cutoff (VORP); bench picks
		// against the deeper "rostered" cutoff so depth is ranked sensibly.
		mult, need, bench := 1.0, "", false
		switch {
		case have[p.Pos] < f.Slots[p.Pos]:
			need = fmt.Sprintf("fills your %s%d slot", p.Pos, have[p.Pos]+1)
		case flexPos[p.Pos] && flexUsed < f.Slots["FLEX"]:
			mult, need = 0.9, "fills your FLEX"
		case p.Pos == "K" || p.Pos == "D/ST":
			mult = -1 // never a second kicker / defense
		case p.Pos == "RB" || p.Pos == "WR":
			mult, need, bench = 0.6, "bench depth", true
		default:
			mult, need, bench = 0.3, "backup", true
			if have[p.Pos] > f.Slots[p.Pos] {
				mult = -1 // one backup QB/TE is plenty; bench spots go to RB/WR upside
			}
		}

		// One scale for everyone: points over the last rostered player at the
		// position, plus a scarcity bonus when it's a starter we still need.
		value := p.Adj - deep[p.Pos]
		if need == "fills your FLEX" {
			// FLEX is won on raw points: compare RB/WR/TE on one shared baseline.
			value = p.Adj - math.Max(deep["RB"], deep["WR"])
		}
		if !bench {
			value += 0.35 * math.Max(r.VONA, 0)
		} else {
			if roundsLeft <= openStarters {
				mult *= 0.1 // out of rounds: starters only
			}
		}
		switch {
		case mult < 0:
			r.Score = -1e9
		case value >= 0:
			r.Score = value * mult
		default:
			r.Score = value * (2 - math.Min(mult, 1))
		}
		if (p.Pos == "K" || p.Pos == "D/ST") && mult > 0 {
			if roundsLeft > kdstNeeded {
				need = "wait — take K/DST in your last rounds"
				r.Score = -1e6 + value
			} else {
				need = "take your " + p.Pos + " now (last rounds)"
				r.Score = 1e6 + value
			}
		}

		r.Steal = espnOrder(p) - p.ValueRank
		if (p.ESPNRank == 0 && p.ADP == 0) || p.Pos == "K" || p.Pos == "D/ST" {
			r.Steal = 0
		}
		if need != "" {
			r.Tags = append(r.Tags, need)
		}
		if r.Steal >= 15 && p.VORP > 0 {
			r.Tags = append(r.Tags, fmt.Sprintf("UNDERVALUED: ESPN #%d, our #%d", espnOrder(p), p.ValueRank))
		} else if r.Steal <= -20 {
			r.Tags = append(r.Tags, fmt.Sprintf("overhyped: ESPN #%d, our #%d", espnOrder(p), p.ValueRank))
		}
		if r.VONA > 25 {
			r.Tags = append(r.Tags, fmt.Sprintf("position runs dry: next %s at your following pick is %.0f pts worse", p.Pos, r.VONA))
		}
		if p.Injury != "ACTIVE" {
			r.Tags = append(r.Tags, "injury: "+p.Injury)
		}
		switch prefs[p.ID] {
		case "want":
			r.Score += math.Abs(r.Score)*0.2 + 10
			r.Tags = append([]string{"★ on your list"}, r.Tags...)
		case "avoid":
			r.Score = -1e10
			r.Tags = append([]string{"🚫 avoided"}, r.Tags...)
		}
		r.LikelyGo = goneBefore[p.ID]
		r.LastChance = !r.LikelyGo && goneByNext[p.ID]
		// Draft the guy who won't come back. If ESPN's list (what friends use)
		// says he'll still be there at my following pick, he can wait.
		if len(next) >= 2 && !goneByNext[p.ID] && !bench && mult > 0 && r.Score > 0 && r.Score < 1e5 {
			r.CanWait = true
			r.Score *= 0.8
			r.Tags = append(r.Tags, fmt.Sprintf("can wait: likely still there at #%d", next[1]))
		}
		if p.Trend >= 5 {
			r.Tags = append(r.Tags, fmt.Sprintf("trending: +%.0f%% rostered this week", p.Trend))
		}
		if r.LastChance && p.VORP > 0 {
			r.Tags = append(r.Tags, "last chance: likely gone by your following pick")
		}
		recs = append(recs, r)
	}
	// Players predicted to be gone before my pick sink below the ones I can
	// actually get (they still show, dimmed, in case they slip).
	sort.Slice(recs, func(i, j int) bool {
		if recs[i].LikelyGo != recs[j].LikelyGo {
			return !recs[i].LikelyGo
		}
		return recs[i].Score > recs[j].Score
	})
	return recs
}

// espnOrder is where a player sits on ESPN's default draft list.
func espnOrder(p *Player) int {
	if p.ESPNRank > 0 {
		return p.ESPNRank
	}
	if p.ADP > 0 {
		return int(p.ADP + 0.5)
	}
	return 999
}

// simPick is how a casual friend picks: best on ESPN's list that fits a need.
func simPick(opp []*Player, used map[int]bool, have map[string]int, f Format) *Player {
	made := 0
	for _, v := range have {
		made += v
	}
	roundsLeft := f.Rounds - made
	kdst := 0
	for _, pos := range []string{"K", "D/ST"} {
		if d := f.Slots[pos] - have[pos]; d > 0 {
			kdst += d
		}
	}
	var fallback *Player
	for _, p := range opp {
		if used[p.ID] {
			continue
		}
		if fallback == nil {
			fallback = p
		}
		switch p.Pos {
		case "K", "D/ST":
			if have[p.Pos] >= f.Slots[p.Pos] || roundsLeft > kdst+1 {
				continue
			}
		case "QB", "TE":
			if have[p.Pos] >= f.Slots[p.Pos] && made < f.Rounds/2 {
				continue
			}
		}
		return p
	}
	return fallback
}

type PlanPick struct {
	Overall int       `json:"overall"`
	Round   int       `json:"round"`
	Player  *Player   `json:"player"`
	Alts    []*Player `json:"alts"`
	Role    string    `json:"role"` // why this pick: which slot it fills
}

// Plan plays the rest of the draft forward: friends pick like simPick, I take
// my top recommendation each turn. The result is the position order that
// gets the most value, and who's realistically there at each of my picks.
func Plan(all []*Player, taken map[int]bool, mine []*Player, f Format, currentPick, myTeam int, prefs map[int]string, teams map[int]map[string]int, maxPicks int) []PlanPick {
	tk := map[int]bool{}
	for id := range taken {
		tk[id] = true
	}
	my := append([]*Player(nil), mine...)
	th := map[int]map[string]int{}
	for t, m := range teams {
		th[t] = map[string]int{}
		for k, v := range m {
			th[t][k] = v
		}
	}
	opp := append([]*Player(nil), all...)
	sort.Slice(opp, func(i, j int) bool { return espnOrder(opp[i]) < espnOrder(opp[j]) })

	var out []PlanPick
	for o := currentPick; o <= f.Teams*f.Rounds && len(out) < maxPicks; o++ {
		t := f.TeamForPick(o)
		if t == myTeam {
			recs := Rank(all, tk, my, f, o, myTeam, prefs, th)
			if len(recs) == 0 {
				break
			}
			p := recs[0].Player
			pp := PlanPick{Overall: o, Round: (o-1)/f.Teams + 1, Player: p}
			if len(recs[0].Tags) > 0 {
				for _, t := range recs[0].Tags {
					if strings.HasPrefix(t, "fills") || strings.HasPrefix(t, "bench") || strings.HasPrefix(t, "backup") || strings.HasPrefix(t, "take your") {
						pp.Role = t
						break
					}
				}
			}
			for _, r := range recs[1:min(4, len(recs))] {
				pp.Alts = append(pp.Alts, r.Player)
			}
			out = append(out, pp)
			tk[p.ID] = true
			my = append(my, p)
			continue
		}
		if th[t] == nil {
			th[t] = map[string]int{}
		}
		p := simPick(opp, tk, th[t], f)
		if p == nil {
			break
		}
		tk[p.ID] = true
		th[t][p.Pos]++
	}
	return out
}
