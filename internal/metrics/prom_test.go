package metrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alee792/substrate-console/internal/backend"
)

func TestQueryBuildsPromQL(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		got = append(got, q)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(q, "count(") {
			// No UTF-8 names here: classic scrape.
			w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
			return
		}
		w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[
			{"metric":{"ate_template_name":"agent-devbox-v12"},"values":[[1700000000,"395"],[1700000060,"401"]]},
			{"metric":{"ate_template_name":"agent-devbox-v11"},"values":[[1700000000,"440"],[1700000060,"NaN"]]}]}}`))
	}))
	defer srv.Close()

	p := NewPrometheus(srv.URL)
	s, err := p.Query(context.Background(), backend.MetricQuery{Metric: backend.MetricResumeP95, By: "template", Atespace: "triage", Window: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	want := `1000 * histogram_quantile(0.95, sum by (le, ate_template_name) (rate(ate_actor_lifecycle_operation_duration_seconds_bucket{ate_actor_operation_name="resume", ate_template_atespace="triage"}[5m])))`
	if got[len(got)-1] != want {
		t.Errorf("query\n got %s\nwant %s", got[len(got)-1], want)
	}
	if len(s) != 2 || s[0].Label != "agent-devbox-v11" || len(s[0].Points) != 1 || s[1].Points[1].V != 401 {
		t.Errorf("series = %+v", s)
	}
	if _, err := p.Query(context.Background(), backend.MetricQuery{Metric: backend.MetricCapacity}); err != backend.ErrUnsupported {
		t.Errorf("capacity err = %v", err)
	}
}

func TestDottedNames(t *testing.T) {
	sel := dotted.sel(dotted.crashes, `"ate.template.atespace"="triage"`)
	if sel != `{"ate.actor.crashes", "ate.template.atespace"="triage"}` {
		t.Errorf("sel = %s", sel)
	}
}
