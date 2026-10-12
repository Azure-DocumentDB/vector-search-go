package documentdb

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const AzureMongoScope = "https://ossrdbms-aad.database.windows.net/.default"

var namePattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{0,79}$`)
var databasePattern = regexp.MustCompile(`^vectorsearch_disposable(_[a-z0-9_]+)?$`)
var azureHostPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*\.(global\.)?mongocluster\.cosmos\.azure\.com$`)

// Only the documented Azure OpenAI resource origin is supported, not arbitrary gateways.
var azureOpenAIHostPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.openai\.azure\.com$`)

type Config struct {
	AuthMode, Host, ConnectionString, OIDCUsername string
	DatabaseName, CollectionPrefix, CollectionName string
	DataFile, VectorField, SeedIDField             string
	EmbeddingMode, QueryVectorFile, QueryText      string
	OpenAIEndpoint, OpenAIKey, ModelName           string
	Dimensions, BatchSize, TopK                    int
	Timeout                                        time.Duration
	AllowWrites                                    bool
}

func GetEnvOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// LoadConfig reads only the process environment. It never loads or executes .env.
func LoadConfig() (Config, error) { return loadConfig(os.Getenv) }

func loadConfig(get func(string) string) (Config, error) {
	value := func(key, fallback string) string {
		if v := get(key); v != "" {
			return v
		}
		return fallback
	}
	c := Config{
		AuthMode: value("MONGO_AUTH_MODE", "oidc"), Host: get("MONGO_HOST"), ConnectionString: get("MONGO_CONNECTION_STRING"), OIDCUsername: get("MONGO_OIDC_USERNAME"),
		DatabaseName: value("MONGO_DATABASE", "vectorsearch_disposable"), CollectionPrefix: value("MONGO_COLLECTION_PREFIX", "hotels"), CollectionName: get("MONGO_COLLECTION"),
		DataFile: value("DATA_FILE_WITH_VECTORS", "data/Hotels_Vector.json"), VectorField: value("EMBEDDED_FIELD", "DescriptionVector"), SeedIDField: value("SEED_ID_FIELD", "HotelId"),
		EmbeddingMode: value("EMBEDDING_MODE", "azure-openai"), QueryVectorFile: get("QUERY_VECTOR_FILE"), QueryText: value("QUERY_TEXT", "quintessential lodging near running trails, eateries, retail"),
		OpenAIEndpoint: get("AZURE_OPENAI_EMBEDDING_ENDPOINT"), OpenAIKey: get("AZURE_OPENAI_EMBEDDING_KEY"), ModelName: get("AZURE_OPENAI_EMBEDDING_MODEL"),
	}
	if c.Host == "" && get("MONGO_CLUSTER_NAME") != "" {
		c.Host = get("MONGO_CLUSTER_NAME") + ".global.mongocluster.cosmos.azure.com"
	}
	for _, p := range []struct {
		key, def string
		dst      *int
		max      int
	}{
		{"EMBEDDING_DIMENSIONS", "1536", &c.Dimensions, 2000}, {"LOAD_SIZE_BATCH", "100", &c.BatchSize, 1000}, {"TOP_K", "5", &c.TopK, 1000},
	} {
		n, err := strconv.Atoi(value(p.key, p.def))
		if err != nil || n < 1 || n > p.max {
			return c, fmt.Errorf("%s must be an integer in [1,%d]", p.key, p.max)
		}
		*p.dst = n
	}
	var err error
	c.Timeout, err = time.ParseDuration(value("VECTOR_SEARCH_TIMEOUT", "5m"))
	if err != nil || c.Timeout < time.Second || c.Timeout > 30*time.Minute {
		return c, fmt.Errorf("VECTOR_SEARCH_TIMEOUT must be between 1s and 30m")
	}
	switch value("VECTOR_SEARCH_ALLOW_WRITES", "false") {
	case "true":
		c.AllowWrites = true
	case "false":
	default:
		return c, fmt.Errorf("VECTOR_SEARCH_ALLOW_WRITES must be true or false")
	}
	return c, c.Validate()
}

func (c Config) Validate() error {
	if !databasePattern.MatchString(c.DatabaseName) || len(c.DatabaseName) > 63 {
		return fmt.Errorf("MONGO_DATABASE must be vectorsearch_disposable or vectorsearch_disposable_<lowercase run ID>, at most 63 characters")
	}
	for _, s := range []string{c.CollectionPrefix, c.VectorField, c.SeedIDField} {
		if !namePattern.MatchString(s) {
			return fmt.Errorf("collection prefix and field names must be simple identifiers of at most 80 characters")
		}
	}
	if c.CollectionName != "" && !namePattern.MatchString(c.CollectionName) {
		return fmt.Errorf("MONGO_COLLECTION must be a simple identifier of at most 80 characters")
	}
	if c.Dimensions < 1 || c.Dimensions > 2000 || c.BatchSize < 1 || c.BatchSize > 1000 || c.TopK < 1 || c.TopK > 1000 {
		return fmt.Errorf("invalid dimensions, batch size, or top K")
	}
	if c.Timeout < time.Second || c.Timeout > 30*time.Minute {
		return fmt.Errorf("invalid operation timeout")
	}
	switch c.AuthMode {
	case "oidc":
		if c.ConnectionString != "" {
			return fmt.Errorf("unset MONGO_CONNECTION_STRING for oidc mode; no credential fallback is permitted")
		}
		if !azureHostPattern.MatchString(c.Host) {
			return fmt.Errorf("OIDC requires MONGO_CLUSTER_NAME or MONGO_HOST in the public Azure DocumentDB domain")
		}
	case "connection-string":
		if err := validateConnectionURI(c.ConnectionString); err != nil {
			return err
		}
	default:
		return fmt.Errorf("MONGO_AUTH_MODE must be oidc or connection-string")
	}
	switch c.EmbeddingMode {
	case "offline":
		if c.QueryVectorFile == "" {
			return fmt.Errorf("offline mode requires QUERY_VECTOR_FILE (JSON numeric array); no model fallback")
		}
	case "azure-openai":
		if c.QueryVectorFile != "" {
			return fmt.Errorf("QUERY_VECTOR_FILE requires explicit EMBEDDING_MODE=offline")
		}
		if err := validateOpenAI(c); err != nil {
			return err
		}
	default:
		return fmt.Errorf("EMBEDDING_MODE must be azure-openai or offline")
	}
	return nil
}

func validateOpenAI(c Config) error {
	u, err := url.Parse(c.OpenAIEndpoint)
	if err != nil || u.Scheme != "https" || !azureOpenAIHostPattern.MatchString(u.Hostname()) ||
		(u.Host != u.Hostname() && u.Host != u.Hostname()+":443") ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") {
		return fmt.Errorf("AZURE_OPENAI_EMBEDDING_ENDPOINT must be https://<resource>.openai.azure.com with no path or query and default port 443")
	}
	if c.ModelName == "" || strings.ContainsAny(c.ModelName, "<>\r\n") {
		return fmt.Errorf("AZURE_OPENAI_EMBEDDING_MODEL must name your deployed embedding model")
	}
	return nil
}

// Reject TLS bypasses before the driver can resolve DNS or acquire credentials.
func validateConnectionURI(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "mongodb+srv" || !azureHostPattern.MatchString(u.Hostname()) || u.Port() != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return fmt.Errorf("MONGO_CONNECTION_STRING must be an Azure DocumentDB mongodb+srv URI without a database path")
	}
	if u.User == nil {
		return fmt.Errorf("connection-string mode requires explicit username and password")
	}
	password, set := u.User.Password()
	if u.User.Username() == "" || !set || password == "" {
		return fmt.Errorf("connection-string mode requires explicit username and password")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return fmt.Errorf("invalid connection string options")
	}
	seen := map[string]bool{}
	for k, values := range q {
		key := strings.ToLower(k)
		if seen[key] || len(values) != 1 {
			return fmt.Errorf("duplicate connection string option")
		}
		seen[key] = true
		v := strings.ToLower(values[0])
		switch key {
		case "tls", "ssl":
			if v != "true" {
				return fmt.Errorf("TLS must be enabled")
			}
		case "tlsinsecure", "tlsallowinvalidcertificates", "tlsallowinvalidhostnames":
			if v != "false" {
				return fmt.Errorf("TLS certificate and hostname verification must remain enabled")
			}
		case "authmechanism":
			if v != "scram-sha-256" && v != "scram-sha-1" {
				return fmt.Errorf("connection-string mode only supports SCRAM credentials")
			}
		case "authmechanismproperties":
			return fmt.Errorf("authMechanismProperties is not supported in connection-string mode")
		}
	}
	return nil
}

func (c Config) Collection(algorithm string) string {
	if c.CollectionName != "" {
		return c.CollectionName
	}
	return c.CollectionPrefix + "_" + algorithm
}

func (c Config) ValidateWriteScope(algorithm string) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if !c.AllowWrites {
		return fmt.Errorf("writes disabled: explicitly set VECTOR_SEARCH_ALLOW_WRITES=true for an approved disposable database")
	}
	if algorithm != "ivf" && algorithm != "hnsw" && algorithm != "diskann" {
		return fmt.Errorf("unsupported vector algorithm")
	}
	return nil
}
