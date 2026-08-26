package handler

import (
	"fmt"
	"net/url"

	"github.com/gin-gonic/gin"
	"ikik-api/internal/service"
)

func (h *AuthHandler) completeDirectOAuthIdentityLogin(
	c *gin.Context,
	frontendCallback string,
	redirectTo string,
	input service.EmailOAuthIdentityInput,
	beforeRedirect func(*service.User) error,
) error {
	// This helper is only used after callers have ruled out an existing identity.
	if err := h.ensureBackendModeAllowsNewUserLogin(c.Request.Context()); err != nil {
		return err
	}
	if err := h.ensureLoginAgreementAccepted(c.Request.Context(), readOAuthLoginAgreementCookie(c)); err != nil {
		return err
	}

	tokenPair, user, created, err := h.authService.CompletePendingEmailOAuthWithSignupCodes(
		c.Request.Context(),
		input,
		"",
		readOAuthAffiliateCode(c),
		readOAuthPromoCode(c),
	)
	if err != nil {
		return err
	}
	rollbackCreatedUser := func() {
		if created {
			_ = h.authService.RollbackOAuthEmailAccountCreation(c.Request.Context(), user.ID, "")
		}
	}
	if err := h.ensureBackendModeAllowsUser(c.Request.Context(), user); err != nil {
		rollbackCreatedUser()
		return err
	}
	if beforeRedirect != nil {
		if err := beforeRedirect(user); err != nil {
			rollbackCreatedUser()
			return err
		}
	}

	clearOAuthPendingSessionCookie(c, isRequestHTTPS(c))
	clearOAuthPendingBrowserCookie(c, isRequestHTTPS(c))
	fragment := url.Values{}
	fragment.Set("access_token", tokenPair.AccessToken)
	fragment.Set("refresh_token", tokenPair.RefreshToken)
	fragment.Set("expires_in", fmt.Sprintf("%d", tokenPair.ExpiresIn))
	fragment.Set("token_type", "Bearer")
	fragment.Set("redirect", redirectTo)
	redirectWithFragment(c, frontendCallback, fragment)
	return nil
}
