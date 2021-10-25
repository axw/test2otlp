package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"runtime"
	"time"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.4.0"
	"go.opentelemetry.io/otel/trace"
)

type TestEvent struct {
	Time    time.Time
	Action  string
	Package string
	Test    string
	Elapsed float64 // seconds
	Output  string
}

func initTracerProvider(ctx context.Context) (*sdktrace.TracerProvider, error) {
	traceExporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, err
	}
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(sdktrace.NewBatchSpanProcessor(traceExporter)),
	)
	return tracerProvider, nil
}

func Main(ctx context.Context) error {
	provider, err := initTracerProvider(ctx)
	if err != nil {
		return err
	}
	defer provider.Shutdown(ctx)

	type spanDetails struct {
		trace.Span
		start time.Time
	}

	tracer := provider.Tracer("test2otlp")
	spans := make(map[string]*spanDetails)
	decoder := json.NewDecoder(os.Stdin)
	var outerSpan trace.Span
	var lastEndTime time.Time
	for {
		var event TestEvent
		if err := decoder.Decode(&event); err != nil {
			if err == io.EOF {
				break
			}
			return err
		}

		// TODO(axw) handle benchmarks.
		if event.Test == "" {
			continue
		}

		if event.Action == "run" {
			if outerSpan == nil {
				// Start outer span which groups all the others.
				ctx, outerSpan = tracer.Start(ctx, "test2json", trace.WithTimestamp(event.Time))
				defer func() {
					outerSpan.End(trace.WithTimestamp(lastEndTime))
				}()
				outerSpan.SetStatus(codes.Ok, "")
			}
			_, span := tracer.Start(ctx, event.Test,
				trace.WithTimestamp(event.Time),
				trace.WithAttributes(
					semconv.CodeNamespaceKey.String(event.Package),
					semconv.CodeFunctionKey.String(event.Test),
					semconv.HostArchKey.String(runtime.GOARCH),
					semconv.OSNameKey.String(runtime.GOOS),
				),
			)
			spans[event.Test] = &spanDetails{
				Span:  span,
				start: event.Time,
			}
			continue
		}

		span := spans[event.Test]
		switch event.Action {
		case "skip":
			span.End()
			delete(spans, event.Test)
		case "pause", "cont":
			span.AddEvent(event.Action, trace.WithTimestamp(event.Time))
		case "output":
			span.AddEvent(event.Output, trace.WithTimestamp(event.Time))
		case "pass", "fail":
			duration := time.Duration(event.Elapsed * float64(time.Second))
			endTime := span.start.Add(duration)
			if endTime.After(lastEndTime) {
				lastEndTime = endTime
			}
			if event.Action == "pass" {
				span.SetStatus(codes.Ok, "")
			} else {
				span.SetStatus(codes.Error, "")
				outerSpan.SetStatus(codes.Error, "")
			}
			span.End(trace.WithTimestamp(endTime))
		}
	}
	return nil
}

func main() {
	if err := Main(context.Background()); err != nil {
		log.Fatal(err)
	}
}
