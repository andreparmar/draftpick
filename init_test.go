package main

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestParseRealInit(t *testing.T) {
	// Parses a local draft-room capture (draftws.log) for your own league.
	// The league id comes from .env so no real id lives in the code.
	loadDotEnv(".env")
	league, _ := strconv.Atoi(parseLeagueID(os.Getenv("LEAGUE_ID")))
	if league == 0 {
		t.Skip("no LEAGUE_ID in .env")
	}
	data, err := os.ReadFile("draftws.log")
	if err != nil {
		t.Skip("no capture")
	}
	var b64 string
	for _, l := range strings.Split(string(data), "\n") {
		if i := strings.Index(l, "| INIT "); i >= 0 {
			b64 = strings.TrimSpace(l[i+7:])
		}
	}
	ps := parseInit(b64, league, 190)
	for _, p := range ps {
		t.Logf("#%d team %d player %d", p.Overall, p.TeamID, p.PlayerID)
	}
	if len(ps) < 15 {
		t.Fatalf("expected the picks so far, got %d", len(ps))
	}
}
