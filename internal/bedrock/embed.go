package bedrock

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
)

// Tipos para a API de embedding do Amazon Titan v2 sobre Bedrock.
// Cohere v3 e Titan v1 têm payloads compatíveis o suficiente para reaproveitar
// inputText/embedding, mas oficialmente suportamos Titan v2.

type embedRequest struct {
	InputText string `json:"inputText"`
}

type embedResponse struct {
	Embedding           []float32 `json:"embedding"`
	InputTextTokenCount int       `json:"inputTextTokenCount"`
}

// Embed gera embeddings para um ou mais textos via Bedrock. Titan não suporta
// batch nativo, então fazemos sequencial. Mantém a mesma assinatura do antigo
// openrouter.Embed para minimizar mudanças.
func (c *Client) Embed(ctx context.Context, model string, inputs []string) ([][]float32, error) {
	if model == "" {
		model = DefaultEmbedModel
	}

	out := make([][]float32, len(inputs))
	for i, text := range inputs {
		v, err := c.embedOne(ctx, model, text)
		if err != nil {
			return nil, fmt.Errorf("bedrock embed [%d]: %w", i, err)
		}
		out[i] = v
	}
	return out, nil
}

// EmbedOne é um atalho para embedding de um único texto.
func (c *Client) EmbedOne(ctx context.Context, model, text string) ([]float32, error) {
	if model == "" {
		model = DefaultEmbedModel
	}
	return c.embedOne(ctx, model, text)
}

func (c *Client) embedOne(ctx context.Context, model, text string) ([]float32, error) {
	raw, err := json.Marshal(embedRequest{InputText: text})
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	out, err := c.rt.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
		ModelId:     aws.String(model),
		ContentType: aws.String("application/json"),
		Accept:      aws.String("application/json"),
		Body:        raw,
	})
	if err != nil {
		return nil, fmt.Errorf("invoke %s: %w", model, err)
	}

	var resp embedResponse
	if err := json.Unmarshal(out.Body, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	if len(resp.Embedding) == 0 {
		return nil, fmt.Errorf("empty embedding from %s", model)
	}
	return resp.Embedding, nil
}
