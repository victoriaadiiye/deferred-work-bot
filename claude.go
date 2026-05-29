package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

type ClaudeRunner struct {
	Bin     string        // path to `claude` CLI (default: "claude")
	Timeout time.Duration // default 5min
}

func NewClaudeRunner() *ClaudeRunner {
	return &ClaudeRunner{Bin: "claude", Timeout: 5 * time.Minute}
}

// Run feeds the prompt on stdin to `claude -p --output-format text` and
// returns stdout. Errors include stderr for diagnosis.
func (r *ClaudeRunner) Run(ctx context.Context, prompt string) (string, error) {
	bin := r.Bin
	if bin == "" {
		bin = "claude"
	}
	to := r.Timeout
	if to == 0 {
		to = 5 * time.Minute
	}
	cctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()

	var args []string
	if strings.HasSuffix(bin, "claude") {
		args = append(args, "-p", "--output-format", "text")
	} else {
		// Non-claude binary (e.g. /bin/echo in tests): pass prompt as argument.
		args = append(args, prompt)
	}
	cmd := exec.CommandContext(cctx, bin, args...)
	cmd.Stdin = strings.NewReader(prompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: %w (stderr: %s)", bin, err, stderr.String())
	}
	return stdout.String(), nil
}

var (
	reJSONFence  = regexp.MustCompile("(?s)```(?:json)?\\s*(\\{.*?\\}|\\[.*?\\])\\s*```")
	reJSONObject = regexp.MustCompile(`(?s)(\{.*\}|\[.*\])`)
)

// ExtractJSON finds the first JSON object or array in text, stripped of fences.
func ExtractJSON(text string) (string, error) {
	if m := reJSONFence.FindStringSubmatch(text); len(m) == 2 {
		return strings.TrimSpace(m[1]), nil
	}
	if m := reJSONObject.FindString(text); m != "" {
		return strings.TrimSpace(m), nil
	}
	return "", errors.New("no JSON object or array found in claude output")
}
