// Package logger provides the human-readable slog handler used by the server
// for local development and terminal output.
package logger

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// timeFormat renders local time with millisecond precision, e.g.
// [02-01-2006 15:04:05.000].
const timeFormat = "02-01-2006 15:04:05.000"

// Handler is a slog.Handler that renders one line per record:
//
//	[02-01-2006 15:04:05.000] INFO  http_request method=GET path=/ status=404
//
// Attributes are written as sorted key=value pairs after the message.
type Handler struct {
	out  io.Writer
	opts slog.HandlerOptions
	mu   *sync.Mutex

	attrs  []slog.Attr
	groups []string
}

// NewPrettyHandler builds a pretty handler writing to out. A nil opts is
// equivalent to &slog.HandlerOptions{} (level defaults to Info).
func NewPrettyHandler(out io.Writer, opts *slog.HandlerOptions) *Handler {
	if opts == nil {
		opts = &slog.HandlerOptions{}
	}
	return &Handler{out: out, opts: *opts, mu: &sync.Mutex{}}
}

func (h *Handler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.opts.Level.Level()
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	nh := *h
	nh.attrs = append(h.attrs, attrs...) //nolint:gocritic // copy-on-append, safe for read-only concurrent use
	return &nh
}

func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	nh := *h
	nh.groups = append(nh.groups, name) //nolint:gocritic // copy-on-append, safe for read-only concurrent use
	return &nh
}

func (h *Handler) Handle(_ context.Context, r slog.Record) error {
	var buf bytes.Buffer

	if !r.Time.IsZero() {
		fmt.Fprintf(&buf, "[%s] ", r.Time.Local().Format(timeFormat))
	}

	fmt.Fprintf(&buf, "%-5s", strings.ToUpper(r.Level.String()))

	if r.Message != "" {
		buf.WriteByte(' ')
		buf.WriteString(r.Message)
	}

	attrs := make([]slog.Attr, 0, r.NumAttrs()+len(h.attrs))
	r.Attrs(func(a slog.Attr) bool {
		if h.opts.ReplaceAttr != nil {
			a = h.opts.ReplaceAttr(h.groups, a)
		}
		attrs = append(attrs, a)
		return true
	})
	for _, a := range h.attrs {
		if h.opts.ReplaceAttr != nil {
			a = h.opts.ReplaceAttr(h.groups, a)
		}
		attrs = append(attrs, a)
	}

	if len(attrs) > 0 {
		sort.Slice(attrs, func(i, j int) bool { return attrs[i].Key < attrs[j].Key })
		h.writeAttrs(&buf, h.groups, attrs)
	}

	buf.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := h.out.Write(buf.Bytes())
	return err
}

// writeAttrs renders attrs as " key=value" pairs. base is the dot-joined
// prefix from enclosing groups; group-typed values recurse into a wider key.
func (h *Handler) writeAttrs(buf *bytes.Buffer, base []string, attrs []slog.Attr) {
	for _, a := range attrs {
		a.Value = a.Value.Resolve()

		if a.Value.Kind() == slog.KindGroup {
			h.writeAttrs(buf, append(append([]string{}, base...), a.Key), a.Value.Group())
			continue
		}

		key := strings.Join(append(append([]string{}, base...), a.Key), ".")
		buf.WriteByte(' ')
		buf.WriteString(key)
		buf.WriteByte('=')
		buf.WriteString(h.valueString(a.Value))
	}
}

func (h *Handler) valueString(v slog.Value) string {
	switch v.Kind() {
	case slog.KindString:
		return quoteIfNeeded(v.String())
	case slog.KindBool:
		return strconv.FormatBool(v.Bool())
	case slog.KindDuration:
		return v.Duration().String()
	case slog.KindTime:
		return v.Time().Local().Format(timeFormat)
	case slog.KindInt64:
		return strconv.FormatInt(v.Int64(), 10)
	case slog.KindUint64:
		return strconv.FormatUint(v.Uint64(), 10)
	case slog.KindFloat64:
		return strconv.FormatFloat(v.Float64(), 'g', -1, 64)
	default:
		return quoteIfNeeded(fmt.Sprint(v.Any()))
	}
}

// quoteIfNeeded quotes values that contain whitespace or other characters that
// would make the key=value output ambiguous.
func quoteIfNeeded(s string) string {
	if s == "" || strings.ContainsAny(s, " \t\n\"=") {
		return strconv.Quote(s)
	}
	return s
}

var _ slog.Handler = (*Handler)(nil)
