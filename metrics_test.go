package main

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMetrics_JobLatencyAndJiraErrors(t *testing.T) {
	m := NewAppMetrics()

	m.RecordJob("propose", 150*time.Millisecond)
	m.RecordJob("propose", 50*time.Millisecond)
	m.RecordJob("file", 200*time.Millisecond)
	m.IncJiraError()
	m.IncJiraError()
	m.IncJiraError()

	var sb strings.Builder
	m.WriteMetrics(&sb)
	out := sb.String()

	// propose: 200ms total, count 2
	if !strings.Contains(out, `job_latency_ms_sum{kind="propose"}`) {
		t.Fatalf("missing propose sum: %s", out)
	}
	if !strings.Contains(out, `job_latency_ms_count{kind="propose"} 2`) {
		t.Fatalf("missing propose count: %s", out)
	}
	// file: 200ms total, count 1
	if !strings.Contains(out, `job_latency_ms_sum{kind="file"}`) {
		t.Fatalf("missing file sum: %s", out)
	}
	if !strings.Contains(out, `job_latency_ms_count{kind="file"} 1`) {
		t.Fatalf("missing file count: %s", out)
	}
	// jira errors
	if !strings.Contains(out, "jira_errors_total 3") {
		t.Fatalf("missing jira_errors_total: %s", out)
	}
}

func TestMetrics_MetricsEndpointEmitsLatencyAndJiraErrors(t *testing.T) {
	store := newTestStore(t)
	w := &Worker{queue: make(chan job, 1)}
	m := NewAppMetrics()
	m.RecordJob("propose", 100*time.Millisecond)
	m.IncJiraError()

	srv := NewHealthServer(HealthDeps{Store: store, Worker: w, Metrics: m})
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()

	if !strings.Contains(body, `job_latency_ms_sum{kind="propose"}`) {
		t.Fatalf("missing latency from /metrics: %s", body)
	}
	if !strings.Contains(body, "jira_errors_total 1") {
		t.Fatalf("missing jira_errors_total from /metrics: %s", body)
	}
}

func TestMetricsJira_IncrementsOnError(t *testing.T) {
	m := NewAppMetrics()
	inner := &fakeJira{failSearch: true}
	mj := newMetricsJira(inner, m)

	_, err := mj.Search(JiraSearchInput{})
	if err == nil {
		t.Fatal("expected error from fakeJira.Search with failSearch=true")
	}

	var sb strings.Builder
	m.WriteMetrics(&sb)
	if !strings.Contains(sb.String(), "jira_errors_total 1") {
		t.Fatalf("expected jira_errors_total 1: %s", sb.String())
	}
}
