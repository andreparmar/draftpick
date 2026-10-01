package main

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const espnBase = "https://lm-api-reads.fantasy.espn.com/apis/v3/games/ffl/seasons/%d/segments/0"

// ESPN position ids → label.
var posName = map[int]string{1: "QB", 2: "RB", 3: "WR", 4: "TE", 5: "K", 16: "D/ST"}

// ESPN lineup slot ids.
const (
	slotQB    = 0
	slotRB    = 2
	slotWR    = 4
	slotTE    = 6
	slotOP    = 7 // superflex (QB/RB/WR/TE)
	slotBench = 20
	slotIR    = 21
	slotFlex  = 23 // RB/WR/TE
	slotDST   = 16
	slotK     = 17
)

var proTeam = map[int]string{
	0: "FA", 1: "ATL", 2: "BUF", 3: "CHI", 4: "CIN", 5: "CLE", 6: "DAL", 7: "DEN", 8: "DET",
	9: "GB", 10: "TEN", 11: "IND", 12: "KC", 13: "LV", 14: "LAR", 15: "MIA", 16: "MIN",
	17: "NE", 18: "NO", 19: "NYG", 20: "NYJ", 21: "PHI", 22: "ARI", 23: "PIT", 24: "LAC",
	25: "SF", 26: "SEA", 27: "TB", 28: "WSH", 29: "CAR", 30: "JAX", 33: "BAL", 34: "HOU",
}

type ESPN struct {
	Season   int
	LeagueID string
	S2, SWID string
	http     *http.Client
	byes     map[string]int // NFL team → upcoming bye week (from the full player pool)
}

func NewESPN(season int, leagueID, s2, swid string) *ESPN {
	return &ESPN{Season: season, LeagueID: leagueID, S2: s2, SWID: swid, http: &http.Client{Timeout: 20 * time.Second}}
}

func (e *ESPN) get(url, filter string, out any) error {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36")
	if filter != "" {
		req.Header.Set("X-Fantasy-Filter", filter)
	}
	if e.S2 != "" && e.SWID != "" {
		req.Header.Set("Cookie", fmt.Sprintf("espn_s2=%s; SWID=%s", e.S2, e.SWID))
	}
	resp, err := e.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return fmt.Errorf("ESPN %d: league is private — set ESPN_S2 and SWID cookies", resp.StatusCode)
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("ESPN %d: %.200s", resp.StatusCode, body)
	}
	return json.Unmarshal(body, out)
}

// ---- league ----

type espnLeague struct {
	Settings struct {
		Name           string `json:"name"`
		Size           int    `json:"size"`
		RosterSettings struct {
			LineupSlotCounts map[string]int `json:"lineupSlotCounts"`
		} `json:"rosterSettings"`
		DraftSettings struct {
			PickOrder        []int  `json:"pickOrder"`
			Type             string `json:"type"`
			Date             int64  `json:"date"`
			TimePerSelection int    `json:"timePerSelection"`
			OrderType        string `json:"orderType"`
		} `json:"draftSettings"`
		ScoringSettings struct {
			ScoringItems []struct {
				StatID int     `json:"statId"`
				Points float64 `json:"points"`
			} `json:"scoringItems"`
		} `json:"scoringSettings"`
	} `json:"settings"`
	Teams []struct {
		ID       int      `json:"id"`
		Name     string   `json:"name"`
		Location string   `json:"location"`
		Nickname string   `json:"nickname"`
		Abbrev   string   `json:"abbrev"`
		Owners   []string `json:"owners"`
	} `json:"teams"`
	DraftDetail struct {
		Drafted    bool `json:"drafted"`
		InProgress bool `json:"inProgress"`
		Picks      []struct {
			OverallPickNumber int `json:"overallPickNumber"`
			PlayerID          int `json:"playerId"`
			TeamID            int `json:"teamId"`
		} `json:"picks"`
	} `json:"draftDetail"`
}

func (e *ESPN) leagueURL(views ...string) string {
	u := fmt.Sprintf(espnBase, e.Season) + "/leagues/" + e.LeagueID + "?"
	for i, v := range views {
		if i > 0 {
			u += "&"
		}
		u += "view=" + v
	}
	return u
}

func (e *ESPN) League() (*espnLeague, error) {
	var l espnLeague
	err := e.get(e.leagueURL("mSettings", "mTeam", "mDraftDetail"), "", &l)
	return &l, err
}

