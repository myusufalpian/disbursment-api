package dto

type LoginRequest struct {
	Username string `json:"username" validate:"required,maxchars=100"`
	Password string `json:"password" validate:"required,maxchars=256"`
}

type RefreshRequest struct {
	RefreshToken string `json:"refresh_token" validate:"required,maxchars=4096"`
}

type LogoutRequest struct {
	RefreshToken string `json:"refresh_token" validate:"required,maxchars=4096"`
}

type TokenResponse struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	TokenType        string `json:"token_type"`
	ExpiresIn        int64  `json:"expires_in"`
	RefreshExpiresIn int64  `json:"refresh_expires_in"`
}
