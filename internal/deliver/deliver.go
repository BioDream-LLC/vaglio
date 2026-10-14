// Package deliver sends a reviewer's decision on a pended case to the PAS server that asked, as Claim/$decide.
//
// The PAS server gave the request's id with it (X-PAS-Request-Id); the decision goes back under that id, for the one
// item the case was about, with the reviewer's reason and NPI. Delivery is at least once: a review is written to the audit
// log before anything is sent, a failure to reach the server is retried until it succeeds, and a refusal - the server has
// no such request, or someone there decided it first - is recorded as final, for a person to look at, rather than
// retried for ever.
package deliver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/biodream-llc/vaglio/internal/cases"
)

// Client sends reviews to one PAS server.
type Client struct {
	Base  string // the PAS server's FHIR base, e.g. http://127.0.0.1:8080/fhir
	API   string // the PAS server's own API base, for documents sent later; empty derives it from Base
	Token string
	HTTP  *http.Client
}

// Error is a delivery that failed. Final says retrying would get the same answer.
type Error struct {
	Msg   string
	Final bool
}

func (e *Error) Error() string { return e.Msg }

// Send delivers one reviewed case.
func (c *Client) Send(ctx context.Context, cs cases.Case) error {
	if cs.Review == nil || cs.Request == "" {
		return &Error{Msg: "the case has no review, or the PAS server gave no request id to send it under", Final: true}
	}
	item, err := strconv.Atoi(cs.Item)
	if err != nil {
		return &Error{Msg: fmt.Sprintf("item %q is not a sequence number", cs.Item), Final: true}
	}
	params := []map[string]any{
		{"name": "claimResponse", "valueString": cs.Request},
		{"name": "decision", "valueCode": cs.Review.Decision},
		{"name": "reason", "valueString": cs.Review.Reason},
		{"name": "item", "valueInteger": item},
	}
	if cs.Review.ReviewerNPI != "" {
		params = append(params, map[string]any{"name": "reviewer", "valueString": cs.Review.ReviewerNPI})
	}
	body, _ := json.Marshal(map[string]any{"resourceType": "Parameters", "parameter": params})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(c.Base, "/")+"/Claim/$decide", bytes.NewReader(body))
	if err != nil {
		return &Error{Msg: err.Error(), Final: true}
	}
	req.Header.Set("Content-Type", "application/fhir+json")
	req.Header.Set("Accept", "application/fhir+json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return &Error{Msg: "the PAS server could not be reached: " + err.Error()}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	msg := fmt.Sprintf("the PAS server answered %d: %s", resp.StatusCode, outcome(raw))
	switch resp.StatusCode {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity:
		return &Error{Msg: msg, Final: true}
	}
	return &Error{Msg: msg} // 401, 403, 5xx: a configuration or a server to fix; the review is kept and retried
}

// outcome is the diagnostics of an OperationOutcome, or the start of whatever else came back.
func outcome(raw []byte) string {
	var oo struct {
		Issue []struct {
			Diagnostics string `json:"diagnostics"`
		} `json:"issue"`
	}
	if json.Unmarshal(raw, &oo) == nil && len(oo.Issue) > 0 && oo.Issue[0].Diagnostics != "" {
		return oo.Issue[0].Diagnostics
	}
	s := strings.TrimSpace(string(raw))
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}

// Deliver sends one case and records the attempt.
func (c *Client) Deliver(ctx context.Context, store *cases.Store, cs cases.Case) {
	err := c.Send(ctx, cs)
	final := false
	if e, ok := err.(*Error); ok {
		final = e.Final
	}
	if err != nil {
		log.Printf("deliver: case %s: %v", cs.ID, err)
	}
	if rerr := store.Delivered(cs.ID, err, final); rerr != nil {
		log.Printf("deliver: case %s: recording the attempt: %v", cs.ID, rerr)
	}
}

// Run retries undelivered reviews every interval until ctx ends.
func (c *Client) Run(ctx context.Context, store *cases.Store, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		for _, cs := range store.Undelivered() {
			c.Deliver(ctx, store, cs)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
