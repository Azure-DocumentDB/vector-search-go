package documentdb

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

type mockTransport func(*http.Request) (*http.Response, error)

func (f mockTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func embeddingClient(t *testing.T, body string, status int) openai.Client {
	t.Helper()
	return openai.NewClient(option.WithBaseURL("https://example.invalid/openai/v1/"), option.WithAPIKey("unit-test-only"), option.WithMaxRetries(0), option.WithHTTPClient(&http.Client{Transport: mockTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/openai/v1/embeddings" || r.Method != "POST" {
			t.Fatal("unexpected embedding endpoint")
		}
		var request map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request["model"] != "actual-deployment-name" || request["dimensions"] != float64(2) {
			t.Fatal("deployment/dimensions not forwarded")
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}))
}

func TestEmbeddingResponseOrderingAndDimensions(t *testing.T) {
	client := embeddingClient(t, `{"data":[{"index":1,"embedding":[0,1]},{"index":0,"embedding":[1,0]}]}`, 200)
	vectors, err := GenerateEmbeddings(context.Background(), client, []string{"first", "second"}, "actual-deployment-name", 2)
	if err != nil || !reflect.DeepEqual(vectors, [][]float64{{1, 0}, {0, 1}}) {
		t.Fatalf("response mapping failed: %v %v", vectors, err)
	}
}

func TestEmbeddingResponseErrors(t *testing.T) {
	for _, body := range []string{`{"data":[]}`, `{"data":[{"index":0,"embedding":[1]}]}`, `{"data":[{"index":4,"embedding":[1,0]}]}`, `{"data":[{"index":0,"embedding":[0,0]}]}`} {
		if _, err := GenerateEmbeddings(context.Background(), embeddingClient(t, body, 200), []string{"query"}, "actual-deployment-name", 2); err == nil {
			t.Fatal("invalid embedding response accepted")
		}
	}
	client := embeddingClient(t, `{"data":[{"index":0,"embedding":[1,0]},{"index":0,"embedding":[0,1]}]}`, 200)
	if _, err := GenerateEmbeddings(context.Background(), client, []string{"first", "second"}, "actual-deployment-name", 2); err == nil {
		t.Fatal("duplicate response indices accepted")
	}
	client = embeddingClient(t, `{"error":{"message":"sensitive request details"}}`, 401)
	if _, err := GenerateEmbeddings(context.Background(), client, []string{"query"}, "actual-deployment-name", 2); err == nil || strings.Contains(err.Error(), "sensitive request") {
		t.Fatal("API error swallowed or leaked")
	}
	if _, err := GenerateEmbeddings(context.Background(), client, nil, "actual-deployment-name", 2); err == nil {
		t.Fatal("empty input accepted")
	}
}

func TestEmbeddingCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := openai.NewClient(option.WithBaseURL("https://example.invalid/"), option.WithAPIKey("test"), option.WithMaxRetries(0), option.WithHTTPClient(&http.Client{Transport: mockTransport(func(r *http.Request) (*http.Response, error) { return nil, r.Context().Err() })}))
	if _, err := GenerateEmbeddings(ctx, client, []string{"query"}, "deployment", 2); !errors.Is(err, context.Canceled) {
		t.Fatalf("context cancellation lost: %v", err)
	}
}

func TestOfflineQueryNeverInitializesModel(t *testing.T) {
	c := configFixture(t)
	c.Dimensions = 2
	c.QueryVectorFile = filepath.Join(t.TempDir(), "query.json")
	c.OpenAIEndpoint = "not-a-valid-endpoint"
	c.ModelName = ""
	if err := os.WriteFile(c.QueryVectorFile, []byte(`[1,0]`), 0600); err != nil {
		t.Fatal(err)
	}
	vector, err := QueryVector(context.Background(), c)
	if err != nil || !reflect.DeepEqual(vector, []float64{1, 0}) {
		t.Fatal("offline query unexpectedly depended on OpenAI")
	}
}
