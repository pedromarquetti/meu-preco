package nfce

import (
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"sync"
)

// Outcome classifies the terminal result of one portal interaction.
//
// The set is deliberately small: it separates the cases the block investigation
// needs to tell apart (a JavaScript challenge vs. an explicit rejection vs. a
// transport reset vs. a locally refused request) without recording any receipt
// content, cookie value, or QR payload.
type Outcome string

const (
	// OutcomeSuccess: the portal returned a DANFE page.
	OutcomeSuccess Outcome = "success"
	// OutcomeCaptcha: the portal served or accepted a human-verification
	// challenge (image fetch or solve round trip).
	OutcomeCaptcha Outcome = "captcha"
	// OutcomeAntiBotRejection: an explicit block page ("Request Rejected", an
	// HTTP 403/429, an empty HTTP 200, or a connection dropped by the portal
	// edge before a response).
	OutcomeAntiBotRejection Outcome = "anti_bot_rejection"
	// OutcomeJSChallenge: an F5/BIG-IP JavaScript challenge page. The server
	// never executes it.
	OutcomeJSChallenge Outcome = "js_challenge"
	// OutcomeTransport: a non-block transport failure (connection drain,
	// truncated body, exhausted retries, upstream 5xx).
	OutcomeTransport Outcome = "transport"
	// OutcomeCooldown: a request refused locally because a shared anti-bot
	// cooldown is active.
	OutcomeCooldown Outcome = "cooldown"
	// OutcomeBusy: a request refused locally because the pacing queue is
	// saturated.
	OutcomeBusy Outcome = "busy"
)

// DefaultBlockRateWarnThreshold is the block rate, as a percentage of all
// recorded outcomes for a host, at which PortalOutcomes logs a warning.
const DefaultBlockRateWarnThreshold = 20.0

// minBlockRateSamples keeps the block-rate warning from firing on one or two
// early requests, when the percentage is statistically meaningless.
const minBlockRateSamples = 10

// OutcomeCounts is an immutable snapshot of one host's portal outcomes.
type OutcomeCounts struct {
	Success          int64 `json:"success"`
	Captcha          int64 `json:"captcha"`
	AntiBotRejection int64 `json:"anti_bot_rejection"`
	JSChallenge      int64 `json:"js_challenge"`
	Transport        int64 `json:"transport"`
	Cooldown         int64 `json:"cooldown"`
	Busy             int64 `json:"busy"`
}

// Total returns the number of recorded outcomes.
func (c OutcomeCounts) Total() int64 {
	return c.Success + c.Captcha + c.AntiBotRejection + c.JSChallenge +
		c.Transport + c.Cooldown + c.Busy
}

// Blocked returns the number of outcomes classified as an anti-bot block.
func (c OutcomeCounts) Blocked() int64 {
	return c.AntiBotRejection + c.JSChallenge
}

// BlockRate returns Blocked as a percentage of Total, or 0 with no data.
func (c OutcomeCounts) BlockRate() float64 {
	total := c.Total()
	if total == 0 {
		return 0
	}
	return float64(c.Blocked()) / float64(total) * 100.0
}

func (c *OutcomeCounts) increment(o Outcome) {
	switch o {
	case OutcomeSuccess:
		c.Success++
	case OutcomeCaptcha:
		c.Captcha++
	case OutcomeAntiBotRejection:
		c.AntiBotRejection++
	case OutcomeJSChallenge:
		c.JSChallenge++
	case OutcomeTransport:
		c.Transport++
	case OutcomeCooldown:
		c.Cooldown++
	case OutcomeBusy:
		c.Busy++
	}
}

// PortalOutcomes is a process-local, concurrency-safe counter set keyed by
// portal host. It intentionally has no durable storage: counters reset on
// restart and no browser cookie is ever persisted.
//
// One recorder is shared by every path that talks to the same portal (the
// fetcher and the captcha solver), so the block rate reflects total traffic
// rather than one subsystem.
type PortalOutcomes struct {
	mu            sync.Mutex
	log           *slog.Logger
	warnThreshold float64
	hosts         map[string]*OutcomeCounts
	total         OutcomeCounts
	warned        map[string]bool
}

// NewPortalOutcomes returns an empty recorder. A nil logger disables the
// block-rate warning but keeps counting.
func NewPortalOutcomes(log *slog.Logger) *PortalOutcomes {
	return &PortalOutcomes{
		log:           log,
		warnThreshold: DefaultBlockRateWarnThreshold,
		hosts:         make(map[string]*OutcomeCounts),
		warned:        make(map[string]bool),
	}
}

// Record counts one outcome for host. It is safe to call on a nil receiver and
// with an empty host, so callers do not need nil checks on the hot path.
func (o *PortalOutcomes) Record(host string, outcome Outcome) {
	if o == nil {
		return
	}
	host = OutcomeHost(host)

	o.mu.Lock()
	counts := o.hosts[host]
	if counts == nil {
		counts = &OutcomeCounts{}
		o.hosts[host] = counts
	}
	counts.increment(outcome)
	o.total.increment(outcome)

	warn := o.shouldWarnLocked(host, counts)
	snapshot := *counts
	threshold := o.warnThreshold
	o.mu.Unlock()

	if !warn || o.log == nil {
		return
	}
	o.log.Warn("nfce: portal block rate above threshold",
		"host", host,
		"block_rate_pct", roundRate(snapshot.BlockRate()),
		"warn_threshold_pct", threshold,
		"blocked", snapshot.Blocked(),
		"total", snapshot.Total(),
	)
}

// shouldWarnLocked applies hysteresis to the block-rate warning: it fires once
// per host when the rate crosses the threshold and is re-armed only after the
// rate drops back below it. The caller must hold o.mu.
func (o *PortalOutcomes) shouldWarnLocked(host string, c *OutcomeCounts) bool {
	if c.Total() < minBlockRateSamples {
		return false
	}
	if c.BlockRate() < o.warnThreshold {
		o.warned[host] = false
		return false
	}
	if o.warned[host] {
		return false
	}
	o.warned[host] = true
	return true
}

// Totals returns a snapshot of the aggregate counters across every host.
func (o *PortalOutcomes) Totals() OutcomeCounts {
	if o == nil {
		return OutcomeCounts{}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.total
}

// Snapshot returns a copy of the per-host counters. The returned map is safe to
// mutate and serialize.
func (o *PortalOutcomes) Snapshot() map[string]OutcomeCounts {
	if o == nil {
		return map[string]OutcomeCounts{}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make(map[string]OutcomeCounts, len(o.hosts))
	for host, c := range o.hosts {
		out[host] = *c
	}
	return out
}

// BlockRate returns the aggregate block rate across all hosts.
func (o *PortalOutcomes) BlockRate() float64 {
	return o.Totals().BlockRate()
}

// OutcomeHost normalizes a URL or host into a stable counter key.
func OutcomeHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "unknown"
	}
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return raw
}

// roundRate rounds to one decimal place for log output.
func roundRate(v float64) float64 {
	return float64(int64(v*10+0.5)) / 10
}

// recordWaitOutcome counts a shared-limiter refusal. It is used by the fetcher
// and the captcha solver so cooldown/busy outcomes are recorded even though
// the portal was never contacted.
func recordWaitOutcome(o *PortalOutcomes, host string, err error) {
	if err == nil {
		return
	}
	switch {
	case errors.Is(err, ErrPortalCoolingDown):
		o.Record(host, OutcomeCooldown)
	case errors.Is(err, ErrPortalBusy):
		o.Record(host, OutcomeBusy)
	}
}
