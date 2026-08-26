//go:build unit

package handler

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"ikik-api/internal/config"
)

func TestParseGoogleOAuthProfileAllowsMissingUnverifiedEmail(t *testing.T) {
	profile, err := parseGoogleOAuthProfile(`{"sub":"google-subject","name":"No Email"}`)

	require.NoError(t, err)
	require.Equal(t, "google-subject", profile.Subject)
	require.Empty(t, profile.Email)
	require.False(t, profile.EmailVerified)
}

func TestParseGoogleOAuthProfilePreservesOptionalUpstreamEmail(t *testing.T) {
	profile, err := parseGoogleOAuthProfile(`{"sub":"google-subject","email":"User@Example.com","email_verified":false,"given_name":"User"}`)

	require.NoError(t, err)
	require.Equal(t, "User@Example.com", profile.Email)
	require.False(t, profile.EmailVerified)
	require.Equal(t, false, profile.Metadata["email_verified"])
}

func TestParseGitHubOAuthProfileAllowsMissingEmailEndpoint(t *testing.T) {
	profile, err := parseGitHubOAuthProfile(
		context.Background(),
		config.EmailOAuthProviderConfig{},
		&emailOAuthTokenResponse{AccessToken: "token"},
		`{"id":12345,"login":"octocat"}`,
	)

	require.NoError(t, err)
	require.Equal(t, "12345", profile.Subject)
	require.Empty(t, profile.Email)
	require.False(t, profile.EmailVerified)
}

func TestParseGitHubOAuthProfilePreservesOptionalUpstreamEmail(t *testing.T) {
	profile, err := parseGitHubOAuthProfile(
		context.Background(),
		config.EmailOAuthProviderConfig{},
		&emailOAuthTokenResponse{AccessToken: "token"},
		`{"id":12345,"login":"octocat","email":"octocat@example.com"}`,
	)

	require.NoError(t, err)
	require.Equal(t, "octocat@example.com", profile.Email)
	require.False(t, profile.EmailVerified)
}

func TestEmailOAuthPendingIdentityUsesOIDCStorageProvider(t *testing.T) {
	identity := emailOAuthPendingIdentity("GitHub", " subject ")

	require.Equal(t, "oidc", identity.ProviderType)
	require.Equal(t, "github", identity.ProviderKey)
	require.Equal(t, "subject", identity.ProviderSubject)
}

func TestEmailOAuthPendingProviderUsesKeyThenClaims(t *testing.T) {
	require.Equal(t, "github", emailOAuthPendingProvider(" GitHub ", map[string]any{"provider": "google"}))
	require.Equal(t, "google", emailOAuthPendingProvider("", map[string]any{"provider": " Google "}))
}
