//go:build unit

package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"ikik-api/ent/authidentity"
	"ikik-api/internal/service"
)

type emailOAuthRefreshTokenCacheStub struct{}

func (*emailOAuthRefreshTokenCacheStub) StoreRefreshToken(context.Context, string, *service.RefreshTokenData, time.Duration) error {
	return nil
}

func (*emailOAuthRefreshTokenCacheStub) GetRefreshToken(context.Context, string) (*service.RefreshTokenData, error) {
	return nil, service.ErrRefreshTokenNotFound
}

func (*emailOAuthRefreshTokenCacheStub) DeleteRefreshToken(context.Context, string) error {
	return nil
}

func (*emailOAuthRefreshTokenCacheStub) DeleteUserRefreshTokens(context.Context, int64) error {
	return nil
}

func (*emailOAuthRefreshTokenCacheStub) DeleteTokenFamily(context.Context, string) error {
	return nil
}

func (*emailOAuthRefreshTokenCacheStub) AddToUserTokenSet(context.Context, int64, string, time.Duration) error {
	return nil
}

func (*emailOAuthRefreshTokenCacheStub) AddToFamilyTokenSet(context.Context, string, string, time.Duration) error {
	return nil
}

func (*emailOAuthRefreshTokenCacheStub) GetUserTokenHashes(context.Context, int64) ([]string, error) {
	return nil, nil
}

func (*emailOAuthRefreshTokenCacheStub) GetFamilyTokenHashes(context.Context, string) ([]string, error) {
	return nil, nil
}

func (*emailOAuthRefreshTokenCacheStub) IsTokenInFamily(context.Context, string, string) (bool, error) {
	return false, nil
}

func TestEmailOAuthCreatesSyntheticAccountWithoutAdoptingUpstreamEmail(t *testing.T) {
	svc, repo, client := newAuthServiceWithEnt(t, map[string]string{
		service.SettingKeyRegistrationEnabled: "true",
	}, nil)
	ctx := context.Background()

	existing := &service.User{
		Email:        "shared@example.com",
		PasswordHash: "hash",
		Role:         service.RoleUser,
		Status:       service.StatusActive,
	}
	require.NoError(t, repo.Create(ctx, existing))

	_, created, err := svc.LoginOrRegisterVerifiedEmailOAuth(ctx, service.EmailOAuthIdentityInput{
		ProviderType:    "github",
		ProviderKey:     "github",
		ProviderSubject: "github-subject",
		Email:           "shared@example.com",
		EmailVerified:   false,
		Username:        "octocat",
	})
	require.NoError(t, err)
	require.NotEqual(t, existing.ID, created.ID)
	require.Contains(t, created.Email, service.GitHubConnectSyntheticEmailDomain)

	identity, err := client.AuthIdentity.Query().Where(
		authidentity.ProviderTypeEQ("oidc"),
		authidentity.ProviderKeyEQ("github"),
		authidentity.ProviderSubjectEQ("github-subject"),
	).Only(ctx)
	require.NoError(t, err)
	require.Equal(t, created.ID, identity.UserID)
	require.Equal(t, "shared@example.com", identity.Metadata["upstream_email"])
	require.Equal(t, false, identity.Metadata["email_verified"])
}

func TestEmailOAuthInvitationGateRunsBeforeSyntheticAccountCreation(t *testing.T) {
	svc, _, client := newAuthServiceWithEnt(t, map[string]string{
		service.SettingKeyRegistrationEnabled:   "true",
		service.SettingKeyInvitationCodeEnabled: "true",
	}, nil)
	ctx := context.Background()

	_, user, err := svc.LoginOrRegisterVerifiedEmailOAuth(ctx, service.EmailOAuthIdentityInput{
		ProviderType:    "google",
		ProviderKey:     "google",
		ProviderSubject: "google-subject",
	})
	require.ErrorIs(t, err, service.ErrOAuthInvitationRequired)
	require.Nil(t, user)
	count, countErr := client.User.Query().Count(ctx)
	require.NoError(t, countErr)
	require.Zero(t, count)
}
