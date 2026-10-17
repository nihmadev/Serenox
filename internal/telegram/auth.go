package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	gotdtelegram "github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
)

var (
	ErrAuthPromptRequired = errors.New("Telegram session is not authorized; an authentication prompt is required")
	ErrSignUpUnsupported  = errors.New("Telegram account sign-up is not supported; create the account in an official client first")
	ErrTermsUnsupported   = errors.New("Telegram terms acceptance is not supported during sign-in; complete it in an official client first")
)

// CodePrompt contains non-secret delivery metadata that a UI may display.
// Neither it nor any Serenox error contains the phone, code, or 2FA password.
type CodePrompt struct {
	Delivery string
	Length   int
	Timeout  time.Duration
}

// Prompt supplies interactive phone authentication values. Implementations
// must read Password without terminal echo and must not log returned values.
// The interface is deliberately injectable so daemon, TTY, and test frontends
// can own their input lifecycle.
type Prompt interface {
	Phone(context.Context) (string, error)
	Code(context.Context, CodePrompt) (string, error)
	Password(context.Context) (string, error)
}

type promptAuthenticator struct {
	prompt Prompt
}

func newPromptAuthenticator(prompt Prompt) auth.UserAuthenticator {
	return &promptAuthenticator{prompt: prompt}
}

func (a *promptAuthenticator) Phone(ctx context.Context) (string, error) {
	phone, err := a.prompt.Phone(ctx)
	if err != nil {
		return "", fmt.Errorf("read phone: %w", err)
	}
	phone = strings.TrimSpace(phone)
	if phone == "" {
		return "", errors.New("phone must not be empty")
	}
	return phone, nil
}

func (a *promptAuthenticator) Code(ctx context.Context, sent *tg.AuthSentCode) (string, error) {
	metadata := codePrompt(sent)
	code, err := a.prompt.Code(ctx, metadata)
	if err != nil {
		return "", fmt.Errorf("read authentication code: %w", err)
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return "", errors.New("authentication code must not be empty")
	}
	return code, nil
}

func (a *promptAuthenticator) Password(ctx context.Context) (string, error) {
	password, err := a.prompt.Password(ctx)
	if err != nil {
		return "", fmt.Errorf("read 2FA password: %w", err)
	}
	if password == "" {
		return "", errors.New("2FA password must not be empty")
	}
	return password, nil
}

func (*promptAuthenticator) AcceptTermsOfService(context.Context, tg.HelpTermsOfService) error {
	return ErrTermsUnsupported
}

func (*promptAuthenticator) SignUp(context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, ErrSignUpUnsupported
}

func codePrompt(sent *tg.AuthSentCode) CodePrompt {
	if sent == nil {
		return CodePrompt{Delivery: "unknown"}
	}
	result := CodePrompt{Delivery: codeDelivery(sent.Type)}
	if withLength, ok := sent.Type.(interface{ GetLength() int }); ok {
		result.Length = withLength.GetLength()
	}
	if timeout, ok := sent.GetTimeout(); ok && timeout > 0 {
		result.Timeout = time.Duration(timeout) * time.Second
	}
	return result
}

func codeDelivery(value tg.AuthSentCodeTypeClass) string {
	switch value.(type) {
	case *tg.AuthSentCodeTypeApp:
		return "telegram_app"
	case *tg.AuthSentCodeTypeSMS:
		return "sms"
	case *tg.AuthSentCodeTypeCall:
		return "voice_call"
	case *tg.AuthSentCodeTypeFlashCall:
		return "flash_call"
	case *tg.AuthSentCodeTypeMissedCall:
		return "missed_call"
	case *tg.AuthSentCodeTypeEmailCode:
		return "email"
	case *tg.AuthSentCodeTypeFragmentSMS:
		return "fragment_sms"
	case *tg.AuthSentCodeTypeFirebaseSMS:
		return "firebase_sms"
	case *tg.AuthSentCodeTypeSMSWord:
		return "sms_word"
	case *tg.AuthSentCodeTypeSMSPhrase:
		return "sms_phrase"
	default:
		return "unknown"
	}
}

// Identity is the verified account bound to the persisted Telegram session.
type Identity struct {
	UserID    int64
	Username  string
	FirstName string
	LastName  string
}

// Authorize verifies an existing session or performs phone + code + optional
// 2FA authorization through prompt. prompt may be nil only for an already
// authorized session. The gotd client is closed before Authorize returns.
func Authorize(ctx context.Context, config LiveConfig, prompt Prompt) (Identity, error) {
	if ctx == nil {
		return Identity{}, errors.New("nil authorization context")
	}
	config, err := config.normalized()
	if err != nil {
		return Identity{}, err
	}
	client := gotdtelegram.NewClient(config.AppID, config.AppHash, gotdtelegram.Options{
		SessionStorage: gotdSessionStorage{store: config.Session},
		NoUpdates:      true,
		Device:         config.Device.gotd(),
	})
	var identity Identity
	err = client.Run(ctx, func(clientCtx context.Context) error {
		status, statusErr := client.Auth().Status(clientCtx)
		if statusErr != nil {
			return fmt.Errorf("check Telegram authorization: %w", statusErr)
		}
		if !status.Authorized {
			if prompt == nil {
				return ErrAuthPromptRequired
			}
			flow := auth.NewFlow(newPromptAuthenticator(prompt), auth.SendCodeOptions{})
			if authErr := client.Auth().IfNecessary(clientCtx, flow); authErr != nil {
				return fmt.Errorf("authorize Telegram session: %w", authErr)
			}
		}
		verified, verifyErr := verifyIdentity(clientCtx, client)
		if verifyErr != nil {
			return verifyErr
		}
		identity = verified
		return nil
	})
	if err != nil {
		return Identity{}, err
	}
	return identity, nil
}

func verifyIdentity(ctx context.Context, client *gotdtelegram.Client) (Identity, error) {
	status, err := client.Auth().Status(ctx)
	if err != nil {
		return Identity{}, fmt.Errorf("verify Telegram authorization: %w", err)
	}
	if !status.Authorized || status.User == nil || status.User.ID == 0 {
		return Identity{}, ErrNotAuthorized
	}
	self, err := client.Self(ctx)
	if err != nil {
		return Identity{}, fmt.Errorf("load Telegram self: %w", err)
	}
	if self.ID == 0 || self.ID != status.User.ID {
		return Identity{}, errors.New("Telegram authorization identity mismatch")
	}
	username, _ := self.GetUsername()
	first, _ := self.GetFirstName()
	last, _ := self.GetLastName()
	return Identity{UserID: self.ID, Username: username, FirstName: first, LastName: last}, nil
}
