package bedrock

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
)

// Chat envia uma requisição de chat completion ao Bedrock e devolve o resultado
// no mesmo shape que o antigo openrouter.ChatResponse.
func (c *Client) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	if req.Model == "" {
		req.Model = DefaultChatModel
	}

	body, err := toAnthropicBody(req)
	if err != nil {
		return nil, fmt.Errorf("bedrock chat: %w", err)
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("bedrock chat: marshal body: %w", err)
	}

	out, err := c.rt.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
		ModelId:     aws.String(req.Model),
		ContentType: aws.String("application/json"),
		Accept:      aws.String("application/json"),
		Body:        raw,
	})
	if err != nil {
		return nil, fmt.Errorf("bedrock chat: invoke model %s: %w", req.Model, err)
	}

	var resp anthropicResponse
	if err := json.Unmarshal(out.Body, &resp); err != nil {
		return nil, fmt.Errorf("bedrock chat: unmarshal response: %w", err)
	}

	converted := fromAnthropicResponse(resp)
	if converted.Model == "" {
		converted.Model = req.Model
	}
	return &converted, nil
}
