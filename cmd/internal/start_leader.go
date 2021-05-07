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

func cleanName(name string) string {
	name = strings.Replace(name, "/", "-", -1)
	name = strings.Replace(name, "_", "-", -1)
	if len(name) > 64 {
		name = name[len(name)-64:]
	}
	return name
}

func StartLeaderElection(ctx context.Context, cfg *SidecarConfig) error {
	externalLabels := cfg.Monitor.GetGlobalConfig().ExternalLabels

	lockNamespace := cfg.LeaderElection.K8S.Namespace
	if lockNamespace == "" {
		lockNamespace = config.LeaderLockDefaultNamespace
	}

	lockName := cleanName(externalLabels.Get(nameKey))
	if lockName == "" {
		lockName = config.LeaderLockDefaultName
	}
	lockID := cleanName(externalLabels.Get(IDKey))
	if lockID == "" {
		lockID = fmt.Sprintf("unlabeled-%016x", rand.Uint64())
	}

	logger := log.With(cfg.Logger, "component", "leader")

	var err error
	cfg.LeaderElector, err = leader.NewCandidate(
		lockNamespace,
		lockName,
		lockID,
		logger,
	)
	if err != nil {
		return errors.Wrap(err, "leader election candidate")
	}

	level.Info(cfg.Logger).Log(
		"msg", "starting leader election",
		"namespace", lockNamespace,
		"name", lockName,
		"ID", lockID,
	)

	if err := cfg.LeaderElector.Start(ctx); err != nil {
		return errors.Wrap(err, "leader election start")
	}
	return nil
}
