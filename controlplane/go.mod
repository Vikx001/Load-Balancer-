module github.com/omega-lb/omega-lb

go 1.22

require (
	github.com/cilium/ebpf v0.14.0
	github.com/envoyproxy/go-control-plane v0.12.1
	google.golang.org/grpc v1.64.0
	google.golang.org/protobuf v1.34.2
	go.opentelemetry.io/otel v1.27.0
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc v1.27.0
	go.opentelemetry.io/otel/sdk/metric v1.27.0
	github.com/orcaman/concurrent-map/v2 v2.0.1
	go.uber.org/zap v1.27.0
	github.com/spf13/viper v1.19.0
	onnxruntime-go v0.0.0-00010101000000-000000000000
)

// onnxruntime-go is a local replace until upstream package is used
replace onnxruntime-go => ./vendor/onnxruntime-go
