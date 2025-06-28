package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v4"
)

// Global variables for environment configurations
var (
	authLoginEndpoint   string
	authRefreshEndpoint string
	authEmail           string
	authPassword        string
	authClientID        int
)

// Initialize environment variables once at startup
func init() {
	authLoginEndpoint = os.Getenv("AUTH_LOGIN_ENDPOINT")
	if authLoginEndpoint == "" {
		authLoginEndpoint = "https://api.systemiq.ai/auth/login" // Default value
	}

	authRefreshEndpoint = os.Getenv("AUTH_REFRESH_ENDPOINT")
	if authRefreshEndpoint == "" {
		authRefreshEndpoint = "https://api.systemiq.ai/auth/refresh-token" // Default value
	}

	authEmail = os.Getenv("AUTH_EMAIL")
	authPassword = os.Getenv("AUTH_PASSWORD")

	clientIDStr := os.Getenv("AUTH_CLIENT_ID")
	if clientIDStr == "" {
		log.Fatal("AUTH_CLIENT_ID is not set")
	}

	var err error
	authClientID, err = strconv.Atoi(clientIDStr)
	if err != nil || authClientID == 0 {
		log.Fatal("AUTH_CLIENT_ID must be a valid integer")
	}

	// Check other required environment variables
	if authLoginEndpoint == "" || authRefreshEndpoint == "" || authEmail == "" || authPassword == "" {
		log.Fatal("One or more required environment variables are missing")
	}
}

// TokenResponse represents the structure of the login response
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ClientID     int    `json:"client_id"`
}

// ClientToken represents a single client's token details in the login response
type ClientToken struct {
	ClientID     int    `json:"client_id"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

// LoginResponse represents the full login response containing client tokens
type LoginResponse struct {
	Clients []ClientToken `json:"clients"`
}

// AuthHandler manages authentication and token refreshing
type AuthHandler struct {
	accessToken  string
	refreshToken string
	expiry       time.Time
	client       *http.Client
	mu           sync.Mutex
	ticker       *time.Ticker
	stopChan     chan struct{}
}

// NewAuthHandler creates a new AuthHandler instance and starts the background refresher
func NewAuthHandler() (*AuthHandler, error) {
	handler := &AuthHandler{
		client:   &http.Client{},
		ticker:   time.NewTicker(1 * time.Minute), // Check every minute
		stopChan: make(chan struct{}),
	}
	if err := handler.Login(); err != nil {
		return nil, err
	}

	// Start background token refresh
	go handler.startRefresher()
	return handler, nil
}

// startRefresher runs in the background to refresh the token before expiration
func (a *AuthHandler) startRefresher() {
	for {
		select {
		case <-a.ticker.C:
			var expiryUTC time.Time
			a.mu.Lock()
			expiryUTC = a.expiry.UTC()
			a.mu.Unlock()

			if time.Until(expiryUTC) < 5*time.Minute { // Refresh if token expires within 5 minutes
				log.Println("Token nearing expiration, refreshing...")
				if err := a.RefreshToken(); err != nil {
					log.Printf("Failed to refresh token: %v", err)
					if loginErr := a.Login(); loginErr != nil {
						log.Printf("Failed to re-login: %v", loginErr)
					}
				}
			}
		case <-a.stopChan:
			log.Print("Stopping refresher")
			return
		}
	}
}

// Login authenticates with the server and retrieves the access and refresh tokens
func (a *AuthHandler) Login() error {
	payload := map[string]string{
		"email":     authEmail,
		"password":  authPassword,
		"client_id": fmt.Sprintf("%d", authClientID),
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", authLoginEndpoint, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return errors.New("failed to authenticate: " + resp.Status)
	}

	var loginResponse LoginResponse
	if err := json.NewDecoder(resp.Body).Decode(&loginResponse); err != nil {
		return err
	}

	var foundClient *ClientToken
	for _, client := range loginResponse.Clients {
		if client.ClientID == authClientID {
			foundClient = &client
			break
		}
	}

	if foundClient == nil {
		return errors.New("client_id not found in login response")
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	a.accessToken = foundClient.AccessToken
	a.refreshToken = foundClient.RefreshToken
	a.expiry, err = parseTokenExpiry(a.accessToken)
	if err != nil {
		return err
	}

	log.Println("Successfully authenticated")
	return nil
}

// RefreshToken refreshes the access token using the refresh token
func (a *AuthHandler) RefreshToken() error {
	a.mu.Lock()
	refreshToken := a.refreshToken
	a.mu.Unlock()

	if refreshToken == "" {
		return errors.New("no refresh token available")
	}

	payload := map[string]string{
		"refresh_token": refreshToken,
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", authRefreshEndpoint, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return errors.New("failed to refresh token: " + resp.Status)
	}

	var tokenResponse TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResponse); err != nil {
		return err
	}

	if tokenResponse.ClientID != authClientID {
		return errors.New("client_id mismatch in refresh response")
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	a.accessToken = tokenResponse.AccessToken
	a.refreshToken = tokenResponse.RefreshToken
	a.expiry, err = parseTokenExpiry(a.accessToken)
	if err != nil {
		return err
	}

	log.Println("Successfully refreshed access token")
	return nil
}

// GetToken returns a valid access token, ensuring it is refreshed if necessary
func (a *AuthHandler) GetToken() (string, error) {
	var expiryUTC time.Time
	a.mu.Lock()
	expiryUTC = a.expiry.UTC()
	a.mu.Unlock()

	if time.Now().UTC().After(expiryUTC) {
		log.Println("Access token expired, refreshing...")
		if err := a.RefreshToken(); err != nil {
			log.Println("Failed to refresh token, logging in again...")
			if err := a.Login(); err != nil {
				return "", err
			}
		}
	}

	a.mu.Lock()
	token := a.accessToken
	a.mu.Unlock()
	return token, nil
}

// StopRefresher stops the background refresher when the application is shutting down
func (a *AuthHandler) StopRefresher() {
	close(a.stopChan)
	a.ticker.Stop()
}

// parseTokenExpiry decodes the JWT token and extracts the "exp" claim
func parseTokenExpiry(tokenString string) (time.Time, error) {
	token, _, err := new(jwt.Parser).ParseUnverified(tokenString, jwt.MapClaims{})
	if err != nil {
		return time.Time{}, err
	}

	if claims, ok := token.Claims.(jwt.MapClaims); ok {
		if exp, ok := claims["exp"].(float64); ok {
			return time.Unix(int64(exp), 0).UTC(), nil
		}
	}

	return time.Time{}, errors.New("expiration claim 'exp' not found")
}
