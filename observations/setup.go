package observations

import (
	"expvar"
	"net/http"
	"net/http/pprof"
	"os"

	prom "github.com/gxed/client_golang/prometheus"
	"github.com/gxed/opencensus-go/exporter/jaeger"
	"github.com/gxed/opencensus-go/exporter/prometheus"
	"github.com/gxed/opencensus-go/plugin/ochttp"
	"github.com/gxed/opencensus-go/stats/view"
	"github.com/gxed/opencensus-go/trace"
	"github.com/gxed/opencensus-go/zpages"
	ocgorpc "github.com/lanzafame/go-libp2p-ocgorpc"
)

// Setup configures and starts stats and tracing tooling,
// if enabled.
func Setup(cfg *Config) {
	if cfg.EnableStats {
		logger.Error("stats collection enabled...")
		setupMetrics(cfg)
	}

	if cfg.EnableTracing {
		logger.Error("tracing enabled...")
		SetupTracing(cfg)
	}
}

func setupMetrics(cfg *Config) {
	// setup Prometheus
	registry := prom.NewRegistry()
	goCollector := prom.NewGoCollector()
	procCollector := prom.NewProcessCollector(os.Getpid(), "")
	registry.MustRegister(goCollector, procCollector)
	pe, err := prometheus.NewExporter(prometheus.Options{
		Namespace: "cluster",
		Registry:  registry,
	})
	if err != nil {
		logger.Fatalf("Failed to create Prometheus exporter: %v", err)
	}

	// register prometheus with opencensus
	view.RegisterExporter(pe)
	view.SetReportingPeriod(cfg.StatsReportingInterval)

	// register the metrics views of interest
	if err := view.Register(DefaultViews...); err != nil {
		logger.Fatalf("failed to register views: %v", err)
	}
	if err := view.Register(
		ochttp.ClientCompletedCount,
		ochttp.ClientRoundtripLatencyDistribution,
		ochttp.ClientReceivedBytesDistribution,
		ochttp.ClientSentBytesDistribution,
	); err != nil {
		logger.Fatalf("failed to register views: %v", err)
	}
	if err := view.Register(
		ochttp.ServerRequestCountView,
		ochttp.ServerRequestBytesView,
		ochttp.ServerResponseBytesView,
		ochttp.ServerLatencyView,
		ochttp.ServerRequestCountByMethod,
		ochttp.ServerResponseCountByStatusCode,
	); err != nil {
		logger.Fatalf("failed to register views: %v", err)
	}
	if err := view.Register(ocgorpc.DefaultServerViews...); err != nil {
		logger.Fatalf("failed to register views: %v", err)
	}

	go func() {
		mux := http.NewServeMux()
		zpages.Handle(mux, "/debug")
		mux.Handle("/metrics", pe)
		mux.Handle("/debug/vars", expvar.Handler())
		mux.HandleFunc("/debug/pprof", pprof.Index)
		mux.HandleFunc("/debug/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/profile", pprof.Profile)
		mux.HandleFunc("/debug/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/trace", pprof.Trace)
		mux.Handle("/debug/block", pprof.Handler("block"))
		mux.Handle("/debug/goroutine", pprof.Handler("goroutine"))
		mux.Handle("/debug/heap", pprof.Handler("heap"))
		mux.Handle("/debug/mutex", pprof.Handler("mutex"))
		mux.Handle("/debug/threadcreate", pprof.Handler("threadcreate"))
		if err := http.ListenAndServe(cfg.PrometheusEndpoint, mux); err != nil {
			logger.Fatalf("Failed to run Prometheus /metrics endpoint: %v", err)
		}
	}()
}

// SetupTracing configures a OpenCensus Tracing exporter for Jaeger.
func SetupTracing(cfg *Config) *jaeger.Exporter {
	// setup Jaeger
	je, err := jaeger.NewExporter(jaeger.Options{
		AgentEndpoint: cfg.JaegerAgentEndpoint,
		// CollectorEndpoint: cfg.JaegerCollectorEndpoint,
		// Endpoint:    cfg.JaegerCollectorEndpoint,
		ServiceName: cfg.TracingServiceName,
	})
	if err != nil {
		logger.Fatalf("Failed to create the Jaeger exporter: %v", err)
	}

	// register jaeger with opencensus
	trace.RegisterExporter(je)
	// configure tracing
	trace.ApplyConfig(trace.Config{DefaultSampler: trace.ProbabilitySampler(cfg.TracingSamplingProb)})
	return je
}
