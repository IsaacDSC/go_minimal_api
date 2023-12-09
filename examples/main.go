package main

import (
	gominimalapi "go_minimal_api"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var (
	tracer_healthChecker  = otel.Tracer("healthchecker")
	meter_healthChecker   = otel.Meter("healthchecker")
	rollCnt_healthChecker metric.Int64Counter
)

func init() {
	var err error
	rollCnt_healthChecker, err = meter_healthChecker.Int64Counter("dice.health",
		metric.WithDescription("The status of health service"),
		metric.WithUnit("{health}"))
	if err != nil {
		panic(err)
	}
}

func handler(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer_healthChecker.Start(r.Context(), "health")
	defer span.End()

	// Add the custom attribute to the span and counter.
	rollValueAttr := attribute.String("health.status", string([]byte("OK!!!")))
	span.SetAttributes(rollValueAttr)
	rollCnt_healthChecker.Add(ctx, 1, metric.WithAttributes(rollValueAttr))

	w.Write([]byte("OK!!!"))
}

func main() {
	handlers := []gominimalapi.Handler{
		{HandlerFunc: handler, Path: "/", Method: "GET"},
	}
	if err := gominimalapi.NewServerHttp(
		gominimalapi.SetupHandlers{
			Handlers: handlers,
		},
		":3000",
		"gominimaltest",
		"1.0.0",
		"http://localhost:14268/api/traces",
	).StartServerHttp(); err != nil {
		panic(err)
	}

}
