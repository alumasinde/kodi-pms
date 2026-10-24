package auth

import (
	"net/http"
	"strings"
	"time"

	"kodipms/internal/platform/config"
	"kodipms/internal/platform/httpx"
	"kodipms/internal/rbac"
	"kodipms/internal/tenancy"
)

const refreshCookie = "refresh_token"

type Handler struct {
	svc *Service
	cfg *config.Config
}

func NewHandler(svc *Service, cfg *config.Config) *Handler {
	return &Handler{svc: svc, cfg: cfg}
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var in LoginInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	org, ok := tenancy.FromContext(r.Context())
	if !ok {
		httpx.Fail(w, tenancy.ErrUnknownDomain)
		return
	}
	session, err := h.svc.Login(r.Context(), org, in, h.meta(r))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	h.setRefreshCookie(w, session.RefreshToken)
	httpx.OK(w, session, nil)
}

func (h *Handler) pinLogin(w http.ResponseWriter, r *http.Request) {
	var in PinLoginInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	org, ok := tenancy.FromContext(r.Context())
	if !ok {
		httpx.Fail(w, tenancy.ErrUnknownDomain)
		return
	}
	session, err := h.svc.LoginWithPIN(r.Context(), org, in, h.meta(r))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	h.setRefreshCookie(w, session.RefreshToken)
	httpx.OK(w, session, nil)
}

func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	raw := h.refreshTokenFromRequest(w, r)
	if raw == "" {
		httpx.Fail(w, httpx.ErrUnauthorized)
		return
	}
	session, err := h.svc.Refresh(r.Context(), raw, h.meta(r))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	h.setRefreshCookie(w, session.RefreshToken)
	httpx.OK(w, session, nil)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	p := rbac.FromContext(r.Context())
	raw := h.refreshTokenFromRequest(w, r)
	h.svc.Logout(r.Context(), p, raw, h.meta(r))
	h.clearRefreshCookie(w)
	httpx.NoContent(w)
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	p := rbac.FromContext(r.Context())
	if p == nil {
		httpx.Fail(w, httpx.ErrUnauthorized)
		return
	}
	me, err := h.svc.Me(r.Context(), p)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, me, nil)
}

func (h *Handler) forgotPassword(w http.ResponseWriter, r *http.Request) {
	var in ForgotPasswordInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	org, _ := tenancy.FromContext(r.Context())
	h.svc.ForgotPassword(r.Context(), org, in, h.meta(r))
	httpx.OK(w, map[string]string{
		"message": "If an account matches those details, a reset link has been sent.",
	}, nil)
}

func (h *Handler) resetPassword(w http.ResponseWriter, r *http.Request) {
	var in ResetPasswordInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	if err := h.svc.ResetPassword(r.Context(), in, h.meta(r)); err != nil {
		httpx.Fail(w, err)
		return
	}
	h.clearRefreshCookie(w)
	httpx.NoContent(w)
}

func (h *Handler) acceptInvite(w http.ResponseWriter, r *http.Request) {
	var in AcceptInviteInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	if err := h.svc.AcceptInvite(r.Context(), in, h.meta(r)); err != nil {
		httpx.Fail(w, err)
		return
	}
	h.clearRefreshCookie(w)
	httpx.NoContent(w)
}

func (h *Handler) changePassword(w http.ResponseWriter, r *http.Request) {
	p := rbac.FromContext(r.Context())
	if p == nil {
		httpx.Fail(w, httpx.ErrUnauthorized)
		return
	}
	var in ChangePasswordInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	if err := h.svc.ChangePassword(r.Context(), p, in, h.meta(r)); err != nil {
		httpx.Fail(w, err)
		return
	}
	h.clearRefreshCookie(w)
	httpx.NoContent(w)
}

func (h *Handler) setPIN(w http.ResponseWriter, r *http.Request) {
	p := rbac.FromContext(r.Context())
	if p == nil {
		httpx.Fail(w, httpx.ErrUnauthorized)
		return
	}
	var in SetPINInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Fail(w, err)
		return
	}
	if err := h.svc.SetPIN(r.Context(), p, in, h.meta(r)); err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.NoContent(w)
}

func (h *Handler) meta(r *http.Request) ClientMeta {
	return ClientMeta{
		IP:        httpx.ClientIP(r, h.cfg.TrustProxy),
		UserAgent: r.UserAgent(),
	}
}

// refreshTokenFromRequest prefers the HttpOnly cookie used by the web app,
// and falls back to the JSON body used by mobile clients.
func (h *Handler) refreshTokenFromRequest(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(refreshCookie); err == nil && strings.TrimSpace(c.Value) != "" {
		return strings.TrimSpace(c.Value)
	}
	if r.ContentLength == 0 {
		return ""
	}
	var in RefreshInput
	if err := httpx.Decode(w, r, &in); err != nil {
		return ""
	}
	return strings.TrimSpace(in.RefreshToken)
}

func (h *Handler) setRefreshCookie(w http.ResponseWriter, token string) {
	maxAge := int(h.cfg.RefreshTTL.Seconds())
	http.SetCookie(w, &http.Cookie{
		Name:     refreshCookie,
		Value:    token,
		Path:     "/api/v1/auth",
		Domain:   h.cfg.CookieDomain,
		MaxAge:   maxAge,
		Expires:  time.Now().Add(h.cfg.RefreshTTL),
		HttpOnly: true,
		Secure:   h.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (h *Handler) clearRefreshCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     refreshCookie,
		Value:    "",
		Path:     "/api/v1/auth",
		Domain:   h.cfg.CookieDomain,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
		HttpOnly: true,
		Secure:   h.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}
