package prometheus

import (
	"context"
	"net/http"
	"net/url"
	"path"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
)

func WaitForReady(ctx context.Context, logger log.Logger, promURL *url.URL) error {
	u := *promURL
	u.Path = path.Join(promURL.Path, "/-/ready")

	// warnCount prevents logging on the first failure, since we
	// will try again and this lets us avoid the first sleep
	warnCount := 0

	tick := time.NewTicker(config.DefaultHealthCheckTimeout)
	defer tick.Stop()

	for {
		resp, err := http.Get(u.String())
		if err != nil {
			if warnCount != 0 {
				level.Warn(logger).Log("msg", "Prometheus readiness check", "err", err)
			}
			warnCount++
			continue
		}
		if resp.StatusCode/100 == 2 {
			return nil
		}

		if warnCount != 0 {
			level.Warn(logger).Log("msg", "Prometheus is not ready", "status", resp.Status)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			continue
		}
	}
}
