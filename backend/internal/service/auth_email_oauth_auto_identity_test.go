//go:build unit

package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"ikik-api/ent/authidentity"
	infraerrors "ikik-api/internal/pkg/errors"
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

func TestOIDCDirectRegistrationIgnoresRegistrationAndInvitationSettings(t *testing.T) {
	svc, _, client := newAuthServiceWithEnt(t, map[string]string{
		service.SettingKeyRegistrationEnabled:   "false",
		service.SettingKeyInvitationCodeEnabled: "true",
	}, nil)
	ctx := context.Background()

	tokenPair, user, created, err := svc.CompletePendingOIDCOAuth(ctx, service.EmailOAuthIdentityInput{
		ProviderType:    "oidc",
		ProviderKey:     "https://issuer.example.com",
		ProviderSubject: "oidc-direct-subject",
		Username:        "oidc-user",
	}, "", "")
	require.NoError(t, err)
	require.True(t, created)
	require.NotNil(t, tokenPair)
	require.NotNil(t, user)

	identity, err := client.AuthIdentity.Query().Where(
		authidentity.ProviderTypeEQ("oidc"),
		authidentity.ProviderKeyEQ("https://issuer.example.com"),
		authidentity.ProviderSubjectEQ("oidc-direct-subject"),
	).Only(ctx)
	require.NoError(t, err)
	require.Equal(t, user.ID, identity.UserID)
}

func TestOIDCDirectRegistrationRecoversReservedSyntheticAccount(t *testing.T) {
	svc, repo, client := newAuthServiceWithEnt(t, map[string]string{
		service.SettingKeyRegistrationEnabled: "false",
	}, nil)
	ctx := context.Background()

	email, err := service.OAuthSyntheticEmail("oidc", "https://issuer.example.com", "oidc-reserved-subject")
	require.NoError(t, err)
	orphan := &service.User{
		Email:        email,
		Username:     "orphan-oidc-user",
		PasswordHash: "hash",
		Role:         service.RoleUser,
		Status:       service.StatusActive,
		SignupSource: "oidc",
	}
	require.NoError(t, repo.Create(ctx, orphan))

	tokenPair, user, created, err := svc.CompletePendingOIDCOAuth(ctx, service.EmailOAuthIdentityInput{
		ProviderType:    "oidc",
		ProviderKey:     "https://issuer.example.com",
		ProviderSubject: "oidc-reserved-subject",
		Username:        "oidc-user",
	}, "", "")
	require.NoError(t, err)
	require.False(t, created)
	require.NotNil(t, tokenPair)
	require.Equal(t, orphan.ID, user.ID)

	identity, err := client.AuthIdentity.Query().Where(
		authidentity.ProviderTypeEQ("oidc"),
		authidentity.ProviderKeyEQ("https://issuer.example.com"),
		authidentity.ProviderSubjectEQ("oidc-reserved-subject"),
	).Only(ctx)
	require.NoError(t, err)
	require.Equal(t, orphan.ID, identity.UserID)
}

func TestOIDCDirectRegistrationPolicyRejectsOtherProviders(t *testing.T) {
	providers := []string{"github", "google", "linuxdo", "wechat"}
	for _, provider := range providers {
		t.Run(provider, func(t *testing.T) {
			svc, _, client := newAuthServiceWithEnt(t, map[string]string{
				service.SettingKeyRegistrationEnabled:   "false",
				service.SettingKeyInvitationCodeEnabled: "true",
			}, nil)
			ctx := context.Background()
			input := service.EmailOAuthIdentityInput{
				ProviderType:    provider,
				ProviderKey:     provider,
				ProviderSubject: provider + "-policy-subject",
				Username:        provider + "-user",
			}

			_, _, _, err := svc.CompletePendingOIDCOAuth(ctx, input, "", "")
			require.Equal(t, "OAUTH_PROVIDER_INVALID", infraerrors.Reason(err))

			_, _, _, err = svc.CompletePendingEmailOAuthWithSignupCodes(ctx, input, "", "", "")
			require.ErrorIs(t, err, service.ErrRegDisabled)

			count, countErr := client.User.Query().Count(ctx)
			require.NoError(t, countErr)
			require.Zero(t, count)
		})
	}
}

func TestOIDCDirectRegistrationDoesNotRecoverForeignSyntheticAccount(t *testing.T) {
	svc, repo, client := newAuthServiceWithEnt(t, map[string]string{
		service.SettingKeyRegistrationEnabled: "false",
	}, nil)
	ctx := context.Background()

	email, err := service.OAuthSyntheticEmail("oidc", "https://issuer.example.com", "oidc-foreign-subject")
	require.NoError(t, err)
	foreign := &service.User{
		Email:        email,
		Username:     "foreign-user",
		PasswordHash: "hash",
		Role:         service.RoleUser,
		Status:       service.StatusActive,
		SignupSource: "email",
	}
	require.NoError(t, repo.Create(ctx, foreign))

	_, _, created, err := svc.CompletePendingOIDCOAuth(ctx, service.EmailOAuthIdentityInput{
		ProviderType:    "oidc",
		ProviderKey:     "https://issuer.example.com",
		ProviderSubject: "oidc-foreign-subject",
		Username:        "oidc-user",
	}, "", "")
	require.False(t, created)
	require.Equal(t, "OAUTH_SYNTHETIC_EMAIL_CONFLICT", infraerrors.Reason(err))

	identityCount, countErr := client.AuthIdentity.Query().Where(
		authidentity.ProviderTypeEQ("oidc"),
		authidentity.ProviderKeyEQ("https://issuer.example.com"),
		authidentity.ProviderSubjectEQ("oidc-foreign-subject"),
	).Count(ctx)
	require.NoError(t, countErr)
	require.Zero(t, identityCount)
}
