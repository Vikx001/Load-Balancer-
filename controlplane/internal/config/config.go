package config

import (
	"fmt"

	"github.com/spf13/viper"
)

// Config is the top-level configuration structure.
type Config struct {
	Mode string `mapstructure:"mode"` // kubernetes | baremetal | cloud | edge

	// Stage controls which subsystems are active.
	// This implements the phased delivery model — each stage is independently
	// deployable and benchmarkable before advancing.
	//
	//   Stage 1 (month 1–2): eBPF data plane + static round-robin only.
	//                         No H&A, no RL, no ML.  Already outperforms NGINX.
	//   Stage 2 (month 3):   H&A consistent hash ring replaces round-robin.
	//                         Measure p99 improvement before advancing.
	//   Stage 3 (month 4–5): Full Go daemon: health checker, metrics, circuit breaker.
	//   Stage 4 (month 6–8): RL agent in shadow mode (observes but does not route).
	//   Stage 5 (month 9+):  RL agent takes live traffic control.
	//
	// All stage-N configs include stage-N-1 subsystems.  Never skip stages.
	Stage int `mapstructure:"stage"` // default 1

	EBPF eBPFConfig `mapstructure:"ebpf"`

	Ring RingConfig `mapstructure:"ring"`

	RL RLConfig `mapstructure:"rl"`

	RateLimit RateLimitConfig `mapstructure:"rate_limit"`

	Health HealthConfig `mapstructure:"health"`

	Telemetry TelemetryConfig `mapstructure:"telemetry"`

	XDS XDSConfig `mapstructure:"xds"`

	Consensus ConsensusConfig `mapstructure:"consensus"`

	Metrics MetricsConfig `mapstructure:"metrics"`

	Admin AdminConfig `mapstructure:"admin"`

	TLS TLSConfig `mapstructure:"tls"`
}

type eBPFConfig struct {
	ObjectDir    string `mapstructure:"object_dir"`    // dir with .bpf.o files
	CgroupPath   string `mapstructure:"cgroup_path"`   // cgroup2 mountpoint
	Interface    string `mapstructure:"interface"`     // NIC for XDP / IRQ affinity
	PinPath      string `mapstructure:"pin_path"`      // bpffs map pin dir
	// NUMANode: the NUMA node to bind the daemon to.  -1 means auto-detect from NIC.
	// Set explicitly on multi-socket servers to avoid remote-memory map accesses.
	// Use numactl --cpunodebind=N --membind=N in the systemd ExecStart.
	NUMANode     int    `mapstructure:"numa_node"`     // default -1 (auto)
}

type RingConfig struct {
	VnodesPerServer int     `mapstructure:"vnodes_per_server"` // default 150
	BoundedLoadBeta float64 `mapstructure:"bounded_load_beta"` // default 1.25
	AdjustEveryN    int     `mapstructure:"adjust_every_n"`    // default 100
	AdjustThreshold float64 `mapstructure:"adjust_threshold"`  // default 1.30
	// WAL: write-ahead log path for ring mutations (crash-safe)
	WALPath         string  `mapstructure:"wal_path"`          // default /var/lib/omega-lb/ring.wal
	// Slow-start: add vnodes gradually after backend recovery
	SlowStartBatchSize       int `mapstructure:"slow_start_batch_size"`         // default 15 vnodes/tick
	SlowStartIntervalS       int `mapstructure:"slow_start_interval_s"`         // default 30
	SlowStartMaxErrorRatePct int `mapstructure:"slow_start_max_error_rate_pct"` // default 1 (1%)
}

type RLConfig struct {
	Enabled         bool    `mapstructure:"enabled"`
	ONNXModelPath   string  `mapstructure:"onnx_model_path"`
	InferenceTimeoutMs int  `mapstructure:"inference_timeout_ms"` // default 5
	StepIntervalMs  int     `mapstructure:"step_interval_ms"`     // default 500
	ActionSmoothing float64 `mapstructure:"action_smoothing"`     // default 0.7
	CBFLambda       float64 `mapstructure:"cbf_lambda"`           // default 0.5
	CapacityPctCap  float64 `mapstructure:"capacity_pct_cap"`     // default 0.80
	// Model versioning + hot-reload
	ModelVersion             string `mapstructure:"model_version"`               // e.g. "v1.4.2"
	ModelStorePath           string `mapstructure:"model_store_path"`            // dir with versioned models
	HotReloadWatchIntervalS  int    `mapstructure:"hot_reload_watch_interval_s"` // default 60
}

type RateLimitConfig struct {
	Enabled        bool `mapstructure:"enabled"`
	UpdateIntervalMs int `mapstructure:"update_interval_ms"` // default 100
	// Per-service limits; key is service_id
	Services map[string]ServiceRateLimit `mapstructure:"services"`
}

type ServiceRateLimit struct {
	ServiceID  uint32  `mapstructure:"service_id"`
	InitialRPS float64 `mapstructure:"initial_rps"`
	MinRPS     float64 `mapstructure:"min_rps"`
	MaxRPS     float64 `mapstructure:"max_rps"`
}

type HealthConfig struct {
	ActiveIntervalS  int     `mapstructure:"active_interval_s"`   // default 2
	FailThreshold    int     `mapstructure:"fail_threshold"`      // default 3
	PassiveErrorPct  float64 `mapstructure:"passive_error_pct"`   // default 0.10
	// MinSuccessesBeforeRestore: consecutive successes needed before slow-start.
	// Default 60 (≈ 2 min at 2s poll interval) ensures backend cache is warm.
	MinSuccessesBeforeRestore int `mapstructure:"min_successes_before_restore"` // default 60
}

