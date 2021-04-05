package prometheus

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"time"

	"github.com/go-kit/kit/log/level"
	goversion "github.com/hashicorp/go-version"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/common"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/pkg/errors"
	promconfig "github.com/prometheus/prometheus/config"
	"gopkg.in/yaml.v2"
)

const (
	scrapeIntervalName = config.PrometheusTargetIntervalLengthName
)

func (m *Monitor) scrapeMetrics(inCtx context.Context) (Result, error) {
	ctx, cancel := context.WithTimeout(inCtx, config.DefaultHealthCheckTimeout)
	defer cancel()

	return m.GetMetrics(ctx)
}

func (m *Monitor) checkPrometheusVersion(res Result) error {
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

func (m *Monitor) scrapeIntervals(promcfg promconfig.Config) []time.Duration {
	ds := map[time.Duration]struct{}{}
	for _, sc := range promcfg.ScrapeConfigs {
		si := sc.ScrapeInterval
		if si == 0 {
			si = promcfg.GlobalConfig.ScrapeInterval
		}
		ds[time.Duration(si)] = struct{}{}
	}
	var res []time.Duration
	for d := range ds {
		res = append(res, d)
	}
	return res
}

func (m *Monitor) completedFirstScrapes(res Result, promcfg promconfig.Config, prometheusStartTime time.Time) error {
	scrapeIntervals := m.scrapeIntervals(promcfg)

	summary := res.Summary(scrapeIntervalName)
	foundLabelSets := summary.AllLabels()
	if len(foundLabelSets) == 0 {
		return errors.New("waiting for the first scrape(s) to complete")
	}

	// Prometheus doesn't report zero counts. We expect absent
	// timeseries, not zero counts, but we test for Count() != 0 on
	// the retrieved metrics for added safety below.

	if len(scrapeIntervals) == 0 {
		// If no intervals are configured, wait for the first one.
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

	for _, si := range scrapeIntervals {
		ts := si.String()
		if !foundWhich[ts] {
			// have we waited the max amount of time before moving on
			if time.Since(prometheusStartTime) > si+config.DefaultScrapeIntervalWaitPeriod {
				level.Warn(m.cfg.Logger).Log("msg", "waited until deadline for scrape interval", "missing-interval", ts)
				break
			} else {
				return errors.Errorf("waiting for scrape interval %s", ts)
			}
		}
	}

	return nil
}

func (m *Monitor) getConfig(inCtx context.Context) (promconfig.Config, error) {
	ctx, cancel := context.WithTimeout(inCtx, config.DefaultHealthCheckTimeout)
	defer cancel()

	return m.GetConfig(ctx)
}

func (m *Monitor) GetConfig(ctx context.Context) (promconfig.Config, error) {
	endpoint := *m.cfg.PromURL
	endpoint.Path = path.Join(endpoint.Path, config.PrometheusConfigEndpointPath)
	target := endpoint.String()

	req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
	if err != nil {
		return promconfig.Config{}, fmt.Errorf("config request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return promconfig.Config{}, fmt.Errorf("config get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return promconfig.Config{}, fmt.Errorf("config get status %s", resp.Status)
	}

	var apiResp common.ConfigAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return promconfig.Config{}, errors.Wrap(err, "config json decode")
	}

	// This sets the default interval as Prometheus would.
	promCfg := promconfig.DefaultConfig
	if err := yaml.Unmarshal([]byte(apiResp.Data.YAML), &promCfg); err != nil {
		return promconfig.Config{}, errors.Wrap(err, "config yaml decode")
	}

	return promCfg, nil

}

func (m *Monitor) GetScrapeConfig() []*promconfig.ScrapeConfig {
	return m.scrapeConfig
}

func (m *Monitor) WaitForReady(inCtx context.Context, inCtxCancel context.CancelFunc) error {
	u := *m.cfg.PromURL
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
				var result Result
				result, err = m.scrapeMetrics(inCtx)
				if err != nil {
					return false
				}
				err = m.checkPrometheusVersion(result)
				if err != nil {
					// invalid prometheus version is unrecoverable
					// cancel the caller's context and exit
					level.Warn(m.cfg.Logger).Log("msg", "invalid Prometheus version", "err", err)
					inCtxCancel()
					return false
				}
				var promCfg promconfig.Config
				promCfg, err = m.getConfig(inCtx)
				if err != nil {
					level.Warn(m.cfg.Logger).Log("msg", "invalid Prometheus config", "err", err)
					inCtxCancel()
					return false
				}
				m.scrapeConfig = promCfg.ScrapeConfigs

				// Great! We also need it to have completed
				// a full round of scrapes.
				if err = m.completedFirstScrapes(result, promCfg, m.cfg.StartupDelayEffectiveStartTime); err == nil {
					return true
				}
			}

			if !warnSkipped {
				warnSkipped = true
				return false
			}
			if respOK || err != nil {
				level.Warn(m.cfg.Logger).Log("msg", "Prometheus readiness", "err", err)
			} else {
				level.Warn(m.cfg.Logger).Log("msg", "Prometheus is not ready", "status", resp.Status)
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
