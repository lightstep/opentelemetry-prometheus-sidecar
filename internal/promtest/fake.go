package promtest

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"time"

	"github.com/lightstep/opentelemetry-prometheus-sidecar/common"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/telemetry"
)

// MetadataMap implements a MetadataGetter for exact matches of "job/instance/metric" inputs.
type MetadataMap map[string]*config.MetadataEntry

func (m MetadataMap) Get(ctx context.Context, job, instance, metric string) (*config.MetadataEntry, error) {
	return m[job+"/"+instance+"/"+metric], nil
}

type Config struct {
	Version  string
	Metadata MetadataMap
}

type FakePrometheus struct {
	lock      sync.Mutex
	ready     bool
	segment   int
	intervals []time.Duration
	config    string
	mux       *http.ServeMux
}

func NewFakePrometheus(cfg Config) *FakePrometheus {
	if cfg.Version == "" {
		cfg.Version = config.PrometheusMinVersion
	}

	const segmentName = config.PrometheusCurrentSegmentMetricName
	const scrapeIntervalName = config.PrometheusTargetIntervalLengthName
	const scrapeIntervalSum = scrapeIntervalName + "_sum"
	const scrapeIntervalCount = scrapeIntervalName + "_count"
	const promBuildInfo = config.PrometheusBuildInfoName

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
# HELP %s A metric with a constant '1' value labeled by version, revision, branch, and goversion from which prometheus was built.
# TYPE %s gauge
%s{branch="HEAD",goversion="go1.11.1",revision="167a4b4e73a8eca8df648d2d2043e21bdb9a7449",version="%s"} 1
`, promBuildInfo, promBuildInfo, promBuildInfo, cfg.Version)))
		if err != nil {
			panic(err)
		}

		_, err = w.Write([]byte(fmt.Sprintf(`
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

	// Serve instrument metadata
	fp.mux.HandleFunc("/"+config.PrometheusMetadataEndpointPath,
		func(w http.ResponseWriter, r *http.Request) {
			var metaResp common.MetadataAPIResponse
			for _, entry := range cfg.Metadata {
				// Note: This endpoint is used to request metadata
				// for a specific target.  It does not use the target
				// details and returns constant metadata for testing
				// purposes.
				metaResp.Data = append(metaResp.Data, common.APIMetadata{
					Metric: entry.Metric,
					Help:   "helpful",
					Type:   entry.MetricType,
				})
			}
			metaRespData, err := json.Marshal(metaResp)
			if err != nil {
				panic(err)
			}

			_, _ = w.Write(metaRespData)
		},
	)

	// Serve the server's configuration
	fp.mux.HandleFunc("/"+config.PrometheusConfigEndpointPath,
		func(w http.ResponseWriter, r *http.Request) {
			fp.lock.Lock()
			defer fp.lock.Unlock()

			var cfg common.ConfigAPIResponse
			cfg.Status = "ok"
			cfg.Data.YAML = fp.config

			data, err := json.Marshal(&cfg)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			_, _ = w.Write(data)
		},
	)
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
		Logger:  telemetry.DefaultLogger(),
		PromURL: fp.Test(),
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

func (fp *FakePrometheus) SetPromConfigYaml(config string) {
	fp.lock.Lock()
	defer fp.lock.Unlock()

	fp.config = config
}

func (fp *FakePrometheus) ServeMux() *http.ServeMux {
	return fp.mux
}
