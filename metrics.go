package main

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// metricsJira wraps a jiraAPI implementation and increments the Jira error
// counter on any error returned by the underlying calls.
type metricsJira struct {
	inner   jiraAPI
	metrics *AppMetrics
}

func newMetricsJira(inner jiraAPI, m *AppMetrics) jiraAPI {
	return &metricsJira{inner: inner, metrics: m}
}

func (mj *metricsJira) Search(in JiraSearchInput) ([]JiraIssue, error) {
	res, err := mj.inner.Search(in)
	if err != nil {
		mj.metrics.IncJiraError()
	}
	return res, err
}

func (mj *metricsJira) SearchEpics(projects []string, limit int) ([]JiraIssue, error) {
	res, err := mj.inner.SearchEpics(projects, limit)
	if err != nil {
		mj.metrics.IncJiraError()
	}
	return res, err
}

func (mj *metricsJira) CreateIssue(in CreateIssueInput) (*CreatedIssue, error) {
	res, err := mj.inner.CreateIssue(in)
	if err != nil {
		mj.metrics.IncJiraError()
	}
	return res, err
}

func (mj *metricsJira) AddComment(key, text string) error {
	err := mj.inner.AddComment(key, text)
	if err != nil {
		mj.metrics.IncJiraError()
	}
	return err
}

func (mj *metricsJira) AddLabel(key, label string) error {
	err := mj.inner.AddLabel(key, label)
	if err != nil {
		mj.metrics.IncJiraError()
	}
	return err
}

func (mj *metricsJira) FindAccountID(email string) (string, error) {
	id, err := mj.inner.FindAccountID(email)
	if err != nil {
		mj.metrics.IncJiraError()
	}
	return id, err
}

// AppMetrics tracks simple job latency (sum+count per kind) and Jira error
// totals. It is not a true histogram — we emit sum_ms and count so callers
// can derive a mean; adequate for v1.
type AppMetrics struct {
	mu sync.RWMutex

	// job latency: sum of nanoseconds and count per job kind
	latencyNs    map[string]int64
	latencyCount map[string]int64

	// jira error counter
	jiraErrors int64
}

func NewAppMetrics() *AppMetrics {
	return &AppMetrics{
		latencyNs:    make(map[string]int64),
		latencyCount: make(map[string]int64),
	}
}

// RecordJob records the elapsed duration for a job of the given kind.
func (m *AppMetrics) RecordJob(kind string, d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.latencyNs[kind] += d.Nanoseconds()
	m.latencyCount[kind]++
}

// IncJiraError increments the Jira error counter by one.
func (m *AppMetrics) IncJiraError() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jiraErrors++
}

// WriteMetrics writes Prometheus-style text lines for latency and Jira errors
// to w.
func (m *AppMetrics) WriteMetrics(w io.Writer) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for kind, ns := range m.latencyNs {
		ms := float64(ns) / 1e6
		fmt.Fprintf(w, "job_latency_ms_sum{kind=%q} %.3f\n", kind, ms)
		fmt.Fprintf(w, "job_latency_ms_count{kind=%q} %d\n", kind, m.latencyCount[kind])
	}
	fmt.Fprintf(w, "jira_errors_total %d\n", m.jiraErrors)
}
