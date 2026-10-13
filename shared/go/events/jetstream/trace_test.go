package jetstream

import (
	"context"
	"fmt"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"strings"
	"testing"
	"time"
)

func TestPublishConsumeContinuesTraceWithoutPayloadAttributes(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	previous := otel.GetTracerProvider()
	previousProp := otel.GetTextMapPropagator()
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	defer func() {
		otel.SetTracerProvider(previous)
		otel.SetTextMapPropagator(previousProp)
		_ = provider.Shutdown(context.Background())
	}()
	_, conn := setup(t)
	ctx, parent := provider.Tracer("TEST-owner").Start(context.Background(), "TEST owner job")
	defer parent.End()
	observed := make(chan trace.SpanContext, 1)
	consumer, err := conn.Consume(ctx, ConsumerConfig{Stream: "REALESTATE_TEST", Durable: "TEST-traced-consumer"}, func(ctx context.Context, d events.Delivery) error {
		observed <- trace.SpanContextFromContext(ctx)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Stop()
	e := event(t)
	e.Payload = []byte(`{"private_document_url":"TEST-private-url","token":"TEST-token"}`)
	if _, err := conn.Publish(ctx, "realestate.test.happened.v1", e); err != nil {
		t.Fatal(err)
	}
	var child trace.SpanContext
	select {
	case child = <-observed:
	case <-time.After(5 * time.Second):
		t.Fatal("consumer not traced")
	}
	if !child.IsValid() || child.TraceID() != parent.SpanContext().TraceID() {
		t.Fatal("NATS trace not continued")
	}
	consumer.Stop()
	var producerID, consumerParent trace.SpanID
	for _, span := range exporter.GetSpans() {
		if span.Name == "NATS publish" {
			producerID = span.SpanContext.SpanID()
		}
		if span.Name == "NATS consume" {
			consumerParent = span.Parent.SpanID()
		}
		for _, secret := range []string{"TEST-private-url", "TEST-token"} {
			if strings.Contains(fmt.Sprint(span.Attributes), secret) {
				t.Fatal("payload leaked into NATS span")
			}
		}
	}
	if !producerID.IsValid() || consumerParent != producerID {
		t.Fatal("consumer parent is not the actual publish span")
	}
}
