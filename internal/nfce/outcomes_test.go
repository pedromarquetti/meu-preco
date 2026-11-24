package nfce

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPortalOutcomesRecordsAndComputesRates(t *testing.T) {
	o := NewPortalOutcomes(nil)
	host := "https://www.nfce.fazenda.sp.gov.br/qrcode"

	record := []Outcome{
		OutcomeSuccess, OutcomeSuccess, OutcomeSuccess,
		OutcomeCaptcha,
		OutcomeAntiBotRejection, OutcomeAntiBotRejection,
		OutcomeJSChallenge,
		OutcomeTransport,
		OutcomeCooldown,
		OutcomeBusy,
	}
	for _, outcome := range record {
		o.Record(host, outcome)
	}

	want := OutcomeCounts{
		Success:          3,
		Captcha:          1,
		AntiBotRejection: 2,
		JSChallenge:      1,
		Transport:        1,
		Cooldown:         1,
		Busy:             1,
	}
	got := o.Snapshot()["www.nfce.fazenda.sp.gov.br"]
	if got != want {
		t.Fatalf("host counts = %+v, want %+v", got, want)
	}
	if totals := o.Totals(); totals != want {
		t.Fatalf("totals = %+v, want %+v", totals, want)
	}
	if blocks := got.Blocked(); blocks != 3 {
		t.Errorf("Blocked() = %d, want 3 (anti-bot + JS challenge)", blocks)
	}
	if total := got.Total(); total != 10 {
		t.Errorf("Total() = %d, want 10", total)
	}
	if rate := got.BlockRate(); rate != 30.0 {
		t.Errorf("BlockRate() = %v, want 30", rate)
	}
	if rate := o.BlockRate(); rate != 30.0 {
		t.Errorf("aggregate BlockRate() = %v, want 30", rate)
	}
}

func TestPortalOutcomesNilIsSafe(t *testing.T) {
	var o *PortalOutcomes
	o.Record("https://portal.example/qrcode", OutcomeSuccess)
	if got := o.Totals(); got != (OutcomeCounts{}) {
		t.Errorf("nil Totals() = %+v, want zero", got)
	}
	if got := o.Snapshot(); len(got) != 0 {
		t.Errorf("nil Snapshot() = %v, want empty", got)
	}
	if got := o.BlockRate(); got != 0 {
		t.Errorf("nil BlockRate() = %v, want 0", got)
	}
}

func TestPortalOutcomesHostNormalization(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"https://www.nfce.fazenda.sp.gov.br/qrcode?p=secret", "www.nfce.fazenda.sp.gov.br"},
		{"www.nfce.fazenda.sp.gov.br", "www.nfce.fazenda.sp.gov.br"},
		{"  ", "unknown"},
		{"", "unknown"},
	}
	for _, tt := range tests {
		if got := OutcomeHost(tt.raw); got != tt.want {
			t.Errorf("OutcomeHost(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}
}

func TestPortalOutcomesWarnsWhenBlockRateCrossesThreshold(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	o := NewPortalOutcomes(logger)
	const host = "www.nfce.fazenda.sp.gov.br"

	// Below the minimum sample size: no warning yet.
	for i := 0; i < minBlockRateSamples-1; i++ {
		o.Record(host, OutcomeAntiBotRejection)
	}
	if strings.Contains(logs.String(), "block rate above threshold") {
		t.Fatalf("warned before enough samples: %s", logs.String())
	}

	// Crossing the threshold warns exactly once.
	o.Record(host, OutcomeAntiBotRejection)
	if n := strings.Count(logs.String(), "block rate above threshold"); n != 1 {
		t.Fatalf("warnings = %d, want 1 after crossing the threshold: %s", n, logs.String())
	}

	// Further blocks at the same rate do not spam the log.
	for i := 0; i < 5; i++ {
		o.Record(host, OutcomeAntiBotRejection)
	}
	if n := strings.Count(logs.String(), "block rate above threshold"); n != 1 {
		t.Fatalf("warnings = %d, want 1 (hysteresis): %s", n, logs.String())
	}

	// A long run of successes drops the rate below the threshold and re-arms
	// the warning; crossing it again warns a second time.
	for i := 0; i < 100; i++ {
		o.Record(host, OutcomeSuccess)
	}
	for i := 0; i < 30; i++ {
		o.Record(host, OutcomeAntiBotRejection)
	}
	if n := strings.Count(logs.String(), "block rate above threshold"); n != 2 {
		t.Fatalf("warnings = %d, want 2 after re-crossing: %s", n, logs.String())
	}
}

func TestPortalOutcomesConcurrent(t *testing.T) {
	o := NewPortalOutcomes(nil)
	const host = "portal.example"
	const workers = 50
	const perWorker = 100

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perWorker; j++ {
				o.Record(host, OutcomeSuccess)
			}
		}()
	}
	wg.Wait()

	if got := o.Totals().Success; got != workers*perWorker {
		t.Fatalf("success count = %d, want %d", got, workers*perWorker)
	}
	if got := o.Snapshot()[host].Total(); got != workers*perWorker {
		t.Fatalf("host total = %d, want %d", got, workers*perWorker)
	}
}

