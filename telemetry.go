package main

import (
	"context"
	"net/http"
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/app"
	"github.com/go-faster/sdk/autometric"
	"github.com/go-faster/sdk/zctx"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// instrumentName is the OpenTelemetry instrumentation scope of this server.
const instrumentName = "github.com/gotd/tgmcp"

// mcpMetrics are the metrics produced for incoming MCP requests.
type mcpMetrics struct {
	Requests metric.Int64Counter     `name:"mcp.request.count" description:"Total number of MCP requests"`
	Failures metric.Int64Counter     `name:"mcp.request.failures" description:"Number of MCP requests that returned an error"`
	Duration metric.Float64Histogram `name:"mcp.request.duration" description:"Duration of MCP requests" unit:"s" boundaries:"0.005,0.01,0.025,0.05,0.1,0.25,0.5,1,2.5,5,10,30"`
}

var newMCPMetrics = autometric.Define[mcpMetrics](autometric.InitOptions{})

// tgMetrics are the metrics produced for the Telegram client that
// [github.com/gotd/contrib/oteltg] does not already cover.
type tgMetrics struct {
	FloodWaits metric.Int64Counter `name:"tg.flood_wait.count" description:"Number of FLOOD_WAIT errors waited out"`
}

var newTGMetrics = autometric.Define[tgMetrics](autometric.InitOptions{})

// mcpInstrument traces and measures incoming MCP requests.
type mcpInstrument struct {
	tracer  trace.Tracer
	metrics mcpMetrics
	// logPayloads includes request and response bodies in the debug log, which
	// means the contents of chats. Off unless the operator asked for it.
	logPayloads bool
}

func newMCPInstrument(t *app.Telemetry, logPayloads bool) (*mcpInstrument, error) {
	m, err := newMCPMetrics(t.MeterProvider().Meter(instrumentName))
	if err != nil {
		return nil, errors.Wrap(err, "init MCP metrics")
	}

	return &mcpInstrument{
		tracer:      t.TracerProvider().Tracer(instrumentName),
		metrics:     m,
		logPayloads: logPayloads,
	}, nil
}

// Middleware returns the receiving middleware to install via
// [mcp.Server.AddReceivingMiddleware]. It opens a span per request, records
// call counts and duration, and puts the request fields on the context logger.
func (i *mcpInstrument) Middleware() mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			spanName := method
			attrs := []attribute.KeyValue{attribute.String("mcp.method", method)}
			fields := []zap.Field{zap.String("mcp.method", method)}
			if p, ok := req.GetParams().(*mcp.CallToolParamsRaw); ok {
				spanName = method + " " + p.Name
				attrs = append(attrs, attribute.String("mcp.tool", p.Name))
				fields = append(fields, zap.String("mcp.tool", p.Name))
			}

			ctx, span := i.tracer.Start(ctx, spanName, trace.WithAttributes(attrs...))
			defer span.End()

			ctx = zctx.With(ctx, fields...)
			i.metrics.Requests.Add(ctx, 1, metric.WithAttributes(attrs...))
			lg := zctx.From(ctx)
			request := []zap.Field{}
			if i.logPayloads {
				request = append(request, zap.Any("params", req.GetParams()))
			}
			lg.Debug("MCP request", request...)

			start := time.Now()
			res, err := next(ctx, method, req)
			took := time.Since(start)

			i.metrics.Duration.Record(ctx, took.Seconds(), metric.WithAttributes(attrs...))
			if err != nil {
				span.SetStatus(codes.Error, "MCP error")
				span.RecordError(err)
				i.metrics.Failures.Add(ctx, 1, metric.WithAttributes(attrs...))
			} else {
				span.SetStatus(codes.Ok, "")
			}
			response := []zap.Field{zap.Duration("took", took), zap.Error(err)}
			if i.logPayloads {
				// The payload is the chat: message text, peer details and
				// whatever else a tool returned.
				response = append(response, zap.Any("result", res))
			}
			lg.Debug("MCP response", response...)

			return res, err
		}
	}
}

// statusRecorder captures the HTTP status code written by a handler.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// injectLogger puts lg on the request context, so that handlers reach it via
// [zctx.From]. The HTTP server hands every request a context of its own, which
// would otherwise not carry the base logger set up by [app.Run].
func injectLogger(lg *zap.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := zctx.WithOpenTelemetryZap(r.Context())
		ctx = zctx.Base(ctx, lg)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// instrumentHTTP wraps next with OpenTelemetry HTTP instrumentation, so that a
// trace context sent by the MCP client is continued, and logs every request at
// debug level with its method, path, MCP session id, status, and duration.
func instrumentHTTP(lg *zap.Logger, t *app.Telemetry, next http.Handler) http.Handler {
	logged := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		zctx.From(r.Context()).Debug("HTTP request",
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			zap.String("session", r.Header.Get("Mcp-Session-Id")),
			zap.Int("status", rec.status),
			zap.Duration("took", time.Since(start)),
			zap.String("remote", r.RemoteAddr),
		)
	})

	// Order matters: otelhttp starts the server span, injectLogger then binds
	// the logger to a context that already carries it.
	return otelhttp.NewHandler(injectLogger(lg, logged), "mcp",
		otelhttp.WithTracerProvider(t.TracerProvider()),
		otelhttp.WithMeterProvider(t.MeterProvider()),
		otelhttp.WithPropagators(t.TextMapPropagator()),
	)
}
