package prometheus

import (
	"context"
	"net/url"
	"sync"

	"github.com/lightstep/opentelemetry-prometheus-sidecar/telemetry"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/prom2json"
)

var monitorDuration = telemetry.NewTimer(
	"sidecar.monitor.duration",
	"duration of the /metrics scrape used to monitor Prometheus",
)

type (
	Monitor struct {
		target *url.URL
	}

	Result map[string]*dto.MetricFamily
)

func NewMonitor(target *url.URL) *Monitor {
	return &Monitor{
		target: target,
	}
}

func (m *Monitor) Get() (_ Result, retErr error) {
	var (
		wg  sync.WaitGroup
		res = Result{}
		ch  = make(chan *dto.MetricFamily)
	)

	defer monitorDuration.Start(context.Background()).Stop(&retErr)
	defer wg.Wait()

	wg.Add(1)

	go func() {
		defer wg.Done()
		for mfam := range ch {
			if mfam.Name != nil {
				res[*mfam.Name] = mfam
			}
		}
	}()

	// Note: FetchMetricFamilies closes the channel.
	return res, prom2json.FetchMetricFamilies(m.target.String(), ch, nil)
}
