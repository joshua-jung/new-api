package model

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func TestUserNameLengthValidation(t *testing.T) {
	tests := []struct {
		name        string
		username    string
		displayName string
		wantError   bool
	}{
		{
			name:        "accepts 50 character username and display name",
			username:    strings.Repeat("u", 50),
			displayName: strings.Repeat("名", 50),
		},
		{
			name:        "rejects 51 character username",
			username:    strings.Repeat("u", 51),
			displayName: "display name",
			wantError:   true,
		},
		{
			name:        "rejects 51 character display name",
			username:    "username",
			displayName: strings.Repeat("名", 51),
			wantError:   true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			user := User{
				Username:    test.username,
				Password:    "password",
				DisplayName: test.displayName,
			}

			err := common.Validate.Struct(&user)
			if test.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}
