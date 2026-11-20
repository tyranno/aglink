package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The web settings page reads 204 as "saved and applied"; a save whose live
// apply failed must not look like that, and must carry the apply error.
func TestWriteConfigSaveReply(t *testing.T) {
	cases := []struct {
		name, reply string
		status      int
		bodyHas     string
	}{
		{"applied", `{"ok":true,"applied":true}`, http.StatusNoContent, ""},
		{"legacy ok", `{"ok":true}`, http.StatusNoContent, ""},
		{"apply failed", `{"ok":true,"applied":false,"applyError":"boom"}`, http.StatusOK, "적용하지 못했습니다: boom"},
		{"not saved", `{"ok":false,"error":"bad yaml"}`, http.StatusBadRequest, "bad yaml"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		writeConfigSaveReply(rec, []byte(c.reply))
		if rec.Code != c.status {
			t.Errorf("%s: status = %d, want %d", c.name, rec.Code, c.status)
		}
		if c.bodyHas != "" && !strings.Contains(rec.Body.String(), c.bodyHas) {
			t.Errorf("%s: body = %q, want it to contain %q", c.name, rec.Body.String(), c.bodyHas)
		}
	}
}
