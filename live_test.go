package main

import (
	"strings"
	"testing"
)

func testApp() *App {
	a := &App{prefs: map[int]string{}, lookedUp: map[int]bool{}, teamNames: map[int]string{1: "A", 2: "B", 3: "Me"}, myTeam: 3}
	a.format = Format{Teams: 3, Rounds: 4, Snake: true, PickOrder: []int{1, 2, 3},
		Slots: map[string]int{"QB": 1, "RB": 2, "WR": 2, "TE": 1, "FLEX": 1, "K": 1, "D/ST": 1}}
	a.byID = map[int]*Player{}
	a.espn = NewESPN(2026, "123", "", "")
	add := func(id int, name, pos string, rank int, ros float64) {
		p := &Player{ID: id, Name: name, Pos: pos, ESPNRank: rank, ROS: ros, Adj: ros, Injury: "ACTIVE"}
		a.players = append(a.players, p)
		a.byID[id] = p
	}
	add(1, "RB One", "RB", 1, 300)
	add(2, "RB Two", "RB", 2, 280)
	add(3, "WR One", "WR", 3, 270)
	add(4, "RB Three", "RB", 4, 250)
	add(5, "QB One", "QB", 5, 260)
	add(6, "QB Two", "QB", 6, 255)
	add(7, "WR Two", "WR", 7, 240)
	add(8, "TE One", "TE", 8, 200)
	add(9, "WR Three", "WR", 9, 230)
	return a
}

func TestLiveFlow(t *testing.T) {
	a := testApp()
	a.prefs[2] = "want"

	// I hand-mark RB One as mine at pick #3, but in ESPN I actually take WR One.
	a.manual = []Pick{{Overall: 3, PlayerID: 1, TeamID: 3}}
	a.ingest([]Pick{{1, 4, 1}, {2, 2, 2}}, false, true) // B takes my ★ target
	a.ingest([]Pick{{1, 4, 1}, {2, 2, 2}, {3, 3, 3}}, false, true)

	s := a.snapshot()
	if len(s.Mine) != 1 || s.Mine[0].Name != "WR One" {
		t.Fatalf("roster should follow ESPN's real pick, got %+v", s.Mine)
	}
	for _, r := range s.Recs {
		if r.ID == 1 {
			goto ok
		}
	}
	t.Fatal("RB One (misclicked) should be available again")
ok:
	var kinds []string
	for _, e := range a.events {
		kinds = append(kinds, e.Kind+": "+e.Text)
	}
	joined := strings.Join(kinds, "\n")
	if !strings.Contains(joined, "target: ⚠ Your ★ target RB Two went to B") || !strings.Contains(joined, "mine: ✓ WR One") {
		t.Fatalf("missing alerts:\n%s", joined)
	}
	// Snake: team 3 picks again at #4; RB run = picks 1,2 RB + more
	a.ingest([]Pick{{1, 4, 1}, {2, 2, 2}, {3, 3, 3}, {4, 1, 3}, {5, 9, 2}}, false, true)
	t.Logf("alerts:\n%s", joined)
}

func TestSimSkipsFilledQB(t *testing.T) {
	a := testApp()
	// Team 1 already has a QB; ESPN's list puts QB Two next, but they should take the WR.
	used := map[int]bool{1: true, 2: true, 3: true, 4: true, 5: true}
	opp := []*Player{a.byID[6], a.byID[7]}
	p := simPick(opp, used, map[string]int{"QB": 1}, a.format)
	if p.Name != "WR Two" {
		t.Fatalf("filled-QB team should skip QB Two, took %s", p.Name)
	}
}

func TestRunAlert(t *testing.T) {
	a := testApp()
	a.ingest([]Pick{{1, 1, 1}, {2, 2, 2}, {3, 3, 3}, {4, 4, 3}}, false, true)
	if len(a.events) == 0 || a.events[len(a.events)-1].Kind != "run" {
		t.Fatalf("want RB run alert, got %+v", a.events)
	}
	t.Log(a.events[len(a.events)-1].Text)
}