func (e *ESPN) DraftPicks() (picks []Pick, owners map[int]int, drafted, inProgress bool, err error) {
	var l espnLeague
	if err := e.get(e.leagueURL("mDraftDetail"), "", &l); err != nil {
		return nil, nil, false, false, err
	}
	owners = map[int]int{}
	for _, p := range l.DraftDetail.Picks {
		owners[p.OverallPickNumber] = p.TeamID
		if p.PlayerID > 0 {
			picks = append(picks, Pick{Overall: p.OverallPickNumber, PlayerID: p.PlayerID, TeamID: p.TeamID})
		}
	}
	sort.Slice(picks, func(i, j int) bool { return picks[i].Overall < picks[j].Overall })
	return picks, owners, l.DraftDetail.Drafted, l.DraftDetail.InProgress, nil
}

// ---- players ----

type espnPlayers struct {
	Players []struct {
		ID       int `json:"id"`
		OnTeamID int `json:"onTeamId"`
		Player   struct {
			FullName          string `json:"fullName"`
			DefaultPositionID int    `json:"defaultPositionId"`
			ProTeamID         int    `json:"proTeamId"`
			InjuryStatus      string `json:"injuryStatus"`
			Ownership         struct {
				ADP           float64 `json:"averageDraftPosition"`
				PercentOwned  float64 `json:"percentOwned"`
				PercentChange float64 `json:"percentChange"`
			} `json:"ownership"`
			DraftRanksByRankType map[string]struct {
				Rank int `json:"rank"`
			} `json:"draftRanksByRankType"`
			SeasonOutlook string `json:"seasonOutlook"`
			Outlooks      struct {
				ByWeek map[string]string `json:"outlooksByWeek"`
			} `json:"outlooks"`
			Stats []struct {
				SeasonID        int     `json:"seasonId"`
				ScoringPeriodID int     `json:"scoringPeriodId"`
				StatSourceID    int     `json:"statSourceId"` // 0 actual, 1 projected
				StatSplitTypeID int     `json:"statSplitTypeId"`
				AppliedTotal    float64 `json:"appliedTotal"`
			} `json:"stats"`
		} `json:"player"`
	} `json:"players"`
}

// Players fetches the player pool with weekly projections and actuals.
func (e *ESPN) Players(rankType string, lastWeek int) ([]*Player, int, error) {
	filter := `{"players":{"filterStatus":{"value":["FREEAGENT","WAIVERS","ONTEAM"]},"limit":600,"sortPercOwned":{"sortPriority":1,"sortAsc":false}}}`
	return e.fetchPlayers(filter, rankType, lastWeek, 0)
}

// PlayersByID looks up specific players (e.g. a deep-sleeper pick that isn't
// in the top-600 pool) so every drafted player shows on the board and roster.
func (e *ESPN) PlayersByID(ids []int, rankType string, lastWeek, week int) ([]*Player, error) {
	b, _ := json.Marshal(ids)
	filter := fmt.Sprintf(`{"players":{"filterIds":{"value":%s},"limit":%d,"sortPercOwned":{"sortPriority":1,"sortAsc":false}}}`, b, len(ids)+5)
	ps, _, err := e.fetchPlayers(filter, rankType, lastWeek, week)
	return ps, err
}

