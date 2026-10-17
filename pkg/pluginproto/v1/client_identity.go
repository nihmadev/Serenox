package v1

// User contains deliberately selected public identity metadata. It never
// contains phone numbers, access hashes, session values, or raw Telegram data.
type User struct {
	ID          int64  `json:"id"`
	Username    string `json:"username,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	Bot         bool   `json:"bot,omitempty"`
	Premium     bool   `json:"premium,omitempty"`
	Verified    bool   `json:"verified,omitempty"`
	Public      bool   `json:"public,omitempty"`
}

type Chat struct {
	ID                int64  `json:"id"`
	Kind              string `json:"kind"`
	Title             string `json:"title,omitempty"`
	Username          string `json:"username,omitempty"`
	ParticipantsCount int    `json:"participants_count,omitempty"`
	Forum             bool   `json:"forum,omitempty"`
	Channel           bool   `json:"channel,omitempty"`
	Megagroup         bool   `json:"megagroup,omitempty"`
	Public            bool   `json:"public,omitempty"`
}

func (u User) Validate() error {
	if u.ID == 0 {
		return invalid("user.id", "must be non-zero")
	}
	if err := validString("user.username", u.Username, false); err != nil {
		return err
	}
	return validString("user.display_name", u.DisplayName, false)
}

func (c Chat) Validate() error {
	if c.ID == 0 {
		return invalid("chat.id", "must be non-zero")
	}
	if err := validIdentifier("chat.kind", c.Kind); err != nil {
		return err
	}
	if c.ParticipantsCount < 0 || c.ParticipantsCount > MaxCollectionItems*1000 {
		return invalid("chat.participants_count", "is outside its bound")
	}
	for field, value := range map[string]string{"chat.title": c.Title, "chat.username": c.Username} {
		if err := validString(field, value, false); err != nil {
			return err
		}
	}
	return nil
}
