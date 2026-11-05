package jobs

import "testing"

func TestSMTPAuthRequiresUser(t *testing.T) {
	if smtpAuth("mail.example.com:587", "", "secret") != nil {
		t.Fatal("empty user should skip SMTP AUTH")
	}
	if smtpAuth("mail.example.com:587", "apikey", "secret") == nil {
		t.Fatal("a username should enable SMTP AUTH")
	}
}
