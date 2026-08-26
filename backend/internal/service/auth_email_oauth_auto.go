package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	dbent "ikik-api/ent"
	"ikik-api/ent/authidentity"
	infraerrors "ikik-api/internal/pkg/errors"
	"ikik-api/internal/pkg/logger"
)

type EmailOAuthIdentityInput struct {
	ProviderType     string
	ProviderKey      string
	ProviderSubject  string
	Email            string
	EmailVerified    bool
	Username         string
	DisplayName      string
	AvatarURL        string
	UpstreamMetadata map[string]any
}

func (s *AuthService) LoginOrRegisterVerifiedEmailOAuth(ctx context.Context, input EmailOAuthIdentityInput) (*TokenPair, *User, error) {
	return s.loginOrRegisterVerifiedEmailOAuth(ctx, input, "", "", "")
}

func (s *AuthService) LoginOrRegisterVerifiedEmailOAuthWithInvitation(
	ctx context.Context,
	input EmailOAuthIdentityInput,
	invitationCode string,
	affiliateCode string,
) (*TokenPair, *User, error) {
	return s.loginOrRegisterVerifiedEmailOAuth(ctx, input, invitationCode, affiliateCode, "")
}

func (s *AuthService) LoginOrRegisterVerifiedEmailOAuthWithSignupCodes(
	ctx context.Context,
	input EmailOAuthIdentityInput,
	invitationCode string,
	affiliateCode string,
	promoCode string,
) (*TokenPair, *User, error) {
	return s.loginOrRegisterVerifiedEmailOAuth(ctx, input, invitationCode, affiliateCode, promoCode)
}

type emailOAuthRegistrationPolicy struct {
	ignoreRegistrationDisabled  bool
	ignoreInvitationRequirement bool
	recoverSyntheticAccount     bool
}

func (s *AuthService) loginOrRegisterVerifiedEmailOAuth(
	ctx context.Context,
	input EmailOAuthIdentityInput,
	invitationCode string,
	affiliateCode string,
	promoCode string,
) (*TokenPair, *User, error) {
	tokenPair, user, _, err := s.loginOrRegisterVerifiedEmailOAuthDetailed(ctx, input, invitationCode, affiliateCode, promoCode)
	return tokenPair, user, err
}

// CompletePendingEmailOAuthWithSignupCodes completes an invitation-gated
// GitHub/Google registration without asking for a local email or password.
func (s *AuthService) CompletePendingEmailOAuthWithSignupCodes(
	ctx context.Context,
	input EmailOAuthIdentityInput,
	invitationCode string,
	affiliateCode string,
	promoCode string,
) (*TokenPair, *User, bool, error) {
	return s.loginOrRegisterVerifiedEmailOAuthDetailed(ctx, input, invitationCode, affiliateCode, promoCode)
}

// CompletePendingOIDCOAuth creates or signs in an OIDC-backed account without
// applying the local email registration switch or invitation-code requirement.
func (s *AuthService) CompletePendingOIDCOAuth(
	ctx context.Context,
	input EmailOAuthIdentityInput,
	affiliateCode string,
	promoCode string,
) (*TokenPair, *User, bool, error) {
	if normalizeOAuthSignupSource(input.ProviderType) != "oidc" {
		return nil, nil, false, infraerrors.BadRequest("OAUTH_PROVIDER_INVALID", "oauth provider is invalid")
	}
	return s.loginOrRegisterVerifiedEmailOAuthDetailedWithPolicy(
		ctx,
		input,
		"",
		affiliateCode,
		promoCode,
		emailOAuthRegistrationPolicy{
			ignoreRegistrationDisabled:  true,
			ignoreInvitationRequirement: true,
			recoverSyntheticAccount:     true,
		},
	)
}

