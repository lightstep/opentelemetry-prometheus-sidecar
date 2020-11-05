package sidecar

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"

	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
)

func Example() {
	cfg, _, _, err := config.Configure([]string{
		"program",
		"--config-file=./sidecar.example.yaml",
	}, ioutil.ReadFile)
	if err != nil {
		log.Fatal(err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(string(data))

	// Output:
	// {
	//   "destination": {
	//     "endpoint": "https://ingest.staging.lightstep.com:443",
	//     "headers": {
	//       "Lightstep-Access-Token": "aabbccdd...wwxxyyzz"
	//     },
	//     "attributes": {
	//       "service.name": "demo"
	//     }
	//   },
	//   "prometheus": {
	//     "endpoint": "http://127.0.0.1:19090",
	//     "wal": "/volume/wal"
	//   },
	//   "opentelemetry": {
	//     "metrics_prefix": "prefix.",
	//     "use_meta_labels": true
	//   },
	//   "admin": {
	//     "listen_address": "0.0.0.0:10000"
	//   },
	//   "security": {
	//     "root_certificates": [
	//       "/certs/root1.crt",
	//       "/certs/root2.crt"
	//     ]
	//   },
	//   "startup_delay": "30s",
	//   "filter_sets": [
	//     "metric{label=value}",
	//     "other{l1=v1,l2=v2}"
	//   ],
	//   "metric_renames": [
	//     {
	//       "from": "old_metric",
	//       "to": "new_metric"
	//     },
	//     {
	//       "from": "mistake",
	//       "to": "correct"
	//     }
	//   ],
	//   "static_metadata": [
	//     {
	//       "metric": "network_bps",
	//       "type": "counter",
	//       "value_type": "int64",
	//       "help": "Number of bits transferred by this process."
	//     }
	//   ],
	//   "log_config": {
	//     "level": "warn",
	//     "format": "json"
	//   }
	// }
}