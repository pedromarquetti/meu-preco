package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"meupreco/internal/domain"
)

func snapshotReceiptFixture() *domain.Nfce {
	issued := time.Date(2024, 11, 2, 10, 30, 0, 0, time.UTC)
	created := time.Date(2024, 11, 2, 11, 0, 0, 0, time.UTC)
	createdBy := int64(7)

	return &domain.Nfce{
		ID:        0,
		AccessKey: "35241152323688000144650020000002571115675901",
		Supermarket: domain.Supermarket{
			CNPJ:      "12345678000199",
			LegalName: "MERCADO BOM PRECO LTDA",
			Address:   "RUA EXEMPLO, 100",
		},
		Number:        "257",
		Series:        "002",
		IssuedAt:      &issued,
		Protocol:      "135241234567890",
		Environment:   "Produção",
		PaymentMethod: "Cartão de Crédito",
		Total:         decimal.RequireFromString("59.06"),
		Discount:      decimal.RequireFromString("0.00"),
		PaidValue:     decimal.RequireFromString("59.06"),
		COO:           "123456",
		PayloadRaw:    "<nota>stripped</nota>",
		CreatedBy:     &createdBy,
		CreatedAt:     created,
		Items: []domain.NfceItem{
			{
				Line:        1,
				Code:        "7891000412855",
				Description: "ACHOCOLATADO EM PO NESCAU 350G",
				Qty:         decimal.RequireFromString("1"),
				Unit:        "UN",
				UnitPrice:   decimal.RequireFromString("22.99"),
				TotalPrice:  decimal.RequireFromString("22.99"),
			},
			{
				Line:        2,
				Code:        "",
				Description: "LIMAO TAHITI KG",
				Qty:         decimal.RequireFromString("0.815"),
				Unit:        "KG",
				UnitPrice:   decimal.RequireFromString("5.99"),
				TotalPrice:  decimal.RequireFromString("4.88"),
				ParseError:  "approximate unit price",
			},
		},
		Warnings: []string{"line 2 unit price normalized"},
	}
}

func TestReceiptSnapshotColumnName(t *testing.T) {
	if receiptSnapshotColumn != "receipt_snapshot" {
		t.Fatalf("receipt snapshot column = %q, want receipt_snapshot", receiptSnapshotColumn)
	}
}

func TestReceiptSnapshotRoundTrip(t *testing.T) {
	want := snapshotReceiptFixture()

	raw, err := encodeReceiptSnapshot(want)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	got, err := decodeReceiptSnapshot(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	again, err := encodeReceiptSnapshot(got)
	if err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	if !bytes.Equal(raw, again) {
		t.Fatalf("snapshot did not round-trip:\nfirst:  %s\nsecond: %s", raw, again)
	}

	if got.AccessKey != want.AccessKey {
		t.Errorf("access_key = %q, want %q", got.AccessKey, want.AccessKey)
	}
	if got.Supermarket.CNPJ != want.Supermarket.CNPJ || got.Supermarket.LegalName != want.Supermarket.LegalName {
		t.Errorf("supermarket = %+v, want %+v", got.Supermarket, want.Supermarket)
	}
	if len(got.Items) != len(want.Items) {
		t.Fatalf("items = %d, want %d", len(got.Items), len(want.Items))
	}
	for i := range want.Items {
		if !got.Items[i].Qty.Equal(want.Items[i].Qty) {
			t.Errorf("item %d qty = %s, want %s", i+1, got.Items[i].Qty, want.Items[i].Qty)
		}
		if !got.Items[i].TotalPrice.Equal(want.Items[i].TotalPrice) {
			t.Errorf("item %d total = %s, want %s", i+1, got.Items[i].TotalPrice, want.Items[i].TotalPrice)
		}
	}
}

func TestReceiptSnapshotEnvelopeAndFieldNames(t *testing.T) {
	raw, err := encodeReceiptSnapshot(snapshotReceiptFixture())
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("envelope is not JSON: %v", err)
	}
	if _, ok := envelope["receipt"]; !ok {
		t.Error("envelope missing receipt")
	}

	var version int
	if err := json.Unmarshal(envelope["schema_version"], &version); err != nil {
		t.Fatalf("schema_version: %v", err)
	}
	if version != receiptSnapshotSchemaVersion {
		t.Errorf("schema_version = %d, want %d", version, receiptSnapshotSchemaVersion)
	}

	var receipt map[string]json.RawMessage
	if err := json.Unmarshal(envelope["receipt"], &receipt); err != nil {
		t.Fatalf("receipt is not JSON: %v", err)
	}
	for _, key := range []string{
		"access_key", "supermarket", "issued_at", "payment_method",
		"total", "paid_value", "payload_raw", "items", "warnings",
	} {
		if _, ok := receipt[key]; !ok {
			t.Errorf("receipt missing stable snapshot field %q", key)
		}
	}
}