// TestHTTPSourceRecordsOutcomes proves the fetcher feeds every terminal
// classification into the shared recorder.
func TestHTTPSourceRecordsOutcomes(t *testing.T) {
	key, _ := ParseAccessKey(senateKey)

	t.Run("success", func(t *testing.T) {
		raw, _ := os.ReadFile("../../testdata/nfce_1.html")
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(raw)
		}))
		defer srv.Close()

		o := NewPortalOutcomes(nil)
		src, _ := NewHTTPSource(HTTPSourceOptions{BaseURL: srv.URL, RPS: 1000, MaxRetries: 1, Outcomes: o})
		if _, err := src.Fetch(context.Background(), key, ""); err != nil {
			t.Fatalf("fetch: %v", err)
		}
		if got := o.Totals().Success; got != 1 {
			t.Fatalf("success outcomes = %d, want 1", got)
		}
	})

	t.Run("captcha", func(t *testing.T) {
		raw, _ := os.ReadFile("../../testdata/captcha.html")
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(raw)
		}))
		defer srv.Close()

		o := NewPortalOutcomes(nil)
		src, _ := NewHTTPSource(HTTPSourceOptions{BaseURL: srv.URL, RPS: 1000, MaxRetries: 1, Outcomes: o})
		if _, err := src.Fetch(context.Background(), key, ""); err == nil {
			t.Fatal("expected a captcha error")
		}
		if got := o.Totals().Captcha; got != 1 {
			t.Fatalf("captcha outcomes = %d, want 1", got)
		}
	})

	t.Run("anti-bot rejection", func(t *testing.T) {
		rejected := `<html><body>Request Rejected. Support ID: 12345</body></html>`
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(rejected))
		}))
		defer srv.Close()

		o := NewPortalOutcomes(nil)
		src, _ := NewHTTPSource(HTTPSourceOptions{BaseURL: srv.URL, RPS: 1000, MaxRetries: 1, Cooldown: time.Millisecond, Outcomes: o})
		if _, err := src.Fetch(context.Background(), key, ""); !errors.Is(err, ErrAntiBotRejection) {
			t.Fatalf("fetch error = %v, want ErrAntiBotRejection", err)
		}
		if got := o.Totals().AntiBotRejection; got != 1 {
			t.Fatalf("anti-bot outcomes = %d, want 1", got)
		}
	})

	t.Run("js challenge", func(t *testing.T) {
		challenge := `<html><head><script src="/TSPD/abc?type=challenge"></script></head>` +
			`<body>BIG-IP Please enable JavaScript</body></html>`
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(challenge))
		}))
		defer srv.Close()

		o := NewPortalOutcomes(nil)
		src, _ := NewHTTPSource(HTTPSourceOptions{BaseURL: srv.URL, RPS: 1000, MaxRetries: 1, Cooldown: time.Millisecond, Outcomes: o})
		if _, err := src.Fetch(context.Background(), key, ""); !errors.Is(err, ErrAntiBotChallenge) {
			t.Fatalf("fetch error = %v, want ErrAntiBotChallenge", err)
		}
		if got := o.Totals().JSChallenge; got != 1 {
			t.Fatalf("js challenge outcomes = %d, want 1", got)
		}
	})

	t.Run("transport", func(t *testing.T) {
		o := NewPortalOutcomes(nil)
		src, _ := NewHTTPSource(HTTPSourceOptions{
			BaseURL:    "https://portal.example",
			RPS:        1000,
			MaxRetries: 1,
			Cooldown:   time.Millisecond,
			HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("http2: server sent GOAWAY and closed the connection")
			})},
			Outcomes: o,
		})
		if _, err := src.Fetch(context.Background(), key, ""); !errors.Is(err, ErrUpstream) {
			t.Fatalf("fetch error = %v, want ErrUpstream", err)
		}
		if got := o.Totals().Transport; got != 1 {
			t.Fatalf("transport outcomes = %d, want 1", got)
		}
	})

	t.Run("edge drop is an anti-bot block", func(t *testing.T) {
		o := NewPortalOutcomes(nil)
		src, _ := NewHTTPSource(HTTPSourceOptions{
			BaseURL:    "https://portal.example",
			RPS:        1000,
			MaxRetries: 1,
			Cooldown:   time.Millisecond,
			HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("read tcp: connection reset by peer")
			})},
			Outcomes: o,
		})
		if _, err := src.Fetch(context.Background(), key, ""); !errors.Is(err, ErrAntiBotRejection) {
			t.Fatalf("fetch error = %v, want ErrAntiBotRejection", err)
		}
		if got := o.Totals().AntiBotRejection; got != 1 {
			t.Fatalf("anti-bot outcomes = %d, want 1", got)
		}
	})

	t.Run("cooldown", func(t *testing.T) {
		o := NewPortalOutcomes(nil)
		limiter := NewPortalLimiter(1000, 0)
		limiter.Cooldown(time.Minute)
		src, _ := NewHTTPSource(HTTPSourceOptions{
			BaseURL:    "https://portal.example",
			Limiter:    limiter,
			MaxRetries: 1,
			Outcomes:   o,
		})
		if _, err := src.Fetch(context.Background(), key, ""); !errors.Is(err, ErrPortalCoolingDown) {
			t.Fatalf("fetch error = %v, want ErrPortalCoolingDown", err)
		}
		if got := o.Totals().Cooldown; got != 1 {
			t.Fatalf("cooldown outcomes = %d, want 1", got)
		}
	})

	t.Run("busy", func(t *testing.T) {
		o := NewPortalOutcomes(nil)
		// No queue budget: after the first slot is reserved, the next request
		// fails fast with *PortalBusyError.
		limiter := NewPortalLimiterWithMaxWait(1, time.Hour, 0)
		if err := limiter.Wait(context.Background()); err != nil {
			t.Fatalf("reserve slot: %v", err)
		}
		src, _ := NewHTTPSource(HTTPSourceOptions{
			BaseURL:    "https://portal.example",
			Limiter:    limiter,
			MaxRetries: 1,
			Outcomes:   o,
		})
		if _, err := src.Fetch(context.Background(), key, ""); !errors.Is(err, ErrPortalBusy) {
			t.Fatalf("fetch error = %v, want ErrPortalBusy", err)
		}
		if got := o.Totals().Busy; got != 1 {
			t.Fatalf("busy outcomes = %d, want 1", got)
		}
	})
}