func (e *ESPN) fetchPlayers(filter, rankType string, lastWeek, week int) ([]*Player, int, error) {
	// The league-scoped endpoint only returns this week's projection, not the
	// week-by-week breakdown we need, so read ESPN's scoring defaults instead
	// (3 = PPR, 1 = standard).
	def := 3
	if rankType == "STANDARD" {
		def = 1
	}
	var raw espnPlayers
	err := e.get(fmt.Sprintf(espnBase, e.Season)+fmt.Sprintf("/leaguedefaults/%d?view=kona_player_info", def), filter, &raw)
	if err != nil {
		return nil, 0, err
	}

	// Current week = first week with no actual stats yet.
	cur := 1
	if week > 0 {
		cur = week
	}
	for _, rp := range raw.Players {
		if week > 0 {
			break
		}
		for _, s := range rp.Player.Stats {
			if s.SeasonID == e.Season && s.StatSourceID == 0 && s.StatSplitTypeID == 1 && s.ScoringPeriodID+1 > cur {
				cur = s.ScoringPeriodID + 1
			}
		}
	}

	var out []*Player
	weekly := map[*Player]map[int]float64{}
	for _, rp := range raw.Players {
		pl := rp.Player
		pos, ok := posName[pl.DefaultPositionID]
		if !ok {
			continue
		}
		p := &Player{
			ID: rp.ID, Name: pl.FullName, Pos: pos, PosID: pl.DefaultPositionID,
			Team: proTeam[pl.ProTeamID], Injury: pl.InjuryStatus,
			ADP: pl.Ownership.ADP, Owned: pl.Ownership.PercentOwned, Trend: pl.Ownership.PercentChange,
		}
		if p.Injury == "" {
			p.Injury = "ACTIVE"
		}
		if r, ok := pl.DraftRanksByRankType[rankType]; ok && r.Rank > 0 {
			p.ESPNRank = r.Rank
		}
		weekProj := map[int]float64{}
		var actualSum float64
		for _, s := range pl.Stats {
			if s.SeasonID != e.Season || s.StatSplitTypeID != 1 {
				continue
			}
			switch s.StatSourceID {
			case 1:
				weekProj[s.ScoringPeriodID] = s.AppliedTotal
			case 0:
				actualSum += s.AppliedTotal
				p.Games++
			}
		}
		for w := cur; w <= lastWeek; w++ {
			p.ROS += weekProj[w]
		}
		weekly[p] = weekProj
		p.NextWeek = weekProj[cur]
		if p.Games > 0 {
			p.ActualPPG = actualSum / float64(p.Games)
		}
		p.Outlook = pl.Outlooks.ByWeek[strconv.Itoa(cur)]
		p.SeasonOutlook = pl.SeasonOutlook
		if len(p.SeasonOutlook) > 600 {
			p.SeasonOutlook = p.SeasonOutlook[:600] + "…"
		}
		p.Adj = p.ROS * injuryFactor(p.Injury)
		out = append(out, p)
	}
	// A bye is a TEAM property: the week nearly every one of the team's
	// regular contributors projects 0. (One player's 0 is usually an injury.)
	if len(out) > 100 {
		byes := map[string]int{}
		type tally struct{ zero, total map[int]int }
		t := map[string]*tally{}
		for p, wp := range weekly {
			peak := 0.0
			for _, v := range wp {
				peak = math.Max(peak, v)
			}
			if peak < 3 || p.Injury != "ACTIVE" {
				continue
			}
			if t[p.Team] == nil {
				t[p.Team] = &tally{map[int]int{}, map[int]int{}}
			}
			for w := cur; w <= lastWeek; w++ {
				if v, ok := wp[w]; ok {
					t[p.Team].total[w]++
					if v == 0 {
						t[p.Team].zero[w]++
					}
				}
			}
		}
		for team, tl := range t {
			for w, n := range tl.total {
				if n >= 3 && float64(tl.zero[w]) >= 0.8*float64(n) {
					byes[team] = w
				}
			}
		}
		e.byes = byes
	}
	for _, p := range out {
		p.Bye = e.byes[p.Team]
	}
	return out, cur, nil
}

// injuryFactor discounts rest-of-season value. ESPN's weekly projections already
// zero out weeks a player is ruled out, so these are modest risk haircuts.
func injuryFactor(s string) float64 {
	switch s {
	case "INJURY_RESERVE":
		return 0.6
	case "SUSPENSION":
		return 0.7
	case "OUT":
		return 0.9
	case "DOUBTFUL":
		return 0.95
	case "QUESTIONABLE", "DAY_TO_DAY":
		return 0.98
	}
	return 1
}

func slotCount(m map[string]int, id int) int { return m[strconv.Itoa(id)] }

// ---- player news (ESPN's fantasy news feed) ----

type NewsItem struct {
	Headline  string `json:"headline"`
	Story     string `json:"story"`
	Published string `json:"published"`
}

var tagRe = regexp.MustCompile(`<[^>]*>`)

func (e *ESPN) News(playerID int) ([]NewsItem, error) {
	var raw struct {
		Feed []struct {
			Headline    string `json:"headline"`
			Story       string `json:"story"`
			Description string `json:"description"`
			Published   string `json:"published"`
		} `json:"feed"`
	}
	u := fmt.Sprintf("https://site.api.espn.com/apis/fantasy/v2/games/ffl/news/players?limit=6&playerId=%d", playerID)
	if err := e.get(u, "", &raw); err != nil {
		return nil, err
	}
	var out []NewsItem
	for _, f := range raw.Feed {
		story := strings.TrimSpace(html.UnescapeString(tagRe.ReplaceAllString(f.Story, " ")))
		story = strings.Join(strings.Fields(story), " ")
		if story == "" {
			story = f.Description
		}
		if len(story) > 320 {
			story = story[:320] + "…"
		}
		out = append(out, NewsItem{Headline: f.Headline, Story: story, Published: f.Published})
		if len(out) == 3 {
			break
		}
	}
	return out, nil
}

// ---- in-season: rosters and free agents ----

type RosterEntry struct {
	PlayerID int `json:"playerId"`
	SlotID   int `json:"slotId"` // ESPN lineup slot (20 bench, 21 IR)
}

func (e *ESPN) Rosters() (map[int][]RosterEntry, error) {
	var raw struct {
		Teams []struct {
			ID     int `json:"id"`
			Roster struct {
				Entries []struct {
					PlayerID     int `json:"playerId"`
					LineupSlotID int `json:"lineupSlotId"`
				} `json:"entries"`
			} `json:"roster"`
		} `json:"teams"`
	}
	if err := e.get(e.leagueURL("mRoster"), "", &raw); err != nil {
		return nil, err
	}
	out := map[int][]RosterEntry{}
	for _, t := range raw.Teams {
		for _, en := range t.Roster.Entries {
			out[t.ID] = append(out[t.ID], RosterEntry{en.PlayerID, en.LineupSlotID})
		}
	}
	return out, nil
}

