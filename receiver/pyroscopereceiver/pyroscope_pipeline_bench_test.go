package pyroscopereceiver

import (
	"fmt"
	"path/filepath"
	"testing"
)

type request struct {
	urlParams map[string]string
	jfr       string
}

// Benchmarks a running otelcol pyroscope write pipeline (collector and Clickhouse).
// Adjust collectorAddr to bench a your target if needed.
// Example: go test -bench ^BenchmarkPyroscopePipeline$ github.com/metrico/otel-collector/receiver/pyroscopereceiver -benchtime 10s -count 6 -cpu 1 | tee bench.txt
func BenchmarkPyroscopePipeline(b *testing.B) {
	dist := []request{
		{
			urlParams: map[string]string{
				"name":       "com.example.App{dc=us-east-1,kubernetes_pod_name=app-abcd1234}",
				"from":       "1700332322",
				"until":      "1700332329",
				"format":     "jfr",
				"sampleRate": "100",
			},
			jfr: filepath.Join("testdata", "cortex-dev-01__kafka-0__cpu__0.jfr"),
		},
		{
			urlParams: map[string]string{
				"name":   "com.example.App{dc=us-east-1,kubernetes_pod_name=app-abcd1234}",
				"from":   "1700332322",
				"until":  "1700332329",
				"format": "jfr",
			},
			jfr: filepath.Join("testdata", "memory_alloc_live_example.jfr"),
		},
	}
	collectorAddr := fmt.Sprintf("http://%s%s", defaultHttpAddr, ingestPath)

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		j := 0
		for pb.Next() {
			send(collectorAddr, dist[j].urlParams, dist[j].jfr)
			j = (j + 1) % len(dist)
		}
	})
}
