package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"meupreco/internal/config"
	"meupreco/internal/domain"
	"meupreco/internal/nfce"
)

// senateKey is a real, publicly-disclosed NFC-e key from the Senate CEAPS
// transparency portal (SP restaurant receipt) — used only to exercise the
// validation path; no private data.
const senateKey = "35241152323688000144650020000002571115675901"

type stubResolver struct {
	receipt *domain.Nfce
	err     error
}

func (s stubResolver) Resolve(_ context.Context, _ nfce.AccessKey, _ string) (*domain.Nfce, error) {
	return s.receipt, s.err
}

// captureResolver records the payload the handler forwarded, so tests can
// assert the qr_payload path is actually used.
type captureResolver struct {
	payload string
}

func (c *captureResolver) Resolve(_ context.Context, _ nfce.AccessKey, payload string) (*domain.Nfce, error) {
	c.payload = payload
	return &domain.Nfce{AccessKey: senateKey}, nil
}

func nfceRouter(res nfceResolver) http.Handler {
	return newRouter(config.Config{CORSOrigins: []string{"https://app.example.com"}},
		slog.New(slog.NewTextHandler(io.Discard, nil)), res, fakePinger{})
}

func postSubmit(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/nfce/submit", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	return rec
}

func decodeErrorEnvelope(t *testing.T, rec *httptest.ResponseRecorder) (code string, extra map[string]any) {
	t.Helper()
	var body struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	code, _ = body.Error["code"].(string)
	return code, body.Error
}

func TestNfceSubmitOK(t *testing.T) {
	receipt := &domain.Nfce{
		AccessKey:     senateKey,
		Number:        "257",
		Series:        "002",
		PaymentMethod: "Cartão de Crédito",
		Total:         decimal.RequireFromString("59.06"),
		Items: []domain.NfceItem{
			{Line: 1, Description: "LIMAO TAHITI KG", Qty: decimal.RequireFromString("0.815")},
		},
	}
	rec := postSubmit(t, nfceRouter(stubResolver{receipt: receipt}),
		`{"access_key":"`+senateKey+`"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Receipt domain.Nfce `json:"receipt"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Receipt.AccessKey != senateKey || len(body.Receipt.Items) != 1 {
		t.Errorf("receipt = %+v", body.Receipt)
	}
}

func TestNfceSubmitInvalidKey(t *testing.T) {
	rec := postSubmit(t, nfceRouter(stubResolver{}), `{"access_key":"123"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d", rec.Code)
	}
	if code, _ := decodeErrorEnvelope(t, rec); code != "invalid_access_key" {
		t.Errorf("code = %q", code)
	}
}

func TestNfceSubmitBadBody(t *testing.T) {
	for _, body := range []string{"", "nope", `{"access_key":`} {
		rec := postSubmit(t, nfceRouter(stubResolver{}), body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %q: status = %d, want 400", body, rec.Code)
		}
		if code, _ := decodeErrorEnvelope(t, rec); code != "invalid_request" {
			t.Errorf("body %q: code = %q", body, code)
		}
	}
}

func TestNfceSubmitCaptcha(t *testing.T) {
	rec := postSubmit(t, nfceRouter(stubResolver{
		err: &nfce.CaptchaError{ChallengeID: "abc123", ImageURL: "../DownloadCaptcha.ashx"},
	}), `{"access_key":"`+senateKey+`"}`)

	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	code, extra := decodeErrorEnvelope(t, rec)
	if code != "captcha_required" {
		t.Errorf("code = %q", code)
	}
	if extra["challenge_id"] != "abc123" || extra["image_url"] != "../DownloadCaptcha.ashx" {
		t.Errorf("captcha envelope = %v", extra)
	}
}

func TestNfceSubmitNotFound(t *testing.T) {
	rec := postSubmit(t, nfceRouter(stubResolver{err: nfce.ErrKeyNotFound}),
		`{"access_key":"`+senateKey+`"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
	if code, _ := decodeErrorEnvelope(t, rec); code != "receipt_not_found" {
		t.Errorf("code = %q", code)
	}
}

func TestNfceSubmitUpstream(t *testing.T) {
	for _, err := range []error{
		errors.Join(nfce.ErrUpstream, errors.New("boom")),
		nfce.ErrBadResponse,
	} {
		rec := postSubmit(t, nfceRouter(stubResolver{err: err}),
			`{"access_key":"`+senateKey+`"}`)
		if rec.Code != http.StatusBadGateway {
			t.Errorf("%v: status = %d, want 502", err, rec.Code)
		}
		if code, _ := decodeErrorEnvelope(t, rec); code != "upstream_error" {
			t.Errorf("%v: code = %q", err, code)
		}
	}
}

func TestNfceSubmitUnknownError(t *testing.T) {
	rec := postSubmit(t, nfceRouter(stubResolver{err: errors.New("mystery")}),
		`{"access_key":"`+senateKey+`"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestNfceSubmitQRPayloadReplayed(t *testing.T) {
	// A v3 payload built from the public key, embedded in a full URL as a
	// consumer scanner would hand us.
	v3 := senateKey + "|3|1"
	url := "https://www.nfce.fazenda.sp.gov.br/qrcode?p=" + v3
	capture := &captureResolver{}
	rec := postSubmit(t, nfceRouter(capture),
		`{"qr_payload":"`+url+`"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if capture.payload != v3 {
		t.Errorf("resolver received payload %q, want the exact scanned value %q", capture.payload, v3)
	}
}

func TestNfceSubmitQRPayloadBareKeyNormalized(t *testing.T) {
	capture := &captureResolver{}
	rec := postSubmit(t, nfceRouter(capture),
		`{"qr_payload":"`+senateKey+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if capture.payload != senateKey+"|3|1" {
		t.Errorf("resolver received payload %q, want %q", capture.payload, senateKey+"|3|1")
	}
}

func TestNfceSubmitQRPayloadInvalid(t *testing.T) {
	for _, payload := range []string{"banana", "3524|3|1", "3524115232368800014465002000000257111567590x|3|1"} {
		rec := postSubmit(t, nfceRouter(stubResolver{}), `{"qr_payload":"`+payload+`"}`)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("payload %q: status = %d, want 422", payload, rec.Code)
		}
		if code, _ := decodeErrorEnvelope(t, rec); code != "invalid_qr_payload" {
			t.Errorf("payload %q: code = %q", payload, code)
		}
	}
}

func TestNfceSubmitQRPayloadConflictsWithKey(t *testing.T) {
	other := "35241211222333000181650010000000911449128550"
	rec := postSubmit(t, nfceRouter(stubResolver{}),
		`{"access_key":"`+senateKey+`","qr_payload":"`+other+`|3|1"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if code, _ := decodeErrorEnvelope(t, rec); code != "invalid_qr_payload" {
		t.Errorf("code = %q", code)
	}
}

func TestNfceSubmitUnsupportedUF(t *testing.T) {
	// Valid MG (31) key; the stub resolver answers the way the MultiResolver
	// does for any state without a fixture-verified parser.
	const mgKey = "31240112345000123456650010000002571123456785"
	rec := postSubmit(t, nfceRouter(stubResolver{err: nfce.ErrUnsupportedUF}),
		`{"access_key":"`+mgKey+`"}`)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d", rec.Code)
	}
	if code, _ := decodeErrorEnvelope(t, rec); code != "unsupported_uf" {
		t.Errorf("code = %q", code)
	}
}