type ConsensusConfig struct {
	Enabled        bool     `mapstructure:"enabled"`
	EtcdEndpoints  []string `mapstructure:"etcd_endpoints"`
	LeaderKey      string   `mapstructure:"leader_key"`       // default /omega-lb/leader
	RingStateKey   string   `mapstructure:"ring_state_key"`   // default /omega-lb/ring-state
	LockTTLSeconds int64    `mapstructure:"lock_ttl_seconds"` // default 10
	NodeID         string   `mapstructure:"node_id"`          // hostname or pod name
}

type TelemetryConfig struct {
	OTLPEndpoint string `mapstructure:"otlp_endpoint"` // e.g. "localhost:4317"
	ExportIntervalS int `mapstructure:"export_interval_s"` // default 10
}

// MetricsConfig controls Prometheus metric emission.
// The cardinality guard prevents label explosions that OOM Prometheus.
type MetricsConfig struct {
	// MaxLabelValuesPerDimension caps distinct values per high-cardinality label.
	// When a new value would exceed this limit, "_overflow" is used instead.
	// Default 50 is safe for up to 50 backends + 50 services + 50 paths.
	MaxLabelValuesPerDimension int `mapstructure:"max_label_values"` // default 50
	// PathAggregation: if true, /api/v1/users/123 → /api/v1/users/{id}.
	// This collapses path cardinality from O(users) to O(routes).
	PathAggregation bool `mapstructure:"path_aggregation"` // default true
	// PrometheusListenAddr: address for the /metrics scrape endpoint.
	PrometheusListenAddr string `mapstructure:"prometheus_listen_addr"` // default :9090
}

// AdminConfig controls the HTTP admin/debug server.
type AdminConfig struct {
	// ListenAddr is the HTTP address for the admin API.
	// Endpoints: /admin/explain, /admin/mode, /admin/healthz
	ListenAddr string `mapstructure:"listen_addr"` // default :9000
	// FlightRecorderCapacity is the number of routing decisions retained in
	// memory for the /admin/explain API.  At 100k RPS, 10000 covers ~100ms.
	FlightRecorderCapacity int `mapstructure:"flight_recorder_capacity"` // default 10000
}

type XDSConfig struct {
	ListenAddr string `mapstructure:"listen_addr"` // gRPC xDS server addr
}

// TLSConfig controls how Omega-LB handles TLS traffic.
//
// Three modes:
//
//   "passthrough" (default): the LB forwards encrypted bytes to backends
//   unchanged.  Route rules based on URL path are bypassed — the LB only
//   applies cluster-level routing.  Use this when backends hold the
//   certificate and end-to-end encryption is required.
//
//   "sni": the LB reads the SNI hostname from the TLS ClientHello (which is
//   always plaintext) and uses it as the routing key instead of URL path.
//   No certificate required on the LB.  Only hostname-based L4 routing is
//   possible.  Best for multi-tenant pass-through with per-hostname clusters.
//
//   "terminate" / "ktls": the LB terminates TLS using kernel TLS (kTLS,
//   Linux ≥ 4.13).  After the handshake the kernel decrypts the stream
//   transparently and eBPF programs see plaintext HTTP bytes.  Full L7
//   path-based routing is available.  Requires cert_file + key_file.
type TLSConfig struct {
	// Mode: "passthrough" | "sni" | "terminate"
	Mode     string `mapstructure:"mode"`
	// CertFile and KeyFile: PEM-encoded certificate and private key for
	// "terminate" mode.  Unused in "passthrough" and "sni" modes.
	CertFile string `mapstructure:"cert_file"`
	KeyFile  string `mapstructure:"key_file"`
	// SNIRoutingOnly: when true (sni mode), route_manager treats request_ctx.path
	// as the SNI hostname and skips URL-path rule matching.
	SNIRoutingOnly bool `mapstructure:"sni_routing_only"`
}

// Load reads config from the given path using Viper.
func Load(path string) (*Config, error) {
	v := viper.New()
	v.SetConfigFile(path)
	v.SetConfigType("yaml")

	// Defaults
	v.SetDefault("mode", "baremetal")
	v.SetDefault("ring.vnodes_per_server", 150)
	v.SetDefault("ring.bounded_load_beta", 1.25)
	v.SetDefault("ring.adjust_every_n", 100)
	v.SetDefault("ring.adjust_threshold", 1.30)
	v.SetDefault("rl.enabled", true)
	v.SetDefault("rl.inference_timeout_ms", 5)
	v.SetDefault("rl.step_interval_ms", 500)
	v.SetDefault("rl.action_smoothing", 0.7)
	v.SetDefault("rl.cbf_lambda", 0.5)
	v.SetDefault("rl.capacity_pct_cap", 0.80)
	v.SetDefault("rate_limit.enabled", true)
	v.SetDefault("rate_limit.update_interval_ms", 100)
	v.SetDefault("health.active_interval_s", 2)
	v.SetDefault("health.fail_threshold", 3)
	v.SetDefault("health.passive_error_pct", 0.10)
	v.SetDefault("telemetry.export_interval_s", 10)
	v.SetDefault("xds.listen_addr", ":18000")
	v.SetDefault("metrics.max_label_values", 50)
	v.SetDefault("metrics.path_aggregation", true)
	v.SetDefault("metrics.prometheus_listen_addr", ":9090")
	v.SetDefault("admin.listen_addr", ":9000")
	v.SetDefault("admin.flight_recorder_capacity", 10000)
	v.SetDefault("rl.hot_reload_watch_interval_s", 60)
	v.SetDefault("ebpf.numa_node", -1)
	v.SetDefault("tls.mode", "passthrough")
	v.SetDefault("stage", 1)

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	return &cfg, nil
}
