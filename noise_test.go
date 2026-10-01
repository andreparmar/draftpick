package main

import (
	"math/rand"
	"sort"
	"testing"
)

// Friends who don't follow ESPN's list: each pick is random among the top 6
// available that fit their needs (reaches, favorites, gut calls).
func TestNoisyFriends(t *testing.T) {
	players, _, err := NewESPN(2026, "", "", "").Players("PPR", 17)
	if err != nil {
		t.Skip(err)
	}
	f := Format{Teams: 10, Rounds: 19, Snake: true, PickOrder: []int{4, 9, 8, 6, 7, 1, 5, 3, 2, 10},
		Slots: map[string]int{"QB": 1, "RB": 2, "WR": 2, "TE": 1, "FLEX": 1, "D/ST": 1, "K": 1, "BENCH": 10}}
	me := 3 // your team, slot 8
	rng := rand.New(rand.NewSource(7))
	wins, trials, sumRank := 0, 30, 0
	for tr := 0; tr < trials; tr++ {
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
				p = Rank(players, taken, rosters[me], f, o, me, nil, have)[0].Player
			} else {
				var cands []*Player
				tmp := map[int]bool{}
				for k, v := range taken {
					tmp[k] = v
				}
				for len(cands) < 6 {
					c := simPick(opp, tmp, have[tm], f)
					if c == nil {
						break
					}
					tmp[c.ID] = true
					cands = append(cands, c)
				}
				p = cands[rng.Intn(len(cands))]
			}
			taken[p.ID] = true
			have[tm][p.Pos]++
			rosters[tm] = append(rosters[tm], p)
		}
		mine, rank := lineupPoints(rosters[me], f), 1
		for tm, r := range rosters {
			if tm != me && lineupPoints(r, f) > mine {
				rank++
			}
		}
		if rank == 1 {
			wins++
		}
		sumRank += rank
	}
	t.Logf("unpredictable friends, 30 drafts from slot 8: best roster in %d/30, average finish #%.1f of 10", wins, float64(sumRank)/float64(trials))
}
