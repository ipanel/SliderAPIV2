package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/imroc/req/v3"
	"github.com/tidwall/gjson"
	"ikik-api/internal/config"
	infraerrors "ikik-api/internal/pkg/errors"
	"ikik-api/internal/pkg/oauth"
	"ikik-api/internal/pkg/response"
	"ikik-api/internal/service"
)

const (
	emailOAuthCookiePath      = "/api/v1/auth/oauth"
	emailOAuthStateCookieName = "email_oauth_state"
	emailOAuthRedirectCookie  = "email_oauth_redirect"
	emailOAuthProviderCookie  = "email_oauth_provider"
	emailOAuthAffiliateCookie = "email_oauth_affiliate"
	emailOAuthCookieMaxAgeSec = 10 * 60
	emailOAuthDefaultRedirect = "/dashboard"
)

type emailOAuthTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope,omitempty"`
}

type emailOAuthProfile struct {
	Subject       string
	Email         string
	EmailVerified bool
	Username      string
	DisplayName   string
	AvatarURL     string
	Metadata      map[string]any
}

func (h *AuthHandler) GitHubOAuthStart(c *gin.Context) { h.emailOAuthStart(c, "github") }
func (h *AuthHandler) GoogleOAuthStart(c *gin.Context) { h.emailOAuthStart(c, "google") }

func (h *AuthHandler) GitHubOAuthCallback(c *gin.Context) { h.emailOAuthCallback(c, "github") }
func (h *AuthHandler) GoogleOAuthCallback(c *gin.Context) { h.emailOAuthCallback(c, "google") }

// loginOrRegisterOAuthIdentity completes a third-party login by provider+subject.
// invitationCode is only needed when the site explicitly gates registration.
func (h *AuthHandler) loginOrRegisterOAuthIdentity(
	c *gin.Context,
	input service.EmailOAuthIdentityInput,
	invitationCode string,
	affiliateCode string,
) (*service.TokenPair, *service.User, error) {
	return h.authService.LoginOrRegisterVerifiedEmailOAuthWithSignupCodes(
		c.Request.Context(),
		input,
		strings.TrimSpace(invitationCode),
		strings.TrimSpace(affiliateCode),
		readOAuthPromoCode(c),
	)
}

func redirectOAuthTokenPair(c *gin.Context, frontendCallback, redirectTo string, tokenPair *service.TokenPair) {
	fragment := url.Values{}
	fragment.Set("access_token", tokenPair.AccessToken)
	fragment.Set("refresh_token", tokenPair.RefreshToken)
	fragment.Set("expires_in", fmt.Sprintf("%d", tokenPair.ExpiresIn))
	fragment.Set("token_type", "Bearer")
	fragment.Set("redirect", redirectTo)
	redirectWithFragment(c, frontendCallback, fragment)
}

func (h *AuthHandler) CompleteGitHubOAuthRegistration(c *gin.Context) {
	h.completeEmailOAuthRegistration(c, "github")
}
func (h *AuthHandler) CompleteGoogleOAuthRegistration(c *gin.Context) {
	h.completeEmailOAuthRegistration(c, "google")
}

func (h *AuthHandler) emailOAuthStart(c *gin.Context, provider string) {
	cfg, err := h.getEmailOAuthConfig(c.Request.Context(), provider)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	state, err := oauth.GenerateState()
	if err != nil {
		response.ErrorFrom(c, infraerrors.InternalServer("OAUTH_STATE_GEN_FAILED", "failed to generate oauth state").WithCause(err))
		return
	}
	redirectTo := sanitizeFrontendRedirectPath(c.Query("redirect"))
	if redirectTo == "" {
		redirectTo = emailOAuthDefaultRedirect
	}

	secureCookie := isRequestHTTPS(c)
	loginAgreementRevision := strings.TrimSpace(c.Query("login_agreement_revision"))
	emailOAuthSetCookie(c, emailOAuthStateCookieName, encodeCookieValue(state), secureCookie)
	emailOAuthSetCookie(c, emailOAuthRedirectCookie, encodeCookieValue(redirectTo), secureCookie)
	emailOAuthSetCookie(c, emailOAuthProviderCookie, encodeCookieValue(provider), secureCookie)
	setOAuthLoginAgreementCookie(c, loginAgreementRevision, secureCookie)
	captureOAuthPromoCode(c, secureCookie)
	if affCode := strings.TrimSpace(firstNonEmpty(c.Query("aff_code"), c.Query("aff"))); affCode != "" {
		emailOAuthSetCookie(c, emailOAuthAffiliateCookie, encodeCookieValue(affCode), secureCookie)
	} else {
		emailOAuthClearCookie(c, emailOAuthAffiliateCookie, secureCookie)
	}

	authURL, err := buildEmailOAuthAuthorizeURL(cfg, state)
	if err != nil {
		response.ErrorFrom(c, infraerrors.InternalServer("OAUTH_BUILD_URL_FAILED", "failed to build oauth authorization url").WithCause(err))
		return
	}
	c.Redirect(http.StatusFound, authURL)
}

