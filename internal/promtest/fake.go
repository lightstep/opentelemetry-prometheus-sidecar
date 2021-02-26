package promtest

import (
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"time"

	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/telemetry"
)

type FakePrometheus struct {
	lock      sync.Mutex
	ready     bool
	segment   int
	intervals []time.Duration
	mux       *http.ServeMux
}

func NewFakePrometheus() *FakePrometheus {
	const segmentName = config.PrometheusCurrentSegmentMetricName
	const scrapeIntervalName = config.PrometheusTargetIntervalLengthName
	const scrapeIntervalSum = scrapeIntervalName + "_sum"
	const scrapeIntervalCount = scrapeIntervalName + "_count"

	fp := &FakePrometheus{
		ready:     true,
		segment:   0,
		intervals: []time.Duration{30 * time.Second},
		mux:       http.NewServeMux(),
	}

	fp.mux.HandleFunc("/-/ready", func(w http.ResponseWriter, r *http.Request) {
		fp.lock.Lock()
		defer fp.lock.Unlock()
		if fp.ready {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	})
	fp.mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		fp.lock.Lock()
		defer fp.lock.Unlock()

		_, err := w.Write([]byte(fmt.Sprintf(`
# HELP %s Current segment.
# TYPE %s gauge
%s{} %d
`, segmentName, segmentName, segmentName, fp.segment)))
		if err != nil {
			panic(err)
		}

		_, err = w.Write([]byte(fmt.Sprintf(`
# HELP %s Scrape interval summary.
# TYPE %s summary
`, scrapeIntervalName, scrapeIntervalName)))
		if err != nil {
			panic(err)
		}

		for _, in := range fp.intervals {
			cnt := 1 + rand.Intn(3)
			p99 := in.Seconds() + 0.000123
			sum := float64(cnt) * p99
			_, err = w.Write([]byte(fmt.Sprintf(`
%s{interval="%s",quantile="0.99"} %f
%s{interval="%s"} %f
%s{interval="%s"} %d
`, scrapeIntervalName, in, p99, scrapeIntervalSum, in, sum, scrapeIntervalCount, in, cnt)))
			if err != nil {
				panic(err)
			}
		}
	})
	return fp
}

func (fp *FakePrometheus) Test() *url.URL {
	server := httptest.NewServer(fp.mux)

	fpu, err := url.Parse(server.URL)
	if err != nil {
		panic(err)
	}

	return fpu
}

func (fp *FakePrometheus) ReadyConfig() config.PromReady {
	return config.PromReady{
		Logger:          telemetry.DefaultLogger(),
		PromURL:         fp.Test(),
		ScrapeIntervals: nil,
	}
}

func (fp *FakePrometheus) SetSegment(s int) {
	fp.lock.Lock()
	defer fp.lock.Unlock()

	fp.segment = s
}

func (fp *FakePrometheus) SetReady(r bool) {
	fp.lock.Lock()
	defer fp.lock.Unlock()

	fp.ready = r
}

func (fp *FakePrometheus) SetIntervals(is ...time.Duration) {
	fp.lock.Lock()
	defer fp.lock.Unlock()

	fp.intervals = is
}

func (fp *FakePrometheus) ServeMux() *http.ServeMux {
	return fp.mux
}
