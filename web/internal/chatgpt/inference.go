package chatgpt

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
		return nil, c.resourceError(s, token, err)
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
		return "", c.resourceError(s, token, readProviderError(response))
	}
	return readResponsesBody(response.Body, response.Header.Get("Content-Type"))
}

const (
	maxStreamText = 1 << 20
	maxWire       = 8 << 20
)

func streamFailure(detail string) error {
	return fmt.Errorf("%w %s", ErrIncomplete, detail)
}

func readResponsesBody(body io.Reader, contentType string) (string, error) {
	media, _, _ := mime.ParseMediaType(contentType)
	switch media {
	case "text/event-stream":
		return readStream(body)
	case "application/json":
		return readCompletedJSON(body)
	case "text/html", "application/xhtml+xml":
		return "", streamFailure("OpenAI a renvoyé une page HTML.")
	}

	// An absent or generic header can conceal a real SSE stream. Only attempt
	// recovery for a recognizable SSE field and still require response.completed.
	buffer := bufio.NewReader(body)
	prefix, err := buffer.Peek(16)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", transportError(err)
	}
	prefix = bytes.TrimLeft(bytes.TrimPrefix(prefix, []byte("\xef\xbb\xbf")), " \r\n\t")
	for _, field := range []string{"data:", "event:", ":", "id:", "retry:"} {
		if bytes.HasPrefix(prefix, []byte(field)) {
			return readStream(buffer)
		}
	}
	if len(prefix) == 0 {
		return "", streamFailure("OpenAI a renvoyé une réponse vide.")
	}
	return "", streamFailure("OpenAI a renvoyé un format inattendu.")
}

type completedResponse struct {
	Status string          `json:"status"`
	Output json.RawMessage `json:"output"`
}

func readCompletedJSON(body io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxWire+1))
	if err != nil {
		return "", transportError(err)
	}
	var result struct {
		completedResponse
		Object            string           `json:"object"`
		Error             *json.RawMessage `json:"error"`
		IncompleteDetails *json.RawMessage `json:"incomplete_details"`
	}
	if len(data) > maxWire || json.Unmarshal(data, &result) != nil {
		return "", streamFailure("Le résultat JSON d’OpenAI est illisible ou trop long.")
	}
	if result.Object != "response" {
		return "", streamFailure("Le JSON d’OpenAI ne contient pas d’objet Responses.")
	}
	if result.Status != "completed" || result.Error != nil || result.IncompleteDetails != nil {
		return "", streamFailure("Le JSON d’OpenAI ne confirme pas la fin de la réponse.")
	}
	return completedOutputText(result.Output, true)
}

