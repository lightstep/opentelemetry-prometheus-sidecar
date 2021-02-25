package prometheus

import (
	"context"
	"net/http"
	"path"
	"time"

	"github.com/go-kit/kit/log/level"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/pkg/errors"
)

const (
	scrapeIntervalName = config.PrometheusTargetIntervalLengthName
)

func completedFirstScrapes(inCtx context.Context, cfg config.PromReady) error {
	u := *cfg.PromURL
	u.Path = path.Join(u.Path, "/metrics")

	ctx, cancel := context.WithTimeout(inCtx, config.DefaultHealthCheckTimeout)
	defer cancel()

	mon := NewMonitor(&u)
	res, err := mon.Get(ctx)
	if err != nil {
		return err
	}
	summary := res.Summary(scrapeIntervalName)
	foundLabelSets := summary.AllLabels()
	if len(foundLabelSets) == 0 {
		return errors.New("waiting for the first scrape(s) to complete")
	}

	foundAny := false
	for _, ls := range foundLabelSets {
		if summary.For(ls).Count() != 0 {
			foundAny = true
			break
		}
	}

	if len(cfg.ScrapeIntervals) == 0 && foundAny {
		// TODO: After_some time passes, we can check again and if any
		// new intervals are discovered, print a warning about configuring
		// the --prometheus.scrape-interval setting.
		return nil
	}

	for _, si := range cfg.ScrapeIntervals {
		ts := si.String()
		for _, ls := range foundLabelSets {
			for _, l := range ls {
				if l.Name == "interval" && l.Value == ts {
					return nil
				}
			}
		}
		return errors.Errorf("waiting for scrape interval %s", ts)
	}

	return nil
}

func WaitForReady(inCtx context.Context, cfg config.PromReady) error {
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
				// Great! We also need it to have completed
				// a full round of scrapes.
				err = completedFirstScrapes(inCtx, cfg)
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
