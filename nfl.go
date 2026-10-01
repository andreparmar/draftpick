package main

import (
	"fmt"
	"strings"
	"time"
)

// NFLGame is one NFL team's game this week, from ESPN's public scoreboard.
type NFLGame struct {
	Opp     string `json:"opp"`     // opponent abbreviation
	Home    bool   `json:"home"`    // true = home game
	Kickoff string `json:"kickoff"` // ISO time
	State   string `json:"state"`   // pre | in | post
	Period  int    `json:"period"`  // quarter while live
	Clock   string `json:"clock"`   // game clock while live
	Detail  string `json:"detail"`  // ESPN's short status ("Q3 5:12", "Final", "10/4 - 1:00 PM EDT")
}

// remaining is the share of this game still to be played (1 before kickoff,
// 0 once final), used to project live scores to a final number.
func (g NFLGame) remaining() float64 {
	switch g.State {
	case "post":
		return 0
	case "in":
		mins := 0.0
		if parts := strings.Split(g.Clock, ":"); len(parts) == 2 {
			var m, s float64
			fmt.Sscan(parts[0], &m)
			fmt.Sscan(parts[1], &s)
			mins = m + s/60
		}
		left := float64(4-g.Period)*15 + mins
		if g.Period > 4 {
			left = mins // overtime
		}
		if left < 0 {
			left = 0
		}
		return left / 60
	}
	return 1
}

// Injury is ESPN's NFL injury report entry for a player.
type Injury struct {
	Part    string `json:"part"`    // body part, e.g. "Hamstring"
	Status  string `json:"status"`  // "Questionable", "Out", …
	Comment string `json:"comment"` // short report
}

// NFLWeek fetches every game in an NFL week, keyed by team abbreviation.
func (e *ESPN) NFLWeek(week int) (map[string]NFLGame, error) {
	var raw struct {
		Events []struct {
			Date         string `json:"date"`
			Competitions []struct {
				Status struct {
					Period       int    `json:"period"`
					DisplayClock string `json:"displayClock"`
					Type         struct {
						State       string `json:"state"`
						ShortDetail string `json:"shortDetail"`
					} `json:"type"`
				} `json:"status"`
				Competitors []struct {
					HomeAway string `json:"homeAway"`
					Team     struct {
						Abbreviation string `json:"abbreviation"`
					} `json:"team"`
				} `json:"competitors"`
			} `json:"competitions"`
		} `json:"events"`
	}
	u := fmt.Sprintf("https://site.api.espn.com/apis/site/v2/sports/football/nfl/scoreboard?week=%d&seasontype=2&dates=%d", week, e.Season)
	if err := e.get(u, "", &raw); err != nil {
		return nil, err
	}
	out := map[string]NFLGame{}
	for _, ev := range raw.Events {
		if len(ev.Competitions) == 0 || len(ev.Competitions[0].Competitors) != 2 {
			continue
		}
		c := ev.Competitions[0]
		for _, pair := range [][2]int{{0, 1}, {1, 0}} {
			me, them := c.Competitors[pair[0]], c.Competitors[pair[1]]
			out[nflAbbr(me.Team.Abbreviation)] = NFLGame{Opp: nflAbbr(them.Team.Abbreviation), Home: me.HomeAway == "home", Kickoff: ev.Date,
				State: c.Status.Type.State, Period: c.Status.Period, Clock: c.Status.DisplayClock, Detail: c.Status.Type.ShortDetail}
		}
	}
	return out, nil
}

// NFLInjuries fetches ESPN's league-wide injury report, keyed by player id.
func (e *ESPN) NFLInjuries() (map[int]Injury, error) {
	var raw struct {
		Injuries []struct {
			Injuries []struct {
				Status       string `json:"status"`
				ShortComment string `json:"shortComment"`
				Athlete      struct {
					Links []struct {
						Href string `json:"href"`
					} `json:"links"`
					ID string `json:"id"`
				} `json:"athlete"`
				Details struct {
					Type string `json:"type"`
				} `json:"details"`
			} `json:"injuries"`
		} `json:"injuries"`
	}
	if err := e.get("https://site.api.espn.com/apis/site/v2/sports/football/nfl/injuries", "", &raw); err != nil {
		return nil, err
	}
	out := map[int]Injury{}
	for _, t := range raw.Injuries {
		for _, i := range t.Injuries {
			id := 0
			fmt.Sscan(i.Athlete.ID, &id)
			if id == 0 {
				for _, l := range i.Athlete.Links {
					if k := strings.Index(l.Href, "/id/"); k >= 0 {
						fmt.Sscan(strings.Split(l.Href[k+4:], "/")[0], &id)
						break
					}
				}
			}
			if id != 0 && !strings.EqualFold(i.Status, "Active") {
				c := i.ShortComment
				if len(c) > 220 {
					c = c[:220] + "…"
				}
				out[id] = Injury{Part: i.Details.Type, Status: i.Status, Comment: c}
			}
		}
	}
	return out, nil
}

// ESPN's site API and fantasy API spell a few teams differently.
func nflAbbr(a string) string {
	switch a {
	case "WAS":
		return "WSH"
	case "JAC":
		return "JAX"
	}
	return a
}

// nflLoop keeps this week's NFL schedule/live status and the injury report fresh.
func (a *App) nflLoop() {
	lastInj := time.Time{}
	for {
		a.mu.Lock()
		e, week := a.espn, a.week
		a.mu.Unlock()
		wait := 60 * time.Second
		if week > 0 {
			if g, err := e.NFLWeek(week); err == nil {
				a.mu.Lock()
				a.nfl = g
				for _, x := range g {
					if x.State == "in" {
						wait = 20 * time.Second
					}
				}
				a.mu.Unlock()
			}
		}
		if time.Since(lastInj) > 10*time.Minute {
			if inj, err := e.NFLInjuries(); err == nil {
				a.mu.Lock()
				a.injuries = inj
				a.mu.Unlock()
				lastInj = time.Now()
			}
		}
		time.Sleep(wait)
	}
}
