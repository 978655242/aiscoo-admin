package service

import (
	"context"
	"fmt"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

func init() {
	relayconvert.SetMediaResolver(relayconvert.MediaResolver{
		// relayconvert is gin-free; recover the gin context when the caller
		// passed one so file caching/cleanup keeps working.
		GetBase64Data: func(ctx context.Context, source types.FileSource, reason ...string) (string, string, error) {
			ginCtx, _ := ctx.(*gin.Context)
			return GetBase64Data(ginCtx, source, reason...)
		},
		DecodeBase64FileData: DecodeBase64FileData,
	})
}

func ConvertRequest(c *gin.Context, info *relaycommon.RelayInfo, target types.RelayFormat, request any) (*relayconvert.RequestResult, error) {
	result, err := relayconvert.ConvertRequest(c, info, target, request)
	if result != nil {
		preserveManagedClaudeMaxTokens(info, request, result)
		info.RecordConversionDiagnostics(c, result.Diagnostics)
	}
	return result, err
}

func ConvertRequestByID(c *gin.Context, info *relaycommon.RelayInfo, converter string, request any) (*relayconvert.RequestResult, error) {
	result, err := relayconvert.ConvertRequestByID(c, info, converter, request)
	if result != nil {
		preserveManagedClaudeMaxTokens(info, request, result)
		info.RecordConversionDiagnostics(c, result.Diagnostics)
	}
	return result, err
}

func ConvertRequestVia(c *gin.Context, info *relaycommon.RelayInfo, request any, path ...types.RelayFormat) (*relayconvert.RequestResult, error) {
	result, err := relayconvert.ConvertRequestVia(c, info, request, path...)
	if result != nil {
		preserveManagedClaudeMaxTokens(info, request, result)
		info.RecordConversionDiagnostics(c, result.Diagnostics)
	}
	return result, err
}

func preserveManagedClaudeMaxTokens(info *relaycommon.RelayInfo, request any, result *relayconvert.RequestResult) {
	if info == nil || info.ClientPurpose != model.ClientPurposeXCode {
		return
	}
	claudeRequest, ok := result.Value.(*dto.ClaudeRequest)
	if !ok {
		return
	}
	var tokens *uint
	switch request := request.(type) {
	case *dto.GeneralOpenAIRequest:
		tokens = request.MaxCompletionTokens
		if tokens == nil {
			tokens = request.MaxTokens
		}
	case dto.GeneralOpenAIRequest:
		tokens = request.MaxCompletionTokens
		if tokens == nil {
			tokens = request.MaxTokens
		}
	case *dto.OpenAIResponsesRequest:
		tokens = request.MaxOutputTokens
	case dto.OpenAIResponsesRequest:
		tokens = request.MaxOutputTokens
	case *dto.ClaudeRequest:
		tokens = request.MaxTokens
	case dto.ClaudeRequest:
		tokens = request.MaxTokens
	}
	if tokens != nil && *tokens == 0 {
		// Managed admission approved the supplied limit, not the converter's default.
		claudeRequest.MaxTokens = tokens
	}
}

func ClaudeToOpenAIRequest(claudeRequest dto.ClaudeRequest, info *relaycommon.RelayInfo) (*dto.GeneralOpenAIRequest, error) {
	result, err := ConvertRequest(nil, info, types.RelayFormatOpenAI, &claudeRequest)
	if err != nil {
		return nil, err
	}
	openAIRequest, ok := result.Value.(*dto.GeneralOpenAIRequest)
	if !ok {
		return nil, fmt.Errorf("expected OpenAI chat completions request, got %T", result.Value)
	}
	return openAIRequest, nil
}

func GeminiToOpenAIRequest(geminiRequest *dto.GeminiChatRequest, info *relaycommon.RelayInfo) (*dto.GeneralOpenAIRequest, error) {
	result, err := ConvertRequest(nil, info, types.RelayFormatOpenAI, geminiRequest)
	if err != nil {
		return nil, err
	}
	openAIRequest, ok := result.Value.(*dto.GeneralOpenAIRequest)
	if !ok {
		return nil, fmt.Errorf("expected OpenAI chat completions request, got %T", result.Value)
	}
	return openAIRequest, nil
}
