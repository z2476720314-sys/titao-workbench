package photocraft

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestObjectsDeduplicateByContentsNotFileName(t *testing.T) {
	layers := []layer{{ID: 1, Name: "插画", Kind: "Group", Children: []layer{
		{ID: 2, Kind: "Smart Object", SmartContents: &contents{ID: 7}},
		{ID: 3, Kind: "Smart Object", SmartContents: &contents{ID: 7}},
		{ID: 4, Kind: "Smart Object", SmartContents: &contents{ID: 8}},
	}}}
	got, err := findObjects(layers, "插画")
	if err != nil || len(got) != 2 || got[0].ID != 2 || got[1].ID != 4 {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if _, err := findObjects(layers, "不存在"); err == nil {
		t.Fatal("must reject absent group")
	}
	if countObjects(layers[0].Children) != 3 {
		t.Fatal("shared instances must each count as a placement")
	}
}

func TestToolResponseErrorsDoNotBecomeSuccess(t *testing.T) {
	for _, tc := range []struct {
		reply string
		fail  bool
	}{
		{`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"document\":0}"}],"isError":false}}`, false},
		{`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"permission denied"}],"isError":true}}`, true},
	} {
		var sent bytes.Buffer
		c := &client{enc: json.NewEncoder(&sent), dec: json.NewDecoder(strings.NewReader(tc.reply))}
		var out struct{ Document int }
		err := c.call("doc_new", map[string]any{}, &out)
		if (err != nil) != tc.fail {
			t.Fatalf("err=%v fail=%v", err, tc.fail)
		}
		if !strings.Contains(sent.String(), `"method":"tools/call"`) {
			t.Fatal(sent.String())
		}
	}
}
