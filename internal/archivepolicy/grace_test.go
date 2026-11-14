package archivepolicy

import (
	"testing"
	"time"
)

func TestPersistedGraceBoundary(t *testing.T) {
	at := time.Date(2030, 1, 1, 0, 0, 0, 123, time.UTC)
	stamp := at.Format(time.RFC3339Nano)
	deadline, e := Deadline(stamp, DefaultGrace)
	if e != nil || deadline != at.Add(7*24*time.Hour).Format(time.RFC3339Nano) {
		t.Fatal(deadline, e)
	}
	if e = Eligible(stamp, deadline, at.Add(DefaultGrace-time.Nanosecond)); e == nil {
		t.Fatal("deletion allowed before persisted deadline")
	}
	if e = Eligible(stamp, deadline, at.Add(DefaultGrace)); e != nil {
		t.Fatal("exact boundary refused", e)
	}
	for _, grace := range []time.Duration{-1, MaxGrace + 1} {
		if _, e = Deadline(stamp, grace); e == nil {
			t.Fatal("invalid duration accepted", grace)
		}
	}
	for _, pair := range [][2]string{{"", deadline}, {"bad", ""}, {stamp, "bad"}, {stamp, at.Add(-1).Format(time.RFC3339Nano)}, {stamp, at.Add(MaxGrace + 1).Format(time.RFC3339Nano)}, {stamp, ""}} {
		if e = Eligible(pair[0], pair[1], at.Add(MaxGrace*2)); e == nil {
			t.Fatal("invalid or missing deadline authorized deletion", pair)
		}
	}
}
