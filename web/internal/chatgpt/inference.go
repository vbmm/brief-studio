package chatgpt

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"
)

var modelSlug = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

func (c *Client) Models(ctx context.Context, s *Session) ([]Model, error) {
	ctx, cleanup, err := s.requestContext(ctx)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	token, err := c.accessToken(ctx, s)
	if err != nil {
		return nil, err
	}
	var catalog struct {
		Models []struct {
			Slug        string `json:"slug"`
			DisplayName string `json:"display_name"`
			Visibility  string `json:"visibility"`
		} `json:"models"`
	}
	if err := c.readJSON(ctx, http.MethodGet, modelsURL, token, nil, &catalog); err != nil {
		return nil, publicError(err)
	}

	models := make([]Model, 0)
	visible := make(map[string]bool)
	for _, entry := range catalog.Models {
		slug := entry.Slug
		if entry.Visibility != "list" || !modelSlug.MatchString(slug) || visible[slug] {
			continue
		}
		visible[slug] = true
		name := boundedLabel(entry.DisplayName, 200)
		if name == "" {
			name = slug
		}
		models = append(models, Model{ID: slug, DisplayName: name})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lifetime.Err() != nil || s.tokens.access == "" {
		return nil, ErrSignedOut
	}
	s.models = visible
	return models, nil
}

func (c *Client) Respond(ctx context.Context, s *Session, model, input string) (string, error) {
	ctx, cleanup, err := s.requestContext(ctx)
	if err != nil {
		return "", err
	}
	defer cleanup()
	if strings.TrimSpace(input) == "" || len(input) > 512<<10 {
		return "", ErrIncomplete
	}
	s.mu.Lock()
	eligible := s.models[model]
	canUsePlan := s.tokens.canUsePlan()
	hasToken := s.tokens.access != ""
	s.mu.Unlock()
	if !hasToken {
		return "", ErrSessionExpired
	}
	if !canUsePlan {
		return "", ErrPlanNotGranted
	}
	if !eligible {
		return "", ErrModel
	}
	token, err := c.accessToken(ctx, s)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, streamTimeout)
	defer cancel()

	type message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	payload := struct {
		Model  string    `json:"model"`
		Input  []message `json:"input"`
		Store  bool      `json:"store"`
		Stream bool      `json:"stream"`
	}{Model: model, Input: []message{{Role: "user", Content: input}}, Stream: true}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", ErrIncomplete
	}
	response, err := c.request(ctx, http.MethodPost, responsesURL, token, "application/json", "text/event-stream",
		bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", publicError(readProviderError(response))
	}
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || media != "text/event-stream" {
		return "", ErrIncomplete
	}
	return readStream(response.Body)
}

func readStream(body io.Reader) (string, error) {
	const maxWire = 8 << 20
	const maxEvent = 2 << 20
	const maxText = 1 << 20
	lines := bufio.NewScanner(io.LimitReader(body, maxWire))
	lines.Buffer(make([]byte, 4096), maxEvent)
	lines.Split(scanStreamLine)

	var event, output strings.Builder
	for lines.Scan() {
		line := lines.Text()
		if line != "" {
			if data, ok := strings.CutPrefix(line, "data:"); ok {
				data = strings.TrimPrefix(data, " ")
				if event.Len()+len(data)+1 > maxEvent {
					return "", ErrIncomplete
				}
				event.WriteString(data)
				event.WriteByte('\n')
			}
			continue
		}
		if event.Len() == 0 {
			continue
		}
		frame := strings.TrimSuffix(event.String(), "\n")
		event.Reset()
		if frame == "[DONE]" {
			break // A terminator alone is not evidence of a completed response.
		}
		var data struct {
			Type     string `json:"type"`
			Delta    string `json:"delta"`
			Code     string `json:"code"`
			Response struct {
				Status string `json:"status"`
				Error  struct {
					Code string `json:"code"`
				} `json:"error"`
			} `json:"response"`
		}
		if json.Unmarshal([]byte(frame), &data) != nil {
			return "", ErrIncomplete
		}
		switch data.Type {
		case "response.output_text.delta":
			if output.Len()+len(data.Delta) > maxText {
				return "", ErrIncomplete
			}
			output.WriteString(data.Delta)
		case "response.completed":
			text := strings.TrimSpace(output.String())
			if data.Response.Status != "completed" || text == "" {
				return "", ErrIncomplete
			}
			return text, nil
		case "response.failed", "response.incomplete", "error":
			code := data.Response.Error.Code
			if code == "" {
				code = data.Code
			}
			if code == "subscription_sharing_usage_limit_exceeded" ||
				code == "subscription_sharing_usage_unavailable" ||
				code == "subscription_sharing_user_not_eligible" {
				return "", publicError(&providerError{code: code})
			}
			return "", ErrIncomplete
		}
	}
	if err := lines.Err(); err != nil && !errors.Is(err, bufio.ErrTooLong) {
		return "", transportError(err)
	}
	return "", ErrIncomplete
}

// The SSE format permits LF, CR and CRLF. Preserve empty lines that terminate a frame.
func scanStreamLine(data []byte, atEOF bool) (advance int, token []byte, err error) {
	at := bytes.IndexAny(data, "\r\n")
	if at >= 0 {
		end := at + 1
		if data[at] == '\r' {
			if end == len(data) && !atEOF {
				return 0, nil, nil
			}
			if end < len(data) && data[end] == '\n' {
				end++
			}
		}
		return end, data[:at], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}
