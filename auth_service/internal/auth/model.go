package auth

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type LoginResponse struct {
	PrincipalID string `json:"principal_id"`
	Username    string `json:"username"`
}

type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

type VerifyResponse struct {
	PrincipalID string `json:"principal_id"`
}

type MeResponse struct {
	PrincipalID string `json:"principal_id"`
	Username    string `json:"username"`
}

type errorResponse struct {
	Error string `json:"error"`
}

const invalidCredentials = "invalid credentials"
