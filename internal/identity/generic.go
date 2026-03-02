package identity

import (
	"encoding/json"
	"fmt"
	"os"
)

// ExtractFromGenericAuth reads a generic JSON auth file and attempts to
// extract identity information. Used for newer providers (OpenCode, Cursor)
// where the exact auth format may vary.
func ExtractFromGenericAuth(path string) (*Identity, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read auth file: %w", err)
	}

	var auth map[string]interface{}
	if err := json.Unmarshal(data, &auth); err != nil {
		return nil, fmt.Errorf("parse auth file: %w", err)
	}

	id := &Identity{}

	// Check common identity fields at the top level
	for _, field := range []string{"email", "user_email", "account_email"} {
		if val := stringFromMap(auth, field); val != "" {
			id.Email = val
			return id, nil
		}
	}

	// Check for account ID fields
	for _, field := range []string{"accountId", "account_id", "user_id"} {
		if val := stringFromMap(auth, field); val != "" {
			id.AccountID = val
			id.Email = val // Use as email fallback for display
			return id, nil
		}
	}

	// Check nested user object
	if user, ok := auth["user"].(map[string]interface{}); ok {
		if email := stringFromMap(user, "email"); email != "" {
			id.Email = email
			return id, nil
		}
		if name := stringFromMap(user, "name"); name != "" {
			id.Email = name
			return id, nil
		}
	}

	// Check nested account object
	if account, ok := auth["account"].(map[string]interface{}); ok {
		if email := stringFromMap(account, "email"); email != "" {
			id.Email = email
			return id, nil
		}
	}

	// Try JWT tokens
	tokenFields := []string{"access_token", "accessToken", "token", "id_token", "idToken"}
	for _, field := range tokenFields {
		if token := stringFromMap(auth, field); token != "" {
			jwtID, err := ExtractFromJWT(token)
			if err == nil && jwtID != nil {
				return jwtID, nil
			}
		}
	}

	// Valid auth file but couldn't extract identity
	return nil, fmt.Errorf("no identity found in auth file")
}
