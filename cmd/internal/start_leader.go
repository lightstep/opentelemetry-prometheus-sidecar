package internal

import (
	"context"
	"fmt"
	"math/rand"
	"strings"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/leader"
	"github.com/pkg/errors"
)

const (
	// Conventions. Do we need to configure these?
	nameKey = "prometheus"
	IDKey   = "prometheus_replica"
)

func StartLeaderElection(ctx context.Context, cfg *SidecarConfig) error {
	externalLabels := cfg.Monitor.GetGlobalConfig().ExternalLabels

	lockName := strings.Replace(externalLabels.Get(nameKey), "/", "_", -1)
	if lockName == "" {
		lockName = "default"
	}
	lockID := strings.Replace(externalLabels.Get(IDKey), "/", "_", -1)
	if lockID == "" {
		lockID = fmt.Sprintf("unlabeled-%016x", rand.Uint64())
	}

	logger := log.With(cfg.Logger, "component", "leader")

	var err error
	cfg.LeaderElector, err = leader.NewCandidate(
		config.LeaderLockNamespace,
		lockName,
		lockID,
		logger,
	)
	if err != nil {
		return errors.Wrap(err, "leader election candidate")
	}

	level.Info(cfg.Logger).Log(
		"msg", "starting leader election",
		"name", lockName,
		"ID", lockID,
	)

	if err := cfg.LeaderElector.Start(ctx); err != nil {
		return errors.Wrap(err, "leader election start")
	}
	return nil
}
