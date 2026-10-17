package reader

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/biodream-llc/vaglio/internal/pageimg"
)

// transcribe is what a model is asked of each page.
const transcribe = "This is one page of a document sent with a prior authorization request. Transcribe every word " +
	"of text on it exactly as written, line by line, in reading order. Do not summarize, correct, explain or add " +
	"anything. Write [illegible] for a word that cannot be read. Reply with the transcription only."

// Vision is an extractor that has a model read each page's picture through an OpenAI-compatible chat API that takes
// images: no OCR program needed on this machine. Its text is the model's transcription, which can be wrong as OCR
// can, and is marked as such ("model:NAME"); the quote gate then holds a finding's quote to that text, and the reviewer
// confirms against the original.
func Vision(base, key, model string) Extractor {
	return Extractor{Name: "model:" + model, Types: pictureTypes,
		Run: func(ctx context.Context, ct string, raw []byte) (string, error) {
			pages, err := pageimg.Pages(ct, raw)
			if err != nil {
				return "", err
			}
			var out []string
			for i, p := range pages {
				t, err := visionPage(ctx, base, key, model, p)
				if err != nil {
					return "", fmt.Errorf("page %d: %w", i+1, err)
				}
				out = append(out, strings.TrimSpace(t))
			}
			return joinPages(out), nil
		}}
}

func visionPage(ctx context.Context, base, key, model string, p pageimg.Page) (string, error) {
	url := "data:" + p.ContentType + ";base64," + base64.StdEncoding.EncodeToString(p.Data)
	body, _ := json.Marshal(map[string]any{"model": model, "temperature": 0, "messages": []any{
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": transcribe},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}},
		}}}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(base, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := (&http.Client{Timeout: 3 * time.Minute}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return "", fmt.Errorf("the model answered %d: %s", resp.StatusCode, msg)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Choices) == 0 {
		return "", errors.New("the model's answer had no completion in it")
	}
	return out.Choices[0].Message.Content, nil
}