func (h *AuthHandler) emailOAuthCallback(c *gin.Context, provider string) {
	cfg, cfgErr := h.getEmailOAuthConfig(c.Request.Context(), provider)
	if cfgErr != nil {
		response.ErrorFrom(c, cfgErr)
		return
	}
	frontendCallback := strings.TrimSpace(cfg.FrontendRedirectURL)
	if frontendCallback == "" {
		frontendCallback = "/auth/oauth/callback"
	}
	if providerErr := strings.TrimSpace(c.Query("error")); providerErr != "" {
		redirectOAuthError(c, frontendCallback, "provider_error", providerErr, c.Query("error_description"))
		return
	}
	code := strings.TrimSpace(c.Query("code"))
	state := strings.TrimSpace(c.Query("state"))
	if code == "" || state == "" {
		redirectOAuthError(c, frontendCallback, "missing_params", "missing code/state", "")
		return
	}

	secureCookie := isRequestHTTPS(c)
	defer func() {
		emailOAuthClearCookie(c, emailOAuthStateCookieName, secureCookie)
		emailOAuthClearCookie(c, emailOAuthRedirectCookie, secureCookie)
		emailOAuthClearCookie(c, emailOAuthProviderCookie, secureCookie)
		emailOAuthClearCookie(c, emailOAuthAffiliateCookie, secureCookie)
		clearOAuthLoginAgreementCookie(c, secureCookie)
		clearOAuthPromoCodeCookie(c, secureCookie)
	}()
	expectedState, err := readCookieDecoded(c, emailOAuthStateCookieName)
	if err != nil || expectedState == "" || expectedState != state {
		redirectOAuthError(c, frontendCallback, "invalid_state", "invalid oauth state", "")
		return
	}
	expectedProvider, _ := readCookieDecoded(c, emailOAuthProviderCookie)
	if !strings.EqualFold(strings.TrimSpace(expectedProvider), provider) {
		redirectOAuthError(c, frontendCallback, "invalid_state", "invalid oauth provider", "")
		return
	}
	redirectTo, _ := readCookieDecoded(c, emailOAuthRedirectCookie)
	redirectTo = sanitizeFrontendRedirectPath(redirectTo)
	if redirectTo == "" {
		redirectTo = emailOAuthDefaultRedirect
	}

	tokenResp, err := exchangeEmailOAuthCode(c.Request.Context(), cfg, code)
	if err != nil {
		redirectOAuthError(c, frontendCallback, "token_exchange_failed", "failed to exchange oauth code", singleLine(err.Error()))
		return
	}
	profile, err := fetchEmailOAuthProfile(c.Request.Context(), provider, cfg, tokenResp)
	if err != nil {
		redirectOAuthError(c, frontendCallback, "userinfo_failed", "failed to fetch verified email", singleLine(err.Error()))
		return
	}
	h.emailOAuthCallbackWithProfile(c, provider, cfg, frontendCallback, redirectTo, profile)
}

