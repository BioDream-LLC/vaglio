package faxmodem

import (
	"context"
	"log"
	"time"
)

// Store keeps one received fax, a PDF, in the fax inbox.
type Store func(ctx context.Context, pdf []byte, from string, pages int) error

// Run keeps a modem answering until ctx ends: open the port, set the modem up, wait for a call, keep the fax, and
// after any fault start again from opening the port, ten seconds later.
func Run(ctx context.Context, device string, baud int, cfg Config, store Store) {
	for ctx.Err() == nil {
		if err := runOnce(ctx, device, baud, cfg, store); err != nil && ctx.Err() == nil {
			log.Printf("fax modem %s: %v; starting again in 10s", device, err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Second):
		}
	}
}

func runOnce(ctx context.Context, device string, baud int, cfg Config, store Store) error {
	port, err := OpenPort(device, baud)
	if err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { port.Close() })
	defer stop()
	defer port.Close()
	s := NewSession(port, cfg)
	if err := s.Init(ctx); err != nil {
		return err
	}
	log.Printf("fax modem %s: waiting for calls", device)
	for {
		fax, err := s.Wait(ctx)
		if fax != nil && len(fax.Pages) > 0 {
			if serr := store(ctx, PDF(fax), fax.From, len(fax.Pages)); serr != nil {
				log.Printf("fax modem %s: a fax from %s was received but not kept: %v", device, fax.From, serr)
			} else {
				log.Printf("fax modem %s: %d pages from %s", device, len(fax.Pages), fax.From)
			}
		}
		if err != nil {
			return err
		}
		if err := s.Init(ctx); err != nil { // ready for the next call
			return err
		}
	}
}
