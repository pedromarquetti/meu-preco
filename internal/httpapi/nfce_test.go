package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

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

// captureResolver records the payload the service forwarded, so tests can
// assert the qr_payload path is actually used.
type captureResolver struct {
	payload string
}

func (c *captureResolver) Resolve(_ context.Context, _ nfce.AccessKey, payload string) (*domain.Nfce, error) {
	c.payload = payload
	return &domain.Nfce{AccessKey: senateKey}, nil
}

// nfceRouter builds the full router for user 1 against hermetic stub stores,
// issuing a valid bearer token the middleware accepts.
func nfceRouter(t *testing.T, res nfceResolver, stores *stubStores) http.Handler {
	t.Helper()
	if stores == nil {
		stores = newStubStores()
	}
	return ingestionRouter(t, stores, res, 1)
}

func postNfce(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
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

// uidUncertainReceipt is a GTIN-less receipt line the stub catalog cannot
// identify, so the plan resolves to uid_uncertain.
func uidUncertainReceipt() *domain.Nfce {
	return &domain.Nfce{
		AccessKey:     senateKey,
		Number:        "257",
		Series:        "002",
		PaymentMethod: "Cartão de Crédito",
		Total:         decimal.RequireFromString("59.06"),
		Items: []domain.NfceItem{
			{Line: 1, Description: "LIMAO TAHITI KG", Qty: decimal.RequireFromString("0.815")},
		},
	}
}

func TestNfceNewBuildsDraftAndReplays(t *testing.T) {
	res := stubResolver{receipt: uidUncertainReceipt()}
	h := nfceRouter(t, res, nil)

	rec := postNfce(t, h, "/api/v1/nfce/new", `{"access_key":"`+senateKey+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var plan struct {
		PreviewID int64 `json:"preview_id"`
		Status    string
		AccessKey string `json:"access_key"`
		Items     []struct {
			Line   int    `json:"line"`
			Status string `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &plan); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if plan.PreviewID == 0 || plan.Status != "pending" {
		t.Errorf("preview = %+v, want a pending id", plan)
	}
	if plan.AccessKey != senateKey {
		t.Errorf("access_key = %q, want normalized %q", plan.AccessKey, senateKey)
	}
	if len(plan.Items) != 1 || plan.Items[0].Status != "uid_uncertain" {
		t.Errorf("plan items = %+v, want 1 uid_uncertain", plan.Items)
	}

	// Re-drafting the same receipt while pending replays the same plan.
	rec2 := postNfce(t, h, "/api/v1/nfce/new", `{"access_key":"`+senateKey+`"}`)
	if rec2.Code != http.StatusOK {
		t.Fatalf("replay status = %d", rec2.Code)
	}
	var replay struct {
		PreviewID int64 `json:"preview_id"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &replay); err != nil {
		t.Fatalf("unmarshal replay: %v", err)
	}
	if replay.PreviewID != plan.PreviewID {
		t.Errorf("replay preview_id = %d, want %d", replay.PreviewID, plan.PreviewID)
	}
}

func TestNfceNewInvalidKey(t *testing.T) {
	rec := postNfce(t, nfceRouter(t, stubResolver{}, nil), "/api/v1/nfce/new", `{"access_key":"123"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d", rec.Code)
	}
	if code, _ := decodeErrorEnvelope(t, rec); code != "invalid_access_key" {
		t.Errorf("code = %q", code)
	}
}

func TestNfceNewBadBody(t *testing.T) {
	for _, body := range []string{"", "nope", `{"access_key":`} {
		rec := postNfce(t, nfceRouter(t, stubResolver{}, nil), "/api/v1/nfce/new", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %q: status = %d, want 400", body, rec.Code)
		}
		if code, _ := decodeErrorEnvelope(t, rec); code != "invalid_request" {
			t.Errorf("body %q: code = %q", body, code)
		}
	}
}

func TestNfceNewCaptcha(t *testing.T) {
	rec := postNfce(t, nfceRouter(t, stubResolver{
		err: &nfce.CaptchaError{ChallengeID: "abc123", ImageURL: "../DownloadCaptcha.ashx"},
	}, nil), "/api/v1/nfce/new", `{"access_key":"`+senateKey+`"}`)

	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	code, extra := decodeErrorEnvelope(t, rec)
	if code != "captcha_required" {
		t.Errorf("code = %q", code)
	}
	if extra["challenge_id"] != "abc123" {
		t.Errorf("challenge_id = %v", extra["challenge_id"])
	}
	// The client renders the image through our proxy, never the SEFAZ URL:
	// cookies and viewstate stay server-side.
	if extra["image_url"] != "/api/v1/nfce/captcha/image/abc123" {
		t.Errorf("image_url = %v, want the proxied challenge path", extra["image_url"])
	}
}

func TestNfceNewNotFound(t *testing.T) {
	rec := postNfce(t, nfceRouter(t, stubResolver{err: nfce.ErrKeyNotFound}, nil),
		"/api/v1/nfce/new", `{"access_key":"`+senateKey+`"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
	if code, _ := decodeErrorEnvelope(t, rec); code != "receipt_not_found" {
		t.Errorf("code = %q", code)
	}
}

func TestNfceNewUpstream(t *testing.T) {
	for _, err := range []error{
		errors.Join(nfce.ErrUpstream, errors.New("boom")),
		nfce.ErrBadResponse,
	} {
		rec := postNfce(t, nfceRouter(t, stubResolver{err: err}, nil),
			"/api/v1/nfce/new", `{"access_key":"`+senateKey+`"}`)
		if rec.Code != http.StatusBadGateway {
			t.Errorf("%v: status = %d, want 502", err, rec.Code)
		}
		if code, _ := decodeErrorEnvelope(t, rec); code != "upstream_error" {
			t.Errorf("%v: code = %q", err, code)
		}
	}
}

func TestNfceNewUnknownError(t *testing.T) {
	rec := postNfce(t, nfceRouter(t, stubResolver{err: errors.New("mystery")}, nil),
		"/api/v1/nfce/new", `{"access_key":"`+senateKey+`"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestNfceNewQRPayloadReplayed(t *testing.T) {
	// A v3 payload built from the public key, embedded in a full URL as a
	// consumer scanner would hand us.
	v3 := senateKey + "|3|1"
	url := "https://www.nfce.fazenda.sp.gov.br/qrcode?p=" + v3
	capture := &captureResolver{}
	rec := postNfce(t, nfceRouter(t, capture, nil),
		"/api/v1/nfce/new", `{"qr_payload":"`+url+`"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if capture.payload != v3 {
		t.Errorf("resolver received payload %q, want the exact scanned value %q", capture.payload, v3)
	}
}

func TestNfceNewQRPayloadBareKeyNormalized(t *testing.T) {
	capture := &captureResolver{}
	rec := postNfce(t, nfceRouter(t, capture, nil),
		"/api/v1/nfce/new", `{"qr_payload":"`+senateKey+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if capture.payload != senateKey+"|3|1" {
		t.Errorf("resolver received payload %q, want %q", capture.payload, senateKey+"|3|1")
	}
}

func TestNfceNewQRPayloadInvalid(t *testing.T) {
	for _, payload := range []string{"banana", "3524|3|1", "3524115232368800014465002000000257111567590x|3|1"} {
		rec := postNfce(t, nfceRouter(t, stubResolver{}, nil),
			"/api/v1/nfce/new", `{"qr_payload":"`+payload+`"}`)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("payload %q: status = %d, want 422", payload, rec.Code)
		}
		if code, _ := decodeErrorEnvelope(t, rec); code != "invalid_qr_payload" {
			t.Errorf("payload %q: code = %q", payload, code)
		}
	}
}

func TestNfceNewQRPayloadConflictsWithKey(t *testing.T) {
	other := "35241211222333000181650010000000911449128550"
	rec := postNfce(t, nfceRouter(t, stubResolver{}, nil),
		"/api/v1/nfce/new", `{"access_key":"`+senateKey+`","qr_payload":"`+other+`|3|1"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if code, _ := decodeErrorEnvelope(t, rec); code != "invalid_qr_payload" {
		t.Errorf("code = %q", code)
	}
}

func TestNfceNewUnsupportedUF(t *testing.T) {
	// Valid MG (31) key; the MultiResolver answers 501 for any state without
	// a fixture-verified parser.
	const mgKey = "31240112345000123456650010000002571123456785"
	rec := postNfce(t, nfceRouter(t, stubResolver{err: nfce.ErrUnsupportedUF}, nil),
		"/api/v1/nfce/new", `{"access_key":"`+mgKey+`"}`)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d", rec.Code)
	}
	if code, _ := decodeErrorEnvelope(t, rec); code != "unsupported_uf" {
		t.Errorf("code = %q", code)
	}
}

func TestNfceSubmitBadBody(t *testing.T) {
	for _, body := range []string{"", "nope", `{"preview_id":`} {
		rec := postNfce(t, nfceRouter(t, stubResolver{}, nil), "/api/v1/nfce/submit", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %q: status = %d, want 400", body, rec.Code)
		}
		if code, _ := decodeErrorEnvelope(t, rec); code != "invalid_request" {
			t.Errorf("body %q: code = %q", body, code)
		}
	}
}

func TestNfceSubmitForeignPreviewIs404(t *testing.T) {
	st := newStubStores()
	if err := st.Previews().Create(context.Background(), &domain.Preview{
		UserID: 2, AccessKey: senateKey,
		PlanItems: json.RawMessage(`[{"line":1,"status":"uid_uncertain"}]`),
		Status:    "pending", TTL: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("seed foreign preview: %v", err)
	}

	rec := postNfce(t, nfceRouter(t, stubResolver{}, st), "/api/v1/nfce/submit",
		`{"preview_id":1,"confirm":true}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if code, _ := decodeErrorEnvelope(t, rec); code != "preview_not_found" {
		t.Errorf("code = %q", code)
	}
}

func TestNfceSubmitDoubleCommitIs409(t *testing.T) {
	st := newStubStores()
	if err := st.Previews().Create(context.Background(), &domain.Preview{
		UserID: 1, AccessKey: senateKey,
		PlanItems: json.RawMessage(`[{"line":1,"status":"uid_uncertain"}]`),
		Status:    "committed", TTL: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("seed committed preview: %v", err)
	}

	rec := postNfce(t, nfceRouter(t, stubResolver{}, st), "/api/v1/nfce/submit",
		`{"preview_id":1,"confirm":true}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if code, _ := decodeErrorEnvelope(t, rec); code != "preview_already_committed" {
		t.Errorf("code = %q", code)
	}
}

func TestNfceSubmitExpiredIs422(t *testing.T) {
	st := newStubStores()
	if err := st.Previews().Create(context.Background(), &domain.Preview{
		UserID: 1, AccessKey: senateKey,
		PlanItems: json.RawMessage(`[{"line":1,"status":"uid_uncertain"}]`),
		Status:    "pending", TTL: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("seed expired preview: %v", err)
	}

	rec := postNfce(t, nfceRouter(t, stubResolver{}, st), "/api/v1/nfce/submit",
		`{"preview_id":1,"confirm":true}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if code, _ := decodeErrorEnvelope(t, rec); code != "preview_expired" {
		t.Errorf("code = %q", code)
	}
}

// TestNfceSubmitMissingPreviewIDIs400: a request with a declared confirm but no
// preview_id is a client error, not an internal one.
func TestNfceSubmitMissingPreviewIDIs400(t *testing.T) {
	rec := postNfce(t, nfceRouter(t, stubResolver{}, nil), "/api/v1/nfce/submit",
		`{"confirm":true}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	if code, _ := decodeErrorEnvelope(t, rec); code != "invalid_request" {
		t.Errorf("code = %q, want invalid_request", code)
	}
}

// TestNfceSubmitMissingConfirmIs400: confirm is required; an omitted field must
// not be silently interpreted as confirm=false (a dry run).
func TestNfceSubmitMissingConfirmIs400(t *testing.T) {
	st := newStubStores()
	if err := st.Previews().Create(context.Background(), &domain.Preview{
		UserID: 1, AccessKey: senateKey,
		PlanItems: json.RawMessage(`[{"line":1,"status":"uid_uncertain"}]`),
		Status:    "pending", TTL: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("seed preview: %v", err)
	}

	rec := postNfce(t, nfceRouter(t, stubResolver{}, st), "/api/v1/nfce/submit",
		`{"preview_id":1}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	if code, _ := decodeErrorEnvelope(t, rec); code != "invalid_request" {
		t.Errorf("code = %q, want invalid_request", code)
	}
}
