// Package intervals computes deterministic employment-interval arithmetic.
//
// Extraction may identify spans, but durations, overlaps, and totals are
// computed here so score-relevant date math never depends on model output.
package intervals

import (
	"strconv"
	"strings"
	"time"
)

// MonthInterval is a half-open span of calendar months as year*12+month.
type MonthInterval struct {
	Start int
	End   int
}

// Months is the span length floored at zero.
func (m MonthInterval) Months() int {
	if m.End-m.Start < 0 {
		return 0
	}
	return m.End - m.Start
}

// EmploymentIntervals holds valid and merged spans plus overlap counts.
type EmploymentIntervals struct {
	Valid        []MonthInterval
	Merged       []MonthInterval
	OverlapCount int
}

// TotalMonths sums the merged spans.
func (e EmploymentIntervals) TotalMonths() int {
	total := 0
	for _, interval := range e.Merged {
		total += interval.Months()
	}
	return total
}

// MonthIndex parses "YYYY" or "YYYY-MM" into a month index.
func MonthIndex(value any) (int, bool) {
	text, ok := value.(string)
	if !ok {
		return 0, false
	}
	parts := strings.Split(text, "-")
	year, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, false
	}
	month := 1
	if len(parts) > 1 {
		month, err = strconv.Atoi(parts[1])
		if err != nil {
			return 0, false
		}
	}
	if month < 1 || month > 12 {
		return 0, false
	}
	return year*12 + month, true
}

// Employment builds valid dated intervals from extracted employment facts.
//
// Entries without a start, with an end before their start, or with
// unparsable dates are dropped. A current role without an end runs to the
// reference month so totals stay deterministic for a given time.
func Employment(entries any, now time.Time) EmploymentIntervals {
	var raw []any
	if list, ok := entries.([]any); ok {
		raw = list
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	currentMonth := now.Year()*12 + int(now.Month())
	var valid []MonthInterval
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		start, okStart := MonthIndex(entry["startDate"])
		end, okEnd := MonthIndex(entry["endDate"])
		if !okEnd {
			if current, ok := entry["isCurrent"].(bool); ok && current {
				end, okEnd = currentMonth, true
			}
		}
		if !okStart || !okEnd || end < start {
			continue
		}
		valid = append(valid, MonthInterval{Start: start, End: end})
	}
	merged := merge(valid)
	overlaps := 0
	for i, left := range valid {
		for _, right := range valid[i+1:] {
			if left.Start <= right.End && right.Start <= left.End {
				overlaps++
			}
		}
	}
	return EmploymentIntervals{Valid: valid, Merged: merged, OverlapCount: overlaps}
}

func merge(intervals []MonthInterval) []MonthInterval {
	ordered := append([]MonthInterval(nil), intervals...)
	for i := 1; i < len(ordered); i++ {
		for j := i; j > 0 && (ordered[j].Start < ordered[j-1].Start ||
			(ordered[j].Start == ordered[j-1].Start && ordered[j].End < ordered[j-1].End)); j-- {
			ordered[j], ordered[j-1] = ordered[j-1], ordered[j]
		}
	}
	var merged []MonthInterval
	for _, interval := range ordered {
		if len(merged) > 0 && interval.Start <= merged[len(merged)-1].End {
			previous := merged[len(merged)-1]
			if interval.End > previous.End {
				merged[len(merged)-1] = MonthInterval{Start: previous.Start, End: interval.End}
			}
			continue
		}
		merged = append(merged, interval)
	}
	return merged
}
