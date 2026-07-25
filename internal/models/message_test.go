package models_test

import (
	"encoding/json"
	"testing"

	"opennexus/internal/models"
)

func TestMessage_JSONRoundTrip(t *testing.T) {
	m := &models.Message{
		ID:          1,
		SessionID:   "acp-test-1",
		DBSessionID: 1,
		Role:        models.MessageRoleUser,
		Kind:        models.MessageKindUserMessageChunk,
		Content:     "你好",
		RawJSON:     `{"sessionUpdate":"user_message_chunk"}`,
		Sequence:    1,
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got models.Message
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Content != "你好" || got.SessionID != "acp-test-1" || got.Sequence != 1 {
		t.Errorf("round-trip 失败: %+v", got)
	}
}
