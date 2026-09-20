package cognito

import (
	"context"
	"strings"
	"time"

	"go.uber.org/zap"
)

// ─── template variable substitution ───────────────────────────────────────────

// expandTemplate replaces AWS Cognito template variables in s.
//   - {username} → the user's username
//   - {####}    → the code (verification code, temporary password, or reset code)
func expandTemplate(tmpl, username, code string) string {
	r := strings.NewReplacer("{username}", username, "{####}", code)
	return r.Replace(tmpl)
}

// ─── default templates ────────────────────────────────────────────────────────

const (
	defaultVerificationSubject = "Your verification code"
	defaultVerificationBody    = "Your confirmation code is {####}"

	defaultInviteSubject = "Your temporary password"
	defaultInviteBody    = "Your username is {username} and temporary password is {####}."

	defaultResetSubject = "Your password reset code"
	defaultResetBody    = "Your password reset code is {####}."
)

// emailTimeout bounds how long any single SMTP delivery attempt may take before
// the goroutine gives up and returns. This prevents leaked goroutines when the
// SMTP server is unresponsive.
const emailTimeout = 10 * time.Second

// sendAsync dispatches an email in a background goroutine with a bounded timeout.
// The goroutine is tracked by emailWg so Shutdown() can drain cleanly.
func (s *Service) sendAsync(from string, to []string, subject, body, html, label string) {
	s.emailWg.Add(1)
	go func() {
		defer s.emailWg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), emailTimeout)
		defer cancel()

		done := make(chan error, 1)
		s.emailWg.Add(1)
		go func() {
			defer s.emailWg.Done()
			done <- s.mailer.Send(context.Background(), from, to, subject, body, html)
		}()

		select {
		case err := <-done:
			if err != nil {
				s.log.Warn("failed to send "+label+" email",
					zap.String("to", strings.Join(to, ",")), zap.Error(err))
			}
		case <-ctx.Done():
			s.log.Warn(label+" email send timed out",
				zap.String("to", strings.Join(to, ",")))
		}
	}()
}

// ─── email senders ────────────────────────────────────────────────────────────

// sendVerificationEmail sends a sign-up verification code to the user.
// If no mailer is configured, the event is logged at DEBUG and silently skipped.
// The pool's VerificationMessageTemplate is used when present; otherwise
// defaults apply. ov, when non-nil, is a CustomMessage trigger's response —
// see messageOverride in triggers.go — and takes precedence over both the
// pool template and the built-in default, exactly like the pool template
// takes precedence over the default.
func (s *Service) sendVerificationEmail(pool *UserPool, to, username, code string, ov *messageOverride) {
	if s.mailer == nil {
		s.log.Debug("email delivery not configured; skipping verification email",
			zap.String("username", username))
		return
	}

	subject := defaultVerificationSubject
	body := defaultVerificationBody
	html := ""

	if t := pool.VerificationMessageTemplate; t != nil {
		if t.EmailSubject != "" {
			subject = t.EmailSubject
		}
		if t.EmailMessage != "" {
			body = t.EmailMessage
		}
	}
	if ov != nil {
		if ov.Subject != nil {
			subject = *ov.Subject
		}
		if ov.Email != nil {
			body = *ov.Email
		}
	}

	subject = expandTemplate(subject, username, code)
	body = expandTemplate(body, username, code)

	s.sendAsync(s.cfg.SMTPFrom, []string{to}, subject, body, html, "verification")
}

// sendTempPasswordEmail sends a temporary password to a newly admin-created user.
// The pool's AdminCreateUserConfig.InviteMessageTemplate is used when present.
// See sendVerificationEmail for ov.
func (s *Service) sendTempPasswordEmail(pool *UserPool, to, username, tempPassword string, ov *messageOverride) {
	if s.mailer == nil {
		s.log.Debug("email delivery not configured; skipping temp-password email",
			zap.String("username", username))
		return
	}

	subject := defaultInviteSubject
	body := defaultInviteBody
	html := ""

	if a := pool.AdminCreateUserConfig; a != nil {
		if t := a.InviteMessageTemplate; t != nil {
			if t.EmailSubject != "" {
				subject = t.EmailSubject
			}
			if t.EmailMessage != "" {
				body = t.EmailMessage
			}
		}
	}
	if ov != nil {
		if ov.Subject != nil {
			subject = *ov.Subject
		}
		if ov.Email != nil {
			body = *ov.Email
		}
	}

	subject = expandTemplate(subject, username, tempPassword)
	body = expandTemplate(body, username, tempPassword)

	s.sendAsync(s.cfg.SMTPFrom, []string{to}, subject, body, html, "temporary password")
}

