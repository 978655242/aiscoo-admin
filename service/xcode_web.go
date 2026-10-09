package service

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

func XCodeWebCookieName(origin string) string {
	if strings.HasPrefix(origin, "https://") {
		return "__Secure-new_api_xcode_web"
	}
	return "new_api_xcode_web"
}

var ErrXCodeWebOriginForbidden = errors.New("xcode web request origin is not permitted")

type xcodeWebHandoffState struct {
	AuthIdentity
	ClientPurpose string `json:"client_purpose"`
}

// ValidateXCodeWebRequest uses configured authority, never forwarded host headers.
func ValidateXCodeWebRequest(request *http.Request) (string, error) {
	origin, err := common.GetXCodePublicOrigin()
	if err != nil {
		return "", err
	}
	configured, err := url.Parse(origin)
	if err != nil {
		return "", err
	}
	if request == nil || !strings.EqualFold(request.Host, configured.Host) {
		return "", ErrXCodeWebOriginForbidden
	}
	origins := request.Header.Values("Origin")
	if len(origins) > 1 {
		return "", ErrXCodeWebOriginForbidden
	}
	if len(origins) == 1 {
		actual, err := common.NormalizeOrigin(origins[0])
		expected, expectedErr := common.NormalizeOrigin(origin)
		if err != nil || expectedErr != nil || actual != expected {
			return "", ErrXCodeWebOriginForbidden
		}
	}
	sites := request.Header.Values("Sec-Fetch-Site")
	if len(sites) > 1 {
		return "", ErrXCodeWebOriginForbidden
	}
	if len(sites) == 1 && sites[0] != "same-origin" && sites[0] != "none" {
		return "", ErrXCodeWebOriginForbidden
	}
	// A system-browser navigation has no Origin; a legacy browser may use Referer.
	if len(origins) == 0 && request.Header.Get("Referer") != "" {
		referers := request.Header.Values("Referer")
		if len(referers) != 1 {
			return "", ErrXCodeWebOriginForbidden
		}
		referer, err := url.Parse(referers[0])
		if err != nil || referer.User != nil || referer.Host == "" {
			return "", ErrXCodeWebOriginForbidden
		}
		actual, err := common.NormalizeOrigin(referer.Scheme + "://" + referer.Host)
		expected, expectedErr := common.NormalizeOrigin(origin)
		if err != nil || expectedErr != nil || actual != expected {
			return "", ErrXCodeWebOriginForbidden
		}
	}
	return origin, nil
}

func CreateXCodeWebHandoff(identity AuthIdentity) (*dto.XCodeWebHandoffResponse, error) {
	if identity.UserID <= 0 || identity.SessionID == "" || identity.UserAuthVersion <= 0 || identity.SessionVersion <= 0 {
		return nil, ErrLoginSessionInvalid
	}
	origin, err := common.GetXCodePublicOrigin()
	if err != nil {
		return nil, err
	}
	if model.DB == nil {
		return nil, ErrXCodeBackendUnavailable
	}
	var response dto.XCodeWebHandoffResponse
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		_, session, err := model.ValidateXCodeSourceSessionWithTx(tx, identity.UserID, identity.SessionID,
			identity.UserAuthVersion, identity.SessionVersion, model.ClientPurposeXCode)
		if err != nil {
			return xcodeSourceError(err, ErrLoginSessionRevoked)
		}
		state := xcodeWebHandoffState{
			AuthIdentity: AuthIdentity{UserID: session.UserID, SessionID: session.SID,
				UserAuthVersion: session.UserAuthVersion, SessionVersion: session.Version},
			ClientPurpose: session.ClientPurpose,
		}
		payload, err := common.Marshal(state)
		if err != nil {
			return err
		}
		expiresAt := time.Unix(min(time.Now().Add(time.Minute).Unix(), session.ExpiresAt), 0)
		code, flow, err := model.CreateAuthFlowWithTx(tx, model.AuthFlowCreate{
			Purpose: model.AuthFlowPurposeXCodeWebHandoff, UserId: session.UserID, SessionId: session.SID,
			Payload: string(payload), ExpiresAt: expiresAt,
		})
		if err != nil {
			return err
		}
		if _, _, err := model.ValidateXCodeSourceSessionWithTx(tx, state.UserID, state.SessionID,
			state.UserAuthVersion, state.SessionVersion, model.ClientPurposeXCode); err != nil {
			return xcodeSourceError(err, ErrLoginSessionRevoked)
		}
		response = dto.XCodeWebHandoffResponse{SchemaVersion: 1, ServerTime: time.Now().UnixMilli(),
			URL: origin + "/xcode/plans?" + url.Values{"handoff": {code}}.Encode(), ExpiresAt: flow.ExpiresAt.UnixMilli()}
		return nil
	})
	if err != nil {
		return nil, xcodeFlowError(err, ErrXCodeAuthCodeInvalid)
	}
	return &response, nil
}

