package nfce

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"
)

func TestParseCaptchaPage(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/captcha.html")
	if err != nil {
		t.Fatal(err)
	}
	page := parseCaptchaPage(raw)

	if page.imageSrc != "../Handlers/DownloadCaptcha.ashx" {
		t.Errorf("image src = %q", page.imageSrc)
	}
	if page.formAction != "NFCeConsultaPublica/Paginas/ConsultaPublica.aspx" {
		t.Errorf("form action = %q", page.formAction)
	}
	if page.fields["__VIEWSTATE"] != "wDspAbg03/Cn8Rd+ZxFTt" {
		t.Errorf("__VIEWSTATE = %q", page.fields["__VIEWSTATE"])
	}
	if page.fields["__EVENTVALIDATION"] != "UmksuBUH0wNA5aHRoJ95p5" {
		t.Errorf("__EVENTVALIDATION = %q", page.fields["__EVENTVALIDATION"])
	}
	if _, ok := page.fields["txtCodigo"]; ok {
		t.Errorf("txtCodigo must never be read as a hidden field")
	}
}

func TestChallengeStoreLifecycle(t *testing.T) {
	s := NewChallengeStore()
	ch := s.Register(&Challenge{})
	if ch.ID == "" {
		t.Error("registered challenge has no id")
	}
	if ch.MaxAttempts != defaultChallengeMaxAttempts {
		t.Errorf("MaxAttempts = %d, want default %d", ch.MaxAttempts, defaultChallengeMaxAttempts)
	}
	if ch.TTL.IsZero() || !ch.TTL.After(time.Now()) {
		t.Errorf("TTL %v not in the future", ch.TTL)
	}

	if got, err := s.Get(ch.ID); err != nil || got.ID != ch.ID {
		t.Fatalf("Get = %v, %v", got, err)
	}
	if _, err := s.Get("nope"); !errors.Is(err, ErrChallengeNotFound) {
		t.Fatalf("Get missing = %v, want ErrChallengeNotFound", err)
	}
}

func TestChallengeStoreFailuresThenLock(t *testing.T) {
	s := NewChallengeStore()
	ch := s.Register(&Challenge{MaxAttempts: 2})

	for i := 0; i < 2; i++ {
		got, err := s.RecordFailure(ch.ID, nil)
		if err != nil {
			t.Fatalf("record failure %d: %v", i, err)
		}
		if got.Attempts != i+1 {
			t.Errorf("attempts = %d, want %d", got.Attempts, i+1)
		}
	}

	if _, err := s.Begin(ch.ID); !errors.Is(err, ErrChallengeLocked) {
		t.Fatalf("Begin after budget spent = %v, want ErrChallengeLocked", err)
	}
}

func TestChallengeStoreSingleUse(t *testing.T) {
	s := NewChallengeStore()
	ch := s.Register(&Challenge{})

	if err := s.RecordSuccess(ch.ID); err != nil {
		t.Fatalf("record success: %v", err)
	}
	if _, err := s.Get(ch.ID); !errors.Is(err, ErrChallengeReused) {
		t.Fatalf("Get after solve = %v, want ErrChallengeReused", err)
	}
	if err := s.RecordSuccess(ch.ID); !errors.Is(err, ErrChallengeReused) {
		t.Fatalf("second solve = %v, want ErrChallengeReused", err)
	}
	if _, err := s.Begin(ch.ID); !errors.Is(err, ErrChallengeReused) {
		t.Fatalf("Begin after solve = %v, want ErrChallengeReused", err)
	}
}

func TestChallengeStoreExpiry(t *testing.T) {
	s := NewChallengeStore()
	ch := s.Register(&Challenge{})
	s.now = func() time.Time { return time.Now().Add(time.Hour) } // travel past TTL

	if _, err := s.Get(ch.ID); !errors.Is(err, ErrChallengeExpired) {
		t.Fatalf("Get after TTL = %v, want ErrChallengeExpired", err)
	}
	if n := s.Count(); n != 0 {
		t.Errorf("Count = %d, want 0 (expired swept)", n)
	}
}

// captchaPortal emulates the SP "Consulta Resumida" portal: a GET on the QR
// URL first yields the challenge form, and the challenge form's POST back
// accepts the typed answer (txtCodigo). A right answer returns the DANFE; a
// wrong one re-renders the challenge page.
func captchaPortal(t *testing.T, answer string) *httptest.Server {
	t.Helper()
	danfe, err := os.ReadFile("../../testdata/nfce_1.html")
	if err != nil {
		t.Fatal(err)
	}
	captcha, err := os.ReadFile("../../testdata/captcha.html")
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad form", http.StatusBadRequest)
				return
			}
			if r.Form.Get("txtCodigo") == answer {
				_, _ = w.Write(danfe)
				return
			}
			_, _ = w.Write(captcha)
			return
		}
		_, _ = w.Write(captcha)
	}))
	return srv
}

