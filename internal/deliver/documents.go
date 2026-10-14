package deliver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Documents fetches what a provider has sent for a pended request since it was answered: the documents and
// questionnaire responses the PAS server took through Da Vinci CDex $submit-attachment. PAS has no standard way for a
// decision service to read them, so this reads Perfuse's own API (GET /api/pas/cases/{id}), with the same token as
// Claim/$decide, and returns each as a Bundle entry.
//
// API is the PAS server's API base; empty derives it from Base, the FHIR base, by replacing a final /fhir with /api.
func (c *Client) Documents(ctx context.Context, request string) ([]map[string]any, error) {
	api := c.API
	if api == "" {
		base := strings.TrimSuffix(c.Base, "/")
		if !strings.HasSuffix(base, "/fhir") {
			return nil, errors.New("the PAS server's API is not known: pass -pas-api-url")
		}
		api = strings.TrimSuffix(base, "/fhir") + "/api"
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(api, "/")+"/pas/cases/"+url.PathEscape(request), nil)
	if err != nil {
		return nil, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	req.Header.Set("Accept", "application/json")
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the PAS server answered %s: %s", resp.Status, strings.TrimSpace(string(raw[:min(len(raw), 300)])))
	}
	var pc struct {
		Attachments []struct {
			ID           int64          `json:"id"`
			ResourceType string         `json:"resourceType"`
			Content      map[string]any `json:"content"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal(raw, &pc); err != nil {
		return nil, fmt.Errorf("the PAS server's answer is not a case: %w", err)
	}
	var out []map[string]any
	for _, a := range pc.Attachments {
		if a.Content == nil {
			continue
		}
		id := fmt.Sprint(a.Content["id"])
		if id == "" || id == "<nil>" {
			id = fmt.Sprint(a.ID)
			a.Content["id"] = id
		}
		out = append(out, map[string]any{"fullUrl": fmt.Sprintf("urn:pas-attachment:%s:%d", request, a.ID), "resource": a.Content})
	}
	return out, nil
}
