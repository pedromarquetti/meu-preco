package service

import (
	"context"
	"errors"
	"strings"

	"meupreco/internal/domain"
)

// DeleteReceiptRequest is the wire body of POST /nfce/delete.
type DeleteReceiptRequest struct {
	AccessKey string `json:"access_key"`
}

// DeleteReceiptResult reports what the deleting transaction removed.
type DeleteReceiptResult struct {
	AccessKey       string `json:"access_key"`
	NfceID          int64  `json:"nfce_id"`
	LogsDeleted     int64  `json:"logs_deleted"`
	PreviewsDeleted int64  `json:"previews_deleted"`
}

// DeleteReceipt implements POST /nfce/delete: removes the receipt, its
// price_logs and every preview draft of that access key in one transaction.
// Guarded by ownership (the user who committed it, via nfces.created_by) OR an
// admin caller.
func (s *Service) DeleteReceipt(ctx context.Context, callerID int64, req DeleteReceiptRequest, callerIsAdmin bool) (*DeleteReceiptResult, error) {
	accessKey := strings.TrimSpace(req.AccessKey)
	if accessKey == "" {
		return nil, ErrInvalidFilter
	}

	nf, err := s.Stores.Nfces().GetByAccessKey(ctx, accessKey)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, ErrReceiptNotFound
		}
		return nil, err
	}

	if !callerIsAdmin && (nf.CreatedBy == nil || *nf.CreatedBy != callerID) {
		return nil, ErrForbidden
	}

	res := &DeleteReceiptResult{AccessKey: accessKey, NfceID: nf.ID}
	err = s.Stores.Tx(ctx, func(tx domain.Stores) error {
		logs, err := tx.PriceLogs().DeleteByNfceID(ctx, nf.ID)
		if err != nil {
			return err
		}
		res.LogsDeleted = logs

		previews, err := tx.Previews().DeleteByAccessKey(ctx, accessKey)
		if err != nil {
			return err
		}
		res.PreviewsDeleted = previews

		if err := tx.Nfces().Delete(ctx, nf.ID); err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return ErrReceiptNotFound
			}
			return err
		}
		return nil
	})
	if err != nil {
		return res, err
	}
	return res, nil
}