// TestSolverReplaysSolution walks the whole captcha loop against a fake
// portal: fetch → challenge → wrong answer (retry allowed) → right answer.
func TestSolverReplaysSolution(t *testing.T) {
	srv := captchaPortal(t, "sesamo")
	defer srv.Close()

	stores := NewChallengeStore()
	src, err := NewHTTPSource(HTTPSourceOptions{BaseURL: srv.URL, RPS: 100, MaxRetries: 1})
	if err != nil {
		t.Fatal(err)
	}
	src.challenges = stores
	solver := NewHTTPChallengeSolver(HTTPChallengeSolverOptions{
		HTTPClient: srv.Client(),
		RPS:        100,
	})

	key, _ := ParseAccessKey(senateKey)
	_, err = src.Fetch(context.Background(), key, "")
	var capErr *CaptchaError
	if !errors.As(err, &capErr) {
		t.Fatalf("fetch = %v, want captcha", err)
	}
	ch, err := stores.Get(capErr.ChallengeID)
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	// Wrong solution: the solver carries the fresh challenge page back so the
	// store can refresh its session tokens, and the challenge stays usable.
	raw, err := solver.Solve(context.Background(), &ch, "quatro")
	if !errors.Is(err, ErrWrongSolution) {
		t.Fatalf("wrong solve = %v, want ErrWrongSolution", err)
	}
	if pageHasDANFE(raw) {
		t.Errorf("wrong answer returned a DANFE")
	}
	updated, err := stores.RecordFailure(capErr.ChallengeID, raw)
	if err != nil {
		t.Fatalf("record failure: %v", err)
	}
	if updated.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", updated.Attempts)
	}

	// Correct solution: DANFE bytes, then single-use is enforced.
	ch, _ = stores.Begin(capErr.ChallengeID)
	raw, err = solver.Solve(context.Background(), &ch, "sesamo")
	if err != nil {
		t.Fatalf("solve: %v", err)
	}
	if !pageHasDANFE(raw) {
		t.Errorf("right answer did not return a DANFE")
	}
	if err := stores.RecordSuccess(capErr.ChallengeID); err != nil {
		t.Fatalf("record success: %v", err)
	}
	if _, err := stores.Begin(capErr.ChallengeID); !errors.Is(err, ErrChallengeReused) {
		t.Fatalf("reuse = %v, want ErrChallengeReused", err)
	}
}

// TestSolverImageProxies verifies the image endpoint replays session cookies
// and returns portal bytes + content type, without the app storing anything.
func TestSolverImageProxies(t *testing.T) {
	var gotCookie *http.Cookie
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("ASP.NET_SessionId"); err == nil {
			gotCookie = c
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("fake-png"))
	}))
	defer srv.Close()

	solver := NewHTTPChallengeSolver(HTTPChallengeSolverOptions{HTTPClient: srv.Client(), RPS: 100})
	ch := &Challenge{
		ImageURL: srv.URL + "/DownloadCaptcha.ashx",
		Cookies:  []*http.Cookie{{Name: "ASP.NET_SessionId", Value: "abc123"}},
	}

	body, ctype, err := solver.Image(context.Background(), ch)
	if err != nil {
		t.Fatalf("image: %v", err)
	}
	if string(body) != "fake-png" || ctype != "image/png" {
		t.Errorf("image = %q (%s)", body, ctype)
	}
	if gotCookie == nil || gotCookie.Value != "abc123" {
		t.Errorf("session cookie = %+v, want abc123", gotCookie)
	}
}

func TestResolveURL(t *testing.T) {
	base, _ := url.Parse("https://www.nfce.fazenda.sp.gov.br/qrcode?p=x")
	cases := map[string]string{
		"":                                 "fallback",
		"https://cdn.example/img.png":      "https://cdn.example/img.png",
		"../Handlers/DownloadCaptcha.ashx": "https://www.nfce.fazenda.sp.gov.br/Handlers/DownloadCaptcha.ashx",
		"NFCeConsultaPublica/Paginas/ConsultaPublica.aspx": "https://www.nfce.fazenda.sp.gov.br/NFCeConsultaPublica/Paginas/ConsultaPublica.aspx",
	}
	for ref, want := range cases {
		if got := resolveURL(base, ref, "fallback"); got != want {
			t.Errorf("resolveURL(%q) = %q, want %q", ref, got, want)
		}
	}
	if got := resolveURL(nil, "x", ""); got != "x" {
		t.Errorf("resolveURL(nil base) = %q, want %q", got, "x")
	}
}

func TestSolverRejectsMissingForm(t *testing.T) {
	solver := NewHTTPChallengeSolver(HTTPChallengeSolverOptions{RPS: 100})
	if _, err := solver.Solve(context.Background(), &Challenge{}, "x"); !errors.Is(err, ErrBadResponse) {
		t.Fatalf("Solve on empty challenge = %v, want ErrBadResponse", err)
	}
}
