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
	"github.com/pkg/errors"
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
		req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
		if err != nil {
			return errors.Wrap(err, "build request")
		}

		resp, err := http.DefaultClient.Do(req)
		if err == nil && resp.StatusCode/100 == 2 {
			return nil
		}

		if warnCount != 0 {
			if err != nil {
				level.Warn(logger).Log("msg", "Prometheus readiness check", "err", err)
			} else {
				level.Warn(logger).Log("msg", "Prometheus is not ready", "status", resp.Status)
			}
			warnCount++
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			continue
		}
	}
}