func (h *AuthHandler) emailOAuthCallbackWithProfile(
	c *gin.Context,
	provider string,
	cfg config.EmailOAuthProviderConfig,
	frontendCallback string,
	redirectTo string,
	profile *emailOAuthProfile,
) {
	input := service.EmailOAuthIdentityInput{
		ProviderType:     provider,
		ProviderKey:      provider,
		ProviderSubject:  profile.Subject,
		Email:            profile.Email,
		EmailVerified:    profile.EmailVerified,
		Username:         profile.Username,
		DisplayName:      profile.DisplayName,
		AvatarURL:        profile.AvatarURL,
		UpstreamMetadata: profile.Metadata,
	}
	identityProviderType := strings.ToLower(strings.TrimSpace(provider))
	if identityProviderType == "github" || identityProviderType == "google" {
		identityProviderType = "oidc"
	}
	identityOwner, err := h.findOAuthIdentityUser(c.Request.Context(), service.PendingAuthIdentityKey{
		ProviderType:    identityProviderType,
		ProviderKey:     strings.TrimSpace(input.ProviderKey),
		ProviderSubject: strings.TrimSpace(input.ProviderSubject),
	})
	if err != nil {
		redirectOAuthError(c, frontendCallback, infraerrors.Reason(err), infraerrors.Message(err), "")
		return
	}
	if identityOwner == nil {
		if err := h.ensureBackendModeAllowsNewUserLogin(c.Request.Context()); err != nil {
			redirectOAuthError(c, frontendCallback, "login_blocked", infraerrors.Reason(err), infraerrors.Message(err))
			return
		}
	} else if err := h.ensureBackendModeAllowsUser(c.Request.Context(), &service.User{
		ID:     identityOwner.ID,
		Email:  identityOwner.Email,
		Role:   identityOwner.Role,
		Status: identityOwner.Status,
	}); err != nil {
		redirectOAuthError(c, frontendCallback, "login_blocked", infraerrors.Reason(err), infraerrors.Message(err))
		return
	}
	if err := h.ensureLoginAgreementAccepted(c.Request.Context(), readOAuthLoginAgreementCookie(c)); err != nil {
		redirectOAuthError(c, frontendCallback, "login_blocked", infraerrors.Reason(err), infraerrors.Message(err))
		return
	}

	affiliateCode := h.emailOAuthAffiliateCode(c)
	tokenPair, user, err := h.loginOrRegisterOAuthIdentity(c, input, "", affiliateCode)
	if err != nil {
		if errors.Is(err, service.ErrOAuthInvitationRequired) {
			if pendingErr := h.createEmailOAuthRegistrationPendingSession(c, provider, frontendCallback, redirectTo, profile); pendingErr != nil {
				redirectOAuthError(c, frontendCallback, infraerrors.Reason(pendingErr), infraerrors.Message(pendingErr), "")
				return
			}
			redirectToFrontendCallback(c, frontendCallback)
			return
		}
		redirectOAuthError(c, frontendCallback, infraerrors.Reason(err), infraerrors.Message(err), "")
		return
	}
	if err := h.ensureBackendModeAllowsUser(c.Request.Context(), user); err != nil {
		redirectOAuthError(c, frontendCallback, "login_blocked", infraerrors.Reason(err), infraerrors.Message(err))
		return
	}
	redirectOAuthTokenPair(c, frontendCallback, redirectTo, tokenPair)
}

func (h *AuthHandler) emailOAuthAffiliateCode(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if code, err := readCookieDecoded(c, emailOAuthAffiliateCookie); err == nil {
		return strings.TrimSpace(code)
	}
	return ""
}