func (s *AuthService) loginOrRegisterVerifiedEmailOAuthDetailed(
	ctx context.Context,
	input EmailOAuthIdentityInput,
	invitationCode string,
	affiliateCode string,
	promoCode string,
) (*TokenPair, *User, bool, error) {
	return s.loginOrRegisterVerifiedEmailOAuthDetailedWithPolicy(
		ctx,
		input,
		invitationCode,
		affiliateCode,
		promoCode,
		emailOAuthRegistrationPolicy{},
	)
}

func (s *AuthService) loginOrRegisterVerifiedEmailOAuthDetailedWithPolicy(
	ctx context.Context,
	input EmailOAuthIdentityInput,
	invitationCode string,
	affiliateCode string,
	promoCode string,
	registrationPolicy emailOAuthRegistrationPolicy,
) (*TokenPair, *User, bool, error) {
	if s == nil || s.userRepo == nil || s.entClient == nil {
		return nil, nil, false, ErrServiceUnavailable
	}

	providerType := normalizeOAuthSignupSource(input.ProviderType)
	if !isSupportedOAuthIdentityProvider(providerType) {
		return nil, nil, false, infraerrors.BadRequest("OAUTH_PROVIDER_INVALID", "oauth provider is invalid")
	}
	providerKey := strings.TrimSpace(input.ProviderKey)
	if providerKey == "" {
		providerKey = providerType
	}
	providerSubject := strings.TrimSpace(input.ProviderSubject)
	if providerSubject == "" {
		return nil, nil, false, infraerrors.BadRequest("OAUTH_SUBJECT_MISSING", "oauth subject is missing")
	}
	email, err := OAuthSyntheticEmail(providerType, providerKey, providerSubject)
	if err != nil {
		return nil, nil, false, err
	}

	identityProviderType := oauthIdentityStorageProviderType(providerType)
	identityUser, err := s.findEmailOAuthIdentityOwner(ctx, identityProviderType, providerKey, providerSubject)
	if err != nil {
		return nil, nil, false, err
	}
	user := identityUser
	created := false
	recoveredSyntheticAccount := false
	if user == nil && registrationPolicy.recoverSyntheticAccount {
		user, err = s.findRecoverableOAuthSyntheticAccount(ctx, email, providerType)
		if err != nil {
			return nil, nil, false, err
		}
		recoveredSyntheticAccount = user != nil
	}
	if user == nil {
		user, err = s.createEmailOAuthUser(ctx, email, input.Username, providerType, invitationCode, affiliateCode, registrationPolicy)
		if err == nil {
			created = true
		} else if registrationPolicy.recoverSyntheticAccount && infraerrors.Reason(err) == "OAUTH_SYNTHETIC_EMAIL_CONFLICT" {
			recoveredUser, recoverErr := s.findRecoverableOAuthSyntheticAccount(ctx, email, providerType)
			if recoverErr == nil && recoveredUser != nil {
				user = recoveredUser
				recoveredSyntheticAccount = true
				err = nil
			}
		}
		if err != nil {
			return nil, nil, false, err
		}
	}

	if !user.IsActive() {
		return nil, nil, false, ErrUserNotActive
	}
	metadata := cloneOAuthMetadata(input.UpstreamMetadata)
	upstreamEmail := strings.TrimSpace(strings.ToLower(input.Email))
	if upstreamEmail != "" && !strings.EqualFold(upstreamEmail, email) {
		metadata["upstream_email"] = upstreamEmail
	}
	if err := s.ensureEmailOAuthIdentity(ctx, user.ID, EmailOAuthIdentityInput{
		ProviderType:     identityProviderType,
		ProviderKey:      providerKey,
		ProviderSubject:  providerSubject,
		Email:            email,
		EmailVerified:    input.EmailVerified,
		Username:         input.Username,
		DisplayName:      input.DisplayName,
		AvatarURL:        input.AvatarURL,
		UpstreamMetadata: metadata,
	}); err != nil {
		if created {
			_ = s.RollbackOAuthEmailAccountCreation(ctx, user.ID, invitationCode)
		}
		return nil, nil, false, err
	}

	if user.Username == "" && strings.TrimSpace(input.Username) != "" {
		user.Username = strings.TrimSpace(input.Username)
		if err := s.userRepo.Update(ctx, user); err != nil {
			logger.LegacyPrintf("service.auth", "[Auth] Failed to update username after %s oauth login: %v", providerType, err)
		}
	}
	if created {
		user = s.applyOAuthSignupPromoCode(ctx, user, promoCode)
	} else if !recoveredSyntheticAccount {
		if err := s.ApplyProviderDefaultSettingsOnFirstBind(ctx, user.ID, providerType); err != nil {
			logger.LegacyPrintf("service.auth", "[Auth] Failed to apply %s first bind defaults: %v", providerType, err)
		}
	}
	s.RecordSuccessfulLogin(ctx, user.ID)

	tokenPair, err := s.GenerateTokenPair(ctx, user, "")
	if err != nil {
		if created {
			_ = s.RollbackOAuthEmailAccountCreation(ctx, user.ID, invitationCode)
		}
		return nil, nil, false, fmt.Errorf("generate token pair: %w", err)
	}
	return tokenPair, user, created, nil
}

