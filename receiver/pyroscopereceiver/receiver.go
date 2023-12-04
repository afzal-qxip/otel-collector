package pyroscopereceiver

import (
	"context"
	"errors"
	"fmt"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/metrico/otel-collector/receiver/pyroscopereceiver/compress"
	"github.com/metrico/otel-collector/receiver/pyroscopereceiver/jfrparser"
	"github.com/metrico/otel-collector/receiver/pyroscopereceiver/profile"

	promql_parser "github.com/prometheus/prometheus/promql/parser"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/receiver"
	"go.uber.org/zap"
)

const (
	ingestPath = "/ingest"
	nameLabel  = "__name__"
	jfrFormat  = "jfr"
)

type pyroscopeReceiver struct {
	conf     *Config
	next     consumer.Logs
	settings *receiver.CreateSettings
	logger   *zap.Logger
	host     component.Host

	httpMux      *http.ServeMux
	decompressor *compress.Decompressor
	httpServer   *http.Server
	shutdownWg   sync.WaitGroup
}

func newPyroscopeReceiver(baseCfg *Config, consumer consumer.Logs, params *receiver.CreateSettings) *pyroscopeReceiver {
	recv := &pyroscopeReceiver{
		conf:     baseCfg,
		next:     consumer,
		settings: params,
		logger:   params.Logger,
	}
	recv.decompressor = compress.NewDecompressor(recv.conf.Protocols.Http.MaxRequestBodySize)
	recv.httpMux = http.NewServeMux()
	recv.httpMux.HandleFunc(ingestPath, func(resp http.ResponseWriter, req *http.Request) {
		handleIngest(resp, req, recv)
	})
	return recv
}

func parse(req *http.Request, recv *pyroscopeReceiver) (*plog.Logs, error) {
	var (
		tmp []string
		ok  bool
	)
	logs := plog.NewLogs()
	rec := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()

	params := req.URL.Query()
	if err := setAttrsFromParams(&params, &rec); err != nil {
		return &logs, err
	}

	// support jfr only
	if tmp, ok = params["format"]; !ok || tmp[0] != "jfr" {
		return &logs, fmt.Errorf("unsupported format, supported: [jfr]")
	}

	// support only multipart/form-data
	file, err := recv.openMultipartJfr(req)
	if err != nil {
		return &logs, err
	}
	defer file.Close()

	buf, err := recv.decompressor.Decompress(file, "gzip")
	if err != nil {
		return &logs, fmt.Errorf("failed to decompress body: %w", err)
	}
	resetHeaders(req)

	md := profile.Metadata{SampleRateHertz: 0}
	tmp, ok = params["sampleRate"]
	if ok {
		hz, err := strconv.ParseUint(tmp[0], 10, 64)
		if err != nil {
			return &logs, fmt.Errorf("failed to parse rate: %w", err)
		}
		md.SampleRateHertz = hz
	}

	prof, err := jfrparser.NewJfrParser(buf, md, recv.conf.Protocols.Http.MaxRequestBodySize).ParsePprof()
	if err != nil {
		return &logs, fmt.Errorf("failed to parse pprof: %w", err)
	}
	setAttrsFromProfile(prof, &rec)

	// TODO: consider to avoid copy in FromRaw()
	rec.Body().SetEmptyBytes().FromRaw(prof.Payload.Bytes())

	return &logs, nil
}

func (d *pyroscopeReceiver) openMultipartJfr(unparsed *http.Request) (multipart.File, error) {
	if err := unparsed.ParseMultipartForm(d.conf.Protocols.Http.MaxRequestBodySize); err != nil {
		return nil, fmt.Errorf("failed to parse multipart request: %w", err)
	}
	multipartForm := unparsed.MultipartForm
	defer func() {
		_ = multipartForm.RemoveAll()
	}()

	part, ok := multipartForm.File[jfrFormat]
	if !ok {
		return nil, fmt.Errorf("required jfr part is missing")
	}
	fh := part[0]
	if fh.Filename != jfrFormat {
		return nil, fmt.Errorf("jfr filename is not '%s'", jfrFormat)
	}
	f, err := fh.Open()
	if err != nil {
		return nil, fmt.Errorf("failed to open jfr file")
	}
	return f, nil
}

func resetHeaders(req *http.Request) {
	// reset content-type for the new binary jfr body
	req.Header.Set("Content-Type", "application/octet-stream")
	// multipart content-types cannot have encodings so no need to Del() Content-Encoding
	// reset "Content-Length" to -1 as the size of the decompressed body is unknown
	req.Header.Del("Content-Length")
	req.ContentLength = -1
}

