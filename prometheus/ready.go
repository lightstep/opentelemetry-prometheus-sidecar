package prometheus

import (
	"context"
	"net/http"
	"path"
	"time"

	"github.com/go-kit/kit/log/level"
	goversion "github.com/hashicorp/go-version"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/pkg/errors"
)

const (
	scrapeIntervalName = config.PrometheusTargetIntervalLengthName
)

func scrapeMetrics(inCtx context.Context, cfg config.PromReady) (Result, error) {
	u := *cfg.PromURL
	u.Path = path.Join(u.Path, "/metrics")

	ctx, cancel := context.WithTimeout(inCtx, config.DefaultHealthCheckTimeout)
	defer cancel()

	cfg.Logger.Log("msg", "get prom startup interval")
	mon := NewMonitor(&u)
	return mon.Get(ctx)
}

func checkPrometheusVersion(res Result) error {
	minVersion, _ := goversion.NewVersion(config.PrometheusMinVersion)
	var prometheusVersion *goversion.Version
	err := errors.New("version not found")
	for _, lp := range res.Gauge(config.PrometheusBuildInfoName).AllLabels() {
		if len(lp.Get("version")) > 0 {
			prometheusVersion, err = goversion.NewVersion(lp.Get("version"))
			break
		}
	}

	if err != nil {
		return errors.Wrap(err, "prometheus version unavailable")
	}

	if prometheusVersion.LessThan(minVersion) {
		return errors.Errorf("prometheus version %s+ required, detected: %s", minVersion, prometheusVersion)
	}
	return nil
}

func completedFirstScrapes(res Result, cfg config.PromReady) error {

	summary := res.Summary(scrapeIntervalName)
	foundLabelSets := summary.AllLabels()
	if len(foundLabelSets) == 0 {
		return errors.New("waiting for the first scrape(s) to complete")
	}

	// Prometheus doesn't report zero counts. We expect absent
	// timeseries, not zero counts, but we test for Count() != 0 on
	// the retrieved metrics for added safety below.

	if len(cfg.ScrapeIntervals) == 0 {
		// If no intervals are configured, wait for the first one.
		//
		// TODO: We can't be sure there are only one interval.
		// After time passes, we can check again--if any new
		// intervals are discovered, print a warning about
		// configuring intervals via --prometheus.scrape-interval
		// to ensure safe startup.
		for _, ls := range foundLabelSets {
			if summary.For(ls).Count() != 0 {
				return nil
			}
		}

		return nil
	}

	// Find all the known intervals.
	foundWhich := map[string]bool{}
	for _, ls := range foundLabelSets {
		for _, l := range ls {
			if l.Name == "interval" && summary.For(ls).Count() != 0 {
				foundWhich[l.Value] = true
				break
			}
		}
	}

	for _, si := range cfg.ScrapeIntervals {
		ts := si.String()
		if !foundWhich[ts] {
			return errors.Errorf("waiting for scrape interval %s", ts)
		}
	}

	return nil
}

func WaitForReady(inCtx context.Context, inCtxCancel context.CancelFunc, cfg config.PromReady) error {
	u := *cfg.PromURL
	u.Path = path.Join(u.Path, "/-/ready")

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

			respOK := err == nil && resp.StatusCode/100 == 2

			if respOK {
				result, err := scrapeMetrics(inCtx, cfg)
				if err != nil {
					return false
				}
				err = checkPrometheusVersion(result)
				if err != nil {
					// invalid prometheus version is unrecoverable
					// cancel the caller's context and exit
					level.Warn(cfg.Logger).Log("msg", "Invalid Prometheus version", "err", err)
					inCtxCancel()
					return false
				}
				// Great! We also need it to have completed
				// a full round of scrapes.
				err = completedFirstScrapes(result, cfg)
				if err == nil {
					return true
				}
			}

			if !warnSkipped {
				warnSkipped = true
				return false
			}
			if respOK {
				level.Warn(cfg.Logger).Log("msg", "Prometheus /metrics scrape", "err", err)
			} else if err != nil {
				level.Warn(cfg.Logger).Log("msg", "Prometheus readiness check", "err", err)
			} else {
				level.Warn(cfg.Logger).Log("msg", "Prometheus is not ready", "status", resp.Status)
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