// FreeAgents returns available players in this league → "FREEAGENT" or "WAIVERS".
func (e *ESPN) FreeAgents() (map[int]string, error) {
	filter := `{"players":{"filterStatus":{"value":["FREEAGENT","WAIVERS"]},"limit":400,"sortPercOwned":{"sortPriority":1,"sortAsc":false}}}`
	var raw struct {
		Players []struct {
			ID     int    `json:"id"`
			Status string `json:"status"`
		} `json:"players"`
	}
	if err := e.get(e.leagueURL("kona_player_info"), filter, &raw); err != nil {
		return nil, err
	}
	out := map[int]string{}
	for _, p := range raw.Players {
		out[p.ID] = p.Status
	}
	return out, nil
}

// ---- in-season: whole league (rosters, schedule, records, live points) ----

type Game struct {
	Period  int     `json:"period"`
	Home    int     `json:"home"`
	Away    int     `json:"away"` // 0 = bye
	HomePts float64 `json:"homePts"`
	AwayPts float64 `json:"awayPts"`
}

type Record struct {
	W  int     `json:"w"`
	L  int     `json:"l"`
	T  int     `json:"t"`
	PF float64 `json:"pf"`
	PA float64 `json:"pa"`
}

type SeasonData struct {
	Rosters map[int][]RosterEntry
	Games   []Game
	Records map[int]Record
	Live    map[int]float64 // player → points scored so far in the current scoring period
	Logos   map[int]string  // team → ESPN avatar image
	Period  int
}

type espnSide struct {
	TeamID      int     `json:"teamId"`
	TotalPoints float64 `json:"totalPoints"`
	Roster      struct {
		Entries []struct {
			PlayerID int `json:"playerId"`
			Pool     struct {
				AppliedStatTotal float64 `json:"appliedStatTotal"`
			} `json:"playerPoolEntry"`
		} `json:"entries"`
	} `json:"rosterForCurrentScoringPeriod"`
}

func (e *ESPN) SeasonData() (*SeasonData, error) {
	var raw struct {
		Status struct {
			CurrentMatchupPeriod int `json:"currentMatchupPeriod"`
		} `json:"status"`
		Teams []struct {
			ID     int    `json:"id"`
			Logo   string `json:"logo"`
			Record struct {
				Overall struct {
					Wins          int     `json:"wins"`
					Losses        int     `json:"losses"`
					Ties          int     `json:"ties"`
					PointsFor     float64 `json:"pointsFor"`
					PointsAgainst float64 `json:"pointsAgainst"`
				} `json:"overall"`
			} `json:"record"`
			Roster struct {
				Entries []struct {
					PlayerID     int `json:"playerId"`
					LineupSlotID int `json:"lineupSlotId"`
				} `json:"entries"`
			} `json:"roster"`
		} `json:"teams"`
		Schedule []struct {
			MatchupPeriodID int       `json:"matchupPeriodId"`
			Home            espnSide  `json:"home"`
			Away            *espnSide `json:"away"`
		} `json:"schedule"`
	}
	if err := e.get(e.leagueURL("mRoster", "mMatchup", "mTeam", "mStatus"), "", &raw); err != nil {
		return nil, err
	}
	d := &SeasonData{Rosters: map[int][]RosterEntry{}, Records: map[int]Record{}, Live: map[int]float64{}, Logos: map[int]string{}, Period: raw.Status.CurrentMatchupPeriod}
	for _, t := range raw.Teams {
		o := t.Record.Overall
		d.Records[t.ID] = Record{o.Wins, o.Losses, o.Ties, o.PointsFor, o.PointsAgainst}
		d.Logos[t.ID] = t.Logo
		for _, en := range t.Roster.Entries {
			d.Rosters[t.ID] = append(d.Rosters[t.ID], RosterEntry{en.PlayerID, en.LineupSlotID})
		}
	}
	for _, m := range raw.Schedule {
		g := Game{Period: m.MatchupPeriodID, Home: m.Home.TeamID, HomePts: m.Home.TotalPoints}
		sides := []*espnSide{&m.Home}
		if m.Away != nil {
			g.Away, g.AwayPts = m.Away.TeamID, m.Away.TotalPoints
			sides = append(sides, m.Away)
		}
		d.Games = append(d.Games, g)
		if m.MatchupPeriodID == d.Period {
			for _, s := range sides {
				for _, en := range s.Roster.Entries {
					d.Live[en.PlayerID] = en.Pool.AppliedStatTotal
				}
			}
		}
	}
	return d, nil
}
