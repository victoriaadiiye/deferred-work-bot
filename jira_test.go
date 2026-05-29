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

func TestJira_CreateIssue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/issue" || r.Method != "POST" {
			t.Fatalf("unexpected: %s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		var body struct {
			Fields struct {
				Project struct {
					Key string `json:"key"`
				} `json:"project"`
				Summary   string `json:"summary"`
				IssueType struct {
					Name string `json:"name"`
				} `json:"issuetype"`
				Labels []string `json:"labels"`
			} `json:"fields"`
		}
		json.Unmarshal(b, &body)
		if body.Fields.Project.Key != "QORK" {
			t.Errorf("project: %s", body.Fields.Project.Key)
		}
		if body.Fields.IssueType.Name != "Task" {
			t.Errorf("type: %s", body.Fields.IssueType.Name)
		}
		if body.Fields.Summary != "do the thing" {
			t.Errorf("summary: %s", body.Fields.Summary)
		}
		w.WriteHeader(201)
		w.Write([]byte(`{"key":"QORK-99","self":"https://example/rest/api/3/issue/QORK-99"}`))
	}))
	defer srv.Close()
	c := &JiraClient{BaseURL: srv.URL, Email: "u", Token: "t", HTTP: http.DefaultClient}
	res, err := c.CreateIssue(CreateIssueInput{
		ProjectKey:  "QORK",
		Summary:     "do the thing",
		Description: "details",
		IssueType:   "Task",
		Labels:      []string{"deferred-work", "qompass"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Key != "QORK-99" {
		t.Fatalf("key: %s", res.Key)
	}
	if !strings.Contains(res.URL, "/browse/QORK-99") {
		t.Fatalf("browse url: %s", res.URL)
	}
}

func TestJira_AddComment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/issue/QORK-5/comment" || r.Method != "POST" {
			t.Fatalf("unexpected: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(201)
		w.Write([]byte(`{"id":"1"}`))
	}))
	defer srv.Close()
	c := &JiraClient{BaseURL: srv.URL, Email: "u", Token: "t", HTTP: http.DefaultClient}
	if err := c.AddComment("QORK-5", "follow-up: stuff"); err != nil {
		t.Fatal(err)
	}
}

func TestJira_AddLabel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/issue/QORK-5" || r.Method != "PUT" {
			t.Fatalf("unexpected: %s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(b), `"add":"deferred-work-followup"`) {
			t.Errorf("missing add op: %s", string(b))
		}
		w.WriteHeader(204)
	}))
	defer srv.Close()
	c := &JiraClient{BaseURL: srv.URL, Email: "u", Token: "t", HTTP: http.DefaultClient}
	if err := c.AddLabel("QORK-5", "deferred-work-followup"); err != nil {
		t.Fatal(err)
	}
}
