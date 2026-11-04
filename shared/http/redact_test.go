package http

import (
	"strings"
	"testing"
)

func TestRedactBodyForLog(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		mustNotHave string
		mustHave    string
	}{
		{
			name:        "login body keeps the username and drops the password",
			body:        `{"username":"someone@example.com","password":"not-a-real-password-1111"}`,
			mustNotHave: "not-a-real-password-1111",
			mustHave:    "someone@example.com",
		},
		{
			name:        "key variants are caught, not just the exact word",
			body:        `{"new_password":"a","passwordConfirm":"b","API_KEY":"c"}`,
			mustNotHave: `"a"`,
			mustHave:    redacted,
		},
		{
			name:        "nested one level down is still a credential",
			body:        `{"user":{"name":"x","token":"deadbeef"}}`,
			mustNotHave: "deadbeef",
			mustHave:    "x",
		},
		{
			name:        "inside an array too",
			body:        `{"accounts":[{"secret":"shh"}]}`,
			mustNotHave: "shh",
			mustHave:    redacted,
		},
		{
			name:        "a body we cannot parse is dropped whole rather than guessed at",
			body:        "username=bob&password=hunter2",
			mustNotHave: "hunter2",
			mustHave:    redacted,
		},
		{
			name:        "ordinary bodies are still logged in full",
			body:        `{"destination_code":"359","calls_count":6}`,
			mustNotHave: redacted,
			mustHave:    "359",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := redactBodyForLog([]byte(c.body))
			if strings.Contains(got, c.mustNotHave) {
				t.Errorf("leaked %q in %q", c.mustNotHave, got)
			}
			if !strings.Contains(got, c.mustHave) {
				t.Errorf("expected %q in %q", c.mustHave, got)
			}
		})
	}
}

func TestLoginPasswordNeverReachesTheLog(t *testing.T) {
	const pw = "not-a-real-password-0000"
	got := redactBodyForLog([]byte(`{"username":"u","password":"` + pw + `"}`))
	if strings.Contains(got, pw) {
		t.Fatalf("password survived redaction: %s", got)
	}
}
