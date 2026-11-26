package chatgpt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

const maxAccountBody = 2 << 20

// providerError carries only bounded, recognized classification internally.
// Never forward OAuth response messages or error descriptions to the browser.
type providerError struct {
	status int
	code   string
}

func (e *providerError) Error() string {
	return fmt.Sprintf("Erreur OpenAI (HTTP %d).", e.status)
}

func publicError(err error) error {
	var response *providerError
	if !errors.As(err, &response) {
		return err
	}
	switch {
	case response.code == "subscription_sharing_user_not_eligible":
		return ErrPlanNotGranted
	case response.code == "subscription_sharing_usage_limit_exceeded", response.status == http.StatusTooManyRequests:
		return ErrPlanLimit
	case response.code == "subscription_sharing_usage_unavailable", response.status == http.StatusServiceUnavailable:
		return ErrNetwork
	case response.status == http.StatusUnauthorized, response.code == "invalid_grant":
		return ErrSessionExpired
	}
	return response
}

func transportError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return ErrTimeout
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return ErrTimeout
	}
	return ErrNetwork
}

func (c *Client) request(
	ctx context.Context, method, target, token, contentType, accept string, body io.Reader,
) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, ErrNetwork
	}
	req.Header.Set("Accept", accept)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := c.http.Do(req)
	if err != nil {
		return nil, transportError(err)
	}
	return response, nil
}

func (c *Client) readJSON(
	ctx context.Context, method, target, token string, form url.Values, output any,
) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	var body io.Reader
	contentType := ""
	if form != nil {
		body = strings.NewReader(form.Encode())
		contentType = "application/x-www-form-urlencoded"
	}
	response, err := c.request(ctx, method, target, token, contentType, "application/json", body)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return readProviderError(response)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxAccountBody+1))
	if err != nil {
		return transportError(err)
	}
	if len(data) > maxAccountBody || json.Unmarshal(data, output) != nil {
		return ErrIncomplete
	}
	return nil
}

func readProviderError(response *http.Response) error {
	result := &providerError{status: response.StatusCode}
	const maxErrorBody = 32 << 10
	data, err := io.ReadAll(io.LimitReader(response.Body, maxErrorBody+1))
	if err != nil || len(data) > maxErrorBody {
		return result
	}
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &envelope) == nil {
		if json.Unmarshal(envelope.Error, &result.code) != nil {
			var detail struct {
				Code string `json:"code"`
			}
			if json.Unmarshal(envelope.Error, &detail) == nil {
				result.code = detail.Code
			}
		}
	}
	return result
}
