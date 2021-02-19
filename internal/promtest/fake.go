package promtest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"

	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
)

type FakePrometheus struct {
	lock    sync.Mutex
	ready   bool
	segment int
	server  *httptest.Server
	URL     *url.URL
}

func NewFakePrometheus() *FakePrometheus {
	const name = config.PrometheusCurrentSegmentMetricName
	fp := &FakePrometheus{
		ready:   true,
		segment: 0,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/-/ready", func(w http.ResponseWriter, r *http.Request) {
		fp.lock.Lock()
		defer fp.lock.Unlock()
		if fp.ready {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		fp.lock.Lock()
		defer fp.lock.Unlock()
		_, err := w.Write([]byte(fmt.Sprintf(`
# HELP %s Current segment.
# TYPE %s gauge
%s{} %d
`, name, name, name, fp.segment)))
		if err != nil {
			panic(err)
		}
	})

	fp.server = httptest.NewServer(mux)

	fpu, err := url.Parse(fp.server.URL)
	if err != nil {
		panic(err)
	}

	fp.URL = fpu

	return fp
}

func (fp *FakePrometheus) SetSegment(s int) {
	fp.lock.Lock()
	defer fp.lock.Unlock()

	fp.segment = s
}