func (h *AuthHandler) createEmailOAuthRegistrationPendingSession(
	c *gin.Context,
	provider string,
	frontendCallback string,
	redirectTo string,
	profile *emailOAuthProfile,
) error {
	if h == nil || profile == nil {
		return infraerrors.ServiceUnavailable("PENDING_AUTH_NOT_READY", "pending auth service is not ready")
	}
	browserSessionKey, err := generateOAuthPendingBrowserSession()
	if err != nil {
		return infraerrors.InternalServer("PENDING_AUTH_SESSION_CREATE_FAILED", "failed to create pending auth session").WithCause(err)
	}
	setOAuthPendingBrowserCookie(c, browserSessionKey, isRequestHTTPS(c))

	upstreamEmail := strings.TrimSpace(strings.ToLower(profile.Email))
	syntheticEmail, err := service.OAuthSyntheticEmail(provider, provider, profile.Subject)
	if err != nil {
		return err
	}
	username := strings.TrimSpace(profile.Username)
	affiliateCode := h.emailOAuthAffiliateCode(c)
	loginAgreementRevision := readOAuthLoginAgreementCookie(c)
	upstreamClaims := map[string]any{
		"email":            upstreamEmail,
		"email_verified":   profile.EmailVerified,
		"username":         username,
		"provider":         provider,
		"provider_key":     provider,
		"provider_subject": strings.TrimSpace(profile.Subject),
	}
	if strings.TrimSpace(profile.DisplayName) != "" {
		upstreamClaims["suggested_display_name"] = strings.TrimSpace(profile.DisplayName)
	}
	if strings.TrimSpace(profile.AvatarURL) != "" {
		upstreamClaims["suggested_avatar_url"] = strings.TrimSpace(profile.AvatarURL)
	}
	if affiliateCode != "" {
		upstreamClaims["aff_code"] = affiliateCode
	}
	for key, value := range profile.Metadata {
		if _, exists := upstreamClaims[key]; !exists {
			upstreamClaims[key] = value
		}
	}

	invitationRequired := h != nil && h.settingSvc != nil && h.settingSvc.IsInvitationCodeEnabled(c.Request.Context())
	pendingError := "registration_completion_required"
	choiceReason := "registration_completion_required"
	if invitationRequired {
		pendingError = "invitation_required"
		choiceReason = "invitation_required"
	}
	completionResponse := map[string]any{
		"step":                      oauthPendingChoiceStep,
		"error":                     pendingError,
		"choice_reason":             choiceReason,
		"adoption_required":         false,
		"create_account_allowed":    true,
		"existing_account_bindable": false,
		"force_email_on_signup":     false,
		"invitation_required":       invitationRequired,
		"email":                     upstreamEmail,
		"resolved_email":            syntheticEmail,
		"provider":                  provider,
		"redirect":                  redirectTo,
	}
	if strings.TrimSpace(frontendCallback) != "" {
		completionResponse["frontend_callback"] = strings.TrimSpace(frontendCallback)
	}

	return h.createOAuthPendingSession(c, oauthPendingSessionPayload{
		Intent:                 oauthIntentLogin,
		Identity:               emailOAuthPendingIdentity(provider, profile.Subject),
		ResolvedEmail:          syntheticEmail,
		RedirectTo:             redirectTo,
		BrowserSessionKey:      browserSessionKey,
		LoginAgreementRevision: loginAgreementRevision,
		UpstreamIdentityClaims: upstreamClaims,
		CompletionResponse:     completionResponse,
	})
}

func emailOAuthPendingIdentity(provider, subject string) service.PendingAuthIdentityKey {
	provider = strings.ToLower(strings.TrimSpace(provider))
	providerType := provider
	if provider == "github" || provider == "google" {
		providerType = "oidc"
	}
	return service.PendingAuthIdentityKey{
		ProviderType:    providerType,
		ProviderKey:     provider,
		ProviderSubject: strings.TrimSpace(subject),
	}
}

func emailOAuthPendingProvider(providerKey string, upstreamClaims map[string]any) string {
	provider := strings.ToLower(strings.TrimSpace(providerKey))
	if provider == "" {
		provider = strings.ToLower(pendingSessionStringValue(upstreamClaims, "provider"))
	}
	return provider
}

type completeEmailOAuthRequest struct {
	InvitationCode         string `json:"invitation_code,omitempty"`
	AffCode                string `json:"aff_code,omitempty"`
	LoginAgreementRevision string `json:"login_agreement_revision,omitempty"`
}

