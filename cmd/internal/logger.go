package internal

import (
	"github.com/go-kit/kit/log"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/telemetry"
	"github.com/prometheus/common/promlog"
)

func NewLogger(cfg config.MainConfig, isSupervisor bool) log.Logger {
	vlevel := cfg.LogConfig.Verbose
	if cfg.LogConfig.Level == "debug" {
		vlevel++
	}

	if vlevel > 0 {
		telemetry.SetVerboseLevel(vlevel)
	}

	var plc promlog.Config
	plc.Level = &promlog.AllowedLevel{}
	plc.Format = &promlog.AllowedFormat{}
	plc.Level.Set(cfg.LogConfig.Level)
	plc.Format.Set(cfg.LogConfig.Format)

	logger := promlog.New(&plc)
	if isSupervisor {
		logger = log.With(logger, "supervisor", "true")
	}
	return logger
}
