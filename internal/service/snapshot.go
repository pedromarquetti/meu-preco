package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"meupreco/internal/domain"
)

// This file defines the contract for the receipt snapshot a pending preview
// carries into submit.
//
// The snapshot is the normalized, privacy-stripped domain.Nfce returned by the
// resolver during draft creation — never the raw portal HTML/XML. It is stored
// in previews.receipt_snapshot as a JSONB document with the versioned envelope
// below, so future contract changes can be detected instead of being silently
// interpreted as the current schema.
//
// Submit must decode this snapshot rather than fetching SEFAZ again. A missing
// snapshot is not an invitation to re-resolve the receipt: callers must surface
// the typed error so a legacy/corrupt draft fails explicitly instead of
// reintroducing the portal dependency.
//
// Size note: a 60-line normalized receipt serializes to roughly 12 KiB. That is
// small enough for PostgreSQL JSONB/TOAST and well below the existing
// request/DANFE safeguards, so receipt_snapshot has no separate size cap. The
// snapshot tests pin a 64 KiB budget to catch accidental bloat (for example,
// storing raw portal HTML instead of the parsed receipt).
const (
	// receiptSnapshotColumn is the persisted column name. The same name must
	// be used by the migration, domain field, PostgreSQL store, and tests.
	receiptSnapshotColumn = "receipt_snapshot"

	// receiptSnapshotSchemaVersion is the only envelope version this build
	// understands. Bump it whenever the serialized receipt shape changes in a
	// way that is not backward compatible.
	receiptSnapshotSchemaVersion = 1
)

// Sentinel errors for the snapshot contract. They distinguish a missing
// snapshot (typically a legacy preview created before snapshots were written)
// from an unreadable one (corrupt JSON / receipt payload) and from a version
// this build cannot decode.
var (
	// ErrReceiptSnapshotMissing reports a previews row without a
	// receipt_snapshot value.
	ErrReceiptSnapshotMissing = errors.New("preview receipt snapshot missing")
	// ErrReceiptSnapshotCorrupt reports a receipt_snapshot value that is not a
	// valid envelope or whose receipt payload cannot be decoded.
	ErrReceiptSnapshotCorrupt = errors.New("preview receipt snapshot corrupt")
	// ErrReceiptSnapshotUnsupported reports an envelope whose schema_version is
	// unknown to this build.
	ErrReceiptSnapshotUnsupported = errors.New("preview receipt snapshot version unsupported")
)

// receiptSnapshotEnvelope is the versioned JSON document stored in
// previews.receipt_snapshot:
//
//	{"schema_version": 1, "receipt": { ...domain.Nfce... }}
type receiptSnapshotEnvelope struct {
	SchemaVersion *int         `json:"schema_version"`
	Receipt       *domain.Nfce `json:"receipt"`
}

// encodeReceiptSnapshot serializes the normalized receipt into the versioned
// envelope written to previews.receipt_snapshot. decimal.Decimal round-trips
// through its JSON string representation, so money and quantities never pass
// through floating point.
func encodeReceiptSnapshot(receipt *domain.Nfce) (json.RawMessage, error) {
	if receipt == nil {
		return nil, fmt.Errorf("%w: draft receipt is nil", ErrReceiptSnapshotMissing)
	}

	version := receiptSnapshotSchemaVersion
	raw, err := json.Marshal(receiptSnapshotEnvelope{
		SchemaVersion: &version,
		Receipt:       receipt,
	})
	if err != nil {
		return nil, fmt.Errorf("service: encode %s: %w", receiptSnapshotColumn, err)
	}

	return raw, nil
}

// decodeReceiptSnapshot parses a preview's receipt_snapshot value. It returns
// a typed error for every invalid state and never falls back to the resolver.
func decodeReceiptSnapshot(raw json.RawMessage) (*domain.Nfce, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, fmt.Errorf("%w: %s is empty", ErrReceiptSnapshotMissing, receiptSnapshotColumn)
	}

	var envelope receiptSnapshotEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("%w: decode %s: %w", ErrReceiptSnapshotCorrupt, receiptSnapshotColumn, err)
	}

	if envelope.SchemaVersion == nil {
		return nil, fmt.Errorf("%w: envelope has no schema_version", ErrReceiptSnapshotCorrupt)
	}
	if *envelope.SchemaVersion != receiptSnapshotSchemaVersion {
		return nil, fmt.Errorf("%w: schema_version %d, supported %d",
			ErrReceiptSnapshotUnsupported, *envelope.SchemaVersion, receiptSnapshotSchemaVersion)
	}
	if envelope.Receipt == nil {
		return nil, fmt.Errorf("%w: envelope has no receipt", ErrReceiptSnapshotCorrupt)
	}

	return envelope.Receipt, nil
}
