// Package metrics answers the console's metric queries from Prometheus,
// using the instruments in upstream's docs/metrics/registry/metrics.yaml.
//
// Substrate's instruments have OpenTelemetry names (ate.actor.crashes). How
// they appear in Prometheus depends on the backend: Google Managed Prometheus
// and Prometheus 3 with UTF-8 names keep the dots; a classic scrape of the
// components' /metrics endpoints turns them into underscores and adds unit
// suffixes. The querier detects which form the server holds on first use.
package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alee792/substrate-console/internal/backend"
	"github.com/alee792/substrate-console/internal/model"
)

// Prometheus queries a Prometheus-compatible HTTP API.
type Prometheus struct {
	base   string
	client *http.Client

	once sync.Once
	utf8 bool
}

var _ backend.Metrics = (*Prometheus)(nil)

// NewPrometheus returns a querier for the Prometheus at base, e.g.
// http://localhost:9090.
func NewPrometheus(base string) *Prometheus {
	return &Prometheus{base: strings.TrimRight(base, "/"), client: &http.Client{Timeout: 15 * time.Second}}
}

// names holds one spelling of the instruments and labels we use.
type names struct {
	lifecycleBucket, lifecycleCount, crashes string
	op, tmplName, tmplSpace, pool, errType   string
}

var (
	underscore = names{
		lifecycleBucket: "ate_actor_lifecycle_operation_duration_seconds_bucket",
		lifecycleCount:  "ate_actor_lifecycle_operation_duration_seconds_count",
		crashes:         "ate_actor_crashes_total",
		op:              "ate_actor_operation_name", tmplName: "ate_template_name", tmplSpace: "ate_template_atespace",
		pool: "ate_workerpool_name", errType: "error_type",
	}
	dotted = names{
		lifecycleBucket: `"ate.actor.lifecycle.operation.duration_bucket"`,
		lifecycleCount:  `"ate.actor.lifecycle.operation.duration_count"`,
		crashes:         `"ate.actor.crashes"`,
		op:              `"ate.actor.operation.name"`, tmplName: `"ate.template.name"`, tmplSpace: `"ate.template.atespace"`,
		pool: `"ate.workerpool.name"`, errType: `"error.type"`,
	}
)

func (p *Prometheus) names(ctx context.Context) names {
	p.once.Do(func() {
		res, err := p.instant(ctx, `count({"ate.actor.lifecycle.operation.duration_count"})`)
		p.utf8 = err == nil && len(res) > 0
	})
	if p.utf8 {
		return dotted
	}
	return underscore
}

// sel builds a series selector. With UTF-8 names the metric name moves
// inside the braces.
func (n names) sel(metric string, matchers ...string) string {
	if strings.HasPrefix(metric, `"`) {
		return "{" + strings.Join(append([]string{metric}, matchers...), ", ") + "}"
	}
	return metric + "{" + strings.Join(matchers, ", ") + "}"
}

func (n names) groupLabel(by string) string {
	switch by {
	case "template":
		return n.tmplName
	case "pool":
		return n.pool
	}
	return ""
}

// Query implements backend.Metrics.
func (p *Prometheus) Query(ctx context.Context, q backend.MetricQuery) ([]model.Series, error) {
	n := p.names(ctx)
	var m []string
	if q.Atespace != "" {
		m = append(m, fmt.Sprintf(`%s=%q`, n.tmplSpace, q.Atespace))
	}
	by := n.groupLabel(q.By)
	byClause := func(extra string) string {
		labels := []string{}
		if extra != "" {
			labels = append(labels, extra)
		}
		if by != "" {
			labels = append(labels, by)
		}
		if len(labels) == 0 {
			return ""
		}
		return " by (" + strings.Join(labels, ", ") + ")"
	}
	resume := append([]string{fmt.Sprintf(`%s="resume"`, n.op)}, m...)
	var expr string
	switch q.Metric {
	case backend.MetricResumeP95:
		// Failed resumes share the histogram and are rejected in a few
		// milliseconds, so leave them out or a full pool reads as fast.
		ok := append(append([]string{}, resume...), fmt.Sprintf(`%s=""`, n.errType))
		expr = fmt.Sprintf(`1000 * histogram_quantile(0.95, sum%s (rate(%s[5m])))`, byClause("le"), n.sel(n.lifecycleBucket, ok...))
	case backend.MetricResumeFailures:
		failed := append(append([]string{}, resume...), fmt.Sprintf(`%s="ResourceExhausted"`, n.errType))
		expr = fmt.Sprintf(`100 * (sum%s (rate(%s[5m])) or vector(0)) / sum%s (rate(%s[5m]))`,
			byClause(""), n.sel(n.lifecycleCount, failed...), byClause(""), n.sel(n.lifecycleCount, resume...))
	case backend.MetricCrashes:
		expr = fmt.Sprintf(`sum%s (increase(%s[30m]))`, byClause(""), n.sel(n.crashes, m...))
	default:
		// Capacity and running actors come from the API: kind's Prometheus
		// doesn't collect the worker-pool or ateworker instruments.
		return nil, backend.ErrUnsupported
	}
	if q.Window <= 0 {
		q.Window = 6 * time.Hour
	}
	if q.Step <= 0 {
		q.Step = q.Window / 60
	}
	end := time.Now().Add(-q.Offset)
	return p.rangeQuery(ctx, expr, end.Add(-q.Window), end, q.Step, by)
}

type promResp struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		Result []struct {
			Metric map[string]string `json:"metric"`
			Values [][2]any          `json:"values"`
			Value  [2]any            `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

func (p *Prometheus) get(ctx context.Context, path string, v url.Values) (*promResp, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base+path+"?"+v.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query Prometheus: %w", err)
	}
	defer resp.Body.Close()
	var out promResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode Prometheus response (HTTP %d): %w", resp.StatusCode, err)
	}
	if out.Status != "success" {
		return nil, fmt.Errorf("prometheus: %s", out.Error)
	}
	return &out, nil
}

func (p *Prometheus) instant(ctx context.Context, expr string) ([]float64, error) {
	r, err := p.get(ctx, "/api/v1/query", url.Values{"query": {expr}})
	if err != nil {
		return nil, err
	}
	var out []float64
	for _, s := range r.Data.Result {
		if f, ok := parse(s.Value[1]); ok {
			out = append(out, f)
		}
	}
	return out, nil
}

func (p *Prometheus) rangeQuery(ctx context.Context, expr string, start, end time.Time, step time.Duration, by string) ([]model.Series, error) {
	r, err := p.get(ctx, "/api/v1/query_range", url.Values{
		"query": {expr}, "start": {ts(start)}, "end": {ts(end)}, "step": {strconv.Itoa(int(max(step, time.Second).Seconds()))},
	})
	if err != nil {
		return nil, err
	}
	label := strings.Trim(by, `"`)
	var out []model.Series
	for _, s := range r.Data.Result {
		sr := model.Series{Label: s.Metric[label]}
		if sr.Label == "" {
			sr.Label = "all"
		}
		for _, v := range s.Values {
			t, ok1 := parse(v[0])
			f, ok2 := parse(v[1])
			if ok1 && ok2 {
				sr.Points = append(sr.Points, model.Point{T: time.Unix(0, int64(t*1e9)), V: f})
			}
		}
		out = append(out, sr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out, nil
}

func ts(t time.Time) string { return strconv.FormatFloat(float64(t.UnixNano())/1e9, 'f', 3, 64) }

func parse(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil && f == f // drop NaN
	}
	return 0, false
}
