package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// The keyfunc hands JWTSecret to whatever algorithm the token header names, so an
// attacker who can pick the algorithm gets to choose how our secret is used. Only
// the HS256 the issuer actually signs with may verify.
func TestAuthMiddlewareAcceptsOnlyTheSigningAlgorithmItIssues(t *testing.T) {
	gin.SetMode(gin.TestMode)
	SetJWTSecret("unit-test-secret")
	t.Cleanup(func() { SetJWTSecret("") })

	router := gin.New()
	router.Use(AuthMiddleware())
	router.GET("/api/profile", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	tests := []struct {
		name    string
		method  jwt.SigningMethod
		key     any
		wantKey bool
	}{
		{name: "HS256 as issued", method: jwt.SigningMethodHS256, key: []byte("unit-test-secret"), wantKey: true},
		{name: "HS512 algorithm confusion", method: jwt.SigningMethodHS512, key: []byte("unit-test-secret")},
		{name: "HS384 algorithm confusion", method: jwt.SigningMethodHS384, key: []byte("unit-test-secret")},
		{name: "none", method: jwt.SigningMethodNone, key: jwt.UnsafeAllowNoneSignatureType},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			claims := Claims{
				UserID:   7,
				Username: "tester",
				Role:     "user",
				RegisteredClaims: jwt.RegisteredClaims{
					ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
					IssuedAt:  jwt.NewNumericDate(time.Now()),
				},
			}
			tokenString, err := jwt.NewWithClaims(tc.method, claims).SignedString(tc.key)
			if err != nil {
				t.Fatalf("signing failed: %v", err)
			}

			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/api/profile", nil)
			request.Header.Set("Authorization", "Bearer "+tokenString)
			router.ServeHTTP(recorder, request)

			want := http.StatusOK
			if !tc.wantKey {
				want = http.StatusUnauthorized
			}
			if recorder.Code != want {
				t.Fatalf("status = %d, want %d (%s)", recorder.Code, want, recorder.Body.String())
			}
		})
	}
}

func TestAuthMiddlewareRejectsTokenForAnotherSecret(t *testing.T) {
	gin.SetMode(gin.TestMode)
	SetJWTSecret("unit-test-secret")
	t.Cleanup(func() { SetJWTSecret("") })

	router := gin.New()
	router.Use(AuthMiddleware())
	router.GET("/api/profile", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })

	tokenString, err := jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{
		UserID:           7,
		Username:         "attacker",
		Role:             "admin",
		RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))},
	}).SignedString([]byte("someone-elses-secret"))
	if err != nil {
		t.Fatalf("signing failed: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/profile", nil)
	request.Header.Set("Authorization", "Bearer "+tokenString)
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
}