func oauthIdentityStorageProviderType(providerType string) string {
	switch normalizeOAuthSignupSource(providerType) {
	case "github", "google":
		return "oidc"
	default:
		return normalizeOAuthSignupSource(providerType)
	}
}

func oauthSignupStorageSource(providerType string) string {
	return oauthIdentityStorageProviderType(providerType)
}

func isSupportedOAuthIdentityProvider(providerType string) bool {
	switch providerType {
	case "github", "google", "linuxdo", "oidc", "wechat":
		return true
	default:
		return false
	}
}

// OAuthSyntheticEmail returns a stable, provider-scoped placeholder address.
// It is an internal account key only; upstream email claims are metadata and are
// never used to discover or adopt an existing local account.
func OAuthSyntheticEmail(providerType, providerKey, providerSubject string) (string, error) {
	providerType = normalizeOAuthSignupSource(providerType)
	if !isSupportedOAuthIdentityProvider(providerType) {
		return "", infraerrors.BadRequest("OAUTH_PROVIDER_INVALID", "oauth provider is invalid")
	}
	providerKey = strings.TrimSpace(providerKey)
	if providerKey == "" {
		providerKey = providerType
	}
	providerSubject = strings.TrimSpace(providerSubject)
	if providerSubject == "" {
		return "", infraerrors.BadRequest("OAUTH_SUBJECT_MISSING", "oauth subject is missing")
	}

	switch providerType {
	case "linuxdo":
		return "linuxdo-" + providerSubject + LinuxDoConnectSyntheticEmailDomain, nil
	case "wechat":
		return "wechat-" + providerSubject + WeChatConnectSyntheticEmailDomain, nil
	case "oidc":
		identityKey := providerKey + "\x1f" + providerSubject
		return hashedOAuthSyntheticEmail("oidc", identityKey, OIDCConnectSyntheticEmailDomain), nil
	case "github":
		return hashedOAuthSyntheticEmail("github", providerKey+"\x1f"+providerSubject, GitHubConnectSyntheticEmailDomain), nil
	case "google":
		return hashedOAuthSyntheticEmail("google", providerKey+"\x1f"+providerSubject, GoogleConnectSyntheticEmailDomain), nil
	default:
		return "", infraerrors.BadRequest("OAUTH_PROVIDER_INVALID", "oauth provider is invalid")
	}
}

func hashedOAuthSyntheticEmail(prefix, identityKey, domain string) string {
	sum := sha256.Sum256([]byte(identityKey))
	return prefix + "-" + hex.EncodeToString(sum[:16]) + domain
}

func cloneOAuthMetadata(metadata map[string]any) map[string]any {
	cloned := make(map[string]any, len(metadata)+1)
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}

