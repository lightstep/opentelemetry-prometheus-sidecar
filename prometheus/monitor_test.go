package prometheus

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMonitorScrape(t *testing.T) {

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := json.NewEncoder(w).Encode(
		// handler(
		// r.FormValue("metric"),
		// r.FormValue("match_target"),
		// )
		)
		if err != nil {
			t.Fatal(err)
		}
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

}
