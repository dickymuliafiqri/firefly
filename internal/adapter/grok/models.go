package grok

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/dickymuliafiqri/firefly/internal/textx"
)

// GrokCLIModelsPath is the Grok CLI model-discovery endpoint path.
const GrokCLIModelsPath = "/models"

// maxErrorBodyChars bounds the upstream error body captured in HTTPError; the
// message is surfaced to clients, so it must stay short and single-line.
const maxErrorBodyChars = 512

// HTTPError captures a non-2xx HTTP status and response body from the Grok CLI endpoint.
type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("grok models discovery failed (HTTP %d): %s", e.StatusCode, e.Body)
}

// FetchModels queries the live Grok CLI /models endpoint using the given bearer
// access token and returns the discovered public model ids.
//
// baseURL is the upstream base (e.g. https://cli-chat-proxy.grok.com/v1); when empty
// the default Grok CLI base is used. The caller supplies an *http.Client (typically
// the pooled upstream client). On any failure the caller should fall back to
// SupportedModels().
func FetchModels(ctx context.Context, client *http.Client, baseURL, accessToken string) ([]string, error) {
	if client == nil {
		return nil, fmt.Errorf("grok: nil http client")
	}
	if strings.TrimSpace(accessToken) == "" {
		return nil, fmt.Errorf("grok: missing access token for model discovery")
	}

	base := strings.TrimRight(baseURL, "/")
	if base == "" {
		base = GrokCLIBaseURL
	}
	// If the base already points at /responses, strip it back to the API root.
	base = strings.TrimSuffix(base, GrokCLIResponsesPath)
	url := base + GrokCLIModelsPath

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", GrokCLIUserAgent)
	req.Header.Set("x-xai-token-auth", GrokCLITokenAuth)
	req.Header.Set("x-grok-client-identifier", GrokCLIClientIdentifier)
	req.Header.Set("x-grok-client-version", GrokCLIVersion)
	req.Header.Set("x-grok-client-mode", "headless")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return nil, &HTTPError{
			StatusCode: resp.StatusCode,
			Body:       textx.Excerpt(body, maxErrorBodyChars),
		}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	return parseModelsResponse(body), nil
}

// parseModelsResponse extracts model ids from a Grok CLI /models response. It
// tolerates the common shapes: {data:[...]}, {models:[...]}, {results:[...]},
// a bare array, or an object map. Array items may be strings or objects with
// id/model_id/modelId/model/slug/name fields.
func parseModelsResponse(body []byte) []string {
	if !gjson.ValidBytes(body) {
		return nil
	}
	root := gjson.ParseBytes(body)

	var list gjson.Result
	switch {
	case root.IsArray():
		list = root
	case root.Get("data").IsArray():
		list = root.Get("data")
	case root.Get("models").IsArray():
		list = root.Get("models")
	case root.Get("results").IsArray():
		list = root.Get("results")
	default:
		list = root // object map: iterate values
	}

	seen := make(map[string]bool)
	var ids []string
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		ids = append(ids, id)
	}

	list.ForEach(func(key, item gjson.Result) bool {
		if item.Type == gjson.String {
			add(item.String())
			return true
		}
		if item.IsObject() {
			for _, field := range []string{"id", "model_id", "modelId", "model", "slug", "name"} {
				if v := item.Get(field); v.Exists() && v.String() != "" {
					add(v.String())
					return true
				}
			}
			// Object-map shape: the key is the id.
			if key.Type == gjson.String && key.String() != "" {
				add(key.String())
			}
		}
		return true
	})

	sort.Strings(ids)
	return ids
}
