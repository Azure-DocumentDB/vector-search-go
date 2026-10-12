package documentdb

import (
	"context"
	"crypto/tls"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func envFixture() map[string]string {
	return map[string]string{"MONGO_CLUSTER_NAME": "sample", "EMBEDDING_MODE": "offline", "QUERY_VECTOR_FILE": "query.json", "VECTOR_SEARCH_ALLOW_WRITES": "true"}
}
func configFixture(t *testing.T) Config {
	t.Helper()
	env := envFixture()
	c, err := loadConfig(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestConfigDefaultsAndNamespaces(t *testing.T) {
	c := configFixture(t)
	if c.AuthMode != "oidc" || c.DatabaseName != "vectorsearch_disposable" || c.Dimensions != 1536 || c.Collection("hnsw") != "hotels_hnsw" {
		t.Fatalf("wrong defaults: %+v", c)
	}
	c.DatabaseName = "vectorsearch_disposable_test"
	c.CollectionPrefix = "run1"
	c.CollectionName = "explicit"
	if err := c.ValidateWriteScope("diskann"); err != nil {
		t.Fatal(err)
	}
	if c.Collection("ivf") != "explicit" {
		t.Fatal("collection override ignored")
	}
}

func TestInvalidConfigFailsClosed(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"MONGO_DATABASE", "Hotels"}, {"MONGO_DATABASE", "vectorsearch_disposableevil"}, {"MONGO_DATABASE", "vectorsearch_disposable_../admin"}, {"MONGO_DATABASE", strings.Repeat("vectorsearch_disposable_", 4)},
		{"MONGO_COLLECTION", "a.b"}, {"MONGO_COLLECTION_PREFIX", "$root"}, {"EMBEDDED_FIELD", "x.y"}, {"SEED_ID_FIELD", "$id"},
		{"EMBEDDING_DIMENSIONS", "0"}, {"EMBEDDING_DIMENSIONS", "-1"}, {"EMBEDDING_DIMENSIONS", "x"}, {"EMBEDDING_DIMENSIONS", "2001"},
		{"LOAD_SIZE_BATCH", "0"}, {"LOAD_SIZE_BATCH", "1001"}, {"TOP_K", "0"}, {"TOP_K", "oops"},
		{"VECTOR_SEARCH_TIMEOUT", "0s"}, {"VECTOR_SEARCH_TIMEOUT", "31m"}, {"VECTOR_SEARCH_TIMEOUT", "bad"}, {"VECTOR_SEARCH_ALLOW_WRITES", "yes"},
		{"MONGO_AUTH_MODE", "auto"}, {"MONGO_AUTH_MODE", "connection-string"}, {"MONGO_HOST", "evil.example"}, {"MONGO_CLUSTER_NAME", "evil/user"},
		{"MONGO_CONNECTION_STRING", "secret"}, {"EMBEDDING_MODE", "guess"}, {"QUERY_VECTOR_FILE", ""}, {"EMBEDDING_MODE", "azure-openai"},
	} {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			env := envFixture()
			env[tc.key] = tc.value
			if _, err := loadConfig(func(k string) string { return env[k] }); err == nil {
				t.Fatal("accepted unsafe config")
			}
		})
	}
}

func TestWriteOptInAndAlgorithm(t *testing.T) {
	c := configFixture(t)
	c.AllowWrites = false
	if err := c.ValidateWriteScope("ivf"); err == nil {
		t.Fatal("missing write authorization accepted")
	}
	c.AllowWrites = true
	if err := c.ValidateWriteScope("unknown"); err == nil {
		t.Fatal("unknown algorithm accepted")
	}
}

func TestConnectionStringTLSAndAuth(t *testing.T) {
	base := "mongodb+srv://user:password@sample.mongocluster.cosmos.azure.com/"
	for _, q := range []string{"", "?tls=true&retryWrites=false", "?ssl=true&authMechanism=SCRAM-SHA-256", "?tlsInsecure=false"} {
		if err := validateConnectionURI(base + q); err != nil {
			t.Fatalf("valid URI rejected: %v", err)
		}
	}
	for _, raw := range []string{
		"", "https://example.com", "mongodb://user:password@localhost", "mongodb+srv://sample.mongocluster.cosmos.azure.com/", "mongodb+srv://user:@sample.mongocluster.cosmos.azure.com/", "mongodb+srv://user:password@evil.example/", base + "Hotels", base + "?tls=false", base + "?SSL=false", base + "?tlsInsecure=true", base + "?tlsAllowInvalidCertificates=true", base + "?tlsAllowInvalidHostnames=true", base + "?tls=true&TLS=false", base + "?tls=true&tls=true", base + "?authMechanism=MONGODB-OIDC", base + "?authMechanismProperties=ENVIRONMENT:azure", base + "?tls=%xx",
	} {
		t.Run(strings.ReplaceAll(raw, "password", "REDACTED"), func(t *testing.T) {
			if err := validateConnectionURI(raw); err == nil {
				t.Fatal("unsafe URI accepted")
			} else if strings.Contains(err.Error(), "password@") {
				t.Fatal("credential leaked")
			}
		})
	}
}

