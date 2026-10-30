// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeploymentTaskCapabilityAndRequestRejection(t *testing.T) {
	services := newTestServices(t, "")
	request := httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil)
	response := httptest.NewRecorder()
	services.handler.ServeHTTP(response, request)
	var capability struct {
		DeploymentTasks struct {
			Versions []int    `json:"versions"`
			Phases   []string `json:"phases"`
			Tasks    []any    `json:"tasks"`
		} `json:"deploymentTasks"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &capability); err != nil {
		t.Fatal(err)
	}
	if len(capability.DeploymentTasks.Versions) != 1 || len(capability.DeploymentTasks.Phases) != 4 || capability.DeploymentTasks.Tasks == nil || len(capability.DeploymentTasks.Tasks) != 0 {
		t.Fatalf("capability=%s", response.Body)
	}
	for _, body := range []string{
		`{"artifactId":"synthetic","deploymentTasks":{"version":1,"beforeBoot":[{"id":"prepare","task":"undefined","parameters":{}}]}}`,
		`{"artifactId":"synthetic","deploymentTasks":{"version":1,"shell":"no"}}`,
		`{"artifactId":"synthetic","deploymentTasks":{"version":1,"beforeBoot":[{"id":"prepare","task":"undefined","parameters":{},"command":"no"}]}}`,
	} {
		request = httptest.NewRequest(http.MethodPost, "/api/v1/jobs", strings.NewReader(body))
		response = httptest.NewRecorder()
		services.handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", response.Code, response.Body)
		}
	}
	if len(services.jobs.List()) != 0 {
		t.Fatal("invalid jobs persisted")
	}
}
