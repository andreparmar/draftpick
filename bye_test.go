package main

import "testing"

func TestTeamByes(t *testing.T) {
	e := NewESPN(2026, "", "", "")
	ps, week, err := e.Players("PPR", 17)
	if err != nil {
		t.Skip(err)
	}
	for _, p := range ps {
		if p.Name == "A.J. Brown" || p.Name == "Amon-Ra St. Brown" {
			t.Logf("%s (%s) bye week %d, current week %d, injury %s", p.Name, p.Team, p.Bye, week, p.Injury)
		}
	}
	t.Logf("team byes: %v", e.byes)
}