func xcodeWebHandoffBinding(flow *model.AuthFlow) (xcodeWebHandoffState, error) {
	var state xcodeWebHandoffState
	if flow == nil || flow.Purpose != model.AuthFlowPurposeXCodeWebHandoff || flow.Provider != "" || flow.Intent != "" ||
		common.UnmarshalJsonStr(flow.Payload, &state) != nil || state.ClientPurpose != model.ClientPurposeXCode ||
		state.UserID <= 0 || state.SessionID == "" || state.UserAuthVersion <= 0 || state.SessionVersion <= 0 ||
		state.UserID != flow.UserId || state.SessionID != flow.SessionId {
		return state, ErrXCodeAuthCodeInvalid
	}
	return state, nil
}

func ConsumeXCodeWebHandoff(token string) (string, int64, error) {
	if !xcodeUnreserved(token, 43, 43) {
		return "", 0, ErrXCodeAuthCodeInvalid
	}
	if model.DB == nil {
		return "", 0, ErrXCodeBackendUnavailable
	}
	match := model.AuthFlowMatch{Purpose: model.AuthFlowPurposeXCodeWebHandoff}
	bound, err := model.GetAuthFlow(token, match)
	if err != nil {
		return "", 0, xcodeFlowError(err, ErrXCodeAuthCodeInvalid)
	}
	state, err := xcodeWebHandoffBinding(bound)
	if err != nil {
		return "", 0, err
	}
	var webToken string
	var expiresAt int64
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		// Owner -> source SID -> flow; refresh may rotate the same SID before consumption.
		_, _, err := model.ValidateXCodeSourceSessionWithTx(tx, state.UserID, state.SessionID,
			state.UserAuthVersion, 0, model.ClientPurposeXCode)
		if err != nil {
			return xcodeSourceError(err, ErrLoginSessionRevoked)
		}
		match.UserId, match.SessionId = state.UserID, state.SessionID
		_, err = model.ConsumeAuthFlowWithActionWithTx(tx, token, match, func(tx *gorm.DB, flow *model.AuthFlow) error {
			if flow.Id != bound.Id || flow.Payload != bound.Payload || !flow.ExpiresAt.Equal(bound.ExpiresAt) {
				return ErrXCodeAuthCodeInvalid
			}
			current, err := xcodeWebHandoffBinding(flow)
			if err != nil {
				return err
			}
			_, session, err := model.ValidateXCodeSourceSessionWithTx(tx, current.UserID, current.SessionID,
				current.UserAuthVersion, 0, model.ClientPurposeXCode)
			if err != nil {
				return xcodeSourceError(err, ErrLoginSessionRevoked)
			}
			if flow.ExpiresAt.After(time.Unix(session.ExpiresAt, 0)) {
				return ErrXCodeAuthCodeInvalid
			}
			webToken, expiresAt, err = IssueXCodeWebToken(session)
			if err != nil {
				return err
			}
			_, _, err = model.ValidateXCodeSourceSessionWithTx(tx, current.UserID, current.SessionID,
				current.UserAuthVersion, session.Version, model.ClientPurposeXCode)
			return xcodeSourceError(err, ErrLoginSessionRevoked)
		})
		return err
	})
	if err != nil {
		return "", 0, xcodeFlowError(err, ErrXCodeAuthCodeInvalid)
	}
	return webToken, expiresAt, nil
}

func GetXCodeWebView(userID int) (*dto.XCodeWebView, error) {
	plans, err := GetXCodePlans()
	if err != nil {
		return nil, err
	}
	entitlements, err := GetXCodeEntitlements(userID)
	if err != nil {
		return nil, err
	}
	display, err := GetXCodeQuotaDisplay()
	if err != nil {
		return nil, err
	}
	return &dto.XCodeWebView{SchemaVersion: 1, ServerTime: time.Now().UnixMilli(),
		Plans: plans, Entitlements: entitlements, Display: display}, nil
}
