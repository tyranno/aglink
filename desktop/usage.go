package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// "오늘 얼마 썼어?": the AI cost over a day, week or month, summed here from the
// host's store.json (each conversation keeps a record per turn) without a turn
// of its own. The file is only read; whatever in it does not look as expected
// is skipped.

// usageTurn is the part of the host's TurnStat this needs.
type usageTurn struct {
	At      time.Time `json:"at"`
	CostUSD float64   `json:"costUsd"`
}

type usageConv struct {
	ID        string      `json:"id"`
	Title     string      `json:"title"`
	TurnStats []usageTurn `json:"turnStats"`
}

// usageSum is what UsageSummary reports.
type usageSum struct {
	Turns   int     `json:"turns"`
	CostUSD float64 `json:"costUsd"`
	Top     string  `json:"top"` // title of the conversation that cost most
	TopCost float64 `json:"topCost"`
}

// usagePeriod is the local-time span of "today", "yesterday", "week" (since
// Monday) or "month" (since the 1st), ending at now.
func usagePeriod(kind string, now time.Time) (from, to time.Time, err error) {
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch kind {
	case "today":
		return day, now, nil
	case "yesterday":
		return day.AddDate(0, 0, -1), day, nil
	case "week":
		back := (int(day.Weekday()) + 6) % 7 // days since Monday
		return day.AddDate(0, 0, -back), now, nil
	case "month":
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()), now, nil
	}
	return time.Time{}, time.Time{}, fmt.Errorf("unknown period %q", kind)
}

// storeConvs takes every conversation out of store.json: telegramConv,
// webConvs and each project's conversations, a map or a list each.
func storeConvs(data []byte) []usageConv {
	var store map[string]json.RawMessage
	if json.Unmarshal(data, &store) != nil {
		return nil
	}
	var out []usageConv
	addOne := func(raw json.RawMessage) {
		var c usageConv
		if json.Unmarshal(raw, &c) == nil {
			out = append(out, c)
		}
	}
	addMany := func(raw json.RawMessage) {
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) == nil {
			for _, r := range m {
				addOne(r)
			}
			return
		}
		var l []json.RawMessage
		if json.Unmarshal(raw, &l) == nil {
			for _, r := range l {
				addOne(r)
			}
		}
	}
	if raw, ok := store["telegramConv"]; ok {
		addOne(raw)
	}
	if raw, ok := store["webConvs"]; ok {
		addMany(raw)
	}
	var projects []json.RawMessage
	if raw, ok := store["projects"]; ok {
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) == nil {
			for _, p := range m {
				projects = append(projects, p)
			}
		} else {
			_ = json.Unmarshal(raw, &projects)
		}
	}
	for _, p := range projects {
		var proj struct {
			Conversations json.RawMessage `json:"conversations"`
		}
		if json.Unmarshal(p, &proj) == nil && len(proj.Conversations) > 0 {
			addMany(proj.Conversations)
		}
	}
	return out
}

// sumUsage adds up the turns in [from, to) over all conversations, each
// conversation counted once.
func sumUsage(convs []usageConv, from, to time.Time) usageSum {
	var s usageSum
	seen := map[string]bool{}
	for _, c := range convs {
		if c.ID != "" {
			if seen[c.ID] {
				continue
			}
			seen[c.ID] = true
		}
		cost := 0.0
		for _, t := range c.TurnStats {
			if t.At.Before(from) || !t.At.Before(to) {
				continue
			}
			s.Turns++
			cost += t.CostUSD
		}
		s.CostUSD += cost
		if cost > s.TopCost {
			s.Top, s.TopCost = c.Title, cost
		}
	}
	return s
}

// UsageSummary sums the AI turns and their cost over period ("today",
// "yesterday", "week", "month") as JSON usageSum.
func (v *VoiceService) UsageSummary(period string) (string, error) {
	now := time.Now()
	from, to, err := usagePeriod(period, now)
	if err != nil {
		return "", err
	}
	dir, err := dataDir()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(dir, "store.json"))
	if err != nil {
		return "", err
	}
	// "to" is now: a turn stamped a moment later still counts.
	b, _ := json.Marshal(sumUsage(storeConvs(data), from, to.Add(time.Minute)))
	return string(b), nil
}
