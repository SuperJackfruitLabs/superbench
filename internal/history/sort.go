package history

import "sort"

func sortRecords(records []Record) {
	sort.Slice(records, func(i, j int) bool {
		a, b := records[i].StartedAt, records[j].StartedAt
		if a.IsZero() {
			a = records[i].ObservedAt
		}
		if b.IsZero() {
			b = records[j].ObservedAt
		}
		if a.Equal(b) {
			return records[i].ID < records[j].ID
		}
		return a.After(b)
	})
}
