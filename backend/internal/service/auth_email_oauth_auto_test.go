//go:build unit

package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOAuthSyntheticEmailUsesReservedProviderDomains(t *testing.T) {
	tests := []struct {
		provider string
		domain   string
	}{
		{provider: "github", domain: GitHubConnectSyntheticEmailDomain},
		{provider: "google", domain: GoogleConnectSyntheticEmailDomain},
	}
	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			email, err := OAuthSyntheticEmail(tt.provider, tt.provider, "subject-123")
			require.NoError(t, err)
			require.True(t, strings.HasSuffix(email, tt.domain))
			require.True(t, isReservedEmail(email))
			require.Equal(t, tt.provider, inferLegacySignupSource(email))

			again, err := OAuthSyntheticEmail(tt.provider, tt.provider, "subject-123")
			require.NoError(t, err)
			require.Equal(t, email, again)
		})
	}
}

func TestOAuthSyntheticEmailScopesByProviderAndSubject(t *testing.T) {
	githubA, err := OAuthSyntheticEmail("github", "github", "subject-a")
	require.NoError(t, err)
	githubB, err := OAuthSyntheticEmail("github", "github", "subject-b")
	require.NoError(t, err)
	googleA, err := OAuthSyntheticEmail("google", "google", "subject-a")
	require.NoError(t, err)

	require.NotEqual(t, githubA, githubB)
	require.NotEqual(t, githubA, googleA)
}
