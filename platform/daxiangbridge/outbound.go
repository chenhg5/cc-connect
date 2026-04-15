package daxiangbridge

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// generateCredential produces an AES-GCM credential for the given clientId and timestamp.
// hexSecret must be a 32-char hex string (16 bytes = AES-128).
func generateCredential(clientID string, ts int64, hexSecret string) (string, error) {
	key, err := hex.DecodeString(hexSecret)
	if err != nil {
		return "", fmt.Errorf("daxiangbridge: invalid secret: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	plaintext := []byte(fmt.Sprintf("%s:%d", clientID, ts))
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

func buildFrame(frameType, requestID, sessionID string, payload any) BridgeFrame {
	raw, _ := json.Marshal(payload)
	return BridgeFrame{
		Type:      frameType,
		RequestID: requestID,
		SessionID: sessionID,
		Ts:        time.Now().UnixMilli(),
		Payload:   raw,
	}
}

func buildFinalReplyFrame(requestID, sessionID, text string) BridgeFrame {
	return buildFrame(FrameTypeAgentReplyFinal, requestID, sessionID, AgentReplyPayload{Text: text})
}

func buildStartFrame(requestID, sessionID string) BridgeFrame {
	return buildFrame(FrameTypeAgentReplyStart, requestID, sessionID, struct{}{})
}

func buildDeltaFrame(requestID, sessionID string, seq int, delta string) BridgeFrame {
	return buildFrame(FrameTypeAgentReplyDelta, requestID, sessionID, AgentDeltaPayload{Seq: seq, Delta: delta})
}

func buildEndFrame(requestID, sessionID, finalText string) BridgeFrame {
	return buildFrame(FrameTypeAgentReplyEnd, requestID, sessionID, AgentReplyPayload{Text: finalText})
}

func buildPermissionRequestFrame(requestID, sessionID, permissionID, toolName, action, reason string) BridgeFrame {
	return buildFrame(FrameTypeAgentPermissionRequest, requestID, sessionID, AgentPermissionRequestPayload{
		PermissionID: permissionID,
		ToolName:     toolName,
		Action:       action,
		Reason:       reason,
	})
}

func buildRegisterFrame(clientID string, botID int64, hexSecret string) (BridgeFrame, error) {
	ts := time.Now().UnixMilli()
	cred, err := generateCredential(clientID, ts, hexSecret)
	if err != nil {
		return BridgeFrame{}, err
	}
	return buildFrame(FrameTypeClientRegister, "", "", ClientRegisterPayload{
		ClientID:          clientID,
		BotID:             botID,
		ClientVersion:     "0.1.0",
		StreamCapable:     true,
		PermissionCapable: true,
		Credential:        cred,
		Timestamp:         ts,
	}), nil
}

func buildPingFrame() BridgeFrame {
	return BridgeFrame{Type: FrameTypePing, Ts: time.Now().UnixMilli()}
}
