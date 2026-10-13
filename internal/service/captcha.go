package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"meupreco/internal/nfce"
)

// SolutionIncorrectError reports a wrong-answer rejection on a challenge that
// still accepts retries. It carries the challenge id so the HTTP layer can
// answer 412 again with the remaining attempt budget.
type SolutionIncorrectError struct {
	ChallengeID string
}

func (e *SolutionIncorrectError) Error() string {
	return "captcha solution incorrect"
}

// SolveCaptchaRequest is the input to SolveCaptcha: which challenge the user
// is answering and the typed solution text. The answer lives only in this
// request; it is never stored on the challenge, the preview, or any log row.
type SolveCaptchaRequest struct {
	UserID      int64
	ChallengeID string
	Solution    string
}

// ChallengeInfo is the client-visible state of one challenge.
type ChallengeInfo struct {
	ID           string    `json:"challenge_id"`
	Status       string    `json:"status"` // "pending" | "solved" | "locked"
	AttemptsLeft int       `json:"attempts_left"`
	MaxAttempts  int       `json:"max_attempts"`
	ImageURL     string    `json:"image_url"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// captchaSupport returns the pipeline's captcha wiring when configured.
func (s *Service) captchaSupport() (*CaptchaSupport, bool) {
	if s.Captcha == nil || s.Captcha.Store == nil || s.Captcha.Solver == nil {
		return nil, false
	}
	return s.Captcha, true
}

// SolveCaptcha replays a user-typed captcha solution against the portal in the
// challenge's captured session. On a correct answer it seeds the solved
// receipt (so the later /nfce/submit re-fetch does not re-challenge), builds
// the receipt plan through the normal pipeline and persists it as a pending
// preview — the draft the client then confirms. A rejected answer records a
// failure (retry allowed until the attempt budget is spent), and a challenge
// is strictly single-use once solved.
//
// The portal-facing solve is delegated to the nfce solver; this function is
// the orchestration that turns "the portal accepted the answer" into "the
// receipt is drafted and confirmable".
func (s *Service) SolveCaptcha(ctx context.Context, req SolveCaptchaRequest) (*PreviewResult, error) {
	sup, ok := s.captchaSupport()
	if !ok {
		return nil, fmt.Errorf("captcha flow is not configured")
	}
	if req.ChallengeID == "" || req.Solution == "" {
		return nil, fmt.Errorf("challenge_id and solution are required")
	}

	// The single-use claim happens at Begin: a solved or expired challenge
	// refuses to start another attempt.
	ch, err := sup.Store.Begin(req.ChallengeID)
	if err != nil {
		return nil, err
	}

	raw, err := sup.Solver.Solve(ctx, &ch, req.Solution)
	if err != nil {
		if errors.Is(err, nfce.ErrWrongSolution) {
			updated, _ := sup.Store.RecordFailure(req.ChallengeID, raw)
			if updated.Attempts >= updated.MaxAttempts {
				return nil, nfce.ErrChallengeLocked
			}
			return nil, &SolutionIncorrectError{ChallengeID: req.ChallengeID}
		}
		return nil, err
	}

	// Accepted: burn the challenge (single-use) before anything else so a
	// replayed solve cannot ride the same session twice.
	if err := sup.Store.RecordSuccess(req.ChallengeID); err != nil {
		return nil, err
	}

	key, err := nfce.ParseAccessKey(ch.AccessKey)
	if err != nil {
		return nil, err
	}

	// Bridge the receipt the portal just released: the danfe bytes are cached
	// by access key so ReportPreview / the commit re-fetch serve them without a
	// fresh portal challenge (the SEFAZ session would otherwise gate again).
	if cached, ok := s.Resolver.(SolvedCache); ok {
		cached.CacheSolved(key, raw)
	}

	s.gc(ctx)

	receipt, err := s.resolveReceipt(ctx, ch.AccessKey, "")
	if err != nil {
		return nil, err
	}
	return s.persistDraft(ctx, req.UserID, key, ch.Payload, receipt)
}

// ChallengeStatus reports a challenge's current state for the GET /captcha
// handler. Typed nfce errors (not found / expired / reused) pass through.
func (s *Service) ChallengeStatus(ctx context.Context, id string) (*ChallengeInfo, error) {
	sup, ok := s.captchaSupport()
	if !ok {
		return nil, fmt.Errorf("captcha flow is not configured")
	}
	ch, err := sup.Store.Status(id)
	if err != nil {
		return nil, err
	}
	info := &ChallengeInfo{
		ID:           ch.ID,
		Status:       "pending",
		AttemptsLeft: ch.AttemptsLeft(),
		MaxAttempts:  ch.MaxAttempts,
		ImageURL:     ch.ImageURL,
		ExpiresAt:    ch.TTL,
	}
	if ch.Solved {
		info.Status = "solved"
	}
	return info, nil
}

// ChallengeImage streams the challenge image straight from the portal (never
// stored by the app) so the client can render it.
func (s *Service) ChallengeImage(ctx context.Context, id string) ([]byte, string, error) {
	sup, ok := s.captchaSupport()
	if !ok {
		return nil, "", fmt.Errorf("captcha flow is not configured")
	}
	// Begin, not Get: a solved challenge must no longer serve its image.
	ch, err := sup.Store.Begin(id)
	if err != nil {
		return nil, "", err
	}
	return sup.Solver.Image(ctx, &ch)
}