func (s *AuthService) createEmailOAuthUser(
	ctx context.Context,
	email string,
	username string,
	providerType string,
	invitationCode string,
	affiliateCode string,
	registrationPolicy emailOAuthRegistrationPolicy,
) (*User, error) {
	if !registrationPolicy.ignoreRegistrationDisabled && (s.settingService == nil || !s.settingService.IsRegistrationEnabled(ctx)) {
		return nil, ErrRegDisabled
	}
	var invitationRedeemCode *RedeemCode
	if !registrationPolicy.ignoreInvitationRequirement {
		var err error
		invitationRedeemCode, err = s.validateOAuthRegistrationInvitation(ctx, invitationCode)
		if err != nil {
			if errors.Is(err, ErrInvitationCodeRequired) {
				return nil, ErrOAuthInvitationRequired
			}
			return nil, err
		}
	}

	randomPassword, err := randomHexString(32)
	if err != nil {
		return nil, ErrServiceUnavailable
	}
	hashedPassword, err := s.HashPassword(randomPassword)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}
	grantPlan := s.resolveSignupGrantPlan(ctx, providerType)
	var defaultRPMLimit int
	if s.settingService != nil {
		defaultRPMLimit = s.settingService.GetDefaultUserRPMLimit(ctx)
	}
	user := &User{
		Email:        email,
		Username:     strings.TrimSpace(username),
		PasswordHash: hashedPassword,
		Role:         RoleUser,
		Balance:      grantPlan.Balance,
		Concurrency:  grantPlan.Concurrency,
		RPMLimit:     defaultRPMLimit,
		Status:       StatusActive,
		SignupSource: oauthSignupStorageSource(providerType),
	}
	if err := s.userRepo.Create(ctx, user); err != nil {
		if errors.Is(err, ErrEmailExists) {
			return nil, infraerrors.Conflict("OAUTH_SYNTHETIC_EMAIL_CONFLICT", "oauth account identity is already reserved")
		}
		return nil, ErrServiceUnavailable
	}
	s.postAuthUserBootstrap(ctx, user, oauthSignupStorageSource(providerType), false)
	s.assignSubscriptions(ctx, user.ID, grantPlan.Subscriptions, "auto assigned by signup defaults")
	s.bindOAuthAffiliate(ctx, user.ID, affiliateCode)
	if invitationRedeemCode != nil {
		if err := s.useOAuthRegistrationInvitation(ctx, invitationRedeemCode.ID, user.ID); err != nil {
			_ = s.RollbackOAuthEmailAccountCreation(ctx, user.ID, invitationCode)
			return nil, ErrInvitationCodeInvalid
		}
	}
	return user, nil
}

func (s *AuthService) findRecoverableOAuthSyntheticAccount(ctx context.Context, email, providerType string) (*User, error) {
	user, err := s.userRepo.GetByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return nil, nil
		}
		return nil, ErrServiceUnavailable
	}

	actualSource := strings.TrimSpace(user.SignupSource)
	if actualSource == "" {
		actualSource = inferLegacySignupSource(user.Email)
	} else {
		actualSource = normalizeOAuthSignupSource(actualSource)
	}
	if actualSource != oauthSignupStorageSource(providerType) || !strings.EqualFold(strings.TrimSpace(user.Email), strings.TrimSpace(email)) {
		return nil, infraerrors.Conflict("OAUTH_SYNTHETIC_EMAIL_CONFLICT", "oauth account identity is already reserved")
	}
	return user, nil
}

