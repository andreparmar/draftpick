package main

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestDraftRoomPick(t *testing.T) {
	a := testApp()
	a.wsPicks = map[int]Pick{}
	a.ready = true
	dst := &Player{ID: -16016, Name: "Vikings D/ST", Pos: "D/ST", Injury: "ACTIVE", Adj: 90}
	a.players = append(a.players, dst)
	a.byID[dst.ID] = dst
	draftWSLog, _ = os.OpenFile(os.DevNull, os.O_WRONLY, 0) // never touch the real recording
	req := httptest.NewRequest("POST", "/api/draftws", strings.NewReader(`{"url":"wss://x","data":"SELECTING 1 60000\nSELECTED 1 -16016 8 {ABC}"}`))
	a.handleDraftWS(httptest.NewRecorder(), req)
	a.mu.Lock()
	// ESPN's API poll comes back empty mid-draft; the live pick must survive.
	a.ingest(mergePicks(nil, a.wsPicks), false, true)
	a.mu.Unlock()
	s := a.snapshot()
	if s.CurrentPick != 2 || len(s.Board) != 1 || s.Board[0].Name != "Vikings D/ST" {
		t.Fatalf("live pick not tracked: pick=%d board=%+v", s.CurrentPick, s.Board)
	}
}
