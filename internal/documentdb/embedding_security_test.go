package documentdb

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
)

func modelConfig() Config {
	return Config{EmbeddingMode: "azure-openai", OpenAIEndpoint: "https://unit-test.openai.azure.com/", ModelName: "actual-deployment-name"}
}

func TestEmbeddingOriginsFailBeforeCredentialsOrTransport(t *testing.T) {
	for _, origin := range []string{
		"https://attacker.example", "https://attacker.example:8443", "https://unit.openai.azure.com.evil.example", "https://evilopenai.azure.com", "https://openai.azure.com", "https://nested.unit.openai.azure.com", "https://127.0.0.1", "https://[::1]", "https://unit.openai.azure.com:8443", "https://unit.openai.azure.com:80", "https://unit.openai.azure.com:", "http://unit.openai.azure.com", "https://unit.openai.azure.com./", "https://unit.openai.azure.com?", "https://unit.services.ai.azure.com", "https://unit.cognitiveservices.azure.com", "https://user:pass@unit.openai.azure.com", "https://unit.openai.azure.com/%2f",
	} {
		t.Run(origin, func(t *testing.T) {
			c := modelConfig()
			c.OpenAIEndpoint = origin
			credentialCalls := 0
			transportCalls := 0
			_, err := newEmbeddingClient(c, mockTransport(func(*http.Request) (*http.Response, error) { transportCalls++; return nil, nil }), func() (azcore.TokenCredential, error) { credentialCalls++; return &fakeCredential{}, nil })
			if err == nil || credentialCalls != 0 || transportCalls != 0 {
				t.Fatal("invalid origin accepted or reached credentials/transport")
			}
			// Public production constructor must reject both credential paths as well.
			c.OpenAIKey = "unit-test-only"
			if _, err := NewEmbeddingClient(c); err == nil {
				t.Fatal("public constructor accepted invalid keyed origin")
			}
		})
	}
	for _, origin := range []string{"https://unit.openai.azure.com", "https://unit.openai.azure.com/", "https://unit.openai.azure.com:443/"} {
		c := modelConfig()
		c.OpenAIEndpoint = origin
		if err := validateOpenAI(c); err != nil {
			t.Fatalf("supported origin rejected: %v", err)
		}
	}
}

func mockModelResponse(r *http.Request, status int, location string) *http.Response {
	header := http.Header{"Content-Type": []string{"application/json"}}
	body := `{"data":[{"index":0,"embedding":[1,0]}]}`
	if location != "" {
		header.Set("Location", location)
		body = `{}`
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: r}
}

func TestPublicEmbeddingClientKeyWiring(t *testing.T) {
	// Exercise the actual public production constructor with only the standard
	// HTTP transport replaced. No DNS, Azure credential, or network operation.
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	calls := 0
	http.DefaultTransport = mockTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != "https://unit-test.openai.azure.com/openai/v1/embeddings" || r.Header.Get("Authorization") != "Bearer unit-test-only" {
			t.Fatal("production endpoint/key wiring incorrect")
		}
		return mockModelResponse(r, 200, ""), nil
	})
	c := modelConfig()
	c.OpenAIKey = "unit-test-only"
	client, err := NewEmbeddingClient(c)
	if err != nil {
		t.Fatal(err)
	}
	vectors, err := GenerateEmbeddings(context.Background(), client, []string{"query"}, c.ModelName, 2)
	if err != nil || !reflect.DeepEqual(vectors, [][]float64{{1, 0}}) || calls != 1 {
		t.Fatalf("production request failed: %v", err)
	}
}

func TestProductionEmbeddingEntraWiring(t *testing.T) {
	credential := &fakeCredential{token: azcore.AccessToken{Token: "unit-test-azure-token", ExpiresOn: time.Now().Add(time.Hour)}}
	calls := 0
	factoryCalls := 0
	c := modelConfig()
	client, err := newEmbeddingClient(c, mockTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != "https://unit-test.openai.azure.com/openai/v1/embeddings" || r.Header.Get("Authorization") != "Bearer unit-test-azure-token" {
			t.Fatal("production Entra endpoint/bearer wiring incorrect")
		}
		return mockModelResponse(r, 200, ""), nil
	}), func() (azcore.TokenCredential, error) { factoryCalls++; return credential, nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := GenerateEmbeddings(context.Background(), client, []string{"query"}, c.ModelName, 2); err != nil {
		t.Fatal(err)
	}
	if factoryCalls != 1 || calls != 1 || !reflect.DeepEqual(credential.scopes, []string{"https://cognitiveservices.azure.com/.default"}) {
		t.Fatal("production Entra audience/call count incorrect")
	}
}

func TestProductionEmbeddingRejectsAllRedirects(t *testing.T) {
	for _, mode := range []string{"key", "entra"} {
		for _, target := range []string{
			"https://attacker.example/embeddings", "https://sub.unit-test.openai.azure.com/embeddings", "http://unit-test.openai.azure.com/embeddings", "https://unit-test.openai.azure.com/other", "/same-origin-relative",
		} {
			t.Run(mode+"/"+target, func(t *testing.T) {
				c := modelConfig()
				if mode == "key" {
					c.OpenAIKey = "unit-test-only"
				}
				calls := 0
				client, err := newEmbeddingClient(c, mockTransport(func(r *http.Request) (*http.Response, error) {
					calls++
					if calls > 1 || r.URL.String() != "https://unit-test.openai.azure.com/openai/v1/embeddings" {
						t.Fatal("credentialed redirect was followed")
					}
					return mockModelResponse(r, 307, target), nil
				}), func() (azcore.TokenCredential, error) {
					return &fakeCredential{token: azcore.AccessToken{Token: "unit-test-azure-token", ExpiresOn: time.Now().Add(time.Hour)}}, nil
				})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := GenerateEmbeddings(context.Background(), client, []string{"query"}, c.ModelName, 2); err == nil {
					t.Fatal("redirect unexpectedly counted as successful embedding")
				}
				if calls != 1 {
					t.Fatal("redirect/retry reached another request")
				}
			})
		}
	}
}
