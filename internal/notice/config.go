package notice

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// Config is a plan's notices file (serve -notices FILE).
type Config struct {
	// Events are what the member is mailed a notice of: denied, approved (by a person), approved-all (the engine's
	// approvals too), extended, appeal.
	Events []string `json:"events"`
	// Service is folder (PDFs into Folder for a mail house) or lob (Lob's Print & Mail API, its key in LobKeyEnv).
	Service   string  `json:"service"`
	Folder    string  `json:"folder,omitempty"`
	LobKeyEnv string  `json:"lobKeyEnv,omitempty"`
	LobURL    string  `json:"lobUrl,omitempty"` // a stand-in's address, for a test; Lob's own by default
	From      Address `json:"from"`             // the plan's return address
}

// LoadConfig reads and checks a notices file; secret names a variable holding Lob's key.
func LoadConfig(path string, secret func(env string) (string, error)) (*Config, Sender, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	var c Config
	if err := d.Decode(&c); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	fail := func(msg string) (*Config, Sender, error) { return nil, nil, fmt.Errorf("%s: %s", path, msg) }
	if len(c.Events) == 0 {
		return fail("events is empty: name what the member is mailed notices of")
	}
	for _, e := range c.Events {
		switch e {
		case Denied, Approved, AllOK, Extended, Appeal:
		default:
			return fail(fmt.Sprintf("%q is not an event: denied, approved, approved-all, extended or appeal", e))
		}
	}
	if c.From.Name == "" || len(c.From.Lines) == 0 || c.From.City == "" || !stateRe.MatchString(c.From.State) || !zipRe.MatchString(c.From.Zip) {
		return fail("from is the plan's full return address: name, lines, city, state, zip")
	}
	var s Sender
	switch c.Service {
	case "folder":
		if c.Folder == "" {
			return fail("service folder needs folder, the directory the letters go into")
		}
		s = &Folder{Dir: c.Folder}
	case "lob":
		key, err := secret(c.LobKeyEnv)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: lobKeyEnv: %w", path, err)
		}
		if key == "" {
			return nil, nil, errors.New(path + ": the Lob API key is empty")
		}
		s = &Lob{Key: key, URL: c.LobURL}
	default:
		return fail(fmt.Sprintf("service %q: folder or lob", c.Service))
	}
	return &c, s, nil
}
