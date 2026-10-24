package auth

import (
	"github.com/go-chi/chi/v5"

	"kodipms/internal/rbac"
)

func (h *Handler) PublicRoutes(r chi.Router) {
	r.Post("/login", h.login)
	r.Post("/pin-login", h.pinLogin)
	r.Post("/refresh", h.refresh)
	r.Post("/forgot-password", h.forgotPassword)
	r.Post("/reset-password", h.resetPassword)
	r.Post("/accept-invite", h.acceptInvite)
}

func (h *Handler) ProtectedRoutes(r chi.Router) {
	r.Get("/me", h.me)
	r.Post("/logout", h.logout)
	r.Post("/change-password", h.changePassword)
	r.Post("/pin", h.setPIN)
	r.With(rbac.Require("users.create")).Post("/invites", h.createInvite)
}
