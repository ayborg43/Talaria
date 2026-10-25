package files

import (
	"testing"
	"time"
)

func TestRemainingReadsLikeAClock(t *testing.T) {
	now := time.Now()
	cases := []struct {
		age  time.Duration
		ret  time.Duration
		want string
	}{
		{0, 12 * time.Hour, "12h"},
		{10 * time.Hour, 12 * time.Hour, "2h"},
		{30 * time.Minute, time.Hour, "30m"},
		{90 * time.Minute, 2 * time.Hour, "30m"},
		{13 * time.Hour, 12 * time.Hour, "expired"},
	}
	for _, c := range cases {
		if got := ExpiresIn(now.Add(-c.age), now, c.ret); got != c.want {
			t.Fatalf("age %v ret %v: got %q want %q", c.age, c.ret, got, c.want)
		}
	}
	s := &Store{}
	if got := s.RetentionHours(); got != DefaultRetentionHours {
		t.Fatalf("unset retention: %d", got)
	}
	s.Retention = 48 * time.Hour
	if got := s.RetentionHours(); got != 48 {
		t.Fatalf("set retention: %d", got)
	}
}