func setAttrsFromParams(params *url.Values, rec *plog.LogRecord) error {
	var (
		tmp     []string
		ok      bool
		paramsv = *params
	)

	if tmp, ok = paramsv["from"]; !ok {
		return fmt.Errorf("required start time is missing")
	}
	start, err := strconv.ParseUint(tmp[0], 10, 64)
	if err != nil {
		return fmt.Errorf("failed to parse start time: %w", err)
	}
	rec.SetTimestamp(pcommon.Timestamp(start))

	if tmp, ok = paramsv["name"]; !ok {
		return fmt.Errorf("required labels are missing")
	}
	i := strings.Index(tmp[0], "{")
	length := len(tmp[0])
	if i < 0 {
		i = length
	} else { // optional labels
		promql := tmp[0][i:length]
		labels, err := promql_parser.ParseMetric(promql)
		if err != nil {
			return fmt.Errorf("failed to parse labels: %w", err)
		}
		for _, l := range labels {
			rec.Attributes().PutStr(l.Name, l.Value)
		}
	}
	// required app name
	name := tmp[0][:i]
	rec.Attributes().PutStr(nameLabel, name)

	if tmp, ok = paramsv["until"]; !ok {
		return fmt.Errorf("required end time is missing")
	}
	end, err := strconv.ParseUint(tmp[0], 10, 64)
	if err != nil {
		return fmt.Errorf("failed to parse end time: %w", err)
	}
	rec.Attributes().PutStr("duration_ns", fmt.Sprint((end-start)*1e9))
	return nil
}

func setAttrsFromProfile(prof *profile.Profile, rec *plog.LogRecord) {
	rec.Attributes().PutStr("type", prof.Type.Type)
	rec.Attributes().PutStr("sample_type", strings.Join(prof.Type.SampleType, ","))
	rec.Attributes().PutStr("sample_unit", strings.Join(prof.Type.SampleUnit, ","))
	rec.Attributes().PutStr("period_type", prof.Type.PeriodType)
	rec.Attributes().PutStr("period_unit", prof.Type.PeriodUnit)
	rec.Attributes().PutStr("payload_type", fmt.Sprint(prof.PayloadType))
}

func handleIngest(resp http.ResponseWriter, req *http.Request, recv *pyroscopeReceiver) {
	if req.Method != http.MethodPost {
		msg := fmt.Sprintf("method not allowed, supported: [%s]", http.MethodPost)
		recv.logger.Error(msg)
		writeResponse(resp, "text/plain", http.StatusMethodNotAllowed, []byte(msg))
		return
	}

	ctx := req.Context()
	logs, err := parse(req, recv)
	if err != nil {
		msg := err.Error()
		recv.logger.Error(msg)
		writeResponse(resp, "text/plain", http.StatusBadRequest, []byte(msg))
		return
	}

	// delegate to next consumer in the pipeline
	// TODO: support memorylimiter processor, apply retry policy on "oom", and consider to shift right allocs from the receiver
	recv.next.ConsumeLogs(ctx, *logs)
}

func (recv *pyroscopeReceiver) Start(_ context.Context, host component.Host) error {
	recv.host = host
	var err error

	// applies an interceptor that enforces the configured request body limit
	// TODO: rm redundant interceptors applied by ToServer() like decompressor
	if recv.httpServer, err = recv.conf.Protocols.Http.ToServer(host, recv.settings.TelemetrySettings, recv.httpMux); err != nil {
		return fmt.Errorf("failed to create http server: %w", err)
	}

	recv.logger.Info("server listening on", zap.String("endpoint", recv.conf.Protocols.Http.Endpoint))
	var listener net.Listener
	if listener, err = recv.conf.Protocols.Http.ToListener(); err != nil {
		return fmt.Errorf("failed to create tcp listener: %w", err)
	}

	// TODO: rate limit clients
	recv.shutdownWg.Add(1)
	go func() {
		defer recv.shutdownWg.Done()
		if err := recv.httpServer.Serve(listener); !errors.Is(err, http.ErrServerClosed) && err != nil {
			host.ReportFatalError(err)
		}
	}()
	return nil
}

func (recv *pyroscopeReceiver) Shutdown(ctx context.Context) error {
	if err := recv.httpServer.Shutdown(ctx); err != nil {
		return fmt.Errorf("failed to shutdown: %w", err)
	}
	recv.shutdownWg.Wait()
	return nil
}

func writeResponse(w http.ResponseWriter, contentType string, statusCode int, msg []byte) {
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(statusCode)
	_, _ = w.Write(msg)
}
