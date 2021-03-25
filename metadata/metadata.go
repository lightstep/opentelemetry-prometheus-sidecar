/*
Copyright 2018 Google Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lightstep/opentelemetry-prometheus-sidecar/common"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/telemetry"
	"github.com/pkg/errors"
	"github.com/prometheus/prometheus/pkg/textparse"
	"go.opentelemetry.io/otel/attribute"
)

var (
	fetchTimer = telemetry.NewTimer(
		"sidecar.metadata.fetch.duration",
		"Times the operation to fetch the sidecar's cache of Prometheus metadata.",
	)
)

// Cache populates and maintains a cache of metric metadata it retrieves
// from a given Prometheus server.
// Its methods are not safe for concurrent use.
type Cache struct {
	promURL *url.URL
	client  *http.Client

	metadata       map[string]*cacheEntry
	seenJobs       map[string]struct{}
	staticMetadata map[string]*config.MetadataEntry
}

// TODO: This code could use metrics to report the current size of the
// cache, similar to ../retrieval/series_cache.go has.
//
// TODO: Add garbage collection in this file, somehow.

// NewCache returns a new cache that gets populated by the metadata endpoint
// at the given URL.
// It uses the default endpoint path if no specific path is provided.
func NewCache(client *http.Client, promURL *url.URL, staticMetadata []*config.MetadataEntry) *Cache {
	if client == nil {
		client = http.DefaultClient
	}
	c := &Cache{
		promURL:        promURL,
		client:         client,
		staticMetadata: map[string]*config.MetadataEntry{},
		metadata:       map[string]*cacheEntry{},
		seenJobs:       map[string]struct{}{},
	}
	for _, m := range staticMetadata {
		c.staticMetadata[m.Metric] = m
	}
	return c
}

const retryInterval = 30 * time.Second

type cacheEntry struct {
	Entry     *config.MetadataEntry
	found     bool
	lastFetch time.Time
}

func (e *cacheEntry) shouldRefetch() bool {
	return !e.found && time.Since(e.lastFetch) > retryInterval
}

// Get returns metadata for the given metric and job. If the metadata
// is not in the cache, it blocks until we have retrieved it from the Prometheus server.
// If no metadata is found in the Prometheus server, a matching entry from the
// static metadata or nil is returned.
func (c *Cache) Get(ctx context.Context, job, instance, metric string) (*config.MetadataEntry, error) {
	if md, ok := c.staticMetadata[metric]; ok {
		return md, nil
	}
	md, ok := c.metadata[metric]
	if !ok || md.shouldRefetch() {
		// If we are seeing the job for the first time, preemptively get a full
		// list of all metadata for the instance.
		if _, ok := c.seenJobs[job]; !ok {
			mds, err := c.fetchBatch(ctx, job, instance)
			if err != nil {
				return nil, errors.Wrapf(err, "fetch metadata for job %q", job)
			}
			for _, md := range mds {
				// Only set if we haven't seen the metric before. Changes to metadata
				// are discouraged.
				if _, ok := c.metadata[md.Entry.Metric]; !ok {
					c.metadata[md.Entry.Metric] = md
				}
			}
			c.seenJobs[job] = struct{}{}
		} else {
			md, err := c.fetchSingle(ctx, job, instance, metric)
			if err != nil {
				return nil, errors.Wrapf(err, "fetch metric metadata \"%s/%s/%s\"", job, instance, metric)
			}
			c.metadata[metric] = md
		}
		md = c.metadata[metric]
	}
	if md != nil && md.found {
		return md.Entry, nil
	}
	// The metric might also be produced by a recording rule, which by convention
	// contain at least one `:` character. In that case we can generally assume that
	// it is a gauge. We leave the help text empty.
	if strings.Contains(metric, ":") {
		entry := &config.MetadataEntry{Metric: metric, MetricType: textparse.MetricTypeGauge}
		return entry, nil
	}
	return nil, nil
}

func (c *Cache) fetch(ctx context.Context, mode string, q url.Values) (_ *common.MetadataAPIResponse, retErr error) {
	ctx, cancel := context.WithTimeout(ctx, config.DefaultPrometheusTimeout)
	defer cancel()

	defer fetchTimer.Start(ctx).Stop(&retErr, attribute.String("mode", mode))

	u := *c.promURL
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, errors.Wrap(err, "build request")
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "query Prometheus")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metadata request HTTP status %s", resp.Status)
	}

	var apiResp common.MetadataAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, errors.Wrap(err, "decode response")
	}
	return &apiResp, nil
}

const apiErrorNotFound = "not_found"

// fetchSingle fetches metadata for the given job, instance, and metric combination.
// It returns a not-found entry if the fetch is successful but returns no data.
func (c *Cache) fetchSingle(ctx context.Context, job, instance, metric string) (*cacheEntry, error) {
	job, instance = escapeLval(job), escapeLval(instance)

	apiResp, err := c.fetch(ctx, "single", url.Values{
		"match_target": []string{fmt.Sprintf("{job=\"%s\",instance=\"%s\"}", job, instance)},
		"metric":       []string{metric},
	})
	if err != nil {
		return nil, err
	}
	now := time.Now()

	if apiResp.ErrorType != "" && apiResp.ErrorType != apiErrorNotFound {
		return nil, errors.Wrap(errors.New(apiResp.Error), "lookup failed")
	}
	if len(apiResp.Data) == 0 {
		// Cache a not-found entry.
		return &cacheEntry{
			lastFetch: now,
			found:     false,
		}, nil
	}
	d := apiResp.Data[0]

	// Convert legacy "untyped" type used before Prometheus 2.5.
	if d.Type == config.MetricTypeUntyped {
		d.Type = textparse.MetricTypeUnknown
	}
	return &cacheEntry{
		Entry:     &config.MetadataEntry{Metric: metric, MetricType: d.Type, Help: d.Help},
		lastFetch: now,
		found:     true,
	}, nil
}

// fetchBatch fetches all metric metadata for the given job and instance combination.
// We constrain it by instance to reduce the total payload size.
// In a well-configured setup it is unlikely that instances for the same job have any notable
// difference in their exposed metrics.
func (c *Cache) fetchBatch(ctx context.Context, job, instance string) (map[string]*cacheEntry, error) {
	job, instance = escapeLval(job), escapeLval(instance)

	apiResp, err := c.fetch(ctx, "batch", url.Values{
		"match_target": []string{fmt.Sprintf("{job=\"%s\",instance=\"%s\"}", job, instance)},
	})
	if err != nil {
		return nil, err
	}
	now := time.Now()

	if apiResp.ErrorType == apiErrorNotFound {
		return nil, nil
	}
	if apiResp.ErrorType != "" {
		return nil, errors.Wrap(errors.New(apiResp.Error), "lookup failed")
	}
	// Pre-allocate for all received data plus internal metrics.
	result := make(map[string]*cacheEntry, len(apiResp.Data)+len(internalMetrics))

	for _, md := range apiResp.Data {
		// Convert legacy "untyped" type used before Prometheus 2.5.
		if md.Type == config.MetricTypeUntyped {
			md.Type = textparse.MetricTypeUnknown
		}
		result[md.Metric] = &cacheEntry{
			Entry:     &config.MetadataEntry{Metric: md.Metric, MetricType: md.Type, Help: md.Help},
			lastFetch: now,
			found:     true,
		}
	}
	// Prometheus's scraping layer writes a few internal metrics, which we won't get
	// metadata for via the API. We populate hardcoded metadata for them.
	for _, md := range internalMetrics {
		result[md.Metric] = &cacheEntry{Entry: md, lastFetch: now, found: true}
	}
	return result, nil
}

var internalMetrics = map[string]*config.MetadataEntry{
	"up": &config.MetadataEntry{
		Metric:     "up",
		MetricType: textparse.MetricTypeGauge,
		ValueType:  config.DOUBLE,
		Help:       "Up indicates whether the last target scrape was successful"},
	"scrape_samples_scraped": &config.MetadataEntry{
		Metric:     "scrape_samples_scraped",
		MetricType: textparse.MetricTypeGauge,
		ValueType:  config.DOUBLE,
		Help:       "How many samples were scraped during the last successful scrape"},
	"scrape_duration_seconds": &config.MetadataEntry{
		Metric:     "scrape_duration_seconds",
		MetricType: textparse.MetricTypeGauge,
		ValueType:  config.DOUBLE,
		Help:       "Duration of the last scrape"},
	"scrape_samples_post_metric_relabeling": &config.MetadataEntry{
		Metric:     "scrape_samples_post_metric_relabeling",
		MetricType: textparse.MetricTypeGauge,
		ValueType:  config.DOUBLE,
		Help:       "How many samples were ingested after relabeling"},
	"scrape_series_added": &config.MetadataEntry{
		Metric:     "scrape_series_added",
		MetricType: textparse.MetricTypeGauge,
		ValueType:  config.DOUBLE,
		Help:       "Number of new series in the last successful scrape"},
}

var lvalReplacer = strings.NewReplacer(
	"\"", `\"`,
	"\\", `\\`,
	"\n", `\n`,
)

// escapeLval escapes a label value.
func escapeLval(s string) string {
	return lvalReplacer.Replace(s)
}
