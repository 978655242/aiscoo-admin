package controller

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/oauth"
	"github.com/gin-gonic/gin"
)

const weChatOpenBrowserProofHeader = "X-OAuth-Browser-Proof"

type weChatOpenFlowBinding struct {
	ProofHash         string `json:"proof_hash"`
	ConfigurationHash string `json:"configuration_hash"`
}

func weChatOpenProofHash(c *gin.Context) (string, error) {
	proof := c.GetHeader(weChatOpenBrowserProofHeader)
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(proof)
	if err != nil || len(decoded) != 32 || len(proof) != 43 {
		return "", model.ErrAuthFlowInvalid
	}
	hash := sha256.Sum256(decoded)
	return hex.EncodeToString(hash[:]), nil
}

func newWeChatOpenFlowBinding(c *gin.Context) (*weChatOpenFlowBinding, error) {
	proofHash, err := weChatOpenProofHash(c)
	if err != nil {
		return nil, err
	}
	fingerprint, err := oauth.WeChatOpenFingerprint()
	if err != nil {
		return nil, err
	}
	return &weChatOpenFlowBinding{ProofHash: proofHash, ConfigurationHash: fingerprint}, nil
}

func validateWeChatOpenFlow(c *gin.Context, flow *model.AuthFlow) error {
	var payload oauthFlowPayload
	if err := common.UnmarshalJsonStr(flow.Payload, &payload); err != nil || payload.WeChatOpen == nil {
		return model.ErrAuthFlowInvalid
	}
	current, err := newWeChatOpenFlowBinding(c)
	if err != nil {
		return model.ErrAuthFlowInvalid
	}
	// 微信 state 本身在回调 URL 中可见，必须额外证明来自发起浏览器，并拒绝配置变更前的旧流程。
	if subtle.ConstantTimeCompare([]byte(current.ProofHash), []byte(payload.WeChatOpen.ProofHash)) != 1 ||
		subtle.ConstantTimeCompare([]byte(current.ConfigurationHash), []byte(payload.WeChatOpen.ConfigurationHash)) != 1 {
		return model.ErrAuthFlowInvalid
	}
	return nil
}
