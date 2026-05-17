package config

import (
	"fmt"

	"github.com/spf13/viper"
)

// Config is the top-level configuration structure.
type Config struct {
	Mode string `mapstructure:"mode"` // kubernetes | baremetal | cloud | edge

	EBPF eBPFConfig `mapstructure:"ebpf"`

	Ring RingConfig `mapstructure:"ring"`

	RL RLConfig `mapstructure:"rl"`

	RateLimit RateLimitConfig `mapstructure:"rate_limit"`

	Health HealthConfig `mapstructure:"health"`

	Telemetry TelemetryConfig `mapstructure:"telemetry"`

	XDS XDSConfig `mapstructure:"xds"`
}

type eBPFConfig struct {
	ObjectDir    string `mapstructure:"object_dir"`    // dir with .bpf.o files
	CgroupPath   string `mapstructure:"cgroup_path"`   // cgroup2 mountpoint
	Interface    string `mapstructure:"interface"`     // NIC for XDP (baremetal)
	PinPath      string `mapstructure:"pin_path"`      // bpffs map pin dir
}

type RingConfig struct {
	VnodesPerServer int     `mapstructure:"vnodes_per_server"` // default 150
	BoundedLoadBeta float64 `mapstructure:"bounded_load_beta"` // default 1.25
	AdjustEveryN    int     `mapstructure:"adjust_every_n"`    // default 100
	AdjustThreshold float64 `mapstructure:"adjust_threshold"`  // default 1.30
}

type RLConfig struct {
	Enabled         bool    `mapstructure:"enabled"`
	ONNXModelPath   string  `mapstructure:"onnx_model_path"`
	InferenceTimeoutMs int  `mapstructure:"inference_timeout_ms"` // default 5
	StepIntervalMs  int     `mapstructure:"step_interval_ms"`     // default 500
	ActionSmoothing float64 `mapstructure:"action_smoothing"`     // default 0.7
	CBFLambda       float64 `mapstructure:"cbf_lambda"`           // default 0.5
	CapacityPctCap  float64 `mapstructure:"capacity_pct_cap"`     // default 0.80
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
	ActiveIntervalS  int `mapstructure:"active_interval_s"`   // default 2
	FailThreshold    int `mapstructure:"fail_threshold"`      // default 3
	PassiveErrorPct  float64 `mapstructure:"passive_error_pct"` // default 0.10
}

type TelemetryConfig struct {
	OTLPEndpoint string `mapstructure:"otlp_endpoint"` // e.g. "localhost:4317"
	ExportIntervalS int `mapstructure:"export_interval_s"` // default 10
}

type XDSConfig struct {
	ListenAddr string `mapstructure:"listen_addr"` // gRPC xDS server addr
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

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	return &cfg, nil
}
