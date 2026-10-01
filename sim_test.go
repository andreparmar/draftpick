package main

import (
	"sort"
	"testing"
)

// lineupPoints = best starting lineup's rest-of-season projection.
func lineupPoints(ps []*Player, f Format) float64 {
	sort.Slice(ps, func(i, j int) bool { return ps[i].Adj > ps[j].Adj })
	used := map[int]bool{}
	total := 0.0
	take := func(ok func(string) bool) {
		for _, p := range ps {
			if !used[p.ID] && ok(p.Pos) {
				used[p.ID] = true
				total += p.Adj
				return
			}
		}
	}
	for _, pos := range []string{"QB", "RB", "WR", "TE", "K", "D/ST"} {
		for i := 0; i < f.Slots[pos]; i++ {
			take(func(x string) bool { return x == pos })
		}
	}
	for i := 0; i < f.Slots["FLEX"]; i++ {
		take(func(x string) bool { return x == "RB" || x == "WR" || x == "TE" })
	}
	return total
}

func TestFullDraftSim(t *testing.T) {
	players, _, err := NewESPN(2026, "", "", "").Players("PPR", 17)
	if err != nil {
		t.Skip(err)
	}
	f := Format{Teams: 10, Rounds: 19, Snake: true, PickOrder: []int{4, 9, 8, 6, 7, 1, 5, 3, 2, 10},
		Slots: map[string]int{"QB": 1, "RB": 2, "WR": 2, "TE": 1, "FLEX": 1, "D/ST": 1, "K": 1, "BENCH": 10}}
	for slot := 0; slot < 10; slot++ {
		me := f.PickOrder[slot]
		taken := map[int]bool{}
		rosters := map[int][]*Player{}
		have := map[int]map[string]int{}
		opp := append([]*Player(nil), players...)
		sort.Slice(opp, func(i, j int) bool { return espnOrder(opp[i]) < espnOrder(opp[j]) })
		for o := 1; o <= f.Teams*f.Rounds; o++ {
			tm := f.TeamForPick(o)
			if have[tm] == nil {
				have[tm] = map[string]int{}
			}
			var p *Player
			if tm == me {
				recs := Rank(players, taken, rosters[me], f, o, me, nil, have)
				p = recs[0].Player
			} else {
				p = simPick(opp, taken, have[tm], f)
			}
			taken[p.ID] = true
			have[tm][p.Pos]++
			rosters[tm] = append(rosters[tm], p)
		}
		mine := lineupPoints(rosters[me], f)
		rank, best := 1, 0.0
		for tm, r := range rosters {
			v := lineupPoints(r, f)
			if tm != me && v > mine {
				rank++
			}
			if tm != me && v > best {
				best = v
			}
		}
		t.Logf("draft slot %2d: our lineup %4.0f pts | best rival %4.0f | we finish #%d of 10", slot+1, mine, best, rank)
	}
}
