// Package bedrock implementa o backend de LLM (chat + embeddings) sobre o AWS
// Bedrock Runtime. Mantém os mesmos tipos públicos que o antigo pacote
// openrouter (formato OpenAI-like) para minimizar mudanças nas call sites; a
// tradução para o formato Anthropic-on-Bedrock acontece internamente.
package bedrock

import (
	"context"
	"fmt"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
)

const (
	DefaultRegion     = "us-east-1"
	DefaultChatModel  = "us.anthropic.claude-sonnet-4-6"
	DefaultEmbedModel = "amazon.titan-embed-text-v2:0"
	DefaultHaikuModel = "us.anthropic.claude-haiku-4-5"

	// anthropicVersion é o valor obrigatório no body do Bedrock para modelos
	// Anthropic invocados via InvokeModel.
	anthropicVersion = "bedrock-2023-05-31"
)

// Client expõe chat completions e embeddings via AWS Bedrock Runtime.
type Client struct {
	rt      *bedrockruntime.Client
	appName string
}

type Option func(*Client)

func WithAppName(name string) Option { return func(c *Client) { c.appName = name } }

// New cria um Client. Usa a default credentials chain do AWS SDK Go v2, que
// resolve env vars, ~/.aws/credentials, sessões SSO (aws sso login), assume-role,
// e roles de container/IMDS — nessa ordem. Se region for vazio, usa DefaultRegion.
func New(ctx context.Context, region string, opts ...Option) (*Client, error) {
	if region == "" {
		region = DefaultRegion
	}

	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("bedrock: load aws config: %w (rode `aws sso login` ou configure ~/.aws/credentials)", err)
	}

	c := &Client{
		rt:      bedrockruntime.NewFromConfig(cfg),
		appName: "agent-service",
	}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}
