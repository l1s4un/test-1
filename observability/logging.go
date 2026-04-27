package observability

import (
	"context"
	"io"
	"log/slog"
	"os"
	"runtime/debug"
	"strings"
)

// Logger is the interface use-cases depend on. We deliberately do NOT expose
// slog.Logger directly so adapters can be swapped (zap, zerolog, OTel logs).
type Logger interface {
	Debug(ctx context.Context, msg string, attrs ...Attr)
	Info(ctx context.Context, msg string, attrs ...Attr)
	Warn(ctx context.Context, msg string, attrs ...Attr)
	Error(ctx context.Context, msg string, err error, attrs ...Attr)
	With(attrs ...Attr) Logger
}

// ----------------------------------------------------------------------------
// SlogLogger — production-ready stdlib implementation.
//
// Features:
//   - JSON output suitable for shipping to ELK/Loki/CloudWatch
//   - Automatic trace_id / span_id injection from context
//   - PII redaction via configurable key list
//   - Error logging captures stack trace
// ----------------------------------------------------------------------------

type SlogLogger struct {
	inner    *slog.Logger
	piiKeys  map[string]struct{}
}

type SlogConfig struct {
	Writer  io.Writer // defaults to os.Stdout
	Level   slog.Level
	Service string
	// PIIKeys are attribute keys whose values must be redacted before
	// emission. Common examples: "email", "password", "card_number",
	// "ssn", "phone".
	PIIKeys []string
}

func NewSlogLogger(cfg SlogConfig) *SlogLogger {
	w := cfg.Writer
	if w == nil {
		w = os.Stdout
	}
	pii := make(map[string]struct{}, len(cfg.PIIKeys))
	for _, k := range cfg.PIIKeys {
		pii[strings.ToLower(k)] = struct{}{}
	}
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: cfg.Level})
	l := slog.New(h)
	if cfg.Service != "" {
		l = l.With(slog.String("service", cfg.Service))
	}
	return &SlogLogger{inner: l, piiKeys: pii}
}

func (l *SlogLogger) With(attrs ...Attr) Logger {
	sa := l.toSlogAttrs(attrs)
	args := make([]any, len(sa))
	for i := range sa {
		args[i] = sa[i]
	}
	return &SlogLogger{
		inner:   l.inner.With(args...),
		piiKeys: l.piiKeys,
	}
}

func (l *SlogLogger) Debug(ctx context.Context, msg string, attrs ...Attr) {
	l.log(ctx, slog.LevelDebug, msg, nil, attrs)
}
func (l *SlogLogger) Info(ctx context.Context, msg string, attrs ...Attr) {
	l.log(ctx, slog.LevelInfo, msg, nil, attrs)
}
func (l *SlogLogger) Warn(ctx context.Context, msg string, attrs ...Attr) {
	l.log(ctx, slog.LevelWarn, msg, nil, attrs)
}
func (l *SlogLogger) Error(ctx context.Context, msg string, err error, attrs ...Attr) {
	l.log(ctx, slog.LevelError, msg, err, attrs)
}

func (l *SlogLogger) log(ctx context.Context, lvl slog.Level, msg string, err error, attrs []Attr) {
	if !l.inner.Enabled(ctx, lvl) {
		return
	}
	out := l.toSlogAttrs(attrs)

	// Correlation: pull trace context from ctx so EVERY log line is joinable
	// to a trace without callers having to remember.
	if span := SpanFromContext(ctx); span.TraceID() != "" {
		out = append(out,
			slog.String("trace_id", span.TraceID()),
			slog.String("span_id", span.SpanID()),
		)
	}

	if err != nil {
		out = append(out,
			slog.String("error", err.Error()),
			slog.String("stack", string(debug.Stack())),
		)
	}
	l.inner.LogAttrs(ctx, lvl, msg, out...)
}

// Redactor lets a value type self-redact when logged. Concrete values whose
// type implements this take precedence over key-based PII redaction, so
// even if the caller forgets to register a key, the value still scrubs.
//
//   type Email string
//   func (Email) Redact() string { return "[REDACTED_EMAIL]" }
type Redactor interface{ Redact() string }

func (l *SlogLogger) toSlogAttrs(attrs []Attr) []slog.Attr {
	out := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		if r, ok := a.Value.(Redactor); ok {
			out = append(out, slog.String(a.Key, r.Redact()))
			continue
		}
		if _, redact := l.piiKeys[strings.ToLower(a.Key)]; redact {
			out = append(out, slog.String(a.Key, "[REDACTED]"))
			continue
		}
		out = append(out, slog.Any(a.Key, a.Value))
	}
	return out
}

// ----------------------------------------------------------------------------
// NopLogger — convenient default for tests.
// ----------------------------------------------------------------------------

type NopLogger struct{}

func (NopLogger) Debug(context.Context, string, ...Attr)        {}
func (NopLogger) Info(context.Context, string, ...Attr)         {}
func (NopLogger) Warn(context.Context, string, ...Attr)         {}
func (NopLogger) Error(context.Context, string, error, ...Attr) {}
func (n NopLogger) With(...Attr) Logger                         { return n }