func TestReceiptSnapshotKeepsDecimalsAsStrings(t *testing.T) {
	raw, err := encodeReceiptSnapshot(snapshotReceiptFixture())
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	// decimal.Decimal must stay an exact JSON string; a float conversion would
	// drop the quotes and lose precision/scale.
	for _, want := range []string{`"total":"59.06"`, `"qty":"0.815"`, `"unit_price":"5.99"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("snapshot missing exact decimal encoding %s\nsnapshot: %s", want, raw)
		}
	}
}

func TestEncodeReceiptSnapshotMissingReceipt(t *testing.T) {
	if _, err := encodeReceiptSnapshot(nil); !errors.Is(err, ErrReceiptSnapshotMissing) {
		t.Fatalf("encode nil error = %v, want ErrReceiptSnapshotMissing", err)
	}
}

func TestDecodeReceiptSnapshotMissing(t *testing.T) {
	cases := map[string]json.RawMessage{
		"nil":              nil,
		"empty":            {},
		"whitespace":       json.RawMessage(" \n\t"),
		"json null":        json.RawMessage("null"),
		"padded json null": json.RawMessage(" null "),
	}

	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			receipt, err := decodeReceiptSnapshot(raw)
			if receipt != nil {
				t.Errorf("receipt = %+v, want nil", receipt)
			}
			if !errors.Is(err, ErrReceiptSnapshotMissing) {
				t.Fatalf("error = %v, want ErrReceiptSnapshotMissing", err)
			}
		})
	}
}

func TestDecodeReceiptSnapshotCorrupt(t *testing.T) {
	cases := map[string]json.RawMessage{
		"invalid json":     json.RawMessage("{"),
		"empty object":     json.RawMessage(`{}`),
		"missing version":  json.RawMessage(`{"receipt":{}}`),
		"missing receipt":  json.RawMessage(`{"schema_version":1}`),
		"null receipt":     json.RawMessage(`{"schema_version":1,"receipt":null}`),
		"bad decimal":      json.RawMessage(`{"schema_version":1,"receipt":{"total":"not-a-decimal"}}`),
		"non-int version":  json.RawMessage(`{"schema_version":"1","receipt":{}}`),
		"wrong json shape": json.RawMessage(`[]`),
	}

	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			receipt, err := decodeReceiptSnapshot(raw)
			if receipt != nil {
				t.Errorf("receipt = %+v, want nil", receipt)
			}
			if !errors.Is(err, ErrReceiptSnapshotCorrupt) {
				t.Fatalf("error = %v, want ErrReceiptSnapshotCorrupt", err)
			}
		})
	}
}

func TestDecodeReceiptSnapshotUnsupportedVersion(t *testing.T) {
	raw := json.RawMessage(`{"schema_version":2,"receipt":{}}`)

	receipt, err := decodeReceiptSnapshot(raw)
	if receipt != nil {
		t.Errorf("receipt = %+v, want nil", receipt)
	}
	if !errors.Is(err, ErrReceiptSnapshotUnsupported) {
		t.Fatalf("error = %v, want ErrReceiptSnapshotUnsupported", err)
	}
}

// TestReceiptSnapshotSizeBudget records the storage envelope the retention
// decision relies on: even a long 60-line receipt stays far below the request
// and DANFE safeguards, so receipt_snapshot needs no separate size cap.
func TestReceiptSnapshotSizeBudget(t *testing.T) {
	receipt := snapshotReceiptFixture()
	receipt.Items = make([]domain.NfceItem, 0, 60)
	for i := 1; i <= 60; i++ {
		receipt.Items = append(receipt.Items, domain.NfceItem{
			Line:        i,
			Code:        fmt.Sprintf("789100041%04d", i),
			Description: "ACHOCOLATADO EM PO NESCAU 350G COM FERRO E VITAMINAS",
			Qty:         decimal.RequireFromString("1"),
			Unit:        "UN",
			UnitPrice:   decimal.RequireFromString("22.99"),
			TotalPrice:  decimal.RequireFromString("22.99"),
		})
	}

	raw, err := encodeReceiptSnapshot(receipt)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	const budget = 64 << 10 // 64 KiB
	if len(raw) > budget {
		t.Fatalf("60-line snapshot = %d bytes, above the %d-byte budget", len(raw), budget)
	}
	t.Logf("60-line snapshot = %d bytes (budget %d)", len(raw), budget)
}
