package gominimalapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/go-chi/chi"
	"github.com/go-chi/chi/middleware"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/jaeger"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
)

type ServerHttp struct {
	handlers         http.Handler
	addr             string
	serviceName      string
	serviceVersion   string
	entryPointTracer string
}

func routers(routes SetupHandlers) *chi.Mux {
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	for index := range routes.Handlers {
		method := strings.ToUpper(routes.Handlers[index].Method)
		if method == "POST" {
			r.Post(routes.Handlers[index].Path, routes.Handlers[index].HandlerFunc)
		}
		if method == "GET" {
			r.Get(routes.Handlers[index].Path, routes.Handlers[index].HandlerFunc)
		}
		if method == "PATCH" {
			r.Patch(routes.Handlers[index].Path, routes.Handlers[index].HandlerFunc)
		}
		if method == "PUT" {
			r.Put(routes.Handlers[index].Path, routes.Handlers[index].HandlerFunc)
		}
		if method == "DELETE" {
			r.Put(routes.Handlers[index].Path, routes.Handlers[index].HandlerFunc)
		}
	}
	return r
}

type Handler struct {
	HandlerFunc func(w http.ResponseWriter, r *http.Request)
	Path        string
	Method      string
}
type SetupHandlers struct {
	Handlers []Handler
}

func NewServerHttp(
	handlers SetupHandlers,
	addr string,
	serviceName string,
	serviceVersion string,
	entryPointTracer string,
) *ServerHttp {
	return &ServerHttp{
		handlers:         otelhttp.NewHandler(routers(handlers), "/"),
		addr:             addr,
		serviceName:      serviceName,
		serviceVersion:   serviceVersion,
		entryPointTracer: entryPointTracer,
	}
}

func (csh *ServerHttp) StartServerHttp() (err error) {
	// Handle SIGINT (CTRL+C) gracefully.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// Set up OpenTelemetry.
	// serviceName := csh.ServiceName
	// serviceVersion := csh.ServiceVersion
	otelShutdown, err := csh.setupOTelSDK(ctx)
	if err != nil {
		return
	}
	// Handle shutdown properly so nothing leaks.
	defer func() {
		err = errors.Join(err, otelShutdown(context.Background()))
	}()

	// Start HTTP server.
	srv := &http.Server{
		Addr:         csh.addr,
		BaseContext:  func(_ net.Listener) context.Context { return ctx },
		ReadTimeout:  time.Second,
		WriteTimeout: 10 * time.Second,
		Handler:      csh.handlers,
	}
	srvErr := make(chan error, 1)
	go func() {
		srvErr <- srv.ListenAndServe()
	}()

	// Wait for interruption.
	select {
	case err = <-srvErr:
		// Error when starting HTTP server.
		return
	case <-ctx.Done():
		// Wait for first CTRL+C.
		// Stop receiving signal notifications as soon as possible.
		stop()
	}

	// When Shutdown is called, ListenAndServe immediately returns ErrServerClosed.
	err = srv.Shutdown(context.Background())
	return
}

func (csh *ServerHttp) setupOTelSDK(ctx context.Context) (shutdown func(context.Context) error, err error) {
	var shutdownFuncs []func(context.Context) error

	// shutdown calls cleanup functions registered via shutdownFuncs.
	// The errors from the calls are joined.
	// Each registered cleanup will be invoked once.
	shutdown = func(ctx context.Context) error {
		var err error
		for _, fn := range shutdownFuncs {
			err = errors.Join(err, fn(ctx))
		}
		shutdownFuncs = nil
		return err
	}

	// handleErr calls shutdown for cleanup and makes sure that all errors are returned.
	handleErr := func(inErr error) {
		err = errors.Join(inErr, shutdown(ctx))
	}

	// Set up resource.
	res, err := csh.newResource(csh.serviceName, csh.serviceVersion)
	if err != nil {
		handleErr(err)
		return
	}

	// Set up propagator.
	prop := csh.newPropagator()
	otel.SetTextMapPropagator(prop)

	// Set up trace provider.
	tracerProvider, err := csh.newTraceProvider(res)
	if err != nil {
		handleErr(err)
		return
	}
	shutdownFuncs = append(shutdownFuncs, tracerProvider.Shutdown)
	otel.SetTracerProvider(tracerProvider)

	// Set up meter provider.
	meterProvider, err := newMeterProvider(res)
	if err != nil {
		handleErr(err)
		return
	}
	shutdownFuncs = append(shutdownFuncs, meterProvider.Shutdown)
	otel.SetMeterProvider(meterProvider)

	return
}

func (csh *ServerHttp) newResource(serviceName, serviceVersion string) (*resource.Resource, error) {
	return resource.Merge(resource.Default(),
		resource.NewWithAttributes(semconv.SchemaURL,
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(serviceVersion),
		))
}

func (csh *ServerHttp) newPropagator() propagation.TextMapPropagator {
	return propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)
}

func (csh *ServerHttp) newTraceProvider(res *resource.Resource) (*trace.TracerProvider, error) {
	exp, err := jaeger.New(jaeger.WithCollectorEndpoint(jaeger.WithEndpoint(
		csh.entryPointTracer,
	)))
	if err != nil {
		return nil, err
	}
	traceProvider := trace.NewTracerProvider(
		trace.WithBatcher(exp,
			// Default is 5s. Set to 1s for demonstrative purposes.
			trace.WithBatchTimeout(time.Second)),
		trace.WithResource(res),
	)
	return traceProvider, nil
}

func newMeterProvider(res *resource.Resource) (*metric.MeterProvider, error) {
	// metricExporter, err := stdoutmetric.New()
	// if err != nil {
	// 	return nil, err
	// }

	meterProvider := metric.NewMeterProvider(
		metric.WithResource(res),
		// metric.WithReader(metric.NewPeriodicReader(metricExporter,
		// 	// Default is 1m. Set to 3s for demonstrative purposes.
		// 	metric.WithInterval(3*time.Second))),
	)
	return meterProvider, nil
}
