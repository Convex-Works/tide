package lobby

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWaitSSEApproveDenyAndExpiry(t *testing.T) {
	tests := []struct {
		name  string
		act   func(*Registry, string) error
		event string
		data  string
	}{
		{
			name: "approve",
			act: func(registry *Registry, id string) error {
				return registry.Approve(id, "signed-token", "ws://public.example")
			},
			event: "event: admitted", data: `"token":"signed-token"`,
		},
		{
			name: "deny", act: func(registry *Registry, id string) error { return registry.Deny(id) },
			event: "event: denied", data: "data: {}",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := NewRegistry(time.Minute)
			request, err := registry.Add("calm-otter-412", "Guest")
			if err != nil {
				t.Fatal(err)
			}
			if err := test.act(registry, request.ID); err != nil {
				t.Fatal(err)
			}
			handler := &Handler{registry: registry}
			recorder := httptest.NewRecorder()
			httpRequest := httptest.NewRequest("GET", "/api/lobby/"+request.ID+"/wait", nil)
			httpRequest.SetPathValue("id", request.ID)
			handler.Wait(recorder, httpRequest)
			body := recorder.Body.String()
			if !strings.Contains(body, test.event) || !strings.Contains(body, test.data) {
				t.Fatalf("SSE body = %q", body)
			}
		})
	}

	t.Run("expiry", func(t *testing.T) {
		registry := NewRegistry(10 * time.Millisecond)
		request, err := registry.Add("calm-otter-412", "Guest")
		if err != nil {
			t.Fatal(err)
		}
		handler := &Handler{registry: registry}
		recorder := httptest.NewRecorder()
		httpRequest := httptest.NewRequest("GET", "/api/lobby/"+request.ID+"/wait", nil)
		httpRequest.SetPathValue("id", request.ID)
		handler.Wait(recorder, httpRequest)
		body := recorder.Body.String()
		if !strings.Contains(body, "event: waiting") || !strings.Contains(body, "event: denied") {
			t.Fatalf("SSE body = %q", body)
		}
	})
}