func readStream(body io.Reader) (string, error) {
	const maxEvent = 2 << 20
	lines := bufio.NewScanner(io.LimitReader(body, maxWire))
	lines.Buffer(make([]byte, 4096), maxEvent)
	lines.Split(scanStreamLine)

	var event, deltas, finalized strings.Builder
	first := true
	for lines.Scan() {
		line := lines.Text()
		if first {
			line = strings.TrimPrefix(line, "\xef\xbb\xbf")
			first = false
		}
		if line != "" {
			if data, ok := strings.CutPrefix(line, "data:"); ok {
				data = strings.TrimPrefix(data, " ")
				if event.Len()+len(data)+1 > maxEvent {
					return "", streamFailure("Un événement du flux dépasse la taille autorisée.")
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
		payload := []byte(frame)
		var header struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(payload, &header) != nil || header.Type == "" {
			return "", streamFailure("Un événement du flux est illisible.")
		}
		switch header.Type {
		case "response.output_text.delta", "response.output_text.done":
			var data struct {
				Delta *string `json:"delta"`
				Text  *string `json:"text"`
			}
			if json.Unmarshal(payload, &data) != nil {
				return "", streamFailure("Le texte d’un événement du flux est illisible.")
			}
			part, target := data.Delta, &deltas
			if header.Type == "response.output_text.done" {
				part, target = data.Text, &finalized
			}
			if part == nil || target.Len()+len(*part) > maxStreamText {
				return "", streamFailure("Le texte du flux est absent ou trop long.")
			}
			target.WriteString(*part)
		case "response.completed":
			return completedStreamText(payload, finalized.String(), deltas.String())
		case "response.refusal.delta", "response.refusal.done":
			return "", streamFailure("Le modèle a refusé la demande.")
		case "response.failed", "response.incomplete", "error":
			return "", terminalStreamFailure(payload, header.Type)
		}
	}
	if err := lines.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return "", streamFailure("Un événement du flux dépasse la taille autorisée.")
		}
		return "", transportError(err)
	}
	return "", streamFailure("Le flux s’est arrêté avant la confirmation de fin.")
}

func completedStreamText(payload []byte, finalized, deltas string) (string, error) {
	var terminal struct {
		Response completedResponse `json:"response"`
	}
	if json.Unmarshal(payload, &terminal) != nil || terminal.Response.Status != "completed" {
		return "", streamFailure("OpenAI n’a pas confirmé la fin de la réponse.")
	}
	if terminal.Response.Output != nil {
		return completedOutputText(terminal.Response.Output, false)
	}
	text := strings.TrimSpace(finalized)
	if text == "" {
		text = strings.TrimSpace(deltas)
	}
	if text == "" {
		return "", streamFailure("La réponse confirmée ne contient aucun texte.")
	}
	return text, nil
}

func completedOutputText(raw json.RawMessage, requireMessageStatus bool) (string, error) {
	// Only read final assistant text from the authoritative output.
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil || items == nil {
		return "", streamFailure("Le résultat confirmé est illisible.")
	}
	var output strings.Builder
	for _, raw := range items {
		var item struct {
			Type   string `json:"type"`
			Role   string `json:"role"`
			Status string `json:"status"`
		}
		if json.Unmarshal(raw, &item) != nil {
			return "", streamFailure("Le résultat confirmé est illisible.")
		}
		if item.Type != "message" || item.Role != "assistant" {
			continue
		}
		var message struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if (item.Status != "completed" && (requireMessageStatus || item.Status != "")) || json.Unmarshal(raw, &message) != nil {
			return "", streamFailure("Le message confirmé est incomplet ou illisible.")
		}
		for _, content := range message.Content {
			switch content.Type {
			case "refusal":
				return "", streamFailure("Le modèle a refusé la demande.")
			case "output_text":
				if output.Len()+len(content.Text) > maxStreamText {
					return "", streamFailure("Le texte confirmé dépasse la taille autorisée.")
				}
				output.WriteString(content.Text)
			}
		}
	}
	text := strings.TrimSpace(output.String())
	if text == "" {
		return "", streamFailure("La réponse confirmée ne contient aucun texte.")
	}
	return text, nil
}

func terminalStreamFailure(payload []byte, eventType string) error {
	var data struct {
		Code     string `json:"code"`
		Response struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
			IncompleteDetails struct {
				Reason string `json:"reason"`
			} `json:"incomplete_details"`
		} `json:"response"`
	}
	if json.Unmarshal(payload, &data) != nil {
		return streamFailure("L’événement d’erreur du flux est illisible.")
	}
	code := data.Response.Error.Code
	if code == "" {
		code = data.Code
	}
	switch code {
	case "subscription_sharing_usage_limit_exceeded", "subscription_sharing_usage_unavailable", "subscription_sharing_user_not_eligible":
		return publicError(&providerError{code: code})
	}
	if eventType == "response.incomplete" {
		switch data.Response.IncompleteDetails.Reason {
		case "max_output_tokens":
			return streamFailure("OpenAI a atteint la limite de sortie.")
		case "content_filter":
			return streamFailure("OpenAI a interrompu la sortie pour filtrage de contenu.")
		default:
			return streamFailure("OpenAI a signalé une génération inachevée.")
		}
	}
	return streamFailure("OpenAI a signalé l’échec de la génération.")
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
