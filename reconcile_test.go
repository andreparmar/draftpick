package main

import "testing"

func TestReconcileManual(t *testing.T) {
	a := &App{byID: map[int]*Player{}}
	a.espnPicks = []Pick{{1, 100, 1}, {2, 200, 2}}
	a.manual = []Pick{
		{Overall: 2, PlayerID: 200, TeamID: 2}, // ESPN confirmed
		{Overall: 3, PlayerID: 999, TeamID: 3}, // misclick; ESPN will pick someone else at #3
		{Overall: 4, PlayerID: 400, TeamID: 4}, // ahead of ESPN, keep
	}
	a.reconcileManual()
	if len(a.manual) != 2 {
		t.Fatalf("want 2 pending marks, got %v", a.manual)
	}
	a.espnPicks = append(a.espnPicks, Pick{3, 300, 3}) // ESPN's real #3 differs from the mark
	a.reconcileManual()
	if len(a.manual) != 1 || a.manual[0].PlayerID != 400 {
		t.Fatalf("misclick should be dropped, got %v", a.manual)
	}
}
