package prometheus

import (
	"context"
	"net/url"
	"sync"

	"github.com/lightstep/opentelemetry-prometheus-sidecar/telemetry"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/prom2json"
	"github.com/prometheus/prometheus/pkg/labels"
)

var monitorDuration = telemetry.NewTimer(
	"sidecar.monitor.duration",
	"duration of the /metrics scrape used to monitor Prometheus",
)

type (
	Monitor struct {
		target *url.URL
	}

	Family struct {
		family *dto.MetricFamily
	}

	Result struct {
		values map[string]Family
	}
)

func NewMonitor(target *url.URL) *Monitor {
	return &Monitor{
		target: target,
	}
}

func (m *Monitor) Get() (_ Result, retErr error) {
	var (
		wg  sync.WaitGroup
		ch  = make(chan *dto.MetricFamily)
		res = Result{
			values: map[string]Family{},
		}
	)

	defer monitorDuration.Start(context.Background()).Stop(&retErr)
	defer wg.Wait()

	wg.Add(1)

	go func() {
		defer wg.Done()
		for mfam := range ch {
			res.values[mfam.GetName()] = Family{
				family: mfam,
			}
		}
	}()

	// Note: FetchMetricFamilies closes the channel.
	return res, prom2json.FetchMetricFamilies(m.target.String(), ch, nil)
}

func (r Result) Counter(name string) Family {
	f := r.values[name]
	if f.family.GetType() != dto.MetricType_COUNTER {
		return Family{}
	}
	return f
}

func (r Result) Gauge(name string) Family {
	f := r.values[name]
	if f.family.GetType() != dto.MetricType_GAUGE {
		return Family{}
	}
	return f
}

func exactMatch(query map[string]string, ls []*dto.LabelPair) bool {
	if len(ls) != len(query) {
		return false
	}
	for _, l := range ls {
		if l == nil || l.Name == nil || l.Value == nil {
			return false
		}
		if query[*l.Name] != *l.Value {
			return false
		}
	}
	return true
}

func (f Family) For(ls labels.Labels) float64 {
	if f.family == nil {
		return 0
	}
	match := ls.Map()
	for _, m := range f.family.Metric {
		if !exactMatch(match, m.Label) {
			continue
		}

		switch f.family.GetType() {
		case dto.MetricType_COUNTER:
			if m.Counter != nil && m.Counter.Value != nil {
				return *m.Counter.Value
			}
		case dto.MetricType_GAUGE:
			if m.Gauge != nil && m.Gauge.Value != nil {
				return *m.Gauge.Value
			}
		}
	}
	return 0
}