func (h *AuthHandler) completeEmailOAuthRegistration(c *gin.Context, provider string) {
	var req completeEmailOAuthRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	_, session, clearCookies, err := readPendingOAuthBrowserSession(c, h)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if err := ensurePendingOAuthCompleteRegistrationSession(session); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	pendingProvider := emailOAuthPendingProvider(session.ProviderKey, session.UpstreamIdentityClaims)
	if pendingProvider == "" || !strings.EqualFold(pendingProvider, strings.TrimSpace(provider)) {
		response.BadRequest(c, "Pending oauth session provider mismatch")
		return
	}
	if err := h.ensureBackendModeAllowsNewUserLogin(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if err := h.ensureLoginAgreementAccepted(c.Request.Context(), requestLoginAgreementRevision(req.LoginAgreementRevision, session)); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	affiliateCode := strings.TrimSpace(req.AffCode)
	if affiliateCode == "" {
		affiliateCode = pendingSessionStringValue(session.UpstreamIdentityClaims, "aff_code")
	}

	emailVerified, _ := session.UpstreamIdentityClaims["email_verified"].(bool)
	tokenPair, user, created, err := h.authService.CompletePendingEmailOAuthWithSignupCodes(
		c.Request.Context(),
		service.EmailOAuthIdentityInput{
			ProviderType:     pendingProvider,
			ProviderKey:      strings.TrimSpace(session.ProviderKey),
			ProviderSubject:  strings.TrimSpace(session.ProviderSubject),
			Email:            pendingSessionStringValue(session.UpstreamIdentityClaims, "email"),
			EmailVerified:    emailVerified,
			Username:         pendingSessionStringValue(session.UpstreamIdentityClaims, "username"),
			DisplayName:      pendingSessionStringValue(session.UpstreamIdentityClaims, "suggested_display_name"),
			AvatarURL:        pendingSessionStringValue(session.UpstreamIdentityClaims, "suggested_avatar_url"),
			UpstreamMetadata: session.UpstreamIdentityClaims,
		},
		strings.TrimSpace(req.InvitationCode),
		affiliateCode,
		pendingOAuthPromoCode(session),
	)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if err := h.ensureBackendModeAllowsUser(c.Request.Context(), user); err != nil {
		if created {
			_ = h.authService.RollbackOAuthEmailAccountCreation(c.Request.Context(), user.ID, strings.TrimSpace(req.InvitationCode))
		}
		response.ErrorFrom(c, err)
		return
	}

	client := h.entClient()
	if client == nil {
		if created {
			_ = h.authService.RollbackOAuthEmailAccountCreation(c.Request.Context(), user.ID, strings.TrimSpace(req.InvitationCode))
		}
		response.ErrorFrom(c, infraerrors.ServiceUnavailable("PENDING_AUTH_NOT_READY", "pending auth service is not ready"))
		return
	}
	tx, err := client.Tx(c.Request.Context())
	if err != nil {
		if created {
			_ = h.authService.RollbackOAuthEmailAccountCreation(c.Request.Context(), user.ID, strings.TrimSpace(req.InvitationCode))
		}
		response.ErrorFrom(c, infraerrors.InternalServer("PENDING_AUTH_BIND_APPLY_FAILED", "failed to consume pending oauth session").WithCause(err))
		return
	}
	defer func() { _ = tx.Rollback() }()
	if err := consumePendingOAuthBrowserSessionTx(c.Request.Context(), tx, session); err != nil {
		_ = tx.Rollback()
		if created {
			_ = h.authService.RollbackOAuthEmailAccountCreation(c.Request.Context(), user.ID, strings.TrimSpace(req.InvitationCode))
		}
		clearCookies()
		response.ErrorFrom(c, err)
		return
	}
	if err := tx.Commit(); err != nil {
		if created {
			_ = h.authService.RollbackOAuthEmailAccountCreation(c.Request.Context(), user.ID, strings.TrimSpace(req.InvitationCode))
		}
		response.ErrorFrom(c, infraerrors.InternalServer("PENDING_AUTH_BIND_APPLY_FAILED", "failed to consume pending oauth session").WithCause(err))
		return
	}
	clearCookies()
	writeOAuthTokenPairResponse(c, tokenPair)
}

func (h *AuthHandler) getEmailOAuthConfig(ctx context.Context, provider string) (config.EmailOAuthProviderConfig, error) {
	if h != nil && h.settingSvc != nil {
		return h.settingSvc.GetEmailOAuthProviderConfig(ctx, provider)
	}
	return config.EmailOAuthProviderConfig{}, infraerrors.ServiceUnavailable("CONFIG_NOT_READY", "config not loaded")
}

func buildEmailOAuthAuthorizeURL(cfg config.EmailOAuthProviderConfig, state string) (string, error) {
	u, err := url.Parse(cfg.AuthorizeURL)
	if err != nil {
		return "", fmt.Errorf("parse authorize_url: %w", err)
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", cfg.ClientID)
	q.Set("redirect_uri", cfg.RedirectURL)
	q.Set("state", state)
	if strings.TrimSpace(cfg.Scopes) != "" {
		q.Set("scope", cfg.Scopes)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func exchangeEmailOAuthCode(ctx context.Context, cfg config.EmailOAuthProviderConfig, code string) (*emailOAuthTokenResponse, error) {
	resp, err := req.C().
		R().
		SetContext(ctx).
		SetHeader("Accept", "application/json").
		SetFormData(map[string]string{
			"grant_type":    "authorization_code",
			"client_id":     cfg.ClientID,
			"client_secret": cfg.ClientSecret,
			"code":          code,
			"redirect_uri":  cfg.RedirectURL,
		}).
		Post(cfg.TokenURL)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("token endpoint status %d: %s", resp.StatusCode, truncateLogValue(resp.String(), 1024))
	}
	var tokenResp emailOAuthTokenResponse
	if err := json.Unmarshal(resp.Bytes(), &tokenResp); err != nil {
		return nil, err
	}
	if strings.TrimSpace(tokenResp.AccessToken) == "" {
		return nil, errors.New("missing access_token")
	}
	return &tokenResp, nil
}

func fetchEmailOAuthProfile(ctx context.Context, provider string, cfg config.EmailOAuthProviderConfig, token *emailOAuthTokenResponse) (*emailOAuthProfile, error) {
	resp, err := req.C().
		R().
		SetContext(ctx).
		SetBearerAuthToken(token.AccessToken).
		SetHeader("Accept", "application/json").
		Get(cfg.UserInfoURL)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("userinfo endpoint status %d: %s", resp.StatusCode, truncateLogValue(resp.String(), 1024))
	}
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "github":
		return parseGitHubOAuthProfile(ctx, cfg, token, resp.String())
	case "google":
		return parseGoogleOAuthProfile(resp.String())
	default:
		return nil, errors.New("unsupported oauth provider")
	}
}

func parseGitHubOAuthProfile(ctx context.Context, cfg config.EmailOAuthProviderConfig, token *emailOAuthTokenResponse, body string) (*emailOAuthProfile, error) {
	subject := strings.TrimSpace(gjson.Get(body, "id").String())
	if subject == "" {
		return nil, errors.New("github user id is missing")
	}
	email := strings.TrimSpace(gjson.Get(body, "email").String())
	emailVerified := false
	if emailsURL := strings.TrimSpace(cfg.EmailsURL); emailsURL != "" {
		if verifiedEmail, err := fetchGitHubPrimaryVerifiedEmail(ctx, emailsURL, token.AccessToken); err == nil {
			email = verifiedEmail
			emailVerified = verifiedEmail != ""
		}
	}
	login := strings.TrimSpace(gjson.Get(body, "login").String())
	name := strings.TrimSpace(gjson.Get(body, "name").String())
	return &emailOAuthProfile{
		Subject:       subject,
		Email:         email,
		EmailVerified: emailVerified,
		Username:      firstNonEmpty(login, name, "github_"+subject),
		DisplayName:   firstNonEmpty(name, login),
		AvatarURL:     strings.TrimSpace(gjson.Get(body, "avatar_url").String()),
		Metadata: map[string]any{
			"login": login,
		},
	}, nil
}

func fetchGitHubPrimaryVerifiedEmail(ctx context.Context, emailsURL string, accessToken string) (string, error) {
	resp, err := req.C().
		R().
		SetContext(ctx).
		SetBearerAuthToken(accessToken).
		SetHeader("Accept", "application/json").
		Get(emailsURL)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("github emails endpoint status %d: %s", resp.StatusCode, truncateLogValue(resp.String(), 1024))
	}
	items := gjson.Parse(resp.String()).Array()
	for _, item := range items {
		if item.Get("primary").Bool() && item.Get("verified").Bool() {
			if email := strings.TrimSpace(item.Get("email").String()); email != "" {
				return email, nil
			}
		}
	}
	for _, item := range items {
		if item.Get("verified").Bool() {
			if email := strings.TrimSpace(item.Get("email").String()); email != "" {
				return email, nil
			}
		}
	}
	return "", errors.New("github verified email is missing")
}

func parseGoogleOAuthProfile(body string) (*emailOAuthProfile, error) {
	subject := strings.TrimSpace(gjson.Get(body, "sub").String())
	email := strings.TrimSpace(gjson.Get(body, "email").String())
	verified := gjson.Get(body, "email_verified").Bool()
	if subject == "" {
		return nil, errors.New("google subject is missing")
	}
	name := strings.TrimSpace(gjson.Get(body, "name").String())
	return &emailOAuthProfile{
		Subject:       subject,
		Email:         email,
		EmailVerified: verified,
		Username:      firstNonEmpty(strings.TrimSpace(gjson.Get(body, "given_name").String()), name, email),
		DisplayName:   name,
		AvatarURL:     strings.TrimSpace(gjson.Get(body, "picture").String()),
		Metadata: map[string]any{
			"email_verified": verified,
		},
	}, nil
}

func emailOAuthSetCookie(c *gin.Context, name, value string, secure bool) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     emailOAuthCookiePath,
		MaxAge:   emailOAuthCookieMaxAgeSec,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func emailOAuthClearCookie(c *gin.Context, name string, secure bool) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     emailOAuthCookiePath,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}
