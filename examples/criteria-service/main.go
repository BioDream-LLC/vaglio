// Command criteria-service is an example of the service a plan runs to bring criteria it licenses (or keeps in another
// system) into Vaglio's decisions. It is a starting point to copy, not a product: the one criterion it knows is invented,
// and a plan replaces ask with a call to its own criteria engine, under the terms of its own licence.
//
// Vaglio posts, for each "find criteria NAME CRITERION" in a policy:
//
//	{"criterion": "...", "policy": "...", "policyVersion": "...", "service": "system|code", "bundle": <PAS request Bundle>}
//
// and reads back {"result": "met" | "not-met" | "unknown", "reason": "...", "reference": "..."}. Met documents the
// evidence present; not-met documents it absent, and is only used with its reason, which the proof quotes; unknown (or
// an error, or no answer in five seconds) leaves it not addressed, which never denies. Run it beside Vaglio:
//
//	CRITERIA_TOKEN=... criteria-service -addr 127.0.0.1:8790
//	CRITERIA_TOKEN=... vaglio serve ... -criteria example=http://127.0.0.1:8790/ -criteria-token-env example=CRITERIA_TOKEN
//
// Plain http is accepted on this machine only; anywhere else, serve it over https.
package main

import (
	"crypto/subtle"
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
)

// Question is what Vaglio asks.
type Question struct {
	Criterion     string         `json:"criterion"`
	Policy        string         `json:"policy"`
	PolicyVersion string         `json:"policyVersion"`
	Service       string         `json:"service"`
	Bundle        map[string]any `json:"bundle"`
}

// Answer is what the service says.
type Answer struct {
	Result    string `json:"result"` // met, not-met or unknown
	Reason    string `json:"reason,omitempty"`
	Reference string `json:"reference,omitempty"`
}

// ask answers one criterion. Replace it with a call to the plan's licensed criteria engine. When the engine cannot
// tell from what the request holds, answer unknown: Vaglio then asks the provider for the evidence instead of denying.
func ask(q Question) Answer {
	switch q.Criterion {
	case "EXAMPLE-BMI-40": // invented: a BMI of at least 40 recorded in the request (LOINC 39156-5)
		bmi, ok := latestValue(q.Bundle, "http://loinc.org", "39156-5")
		switch {
		case !ok:
			return Answer{Result: "unknown"}
		case bmi >= 40:
			return Answer{Result: "met", Reason: "BMI recorded as at least 40", Reference: "example-criteria 1.0"}
		default:
			return Answer{Result: "not-met", Reason: "BMI recorded below 40", Reference: "example-criteria 1.0"}
		}
	}
	return Answer{Result: "unknown"} // a criterion this service does not hold
}

// latestValue is the value of a final Observation with this code in the Bundle (the last one, if several).
func latestValue(b map[string]any, system, code string) (float64, bool) {
	var v float64
	found := false
	entries, _ := b["entry"].([]any)
	for _, e := range entries {
		r, _ := e.(map[string]any)["resource"].(map[string]any)
		if r["resourceType"] != "Observation" || r["status"] != "final" {
			continue
		}
		cc, _ := r["code"].(map[string]any)
		codings, _ := cc["coding"].([]any)
		for _, c := range codings {
			m, _ := c.(map[string]any)
			if m["system"] == system && m["code"] == code {
				q, _ := r["valueQuantity"].(map[string]any)
				if n, ok := q["value"].(float64); ok && q["comparator"] == nil {
					v, found = n, true
				}
			}
		}
	}
	return v, found
}

func handler(token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST a question", http.StatusMethodNotAllowed)
			return
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			http.Error(w, "the bearer token is needed", http.StatusUnauthorized)
			return
		}
		var q Question
		if err := json.NewDecoder(io.LimitReader(r.Body, 16<<20)).Decode(&q); err != nil {
			http.Error(w, "not JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ask(q))
	})
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8790", "address to listen on")
	tokenEnv := flag.String("token-env", "CRITERIA_TOKEN", "environment variable holding the bearer token Vaglio sends")
	flag.Parse()
	token := os.Getenv(*tokenEnv)
	if token == "" {
		log.Fatalf("set %s: the service is sent patient records and must not answer just anyone", *tokenEnv)
	}
	log.Printf("criteria service on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, handler(token)))
}
