package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

// Haiku is the cheapest + fastest Claude — right for a 60–90s draft clock.
// Override with DRAFT_MODEL=claude-sonnet-5-5 for sharper takes.
func aiModel() string {
	if m := os.Getenv("DRAFT_MODEL"); m != "" {
		return m
	}
	return "claude-haiku-4-5"
}

const aiSystem = `You advise one person drafting live in a casual 10-team ESPN fantasy football league. They know nothing about football and have a 90-second clock. Be decisive and concrete. No filler, no hedging, no pleasantries.

The draft is happening mid-season, so only rest-of-season value matters. You get a math model's ranked candidates with projections, value, availability, ESPN news and the drafter's roster needs.

Rules:
- PICK must be one of the listed candidates.
- Default to the model's #1. Overrule it only for a concrete reason in the data (injury/IR news, lost role, suspension, trade). Then say that reason.
- Use the other teams' rosters: a team that already has its QB/TE won't take another soon, so those players are more likely to slide; a team missing RBs will take RBs.
- "likely gone before your pick" players can't be picked unless the drafter is on the clock. "can wait" means the player should still be there at the drafter's following pick, so prefer a comparable "last chance" player now.
- Every reason cites a number (projected points, next-week projection, points per game) or a specific news fact.
- Write in plain English. Explain any football term in 3 words.

Reply in exactly 4 lines, nothing else:
PICK: <name> (<pos>)
WHY: <one sentence, max 25 words, with a number>
BACKUP: <name> (<pos>): <max 8 words>
WATCH OUT: <one concrete risk, or "Nothing major">`

type aiReq struct {
	Web     bool
	Context string
}

func askClaude(ctx context.Context, r aiReq) (string, error) {
	client := anthropic.NewClient()
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(aiModel()),
		MaxTokens: 1024,
		System:    []anthropic.TextBlockParam{{Text: aiSystem}},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(r.Context)),
		},
	}
	if r.Web {
		params.MaxTokens = 2048
		params.Tools = []anthropic.ToolUnionParam{
			{OfWebSearchTool20250305: &anthropic.WebSearchTool20250305Param{MaxUses: anthropic.Int(3)}},
		}
		params.Messages[0] = anthropic.NewUserMessage(anthropic.NewTextBlock(r.Context +
			"\n\nSearch the web for the LATEST news (injuries, depth chart, role changes) on the top 3-4 candidates before deciding. Be fast."))
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	resp, err := client.Messages.New(ctx, params)
	if err != nil {
		return "", err
	}
	if resp.StopReason == anthropic.StopReasonRefusal {
		return "", fmt.Errorf("Claude declined this request")
	}
	var b strings.Builder
	for _, block := range resp.Content {
		if t, ok := block.AsAny().(anthropic.TextBlock); ok {
			b.WriteString(t.Text)
		}
	}
	out := strings.TrimSpace(b.String())
	// With web search the answer comes after the search chatter; keep from PICK: on.
	if i := strings.LastIndex(out, "PICK:"); i > 0 {
		out = out[i:]
	}
	return out, nil
}

func aiContext(s *Snapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "League: %d teams, %d rounds, lineup %s. Season week %d (projections are week %d through end of season).\n",
		s.Teams, s.Rounds, s.LineupDesc, s.Week, s.Week)
	fmt.Fprintf(&b, "Current overall pick: %d. My next pick: %d (round %d)", s.CurrentPick, s.MyNextPick, s.MyRound)
	if s.OnClockMe {
		b.WriteString(" — I AM ON THE CLOCK NOW")
	}
	if len(s.Plan) > 1 {
		fmt.Fprintf(&b, ". My pick after that: #%d", s.Plan[1].Overall)
	}
	b.WriteString(".\nStarting slots I still need to fill: ")
	var needs []string
	for _, pos := range []string{"QB", "RB", "WR", "TE", "D/ST", "K"} {
		if n := s.Needs[pos]; n[1] > n[0] {
			needs = append(needs, fmt.Sprintf("%s x%d", pos, n[1]-n[0]))
		}
	}
	if len(needs) == 0 {
		b.WriteString("none (drafting bench depth)")
	}
	b.WriteString(strings.Join(needs, ", ") + "\n")
	b.WriteString("My roster so far: ")
	if len(s.Mine) == 0 {
		b.WriteString("empty")
	}
	for i, p := range s.Mine {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s (%s %s)", p.Name, p.Pos, p.Team)
	}
	// The whole draft: every roster, who picks before me, recent picks, my plan.
	if len(s.Between) > 0 {
		fmt.Fprintf(&b, "\nTeams picking before my turn (in order): %s\n", strings.Join(s.Between, " → "))
	}
	byTeam := map[string][]string{}
	for _, p := range s.Board {
		byTeam[p.Owner] = append(byTeam[p.Owner], fmt.Sprintf("%s %s", p.Pos, p.Name))
	}
	if len(s.Board) > 0 {
		b.WriteString("\nEvery team's roster so far:\n")
		for _, c := range s.BoardCols {
			name, _ := c["name"].(string)
			fmt.Fprintf(&b, "- %s: %s\n", name, strings.Join(byTeam[name], ", "))
		}
		b.WriteString("Last picks: ")
		for i, p := range s.Recent {
			if i == 8 {
				break
			}
			fmt.Fprintf(&b, "#%d %s %s; ", p.Overall, p.Pos, p.Name)
		}
		b.WriteString("\n")
	}
	if len(s.Plan) > 0 {
		b.WriteString("\nMath model's plan for my next picks: ")
		for i, p := range s.Plan {
			if i == 4 {
				break
			}
			fmt.Fprintf(&b, "#%d %s %s; ", p.Overall, p.Player.Pos, p.Player.Name)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n\nTop candidates from the math model (best first):\n")
	for i, r := range s.Recs {
		if i >= 10 {
			break
		}
		fmt.Fprintf(&b, "%d. %s — %s %s | ROS proj %.0f pts | next wk %.1f | actual %.1f ppg in %d gms | ESPN rank #%d, our rank #%d | %s | %s\n",
			i+1, r.Name, r.Pos, r.Team, r.ROS, r.NextWeek, r.ActualPPG, r.Games, espnOrder(r.Player), r.ValueRank, r.Injury, availability(r)+"; "+strings.Join(r.Tags, "; "))
		for j, n := range r.News {
			if j == 2 {
				break
			}
			fmt.Fprintf(&b, "     news (%s): %s. %s\n", strings.SplitN(n.Published, "T", 2)[0], n.Headline, n.Story)
		}
	}
	return b.String()
}

func availability(r Rec) string {
	switch {
	case r.LikelyGo:
		return "likely gone before your pick"
	case r.LastChance:
		return "last chance (won't last to your following pick)"
	case r.CanWait:
		return "can wait (should still be there at your following pick)"
	}
	return "available"
}
