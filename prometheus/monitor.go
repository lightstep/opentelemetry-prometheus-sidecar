package prometheus

import (
	"context"
	"fmt"
	"net/http"
	"path"
	"sync"

	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/telemetry"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/prom2json"
	promconfig "github.com/prometheus/prometheus/config"
	"github.com/prometheus/prometheus/pkg/labels"
)

var monitorDuration = telemetry.NewTimer(
	"sidecar.monitor.duration",
	"duration of the /metrics scrape used to monitor Prometheus",
)

// copied from prom2json
const acceptHeader = `application/vnd.google.protobuf;proto=io.prometheus.client.MetricFamily;encoding=delimited;q=0.7,text/plain;version=0.0.4;q=0.3`

type (
	Monitor struct {
		cfg          config.PromReady
		scrapeConfig []*promconfig.ScrapeConfig
	}

	Family struct {
		family *dto.MetricFamily
	}

	SummaryFamily struct {
		family *dto.MetricFamily
	}

	Result struct {
		values map[string]*dto.MetricFamily
	}

	Summary struct {
		summary *dto.Summary
	}
)

func NewMonitor(cfg config.PromReady) *Monitor {
	return &Monitor{
		cfg: cfg,
	}
}

func (m *Monitor) GetMetrics(ctx context.Context) (_ Result, retErr error) {
	var (
		wg  sync.WaitGroup
		ch  = make(chan *dto.MetricFamily)
		res = Result{
			values: map[string]*dto.MetricFamily{},
		}
	)

	scrape := *m.cfg.PromURL
	scrape.Path = path.Join(scrape.Path, "/metrics")
	target := scrape.String()

	defer monitorDuration.Start(context.Background()).Stop(&retErr)
	defer wg.Wait()

	wg.Add(1)

	go func() {
		defer wg.Done()
		for mfam := range ch {
			res.values[mfam.GetName()] = mfam
		}
	}()

	// Note: copied from FetchMetricFamilies, Context added; this code path closes `ch`.
	req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
	if err != nil {
		close(ch)
		return Result{}, fmt.Errorf("creating GET request for URL %q failed: %v", target, err)
	}
	req.Header.Add("Accept", acceptHeader)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		close(ch)
		return Result{}, fmt.Errorf("executing GET request for URL %q failed: %v", target, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		close(ch)
		return Result{}, fmt.Errorf("GET request for URL %q returned HTTP status %s", target, resp.Status)
	}
	return res, prom2json.ParseResponse(resp, ch)
}

func (r Result) Counter(name string) Family {
	f := r.values[name]
	if f.GetType() != dto.MetricType_COUNTER {
		return Family{}
	}
	return Family{f}
}

func (r Result) Gauge(name string) Family {
	f := r.values[name]
	if f.GetType() != dto.MetricType_GAUGE {
		return Family{}
	}
	return Family{f}
}

func (r Result) Summary(name string) SummaryFamily {
	f := r.values[name]
	if f.GetType() != dto.MetricType_SUMMARY {
		return SummaryFamily{}
	}
	return SummaryFamily{f}
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
		switch f.family.GetType() {
		case dto.MetricType_COUNTER:
			if m.Counter != nil && m.Counter.Value != nil && exactMatch(match, m.Label) {
				return *m.Counter.Value
			}
		case dto.MetricType_GAUGE:
			if m.Gauge != nil && m.Gauge.Value != nil && exactMatch(match, m.Label) {
				return *m.Gauge.Value
			}
		}
	}
	return 0
}

// AllLabels returns the set of labels present for this family of
// Summaries.
func (f SummaryFamily) AllLabels() []labels.Labels {
	if f.family == nil {
		return nil
	}
	var res []labels.Labels
	for _, m := range f.family.Metric {
		var ll labels.Labels
		for _, lp := range m.Label {
			ll = append(ll, labels.Label{
				Name:  *lp.Name,
				Value: *lp.Value,
			})
		}
		res = append(res, ll)
	}
	return res
}

func (f SummaryFamily) For(ls labels.Labels) Summary {
	if f.family == nil {
		return Summary{}
	}
	match := ls.Map()
	for _, m := range f.family.Metric {
		if f.family.GetType() != dto.MetricType_SUMMARY {
			continue
		}
		if !exactMatch(match, m.Label) {
			continue
		}
		return Summary{
			summary: m.Summary,
		}
	}
	return Summary{}
}

func (s Summary) Count() uint64 {
	if s.summary == nil {
		return 0
	}
	return *s.summary.SampleCount
}

// AllLabels returns the set of labels present for this family
func (f Family) AllLabels() []labels.Labels {
	if f.family == nil {
		return nil
	}
	var res []labels.Labels
	for _, m := range f.family.Metric {
		var ll labels.Labels
		for _, lp := range m.Label {
			ll = append(ll, labels.Label{
				Name:  *lp.Name,
				Value: *lp.Value,
			})
		}
		res = append(res, ll)
	}
	return res
}