func (s *AuthService) findEmailOAuthIdentityOwner(ctx context.Context, providerType, providerKey, providerSubject string) (*User, error) {
	identity, err := s.entClient.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ(providerType),
			authidentity.ProviderKeyEQ(providerKey),
			authidentity.ProviderSubjectEQ(providerSubject),
		).
		Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, nil
		}
		return nil, infraerrors.InternalServer("AUTH_IDENTITY_LOOKUP_FAILED", "failed to inspect auth identity ownership").WithCause(err)
	}
	if identity.ProviderType != providerType || identity.ProviderKey != providerKey || identity.ProviderSubject != providerSubject {
		return nil, infraerrors.Conflict("AUTH_IDENTITY_KEY_COLLISION", "auth identity key conflicts with another canonical identity")
	}
	user, err := s.userRepo.GetByID(ctx, identity.UserID)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return nil, nil
		}
		return nil, ErrServiceUnavailable
	}
	return user, nil
}

func (s *AuthService) ensureEmailOAuthIdentity(ctx context.Context, userID int64, input EmailOAuthIdentityInput) error {
	metadata := map[string]any{
		"email":          strings.TrimSpace(strings.ToLower(input.Email)),
		"email_verified": input.EmailVerified,
	}
	for key, value := range input.UpstreamMetadata {
		metadata[key] = value
	}
	if strings.TrimSpace(input.Username) != "" {
		metadata["username"] = strings.TrimSpace(input.Username)
	}
	if strings.TrimSpace(input.DisplayName) != "" {
		metadata["display_name"] = strings.TrimSpace(input.DisplayName)
	}
	if strings.TrimSpace(input.AvatarURL) != "" {
		metadata["avatar_url"] = strings.TrimSpace(input.AvatarURL)
	}

	providerType := normalizeOAuthSignupSource(input.ProviderType)
	providerKey := strings.TrimSpace(input.ProviderKey)
	providerSubject := strings.TrimSpace(input.ProviderSubject)
	identity, err := s.entClient.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ(providerType),
			authidentity.ProviderKeyEQ(providerKey),
			authidentity.ProviderSubjectEQ(providerSubject),
		).
		Only(ctx)
	if err != nil && !dbent.IsNotFound(err) {
		return infraerrors.InternalServer("AUTH_IDENTITY_LOOKUP_FAILED", "failed to inspect auth identity ownership").WithCause(err)
	}
	if dbent.IsNotFound(err) {
		err = s.entClient.AuthIdentity.Create().
			SetUserID(userID).
			SetProviderType(providerType).
			SetProviderKey(providerKey).
			SetProviderSubject(providerSubject).
			SetMetadata(metadata).
			OnConflictColumns(
				authidentity.FieldProviderType,
				authidentity.FieldProviderKey,
				authidentity.FieldProviderSubject,
			).
			DoNothing().
			Exec(ctx)
		if err != nil && !isSQLNoRowsError(err) {
			return infraerrors.InternalServer("AUTH_IDENTITY_CREATE_FAILED", "failed to create auth identity").WithCause(err)
		}
		identity, err = s.entClient.AuthIdentity.Query().
			Where(
				authidentity.ProviderTypeEQ(providerType),
				authidentity.ProviderKeyEQ(providerKey),
				authidentity.ProviderSubjectEQ(providerSubject),
			).
			Only(ctx)
		if err != nil {
			return infraerrors.InternalServer("AUTH_IDENTITY_LOOKUP_FAILED", "failed to inspect auth identity ownership").WithCause(err)
		}
	}
	if identity.ProviderType != providerType || identity.ProviderKey != providerKey || identity.ProviderSubject != providerSubject {
		return infraerrors.Conflict("AUTH_IDENTITY_KEY_COLLISION", "auth identity key conflicts with another canonical identity")
	}
	if identity.UserID != userID {
		return infraerrors.Conflict("AUTH_IDENTITY_OWNERSHIP_CONFLICT", "auth identity already belongs to another user")
	}
	if _, err := s.entClient.AuthIdentity.UpdateOneID(identity.ID).
		SetMetadata(metadata).
		Save(ctx); err != nil {
		return infraerrors.InternalServer("AUTH_IDENTITY_UPDATE_FAILED", "failed to update auth identity").WithCause(err)
	}
	return nil
}
