package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"meupreco/internal/domain"
	"meupreco/internal/nfce"
)

// cachingResolver wraps the stub receipt resolver and records every solved
// receipt the pipeline caches, so tests can prove a commit re-fetch would
// short-circuit the portal.
type cachingResolver struct {
	receipt *domain.Nfce
	cached  []string
}

func (c *cachingResolver) Resolve(_ context.Context, key nfce.AccessKey, _ string) (*domain.Nfce, error) {
	return c.receipt, nil
}

func (c *cachingResolver) CacheSolved(key nfce.AccessKey, _ []byte) {
	c.cached = append(c.cached, key.String())
}

// fakeSolver mirrors the httpapi fake: "OKAY" is correct; anything else
// rejects with the fixture captcha page.
type fakeSolver struct {
	danfe []byte
	wrong []byte
}

func (f *fakeSolver) Solve(_ context.Context, _ *nfce.Challenge, solution string) ([]byte, error) {
	if solution == "OKAY" {
		return f.danfe, nil
	}
	return f.wrong, nfce.ErrWrongSolution
}

func (f *fakeSolver) Image(_ context.Context, _ *nfce.Challenge) ([]byte, string, error) {
	return []byte("png"), "image/png", nil
}

func newFakeSolver(t *testing.T) *fakeSolver {
	t.Helper()
	danfe, err := os.ReadFile("../../testdata/nfce_1.html")
	if err != nil {
		t.Fatalf("read danfe fixture: %v", err)
	}
	wrong, err := os.ReadFile("../../testdata/captcha.html")
	if err != nil {
		t.Fatalf("read captcha fixture: %v", err)
	}
	return &fakeSolver{danfe: danfe, wrong: wrong}
}

func TestSolveCaptchaDraftsAndSeedsSolvedCache(t *testing.T) {
	m := newMemStores()
	res := &cachingResolver{receipt: testReceipt()}
	store := nfce.NewChallengeStore()
	solver := newFakeSolver(t)
	svc := New(m, res, nil, &CaptchaSupport{Store: store, Solver: solver, MaxAttempts: 5})

	id := store.Register(&nfce.Challenge{AccessKey: testAccessKey}).ID
	out, err := svc.SolveCaptcha(context.Background(), SolveCaptchaRequest{
		UserID: 1, ChallengeID: id, Solution: "OKAY",
	})
	if err != nil {
		t.Fatalf("solve: %v", err)
	}
	if out.Status != "pending" || out.PreviewID == 0 {
		t.Errorf("out = %+v, want a pending preview", out)
	}

	// The solved receipt must be cached under the access key so the follow-up
	// fetch (commit re-resolve) never faces the portal again.
	if len(res.cached) != 1 || res.cached[0] != testAccessKey {
		t.Errorf("cached = %v, want [%s]", res.cached, testAccessKey)
	}

	// A draft for user 1 actually landed.
	if _, err := m.Previews().GetPendingByUserAccessKey(context.Background(), 1, testAccessKey); err != nil {
		t.Fatalf("pending preview: %v", err)
	}

	// The typed solution must never be persisted anywhere the plan can echo.
	var planJSON bytes.Buffer
	for _, item := range out.Plan {
		planJSON.WriteString(item.Description)
		planJSON.WriteString(item.ProductName)
	}
	if strings.Contains(planJSON.String(), "OKAY") {
		t.Errorf("solution string leaked into the preview")
	}

	// Single-use: the same challenge must refuse a second solve.
	_, err = svc.SolveCaptcha(context.Background(), SolveCaptchaRequest{
		UserID: 1, ChallengeID: id, Solution: "OKAY",
	})
	if !errors.Is(err, nfce.ErrChallengeReused) {
		t.Fatalf("replay err = %v, want ErrChallengeReused", err)
	}
}

func TestSolveCaptchaWrongAnswerRetriesThenLocks(t *testing.T) {
	m := newMemStores()
	res := &cachingResolver{receipt: testReceipt()}
	store := nfce.NewChallengeStore()
	solver := newFakeSolver(t)
	svc := New(m, res, nil, &CaptchaSupport{Store: store, Solver: solver, MaxAttempts: 5})

	id := store.Register(&nfce.Challenge{AccessKey: testAccessKey}).ID

	for i := 0; i < 5; i++ {
		_, err := svc.SolveCaptcha(context.Background(), SolveCaptchaRequest{
			UserID: 1, ChallengeID: id, Solution: "WRONG",
		})
		if i < 4 {
			var wrong *SolutionIncorrectError
			if !errors.As(err, &wrong) || wrong.ChallengeID != id {
				t.Fatalf("attempt %d: err = %v, want *SolutionIncorrectError", i, err)
			}
			continue
		}
		if !errors.Is(err, nfce.ErrChallengeLocked) {
			t.Fatalf("final attempt err = %v, want ErrChallengeLocked", err)
		}
	}

	if c, err := store.Begin(id); !errors.Is(err, nfce.ErrChallengeLocked) {
		t.Errorf("Begin on exhausted challenge = %+v, %v want ErrChallengeLocked", c, err)
	}
}

func TestSolveCaptchaExpiredChallenge(t *testing.T) {
	m := newMemStores()
	store := nfce.NewChallengeStore()
	svc := New(m, &cachingResolver{receipt: testReceipt()}, nil,
		&CaptchaSupport{Store: store, Solver: newFakeSolver(t), MaxAttempts: 5})

	id := store.Register(&nfce.Challenge{
		AccessKey: testAccessKey,
		TTL:       time.Now().Add(-time.Minute),
	}).ID

	_, err := svc.SolveCaptcha(context.Background(), SolveCaptchaRequest{
		UserID: 1, ChallengeID: id, Solution: "OKAY",
	})
	if !errors.Is(err, nfce.ErrChallengeExpired) {
		t.Fatalf("err = %v, want ErrChallengeExpired", err)
	}
}

func TestSolveCaptchaDisabled(t *testing.T) {
	svc := newTestService(newMemStores()) // captcha support left nil
	_, err := svc.SolveCaptcha(context.Background(), SolveCaptchaRequest{
		UserID: 1, ChallengeID: "x", Solution: "y",
	})
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("err = %v, want a not-configured error", err)
	}
}
