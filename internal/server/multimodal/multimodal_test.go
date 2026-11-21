package multimodal

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/inferencepath/membrane/internal/membrane/permeability"
)

func TestParseMessagesValidatesMediaAndUserTurns(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\npayload")
	url := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	msgs, err := parseMessages([]wireMessage{{
		Role: "user", Content: []byte(`[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"` + url + `"}}]`),
	}})
	if err != nil || len(msgs) != 1 || len(msgs[0].Parts) != 2 || msgs[0].Parts[1].Format != "png" {
		t.Fatalf("parseMessages() = %#v, %v", msgs, err)
	}
	_, err = parseMessages([]wireMessage{{Role: "assistant", Content: []byte(`[{"type":"image_url","image_url":{"url":"` + url + `"}}]`)}})
	if err == nil || !strings.Contains(err.Error(), "only user turns") {
		t.Fatalf("assistant media error = %v", err)
	}
}

func TestCheckPermissionsFailsClosedForMedia(t *testing.T) {
	msgs, err := parseMessages([]wireMessage{{Role: "user", Content: []byte(`"hello"`)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := checkPermissions(permeability.DirectionSets{}, msgs, false); err == nil {
		t.Fatal("expected messages to be denied without outbound permission")
	}
	if err := checkPermissions(permeability.DirectionSets{
		Outbound: permeability.Outbound{Allow: []string{"messages"}},
	}, msgs, false); err != nil {
		t.Fatalf("text-only request = %v", err)
	}
}
