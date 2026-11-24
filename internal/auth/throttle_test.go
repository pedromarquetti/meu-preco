package auth

import (
	"testing"
	"time"
)

func throttleClock() (func() time.Time, func(time.Duration)) {
	t := time.Unix(1_700_000_000, 0)
	return func() time.Time { return t }, func(d time.Duration) { t = t.Add(d) }
}

func TestLoginThrottleLocksAfterMaxFailures(t *testing.T) {
	now, _ := throttleClock()
	th := newLoginThrottle(2, 15*time.Minute, now)

	if !th.Allowed("login:alice") {
		t.Fatal("fresh key should be allowed")
	}
	th.RecordFailure("login:alice")
	if !th.Allowed("login:alice") {
		t.Fatal("one failure of two should still be allowed")
	}
	th.RecordFailure("login:alice")
	if th.Allowed("login:alice") {
		t.Fatal("key should be locked after max failures")
	}
	if got := th.RetryAfter("login:alice"); got <= 0 || got > 15*time.Minute {
		t.Fatalf("RetryAfter = %v, want within (0, 15m]", got)
	}
}

func TestLoginThrottleResetsOnSuccess(t *testing.T) {
	now, _ := throttleClock()
	th := newLoginThrottle(2, 15*time.Minute, now)

	th.RecordFailure("login:alice")
	th.Reset("login:alice")
	if !th.Allowed("login:alice") {
		t.Fatal("Reset should clear the failure window")
	}

	th.RecordFailure("login:alice")
	th.RecordFailure("login:alice")
	if th.Allowed("login:alice") {
		t.Fatal("two failures after reset should lock again")
	}
}

func TestLoginThrottleIsPerAccount(t *testing.T) {
	now, _ := throttleClock()
	th := newLoginThrottle(1, 15*time.Minute, now)

	th.RecordFailure("login:alice")
	if th.Allowed("login:alice") {
		t.Fatal("alice should be locked")
	}
	if !th.Allowed("login:bob") {
		t.Fatal("bob must not be affected by alice")
	}
}

func TestLoginThrottleWindowExpires(t *testing.T) {
	now, advance := throttleClock()
	th := newLoginThrottle(1, 10*time.Minute, now)

	th.RecordFailure("login:alice")
	if th.Allowed("login:alice") {
		t.Fatal("alice should be locked inside the window")
	}

	advance(10*time.Minute + time.Second)
	if !th.Allowed("login:alice") {
		t.Fatal("alice should be allowed after the window expires")
	}
	if got := th.RetryAfter("login:alice"); got != 0 {
		t.Fatalf("RetryAfter after expiry = %v, want 0", got)
	}
}

func TestLoginThrottleBoundsMemory(t *testing.T) {
	now, _ := throttleClock()
	th := newLoginThrottle(10, 15*time.Minute, now)

	for i := 0; i < th.cap+50; i++ {
		th.RecordFailure("login:key-" + time.Duration(i).String())
	}

	if len(th.entries) != th.cap {
		t.Fatalf("entries = %d, want cap %d", len(th.entries), th.cap)
	}
}

func TestLoginThrottleSweepsExpiredEntries(t *testing.T) {
	now, advance := throttleClock()
	th := newLoginThrottle(10, time.Minute, now)

	th.RecordFailure("login:old")
	advance(2 * time.Minute)
	th.RecordFailure("login:new")

	if _, ok := th.entries["login:old"]; ok {
		t.Fatal("expired entry was not swept")
	}
	if _, ok := th.entries["login:new"]; !ok {
		t.Fatal("new entry missing after sweep")
	}
}

func TestLoginThrottleDisabled(t *testing.T) {
	now, _ := throttleClock()

	for _, tc := range []struct {
		name   string
		max    int
		window time.Duration
	}{
		{"zero max", 0, time.Minute},
		{"zero window", 5, 0},
		{"negative", -1, -time.Minute},
	} {
		th := newLoginThrottle(tc.max, tc.window, now)
		for i := 0; i < 100; i++ {
			th.RecordFailure("login:alice")
			if !th.Allowed("login:alice") {
				t.Fatalf("%s: disabled throttle should always allow", tc.name)
			}
		}
		if got := th.RetryAfter("login:alice"); got != 0 {
			t.Fatalf("%s: RetryAfter = %v, want 0", tc.name, got)
		}
	}
}