type fakeCredential struct {
	token  azcore.AccessToken
	err    error
	scopes []string
	ctx    context.Context
}

func (f *fakeCredential) GetToken(ctx context.Context, opts policy.TokenRequestOptions) (azcore.AccessToken, error) {
	f.scopes = opts.Scopes
	f.ctx = ctx
	return f.token, f.err
}

func TestOIDCAudienceAndExpiry(t *testing.T) {
	now := time.Now()
	expiry := now.Add(time.Hour)
	fake := &fakeCredential{token: azcore.AccessToken{Token: "test-token", ExpiresOn: expiry}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err := oidcCallback(fake, func() time.Time { return now })(ctx, &options.OIDCArgs{IDPInfo: &options.IDPInfo{RequestScopes: []string{"https://untrusted"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.scopes) != 1 || fake.scopes[0] != AzureMongoScope || fake.ctx != ctx {
		t.Fatal("wrong token scope or context")
	}
	if result.AccessToken != "test-token" || result.ExpiresAt == nil || !result.ExpiresAt.Equal(expiry) {
		t.Fatal("token expiry not propagated")
	}
}

func TestOIDCRejectsInvalidTokensAndSanitizesErrors(t *testing.T) {
	now := time.Now()
	for _, token := range []azcore.AccessToken{{Token: "", ExpiresOn: now.Add(time.Hour)}, {Token: "x"}, {Token: "x", ExpiresOn: now.Add(-time.Second)}, {Token: "x", ExpiresOn: now.Add(10 * time.Second)}} {
		if _, err := oidcCallback(&fakeCredential{token: token}, func() time.Time { return now })(context.Background(), nil); err == nil {
			t.Fatal("invalid token accepted")
		}
	}
	cause := errors.New("sensitive-token-and-uri")
	_, err := oidcCallback(&fakeCredential{err: cause}, time.Now)(context.Background(), nil)
	if err == nil || strings.Contains(err.Error(), cause.Error()) || !errors.Is(err, cause) {
		t.Fatal("error must preserve cause without exposing it")
	}
}

func TestEmbeddingConfigValidation(t *testing.T) {
	c := configFixture(t)
	if _, err := NewEmbeddingClient(c); err == nil {
		t.Fatal("offline mode created an OpenAI client")
	}
	c.EmbeddingMode = "azure-openai"
	c.QueryVectorFile = ""
	c.ModelName = "my-embedding-deployment"
	c.OpenAIEndpoint = "https://example.openai.azure.com/"
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"", "http://example.com", "https://user:secret@example.com", "https://example.com/path", "https://example.com/?key=secret"} {
		c.OpenAIEndpoint = endpoint
		if err := c.Validate(); err == nil {
			t.Fatal("invalid embedding origin accepted")
		}
	}
}

func TestMongoSecurityOptions(t *testing.T) {
	c := configFixture(t)
	opts, err := configureClientOptions(options.Client(), c, &fakeCredential{})
	if err != nil {
		t.Fatal(err)
	}
	if opts.TLSConfig == nil || opts.TLSConfig.InsecureSkipVerify || opts.TLSConfig.MinVersion < tls.VersionTLS12 {
		t.Fatal("TLS verification/minimum version not enforced")
	}
	if opts.Auth == nil || opts.Auth.AuthMechanism != "MONGODB-OIDC" || opts.Auth.AuthSource != "$external" || opts.Auth.OIDCMachineCallback == nil || opts.Auth.OIDCHumanCallback != nil {
		t.Fatal("incorrect OIDC mechanism/callback")
	}
	if opts.RetryWrites == nil || *opts.RetryWrites {
		t.Fatal("unsupported retryable writes enabled")
	}
	if opts.ServerSelectionTimeout == nil || *opts.ServerSelectionTimeout > 30*time.Second {
		t.Fatal("server selection not bounded")
	}
	if _, err := configureClientOptions(options.Client(), c, nil); err == nil {
		t.Fatal("missing OIDC credential accepted")
	}
}
