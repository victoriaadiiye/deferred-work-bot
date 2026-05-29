package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type JiraClient struct {
	BaseURL string
	Email   string
	Token   string
	HTTP    *http.Client
}

func NewJiraClient(baseURL, email, token string) *JiraClient {
	return &JiraClient{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Email:   email,
		Token:   token,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

type JiraSearchInput struct {
	Projects   []string
	Subproject string
	Keywords   []string
	Limit      int
}

type JiraIssue struct {
	Key    string `json:"key"`
	Fields struct {
		Summary     string   `json:"summary"`
		Description any      `json:"description"`
		Labels      []string `json:"labels"`
		Status      struct {
			Name string `json:"name"`
		} `json:"status"`
	} `json:"fields"`
}

func (c *JiraClient) BuildJQL(in JiraSearchInput) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("project in (%s)", strings.Join(in.Projects, ",")))
	parts = append(parts, "(statusCategory != Done OR resolved > -90d)")
	if in.Subproject != "" {
		parts = append(parts, fmt.Sprintf(`(labels = "%s" OR labels is EMPTY)`, in.Subproject))
	} else {
		parts = append(parts, "labels is EMPTY")
	}
	if len(in.Keywords) > 0 {
		quoted := make([]string, 0, len(in.Keywords))
		for _, k := range in.Keywords {
			k = strings.ReplaceAll(k, `"`, `\"`)
			quoted = append(quoted, fmt.Sprintf(`"%s"`, k))
		}
		parts = append(parts, fmt.Sprintf("text ~ (%s)", strings.Join(quoted, " OR ")))
	}
	return strings.Join(parts, " AND ") + " ORDER BY updated DESC"
}

func (c *JiraClient) Search(in JiraSearchInput) ([]JiraIssue, error) {
	if in.Limit == 0 {
		in.Limit = 20
	}
	body, _ := json.Marshal(map[string]any{
		"jql":        c.BuildJQL(in),
		"maxResults": in.Limit,
		"fields":     []string{"summary", "description", "labels", "status"},
	})
	req, _ := http.NewRequest("POST", c.BaseURL+"/rest/api/3/search", bytes.NewReader(body))
	req.SetBasicAuth(c.Email, c.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("jira search %d: %s", resp.StatusCode, string(b))
	}
	var out struct {
		Issues []JiraIssue `json:"issues"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Issues, nil
}

type CreateIssueInput struct {
	ProjectKey  string
	Summary     string
	Description string
	IssueType   string
	Labels      []string
	Priority    string
}

type CreatedIssue struct {
	Key string
	URL string
}

func (c *JiraClient) CreateIssue(in CreateIssueInput) (*CreatedIssue, error) {
	fields := map[string]any{
		"project":   map[string]any{"key": in.ProjectKey},
		"summary":   in.Summary,
		"issuetype": map[string]any{"name": in.IssueType},
		"labels":    in.Labels,
	}
	if in.Description != "" {
		fields["description"] = adfFromText(in.Description)
	}
	if in.Priority != "" {
		fields["priority"] = map[string]any{"name": in.Priority}
	}
	body, _ := json.Marshal(map[string]any{"fields": fields})
	req, _ := http.NewRequest("POST", c.BaseURL+"/rest/api/3/issue", bytes.NewReader(body))
	req.SetBasicAuth(c.Email, c.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("create issue %d: %s", resp.StatusCode, string(b))
	}
	var out struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &CreatedIssue{Key: out.Key, URL: c.BaseURL + "/browse/" + out.Key}, nil
}

func (c *JiraClient) AddComment(issueKey, text string) error {
	body, _ := json.Marshal(map[string]any{"body": adfFromText(text)})
	req, _ := http.NewRequest("POST", c.BaseURL+"/rest/api/3/issue/"+issueKey+"/comment", bytes.NewReader(body))
	req.SetBasicAuth(c.Email, c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("add comment %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

func (c *JiraClient) AddLabel(issueKey, label string) error {
	body, _ := json.Marshal(map[string]any{
		"update": map[string]any{
			"labels": []map[string]any{{"add": label}},
		},
	})
	req, _ := http.NewRequest("PUT", c.BaseURL+"/rest/api/3/issue/"+issueKey, bytes.NewReader(body))
	req.SetBasicAuth(c.Email, c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("add label %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// adfFromText wraps a plain-text string in Atlassian Document Format, which
// the Jira Cloud v3 API requires for description and comment bodies.
func adfFromText(s string) map[string]any {
	return map[string]any{
		"type":    "doc",
		"version": 1,
		"content": []map[string]any{
			{
				"type": "paragraph",
				"content": []map[string]any{
					{"type": "text", "text": s},
				},
			},
		},
	}
}
