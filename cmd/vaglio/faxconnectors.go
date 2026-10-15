package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/biodream-llc/vaglio/internal/faxsvc"
	"github.com/biodream-llc/vaglio/internal/intake"
	"github.com/biodream-llc/vaglio/internal/server"
)

// faxConnectors is the -fax-connectors file: where faxes come from besides the plain webhook. Secrets are named by
// environment variable, never written in the file.
type faxConnectors struct {
	Sinch *struct {
		PasswordEnv string `json:"passwordEnv"`
	} `json:"sinch,omitempty"`
	Documo *struct {
		PasswordEnv string `json:"passwordEnv"`
		APIKeyEnv   string `json:"apiKeyEnv"`
		API         string `json:"api,omitempty"`
	} `json:"documo,omitempty"`
	SRFax *struct {
		AccessID    string `json:"accessId"`
		PasswordEnv string `json:"passwordEnv"`
		Every       string `json:"every,omitempty"` // default 1m
	} `json:"srfax,omitempty"`
	RingCentral *struct {
		Server          string `json:"server,omitempty"`
		ClientID        string `json:"clientId"`
		ClientSecretEnv string `json:"clientSecretEnv"`
		JWTEnv          string `json:"jwtEnv"`
		Every           string `json:"every,omitempty"`
	} `json:"ringcentral,omitempty"`
	Folder *struct {
		Dir   string `json:"dir"`
		Every string `json:"every,omitempty"` // default 10s
	} `json:"folder,omitempty"`
}

func every(s string, def time.Duration) (time.Duration, error) {
	if s == "" {
		return def, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 5*time.Second {
		return 0, fmt.Errorf("every %q is a duration of 5s or more", s)
	}
	return d, nil
}

func need(env string) (string, error) {
	v, err := secretFrom(env)
	if err != nil {
		return "", err
	}
	if len(v) < 8 {
		return "", fmt.Errorf("%s is empty or too short", env)
	}
	return v, nil
}

// openFaxConnectors reads the file and starts what it names, keeping faxes in cfg's inbox (made if the plan has none).
func openFaxConnectors(ctx context.Context, path string, cfg *server.Config, data string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var fc faxConnectors
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&fc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if cfg.Faxes == nil {
		cfg.Faxes = &intake.Inbox{Dir: filepath.Join(data, "faxes")}
	}
	c := *cfg // StoreFax reads the inbox, extractors and log, all set by now
	store := func(ctx context.Context, raw []byte, ct, from string, pages int, source string) error {
		_, err := c.StoreFax(ctx, raw, ct, from, pages, source)
		return err
	}
	cfg.FaxHooks = map[string]http.Handler{}
	var started []string
	if s := fc.Sinch; s != nil {
		pw, err := need(s.PasswordEnv)
		if err != nil {
			return nil, fmt.Errorf("sinch: %w", err)
		}
		cfg.FaxHooks["sinch"] = &faxsvc.Sinch{Password: pw, Store: store}
		started = append(started, "Sinch webhook at /api/feeds/fax/sinch")
	}
	if d := fc.Documo; d != nil {
		pw, err := need(d.PasswordEnv)
		if err != nil {
			return nil, fmt.Errorf("documo: %w", err)
		}
		key, err := need(d.APIKeyEnv)
		if err != nil {
			return nil, fmt.Errorf("documo: %w", err)
		}
		cfg.FaxHooks["documo"] = &faxsvc.Documo{Password: pw, APIKey: key, API: d.API, Store: store}
		started = append(started, "Documo webhook at /api/feeds/fax/documo")
	}
	if s := fc.SRFax; s != nil {
		pw, err := need(s.PasswordEnv)
		if err != nil {
			return nil, fmt.Errorf("srfax: %w", err)
		}
		d, err := every(s.Every, time.Minute)
		if err != nil || s.AccessID == "" {
			return nil, errors.Join(errors.New("srfax needs accessId"), err)
		}
		p := &faxsvc.SRFax{AccessID: s.AccessID, Password: pw, Store: store, Seen: &faxsvc.Seen{Path: filepath.Join(data, "faxes", "srfax.seen")}}
		go faxsvc.Run(ctx, p, d)
		started = append(started, "SRFax, polled every "+d.String())
	}
	if r := fc.RingCentral; r != nil {
		sec, err := need(r.ClientSecretEnv)
		if err != nil {
			return nil, fmt.Errorf("ringcentral: %w", err)
		}
		jwt, err := need(r.JWTEnv)
		if err != nil {
			return nil, fmt.Errorf("ringcentral: %w", err)
		}
		d, err := every(r.Every, time.Minute)
		if err != nil || r.ClientID == "" {
			return nil, errors.Join(errors.New("ringcentral needs clientId"), err)
		}
		p := &faxsvc.RingCentral{Server: r.Server, ClientID: r.ClientID, ClientSecret: sec, JWT: jwt, Store: store,
			Seen: &faxsvc.Seen{Path: filepath.Join(data, "faxes", "ringcentral.seen")}}
		go faxsvc.Run(ctx, p, d)
		started = append(started, "RingCentral, polled every "+d.String())
	}
	if f := fc.Folder; f != nil {
		if info, err := os.Stat(f.Dir); err != nil || !info.IsDir() {
			return nil, fmt.Errorf("folder %q is not a directory", f.Dir)
		}
		d, err := every(f.Every, 10*time.Second)
		if err != nil {
			return nil, err
		}
		go faxsvc.Run(ctx, &faxsvc.Folder{Dir: f.Dir, Store: store}, d)
		started = append(started, "scan folder "+f.Dir)
	}
	if err := os.MkdirAll(filepath.Join(data, "faxes"), 0o700); err != nil {
		return nil, err
	}
	if len(started) == 0 {
		return nil, fmt.Errorf("%s names no connector", path)
	}
	return started, nil
}