// sendPasswordResetEmail sends a password-reset confirmation code.
// The pool's VerificationMessageTemplate is used for subject/body when
// present. See sendVerificationEmail for ov.
func (s *Service) sendPasswordResetEmail(pool *UserPool, to, username, code string, ov *messageOverride) {
	if s.mailer == nil {
		s.log.Debug("email delivery not configured; skipping password-reset email",
			zap.String("username", username))
		return
	}

	subject := defaultResetSubject
	body := defaultResetBody
	html := ""

	// AWS uses the same VerificationMessageTemplate for password reset emails.
	if t := pool.VerificationMessageTemplate; t != nil {
		if t.EmailSubject != "" {
			subject = t.EmailSubject
		}
		if t.EmailMessage != "" {
			body = t.EmailMessage
		}
	}
	if ov != nil {
		if ov.Subject != nil {
			subject = *ov.Subject
		}
		if ov.Email != nil {
			body = *ov.Email
		}
	}

	subject = expandTemplate(subject, username, code)
	body = expandTemplate(body, username, code)

	s.sendAsync(s.cfg.SMTPFrom, []string{to}, subject, body, html, "password reset")
}

// ─── SMS senders ──────────────────────────────────────────────────────────────

const (
	defaultSMSVerificationBody = "Your confirmation code is {####}"
	defaultSMSInviteBody       = "Your username is {username} and temporary password is {####}."
	defaultSMSResetBody        = "Your password reset code is {####}."
	defaultSMSMfaBody          = "Your authentication code is {####}."
)

// sendMfaSMS delivers an SMS_MFA challenge code. The body comes from the MFA
// configuration's SmsAuthenticationMessage where SetUserPoolMfaConfig set one,
// then from the pool's own SmsAuthenticationMessage, then from the default —
// the same precedence AWS documents for the SMS MFA message.
func (s *Service) sendMfaSMS(pool *UserPool, to, username, code string) {
	if s.smsSender == nil {
		return
	}
	body := defaultSMSMfaBody
	if pool != nil && pool.SmsAuthenticationMessage != "" {
		body = pool.SmsAuthenticationMessage
	}
	if pool != nil && pool.SmsMfaConfiguration != nil && pool.SmsMfaConfiguration.SmsAuthenticationMessage != "" {
		body = pool.SmsMfaConfiguration.SmsAuthenticationMessage
	}
	body = expandTemplate(body, username, code)
	if err := s.smsSender.SendSMS("cognito", "", to, body, "", ""); err != nil {
		s.log.Warn("failed to capture MFA SMS", zap.String("to", to), zap.Error(err))
	}
}

// sendVerificationSMS sends a sign-up verification code to the user's phone.
// Uses the pool's VerificationMessageTemplate.SmsMessage when set. See
// sendVerificationEmail for ov.
func (s *Service) sendVerificationSMS(pool *UserPool, to, username, code string, ov *messageOverride) {
	if s.smsSender == nil {
		return
	}
	body := defaultSMSVerificationBody
	if t := pool.VerificationMessageTemplate; t != nil && t.SmsMessage != "" {
		body = t.SmsMessage
	}
	if ov != nil && ov.SMS != nil {
		body = *ov.SMS
	}
	body = expandTemplate(body, username, code)
	if err := s.smsSender.SendSMS("cognito", "", to, body, "", ""); err != nil {
		s.log.Warn("failed to capture verification SMS", zap.String("to", to), zap.Error(err))
	}
}

// sendTempPasswordSMS sends a temporary-password invitation to the user's phone.
// Uses InviteMessageTemplate.SMSMessage when set. See sendVerificationEmail
// for ov.
func (s *Service) sendTempPasswordSMS(pool *UserPool, to, username, tempPassword string, ov *messageOverride) {
	if s.smsSender == nil {
		return
	}
	body := defaultSMSInviteBody
	if a := pool.AdminCreateUserConfig; a != nil {
		if t := a.InviteMessageTemplate; t != nil && t.SMSMessage != "" {
			body = t.SMSMessage
		}
	}
	if ov != nil && ov.SMS != nil {
		body = *ov.SMS
	}
	body = expandTemplate(body, username, tempPassword)
	if err := s.smsSender.SendSMS("cognito", "", to, body, "", ""); err != nil {
		s.log.Warn("failed to capture invite SMS", zap.String("to", to), zap.Error(err))
	}
}

// sendPasswordResetSMS sends a password-reset confirmation code to the user's phone.
// Uses the pool's VerificationMessageTemplate.SmsMessage when set. See
// sendVerificationEmail for ov.
func (s *Service) sendPasswordResetSMS(pool *UserPool, to, username, code string, ov *messageOverride) {
	if s.smsSender == nil {
		return
	}
	body := defaultSMSResetBody
	if t := pool.VerificationMessageTemplate; t != nil && t.SmsMessage != "" {
		body = t.SmsMessage
	}
	if ov != nil && ov.SMS != nil {
		body = *ov.SMS
	}
	body = expandTemplate(body, username, code)
	if err := s.smsSender.SendSMS("cognito", "", to, body, "", ""); err != nil {
		s.log.Warn("failed to capture password-reset SMS", zap.String("to", to), zap.Error(err))
	}
}
