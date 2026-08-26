//go:build unit

package repository

import (
	"testing"

	"github.com/stretchr/testify/require"
	"ikik-api/internal/service"
)

func TestNormalizeEmailAuthIdentitySubjectSkipsGitHubAndGoogleSyntheticEmails(t *testing.T) {
	require.Empty(t, normalizeEmailAuthIdentitySubject("github-user"+service.GitHubConnectSyntheticEmailDomain))
	require.Empty(t, normalizeEmailAuthIdentitySubject("google-user"+service.GoogleConnectSyntheticEmailDomain))
}
