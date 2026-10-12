package documentdb

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/azure"
	"github.com/openai/openai-go/v3/option"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Preserve errors.Is/As without printing driver URLs, tokens, or documents.
// The driver/SDK may include these in its error strings.
type safeError struct {
	operation string
	cause     error
}

func (e safeError) Error() string {
	return e.operation + " failed (details withheld to protect credentials/data)"
}
func (e safeError) Unwrap() error               { return e.cause }
func protect(operation string, err error) error { return safeError{operation, err} }

func oidcCallback(credential azcore.TokenCredential, now func() time.Time) options.OIDCCallback {
	return func(ctx context.Context, _ *options.OIDCArgs) (*options.OIDCCredential, error) {
		token, err := credential.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{AzureMongoScope}})
		if err != nil {
			return nil, protect("Azure MongoDB token acquisition", err)
		}
		if token.Token == "" || !token.ExpiresOn.After(now().Add(30*time.Second)) {
			return nil, errors.New("Azure MongoDB token is empty, expired, or too close to expiry")
		}
		return &options.OIDCCredential{AccessToken: token.Token, ExpiresAt: &token.ExpiresOn}, nil
	}
}

func mongoOptions(c Config, credential azcore.TokenCredential) (*options.ClientOptions, error) {
	// Validate before ApplyURI (which may resolve SRV records).
	if err := c.Validate(); err != nil {
		return nil, err
	}
	uri := c.ConnectionString
	if c.AuthMode == "oidc" {
		uri = "mongodb+srv://" + c.Host + "/"
	}
	return configureClientOptions(options.Client().ApplyURI(uri), c, credential)
}

// Separated from SRV resolution so security options can be exercised without DNS.
func configureClientOptions(opts *options.ClientOptions, c Config, credential azcore.TokenCredential) (*options.ClientOptions, error) {
	opts = opts.
		SetTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12}).
		SetConnectTimeout(30 * time.Second).SetServerSelectionTimeout(30 * time.Second).
		SetMaxPoolSize(10).SetMaxConnIdleTime(2 * time.Minute).SetRetryWrites(false)
	if c.AuthMode == "oidc" {
		if credential == nil {
			return nil, errors.New("OIDC credential is required")
		}
		opts.SetAuth(options.Credential{AuthMechanism: "MONGODB-OIDC", AuthSource: "$external", Username: c.OIDCUsername, OIDCMachineCallback: oidcCallback(credential, time.Now)})
	}
	if err := opts.Validate(); err != nil {
		return nil, protect("MongoDB options validation", err)
	}
	return opts, nil
}

// ConnectMongo pings to prove authentication, and closes unsuccessful clients.
// No OpenAI client is created here; offline and index inspection need no model.
func ConnectMongo(ctx context.Context, c Config) (*mongo.Client, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	var credential azcore.TokenCredential
	if c.AuthMode == "oidc" {
		var err error
		credential, err = azidentity.NewDefaultAzureCredential(nil)
		if err != nil {
			return nil, protect("Azure credential initialization", err)
		}
	}
	opts, err := mongoOptions(c, credential)
	if err != nil {
		return nil, err
	}
	client, err := mongo.Connect(ctx, opts)
	if err != nil {
		return nil, protect("MongoDB connection", err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		_ = Disconnect(client)
		return nil, protect("MongoDB authentication/ping", err)
	}
	return client, nil
}

func Disconnect(client *mongo.Client) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.Disconnect(ctx); err != nil {
		return protect("MongoDB disconnect", err)
	}
	return nil
}

// NewEmbeddingClient is deliberately independent of MongoDB authentication.
func NewEmbeddingClient(c Config) (openai.Client, error) {
	return newEmbeddingClient(c, http.DefaultTransport, func() (azcore.TokenCredential, error) {
		return azidentity.NewDefaultAzureCredential(nil)
	})
}

// Dependency injection is private and only used by no-network tests. Endpoint
// validation and redirect policy cannot be replaced by callers or environment.
func newEmbeddingClient(c Config, transport http.RoundTripper, credentialFactory func() (azcore.TokenCredential, error)) (openai.Client, error) {
	if c.EmbeddingMode != "azure-openai" {
		return openai.Client{}, errors.New("OpenAI disabled outside azure-openai mode")
	}
	if err := validateOpenAI(c); err != nil {
		return openai.Client{}, err
	}
	httpClient := &http.Client{
		Transport: transport,
		// Never forward any credential to a redirect destination, even a same-
		// host or HTTPS target. The caller receives a non-success response.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	opts := []option.RequestOption{
		option.WithBaseURL(strings.TrimRight(c.OpenAIEndpoint, "/") + "/openai/v1/"),
		option.WithHTTPClient(httpClient),
	}
	if c.OpenAIKey != "" {
		opts = append(opts, option.WithAPIKey(c.OpenAIKey))
	} else {
		credential, err := credentialFactory()
		if err != nil {
			return openai.Client{}, protect("Azure embedding credential initialization", err)
		}
		opts = append(opts, azure.WithTokenCredential(credential))
	}
	return openai.NewClient(opts...), nil
}