// TestHTTPChallengeSolverRecordsCaptchaOutcomes proves the solver path feeds
// the same recorder, so the block rate covers captcha traffic too.
func TestHTTPChallengeSolverRecordsCaptchaOutcomes(t *testing.T) {
	challenge := `<html><body>` +
		`<img src="/captcha.png">` +
		`<form action="/solve"><input type="hidden" name="__VIEWSTATE" value="v"></form>` +
		`</body></html>`
	image := []byte("\x89PNG\r\n\x1a\nfake")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "captcha") {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(image)
			return
		}
		_, _ = w.Write([]byte(challenge))
	}))
	defer srv.Close()

	o := NewPortalOutcomes(nil)
	solver := NewHTTPChallengeSolver(HTTPChallengeSolverOptions{
		HTTPClient: &http.Client{},
		Limiter:    NewPortalLimiter(1000, 0),
		Outcomes:   o,
	})
	ch := &Challenge{
		AccessKey: senateKey,
		ImageURL:  srv.URL + "/captcha.png",
		Cookies:   nil,
	}
	if _, _, err := solver.Image(context.Background(), ch); err != nil {
		t.Fatalf("image: %v", err)
	}
	if got := o.Totals().Captcha; got != 1 {
		t.Fatalf("captcha outcomes = %d, want 1", got)
	}
}
