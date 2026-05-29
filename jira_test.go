package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJira_Search(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/search" {
			t.Errorf("path: %s", r.URL.Path)
		}
		if r.Method != "POST" {
			t.Errorf("method: %s", r.Method)
		}
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(b, &body)
		jql, _ := body["jql"].(string)
		if !strings.Contains(jql, "project in (QORK)") {
			t.Errorf("jql missing project filter: %s", jql)
		}
		if !strings.Contains(jql, `labels = "qompass"`) || !strings.Contains(jql, "labels is EMPTY") {
			t.Errorf("jql missing label filter: %s", jql)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"issues":[{"key":"QORK-1","fields":{"summary":"foo","description":"bar"}}]}`))
	}))
	defer srv.Close()

	c := &JiraClient{BaseURL: srv.URL, Email: "u", Token: "t"}
	res, err := c.Search(JiraSearchInput{
		Projects:   []string{"QORK"},
		Subproject: "qompass",
		Keywords:   []string{"foo", "bar"},
		Limit:      20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Key != "QORK-1" {
		t.Fatalf("mismatch: %+v", res)
	}
}

func TestJira_Search_NoSubproject_OnlyUnlabeled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(b, &body)
		jql, _ := body["jql"].(string)
		if !strings.Contains(jql, "labels is EMPTY") {
			t.Errorf("expected empty-labels filter, got: %s", jql)
		}
		if strings.Contains(jql, `labels = "`) {
			t.Errorf("should not include label= filter when subproject empty: %s", jql)
		}
		w.Write([]byte(`{"issues":[]}`))
	}))
	defer srv.Close()
	c := &JiraClient{BaseURL: srv.URL, Email: "u", Token: "t"}
	_, err := c.Search(JiraSearchInput{Projects: []string{"QORK"}, Keywords: []string{"x"}, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
}
