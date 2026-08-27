//go:build unit

package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	middleware2 "ikik-api/internal/server/middleware"
	"ikik-api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestUserAccountProbeModelListAllowsAuthenticatedUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	accountTestService := service.NewAccountTestService(nil, nil, nil, nil, nil, nil, nil)
	handler := NewUserAccountHandler(nil, nil, accountTestService, nil, nil, nil, nil)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/accounts/model-probe/list", strings.NewReader(`{"platform":"anthropic","api_key":"sk-ant-test"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 42})

	handler.ProbeModelList(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"code":0`)
	require.Contains(t, recorder.Body.String(), `"models"`)
}

func TestUserAccountProbeModelListRequiresAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	accountTestService := service.NewAccountTestService(nil, nil, nil, nil, nil, nil, nil)
	handler := NewUserAccountHandler(nil, nil, accountTestService, nil, nil, nil, nil)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/accounts/model-probe/list", strings.NewReader(`{"platform":"anthropic","api_key":"sk-ant-test"}`))
	c.Request.Header.Set("Content-Type", "application/json")

	handler.ProbeModelList(c)

	require.Equal(t, http.StatusUnauthorized, recorder.Code)
}
