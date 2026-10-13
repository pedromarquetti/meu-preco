package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"meupreco/internal/domain"
	"meupreco/internal/httpx"
	"meupreco/internal/service"
)

// respondServiceError maps the service's typed failures (and the raw domain
// sentinels) onto the canonical error envelope. It covers the read and
// admin endpoints; the ingestion handlers keep their own richer mapping.
func respondServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, service.ErrProductNotFound),
		errors.Is(err, service.ErrSupermarketNotFound),
		errors.Is(err, service.ErrReceiptNotFound),
		errors.Is(err, service.ErrUserNotFound):
		httpx.Error(w, r, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, service.ErrForbidden):
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "you may not perform this action")
	case errors.Is(err, service.ErrInUse):
		httpx.Error(w, r, http.StatusConflict, "in_use",
			"resource still referenced; purge explicitly or remove references first")
	case errors.Is(err, service.ErrInvalidFilter):
		httpx.Error(w, r, http.StatusBadRequest, "invalid_filter", err.Error())
	case errors.Is(err, service.ErrInvalidAssignment):
		httpx.Error(w, r, http.StatusBadRequest, "invalid_assignment", err.Error())
	case errors.Is(err, service.ErrInvalidRole):
		httpx.Error(w, r, http.StatusBadRequest, "invalid_role", err.Error())
	case errors.Is(err, domain.ErrNotFound):
		httpx.Error(w, r, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, domain.ErrConflict):
		httpx.Error(w, r, http.StatusConflict, "conflict", err.Error())
	default:
		httpx.InternalError(w, r)
	}
}

// readPager parses the shared ?page=&limit= query pair. Defaults: page=1,
// limit=20. Invalid numeric values or out-of-range limits -> 400.
func readPager(w http.ResponseWriter, r *http.Request) (page, limit int, ok bool) {
	page, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || page < 1 {
		page = 1
	}
	limitS := r.URL.Query().Get("limit")
	if limitS == "" {
		limit = 20
	} else {
		limit, err = strconv.Atoi(limitS)
		if err != nil {
			httpx.Error(w, r, http.StatusBadRequest, "invalid_filter", "limit must be an integer")
			return 0, 0, false
		}
	}
	return page, limit, true
}

// parseWindow parses an optional ?from=&to= date window (YYYY-MM-DD or RFC3339).
// One malformed endpoit rejects the whole request with 400.
func parseWindow(w http.ResponseWriter, r *http.Request) (from, to *time.Time, ok bool) {
	if raw := r.URL.Query().Get("from"); raw != "" {
		t, perr := parseDateOrTime(raw)
		if perr != nil {
			httpx.Error(w, r, http.StatusBadRequest, "invalid_filter",
				"from must be a date (YYYY-MM-DD) or RFC3339 timestamp")
			return nil, nil, false
		}
		from = &t
	}
	if raw := r.URL.Query().Get("to"); raw != "" {
		t, perr := parseDateOrTime(raw)
		if perr != nil {
			httpx.Error(w, r, http.StatusBadRequest, "invalid_filter",
				"to must be a date (YYYY-MM-DD) or RFC3339 timestamp")
			return nil, nil, false
		}
		to = &t
	}
	return from, to, true
}

func parseDateOrTime(raw string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02", time.RFC3339} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, nil
		}
	}
	return time.Time{}, errors.New("unparseable date")
}

// pathID parses the {id} route parameter as a positive int64.
func pathID(w http.ResponseWriter, r *http.Request, v string) (int64, bool) {
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil || id <= 0 {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_filter", "path id must be a positive integer")
		return 0, false
	}
	return id, true
}
