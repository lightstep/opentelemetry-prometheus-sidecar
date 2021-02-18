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

func WaitForReady(inCtx context.Context, logger log.Logger, promURL *url.URL) error {
	u := *promURL
	u.Path = path.Join(promURL.Path, "/-/ready")

	// warnSkipped prevents logging on the first failure, since we
	// will try again and this lets us avoid the first sleep
	warnSkipped := false

	tick := time.NewTicker(config.DefaultHealthCheckTimeout)
	defer tick.Stop()

	for {
		ctx, cancel := context.WithTimeout(inCtx, config.DefaultHealthCheckTimeout)
		req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
		if err != nil {
			cancel()
			return errors.Wrap(err, "build request")
		}

		success := func() bool {
			defer cancel()
			resp, err := http.DefaultClient.Do(req)

			if resp != nil && resp.Body != nil {
				defer resp.Body.Close()
			}

			if err == nil && resp.StatusCode/100 == 2 {
				return true
			}

			if !warnSkipped {
				warnSkipped = true
				return false
			}
			if err != nil {
				level.Warn(logger).Log("msg", "Prometheus readiness check", "err", err)
			} else {
				level.Warn(logger).Log("msg", "Prometheus is not ready", "status", resp.Status)
			}
			return false
		}()
		if success {
			return nil
		}

		select {
		case <-inCtx.Done():
			return inCtx.Err()
		case <-tick.C:
			continue
		}
	}
}
