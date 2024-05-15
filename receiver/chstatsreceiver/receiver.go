package chstatsreceiver

import (
	"bytes"
	"context"
	"fmt"
	"github.com/ClickHouse/clickhouse-go/v2"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.uber.org/zap"
	"text/template"
	"time"
)

type chReceiver struct {
	cfg       *Config
	db        clickhouse.Conn
	consumer  consumer.Metrics
	templates []*template.Template
	logger    *zap.Logger
	cancel    context.CancelFunc
	ticker    *time.Ticker
}

func (r *chReceiver) Start(ctx context.Context, host component.Host) error {
	opts, err := clickhouse.ParseDSN(r.cfg.DSN)
	if err != nil {
		return err
	}
	db, err := clickhouse.Open(opts)
	if err != nil {
		return err
	}
	r.db = db
	r.templates = make([]*template.Template, len(r.cfg.Queries))
	for i, query := range r.cfg.Queries {
		r.templates[i], err = template.New(fmt.Sprintf("tpl-%d", i)).Parse(query)
		if err != nil {
			return err
		}
	}

	_ctx, cancel := context.WithCancel(ctx)
	r.cancel = cancel

	r.ticker = time.NewTicker(r.cfg.Timeout)

	go r.mainLoop(_ctx)
	return nil
}

func (r *chReceiver) mainLoop(ctx context.Context) error {
	for {
		r.logger.Info("tick start")
		select {
		case <-ctx.Done():
			fmt.Println("tick stop")
			return nil
		case <-r.ticker.C:
			err := r.GetMetrics(ctx)
			if err != nil {
				r.logger.Error("failed to get metrics", zap.Error(err))
			}
		}
		r.logger.Info("tick end")
	}
}

func (r *chReceiver) GetMetrics(ctx context.Context) error {
	for _, tpl := range r.templates {
		err := r.getMetricsTemplate(ctx, tpl)
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *chReceiver) getMetricsTemplate(ctx context.Context, tpl *template.Template) error {
	queryBuf := bytes.Buffer{}
	params := map[string]any{
		"timestamp_ns": time.Now().UnixNano(),
		"timestamp_ms": time.Now().UnixMilli(),
		"timestamp_s":  time.Now().Unix(),
	}
	err := tpl.Execute(&queryBuf, params)
	wrapErr := func(err error) error {
		return fmt.Errorf("failed to execute. Query:%s; error: %w", err)
	}
	if err != nil {
		return wrapErr(err)
	}
	rows, err := r.db.Query(ctx, queryBuf.String())
	if err != nil {
		return wrapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			labels [][]string
			value  float64
		)
		err = rows.Scan(&labels, &value)
		if err != nil {
			return wrapErr(err)
		}
		metrics := pmetric.NewMetrics()
		res := metrics.ResourceMetrics().AppendEmpty()
		res.Resource().Attributes()
		metric := res.ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
		data := metric.SetEmptyGauge().DataPoints().AppendEmpty()
		for _, label := range labels {
			if label[0] == "__name__" {
				metric.SetName(label[1])
				continue
			}
			data.Attributes().PutStr(label[0], label[1])
		}
		data.SetTimestamp(pcommon.NewTimestampFromTime(time.Now()))
		data.SetDoubleValue(value)
		select {
		case <-ctx.Done():
			return nil
		default:
			err = r.consumer.ConsumeMetrics(ctx, metrics)
			if err != nil {
				return wrapErr(err)
			}
		}
	}
	return nil
}

func (r *chReceiver) Shutdown(ctx context.Context) error {
	fmt.Println("shutting down")
	r.cancel()
	r.ticker.Stop()
	r.db.Close()
	return nil
}
