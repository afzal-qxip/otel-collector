package pyroscopereceiver

import (
	"fmt"

	"go.opentelemetry.io/otel/metric"
)

const prefix = "receiver_pyroscope_"

var (
	otelcolReceiverPyroscopeHttpRequestTotal                 metric.Int64Counter
	otelcolReceiverPyroscopeRequestBodyUncompressedSizeBytes metric.Int64Histogram
	otelcolReceiverPyroscopeParsedBodyUncompressedSizeBytes  metric.Int64Histogram
	otelcolReceiverPyroscopeHttpResponseTimeMillis           metric.Int64Histogram
)

func initMetrics(meter metric.Meter) error {
	var err error
	if otelcolReceiverPyroscopeHttpRequestTotal, err = meter.Int64Counter(
		fmt.Sprint(prefix, "http_request_total"),
		metric.WithDescription("Pyroscope receiver http request count"),
	); err != nil {
		return err
	}
	if otelcolReceiverPyroscopeRequestBodyUncompressedSizeBytes, err = meter.Int64Histogram(
		fmt.Sprint(prefix, "request_body_uncompressed_size_bytes"),
		metric.WithDescription("Pyroscope receiver uncompressed request body size in bytes"),
	); err != nil {
		return err
	}
	if otelcolReceiverPyroscopeParsedBodyUncompressedSizeBytes, err = meter.Int64Histogram(
		fmt.Sprint(prefix, "parsed_body_uncompressed_size_bytes"),
		metric.WithDescription("Pyroscope receiver uncompressed parsed body size in bytes"),
	); err != nil {
		return err
	}
	if otelcolReceiverPyroscopeHttpResponseTimeMillis, err = meter.Int64Histogram(
		fmt.Sprint(prefix, "http_response_time_millis"),
		metric.WithDescription("Pyroscope receiver http response time in millis"),
	); err != nil {
		return err
	}
	return nil
}
